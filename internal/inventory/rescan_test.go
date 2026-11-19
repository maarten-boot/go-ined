package inventory

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-218: a rescan interval is set and cleared through PATCH; it needs an
// IPv4 network group no larger than discovery.max_prefix and an interval
// of 15m to 8760h; turning it on makes the scan due at once; a new CIDR
// clears the last scan; RescanDone records scans covering a group's whole
// range only.
func TestRescanSetting(t *testing.T) {
	s, _ := newSvc(t)
	s.SetScanLimit(20)
	r := &recorder{}
	s.SetNotifier(r)
	lan := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.1.0/24"})
	plain := mustGroup(t, s, apitypes.GroupCreate{Name: "plain"})
	v6 := mustGroup(t, s, apitypes.GroupCreate{CIDR: "2001:db8::/64"})
	wide := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.8.0.0/16"})
	patch := func(id int64, every string) (apitypes.Group, error) {
		return s.UpdateGroup(ctx, id, apitypes.GroupPatch{Rescan: apitypes.Some(every)})
	}
	for _, c := range []struct {
		id    int64
		every string
	}{
		{lan.ID, "10m"}, {lan.ID, "9000h"}, {lan.ID, "daily"}, {lan.ID, "-1h"},
		{plain.ID, "1h"}, {v6.ID, "1h"}, {wide.ID, "1h"},
	} {
		if _, err := patch(c.id, c.every); KindOf(err) != Invalid {
			t.Errorf("group %d rescan %q: %v", c.id, c.every, err)
		}
	}
	r.take()
	g, err := patch(lan.ID, "24h0m")
	if err != nil || g.Rescan != "24h" || g.RescanLast != nil {
		t.Fatalf("set: %+v %v", g, err)
	}
	if ev := r.take(); ev != "group.updated 1 unknown" {
		t.Errorf("events: %s", ev)
	}
	now := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	if due := s.RescanDue(now); len(due) != 1 || due[0].ID != lan.ID || due[0].Interval != 24*time.Hour || !s.RescanIsDue(lan.ID, now) {
		t.Fatalf("due at once: %+v", due)
	}

	// A scan of a part doesn't count; one covering the range does.
	ips := []netip.Addr{netip.MustParseAddr("10.0.1.7"), netip.MustParseAddr("10.0.2.9")}
	if err := s.RescanDone(ctx, netip.MustParsePrefix("10.0.1.0/25"), now, ips); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Group(lan.ID); g.RescanLast != nil {
		t.Fatalf("a part counted: %+v", g)
	}
	if err := s.RescanDone(ctx, netip.MustParsePrefix("10.0.0.0/22"), now, ips); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Group(lan.ID); g.RescanLast == nil || !g.RescanLast.Equal(now) || g.RescanFound != 1 {
		t.Fatalf("recorded: %+v", g)
	}
	if ev := r.take(); ev != "group.updated 1 unknown" {
		t.Errorf("events: %s", ev)
	}
	if due := s.RescanDue(now.Add(23 * time.Hour)); len(due) != 0 || s.RescanIsDue(lan.ID, now.Add(23*time.Hour)) {
		t.Errorf("due too early: %+v", due)
	}
	if due := s.RescanDue(now.Add(24 * time.Hour)); len(due) != 1 {
		t.Errorf("not due after the interval: %+v", due)
	}
	// Another interval keeps the last scan; off and on again is due at once.
	if g, _ := patch(lan.ID, "6h"); g.RescanLast == nil {
		t.Errorf("new interval cleared the last scan: %+v", g)
	}
	patch(lan.ID, "off")
	if due := s.RescanDue(now.Add(time.Hour * 1000)); len(due) != 0 {
		t.Errorf("off but due: %+v", due)
	}
	if g, _ := patch(lan.ID, "1h"); g.RescanLast != nil {
		t.Errorf("on again: %+v", g)
	}
	s.RescanDone(ctx, netip.MustParsePrefix("10.0.1.0/24"), now, nil)
	// A CIDR without rescan's conditions is refused; another IPv4 range
	// clears the last scan.
	if _, err := s.UpdateGroup(ctx, lan.ID, apitypes.GroupPatch{CIDR: apitypes.Some("")}); KindOf(err) != Invalid {
		t.Errorf("rescan kept on a plain group: %v", err)
	}
	if _, err := s.UpdateGroup(ctx, lan.ID, apitypes.GroupPatch{CIDR: apitypes.Some("10.9.0.0/16")}); KindOf(err) != Invalid {
		t.Errorf("rescan on a /16 with max /20: %v", err)
	}
	g, err = s.UpdateGroup(ctx, lan.ID, apitypes.GroupPatch{CIDR: apitypes.Some("10.0.4.0/24")})
	if err != nil || g.RescanLast != nil || g.RescanFound != 0 || g.Rescan != "1h" {
		t.Errorf("new range: %+v %v", g, err)
	}
	g, err = s.UpdateGroup(ctx, lan.ID, apitypes.GroupPatch{CIDR: apitypes.Some(""), Rescan: apitypes.Some("")})
	if err != nil || g.Rescan != "" {
		t.Errorf("off and plain together: %+v %v", g, err)
	}
}

// TC-218: interval text.
func TestRescanText(t *testing.T) {
	for in, want := range map[string]string{"": "", "off": "", "OFF": "", "15m": "15m", "90m": "1h30m", "24h": "24h", "168h0m0s": "168h", "8760h": "8760h"} {
		d, err := apitypes.ParseRescan(in)
		if err != nil || apitypes.FormatRescan(d) != want {
			t.Errorf("%q: %v %v, want %q", in, apitypes.FormatRescan(d), err, want)
		}
	}
	for _, in := range []string{"14m59s", "8761h", "1d", "x"} {
		if _, err := apitypes.ParseRescan(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// TC-218 (crosscheck 2026-11-18-02 C2, L1, L2): a rescan that a lower
// discovery.max_prefix made too large doesn't block other edits, only a
// PATCH of rescan or cidr; the setting's generation changes with rescan
// and cidr only; a last scan in the future is due.
func TestRescanLimitLowered(t *testing.T) {
	s, _ := newSvc(t)
	g := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.8.0.0/16"})
	if _, err := s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{Rescan: apitypes.Some("1h")}); err != nil {
		t.Fatal(err)
	}
	gen := s.RescanDue(time.Now())[0].Gen
	s.SetScanLimit(24)
	if _, err := s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{Name: apitypes.Some("site"), Note: apitypes.Some("n")}); err != nil {
		t.Errorf("rename blocked: %v", err)
	}
	if due := s.RescanDue(time.Now()); due[0].Gen != gen {
		t.Errorf("generation changed by a rename")
	}
	if _, err := s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{Rescan: apitypes.Some("2h")}); KindOf(err) != Invalid {
		t.Errorf("new interval on a too large range: %v", err)
	}
	if _, err := s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{Rescan: apitypes.Some("off")}); err != nil {
		t.Errorf("off: %v", err)
	}
	lan := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.1.0/24"})
	g2, _ := s.UpdateGroup(ctx, lan.ID, apitypes.GroupPatch{Rescan: apitypes.Some("1h")})
	g1 := s.RescanDue(time.Now())[0].Gen
	s.UpdateGroup(ctx, lan.ID, apitypes.GroupPatch{Rescan: apitypes.Some("2h")})
	if s.RescanDue(time.Now())[0].Gen == g1 {
		t.Error("generation kept by a new interval")
	}
	// A last scan in the future (the clock set back): due.
	now := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	s.RescanDone(ctx, netip.MustParsePrefix(g2.CIDR), now.Add(48*time.Hour), nil)
	if due := s.RescanDue(now); len(due) != 1 || !s.RescanIsDue(lan.ID, now) {
		t.Errorf("a future last scan holds the group: %+v", due)
	}
}

// TC-326 (inventory; crosscheck 2026-11-18-03 L1, L2): an import with
// --update that turns monitoring off drops the SNMP values, as a PATCH
// does; a new agent drops the values with host.updated; the same agent
// again sends nothing.
func TestSNMPValuesDropped(t *testing.T) {
	s, lan, web := lanRig(t)
	r := &recorder{}
	s.SetNotifier(r)
	ip := netip.MustParseAddr(web.IP)
	a := SNMPAgent{Version: "v2c", Community: "c", CheckedAt: time.Unix(100, 0)}
	if _, ok, err := s.SetSNMP(ctx, web.ID, ip, a); !ok || err != nil {
		t.Fatal(err)
	}
	s.SetSNMPValues(web.ID, ip, a, apitypes.SNMPValues{At: time.Unix(110, 0)}, false)
	if _, ok, _ := s.SetSNMP(ctx, web.ID, ip, SNMPAgent{Version: "v2c", Community: "c", CheckedAt: time.Unix(200, 0)}); !ok {
		t.Fatal("same agent")
	}
	r.take()
	if h, _ := s.Host(web.ID); h.SNMP.Values == nil {
		t.Fatal("same agent dropped the values")
	}
	s.SetSNMP(ctx, web.ID, ip, SNMPAgent{Version: "v1", Community: "c", CheckedAt: time.Unix(300, 0)})
	if ev := r.take(); !strings.Contains(ev, "host.updated") {
		t.Errorf("a new agent sent %q", ev)
	}
	b := SNMPAgent{Version: "v1", Community: "c"}
	s.SetSNMPValues(web.ID, ip, b, apitypes.SNMPValues{At: time.Unix(310, 0)}, false)
	f, _ := s.ExportGroup(lan.ID)
	for i := range f.Group.Hosts {
		f.Group.Hosts[i].Monitored = false
	}
	if _, err := s.Import(ctx, &f, ImportOptions{Update: true, ParentID: Root}); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Host(web.ID); h.Monitored || h.SNMP.Values != nil {
		t.Errorf("after an import turning monitoring off: %+v", h.SNMP)
	}
}

// TC-326 (inventory; crosscheck 2026-11-18-04 C2): "no answer" is written
// every time; the same agent again only after a day.
func TestSNMPSaved(t *testing.T) {
	s, repo := newSvc(t)
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1"})
	ip := netip.MustParseAddr("192.0.2.1")
	t0 := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	set := func(a SNMPAgent, at time.Time) int {
		n := repo.applies
		a.CheckedAt = at
		if _, ok, err := s.SetSNMP(ctx, h.ID, ip, a); !ok || err != nil {
			t.Fatal(err)
		}
		return repo.applies - n
	}
	if set(SNMPAgent{}, t0) != 1 || set(SNMPAgent{}, t0.Add(24*time.Hour)) != 1 {
		t.Error("no answer not written")
	}
	a := SNMPAgent{Version: "v2c", Community: "c"}
	if set(a, t0.Add(25*time.Hour)) != 1 || set(a, t0.Add(30*time.Hour)) != 0 || set(a, t0.Add(49*time.Hour)) != 1 {
		t.Error("the same agent: written at once, then once a day")
	}
}

// TC-328 (inventory): community lists: a host's and a network-view group's
// are set and cleared through PATCH; more than 10, an empty one, one over
// 255 bytes, one twice and a custom-view group are 400; the views show the
// count and where the list comes from, never the strings.
func TestCommunitiesPatch(t *testing.T) {
	s, _ := newSvc(t)
	top := mustGroup(t, s, apitypes.GroupCreate{Name: "top"})
	lan := mustGroup(t, s, apitypes.GroupCreate{Name: "lan", ParentID: &top.ID})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lan.ID})
	view, err := s.CreateView(ctx, apitypes.ViewCreate{Name: "v"})
	if err != nil {
		t.Fatal(err)
	}
	cg := mustGroup(t, s, apitypes.GroupCreate{Name: "c", ViewID: &view.ID})
	set := func(group bool, id int64, l []string) error {
		if group {
			_, err := s.UpdateGroup(ctx, id, apitypes.GroupPatch{SNMPCommunities: apitypes.Some(l)})
			return err
		}
		_, err := s.UpdateHost(ctx, id, apitypes.HostPatch{SNMPCommunities: apitypes.Some(l)})
		return err
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprint(i)
	}
	for _, bad := range [][]string{eleven, {""}, {strings.Repeat("x", 256)}, {"a", "a"}} {
		if KindOf(set(false, h.ID, bad)) != Invalid || KindOf(set(true, lan.ID, bad)) != Invalid {
			t.Errorf("%d entries accepted", len(bad))
		}
	}
	if KindOf(set(true, cg.ID, []string{"x"})) != Invalid {
		t.Error("a custom-view group took a list")
	}
	from := func() string { v, _ := s.Host(h.ID); return v.SNMPCommunitiesFrom }
	if from() != "server" {
		t.Errorf("from %q", from())
	}
	set(true, top.ID, []string{"comm-a", "comm-b"})
	if from() != fmt.Sprintf("group %d", top.ID) {
		t.Errorf("from %q", from())
	}
	set(true, lan.ID, []string{"comm-c"})
	set(false, h.ID, []string{"comm-d", "comm-e", "comm-f"})
	v, _ := s.Host(h.ID)
	g, _ := s.Group(top.ID)
	if v.SNMPCommunitiesFrom != "host" || v.SNMPCommunitiesSet != 3 || g.SNMPCommunitiesSet != 2 {
		t.Errorf("host %+v group %+v", v, g)
	}
	set(false, h.ID, []string{})
	if from() != fmt.Sprintf("group %d", lan.ID) {
		t.Errorf("after inherit: %q", from())
	}
	m, _ := s.MapOf(NetworkView)
	b, _ := json.Marshal([]any{s.Hosts(), s.Groups(), m}) // what the API answers
	f, _ := s.ExportGroup(top.ID)
	fb, _ := json.Marshal(f)
	for _, w := range []string{"comm-a", "comm-c", "comm-d"} {
		if strings.Contains(string(b), w) || strings.Contains(string(fb), w) {
			t.Errorf("%s shown", w)
		}
	}
	if ids, err := s.HostsUnder(top.ID); err != nil || len(ids) != 1 || ids[0] != h.ID {
		t.Errorf("HostsUnder: %v %v", ids, err)
	}
}

// TC-328 (inventory; crosscheck 2026-11-19-01 L3): a group's new list sends
// host.updated for the hosts below it, with their new origin.
func TestCommunitiesHostEvents(t *testing.T) {
	s, _ := newSvc(t)
	r := &recorder{}
	s.SetNotifier(r)
	lab := mustGroup(t, s, apitypes.GroupCreate{Name: "lab"})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lab.ID})
	mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.2"})
	r.take()
	if _, err := s.UpdateGroup(ctx, lab.ID, apitypes.GroupPatch{SNMPCommunities: apitypes.Some([]string{"x"})}); err != nil {
		t.Fatal(err)
	}
	// One group.updated, no host.updated per host below it (crosscheck
	// 2026-11-19-02 C1); the host's origin is read back on request.
	if ev := r.take(); strings.Contains(ev, "host.updated") || !strings.Contains(ev, fmt.Sprintf("group.updated %d", lab.ID)) {
		t.Errorf("events: %s", ev)
	}
	if v, err := s.Host(h.ID); err != nil || v.SNMPCommunitiesFrom != fmt.Sprintf("group %d", lab.ID) {
		t.Errorf("host after the group's list: %+v %v", v.SNMPCommunitiesFrom, err)
	}
	s.UpdateGroup(ctx, lab.ID, apitypes.GroupPatch{SNMPCommunities: apitypes.Some([]string{"x"})})
	if ev := r.take(); strings.Contains(ev, "host.updated") {
		t.Errorf("the same list again: %s", ev)
	}
}
