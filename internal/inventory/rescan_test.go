package inventory

import (
	"net/netip"
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
