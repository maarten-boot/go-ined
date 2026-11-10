package inventory

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// recorder is a Notifier that keeps a compact log of the events.
type recorder struct{ log []string }

func (r *recorder) Publish(typ string, data any) {
	switch d := data.(type) {
	case apitypes.Host:
		r.log = append(r.log, fmt.Sprintf("%s %d %s", typ, d.ID, d.Status))
	case apitypes.Group:
		r.log = append(r.log, fmt.Sprintf("%s %d %s", typ, d.ID, d.Status))
	case apitypes.DeletedEvent:
		r.log = append(r.log, fmt.Sprintf("%s %d", typ, d.ID))
	case apitypes.GroupStatusEvent:
		r.log = append(r.log, fmt.Sprintf("%s %d %s", typ, d.ID, d.Status))
	default:
		r.log = append(r.log, fmt.Sprintf("%s %T", typ, data))
	}
}

func (r *recorder) take() string { s := strings.Join(r.log, "; "); r.log = nil; return s }

func observe(t *testing.T, s *Service, obs ...Observation) {
	t.Helper()
	if err := s.ApplyObservations(context.Background(), obs); err != nil {
		t.Fatal(err)
	}
}

func up(id int64, ip string) Observation {
	rtt := 1.5
	return Observation{HostID: id, IP: netip.MustParseAddr(ip), Status: apitypes.StatusUp, RTTms: &rtt, Replied: true, At: time.Unix(100, 0)}
}

func down(id int64, ip string) Observation {
	return Observation{HostID: id, IP: netip.MustParseAddr(ip), Status: apitypes.StatusDown, LossPct: 100, At: time.Unix(200, 0)}
}

// TC-305: host.status on a status change; group.status for every group
// whose computed status changes, for each cause.
func TestStatusEvents(t *testing.T) {
	s, _ := newSvc(t)
	r := &recorder{}
	s.SetNotifier(r)
	outer := mustGroup(t, s, apitypes.GroupCreate{Name: "outer"})
	inner := mustGroup(t, s, apitypes.GroupCreate{Name: "inner", ParentID: &outer.ID})
	other := mustGroup(t, s, apitypes.GroupCreate{Name: "other"})
	if got := r.take(); got != "group.created 1 unknown; group.created 2 unknown; group.created 3 unknown" {
		t.Fatalf("creates: %s", got)
	}

	h := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1", ParentID: &inner.ID})
	// An unknown host doesn't change the (already unknown) group status.
	if got := r.take(); got != "host.created 1 unknown" {
		t.Fatalf("add: %s", got)
	}

	// Cause: host status change.
	observe(t, s, up(h.ID, "10.0.0.1"))
	if got := r.take(); got != "host.status 1 up; group.status 1 up; group.status 2 up" {
		t.Fatalf("status: %s", got)
	}
	// Same status again: nothing (SPEC-307).
	observe(t, s, up(h.ID, "10.0.0.1"))
	if got := r.take(); got != "" {
		t.Fatalf("repeat: %s", got)
	}

	// Cause: add (a down host under outer).
	h2 := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.2", ParentID: &outer.ID})
	if got := r.take(); got != "host.created 2 unknown; group.status 1 unknown" {
		t.Fatalf("add 2: %s", got)
	}

	// Cause: change to monitored.
	if _, err := s.UpdateHost(context.Background(), h2.ID, apitypes.HostPatch{Monitored: apitypes.Some(false)}); err != nil {
		t.Fatal(err)
	}
	if got := r.take(); got != "host.updated 2 unknown; group.status 1 up" {
		t.Fatalf("unmonitor: %s", got)
	}

	// Cause: move.
	if _, err := s.UpdateHost(context.Background(), h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&other.ID)}); err != nil {
		t.Fatal(err)
	}
	if got := r.take(); got != "host.updated 1 up; group.status 1 unknown; group.status 2 unknown; group.status 3 up" {
		t.Fatalf("move: %s", got)
	}

	// Cause: delete.
	if err := s.DeleteHost(context.Background(), h.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.take(); got != "host.deleted 1; group.status 3 unknown" {
		t.Fatalf("delete: %s", got)
	}
}

// TC-507: a cascade delete emits one deleted event per item; a reparent
// delete emits updated events for the moved children.
func TestDeleteEvents(t *testing.T) {
	s, _ := newSvc(t)
	r := &recorder{}
	s.SetNotifier(r)
	a := mustGroup(t, s, apitypes.GroupCreate{Name: "a"})
	b := mustGroup(t, s, apitypes.GroupCreate{Name: "b", ParentID: &a.ID})
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1", ParentID: &b.ID})
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.2", ParentID: &a.ID})
	r.take()
	if err := s.DeleteGroup(context.Background(), b.ID, apitypes.ChildrenReparent); err != nil {
		t.Fatal(err)
	}
	if got := r.take(); got != "host.updated 1 unknown; group.deleted 2" {
		t.Fatalf("reparent: %s", got)
	}
	if err := s.DeleteGroup(context.Background(), a.ID, apitypes.ChildrenCascade); err != nil {
		t.Fatal(err)
	}
	if got := r.take(); got != "host.deleted 1; host.deleted 2; group.deleted 1" {
		t.Fatalf("cascade: %s", got)
	}
}

// TC-112 (event part): PATCH collapsed emits group.updated.
func TestCollapsedEvent(t *testing.T) {
	s, _ := newSvc(t)
	r := &recorder{}
	s.SetNotifier(r)
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g"})
	r.take()
	if _, err := s.UpdateGroup(context.Background(), g.ID, apitypes.GroupPatch{Collapsed: apitypes.Some(false)}); err != nil {
		t.Fatal(err)
	}
	if got := r.take(); got != "group.updated 1 unknown" {
		t.Fatalf("got %s", got)
	}
}

// TC-308, TC-303: results are recorded; host.status only on a status
// change or when stale clears; the whole batch is one Apply.
func TestApplyObservations(t *testing.T) {
	s, repo := newSvc(t)
	r := &recorder{}
	s.SetNotifier(r)
	h1 := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1"})
	h2 := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.2"})
	r.take()
	applies := repo.applies

	observe(t, s, up(h1.ID, "10.0.0.1"), down(h2.ID, "10.0.0.2"))
	if repo.applies != applies+1 {
		t.Fatalf("batch used %d Apply calls", repo.applies-applies)
	}
	if got := r.take(); got != "host.status 1 up; host.status 2 down" {
		t.Fatalf("events: %s", got)
	}
	got, _ := s.Host(h1.ID)
	if got.RTTms == nil || *got.RTTms != 1.5 || *got.LossPct != 0 || got.LastSeen == nil || !got.LastSeen.Equal(time.Unix(100, 0)) || !got.LastChange.Equal(time.Unix(100, 0)) {
		t.Fatalf("host 1 = %+v", got)
	}
	got, _ = s.Host(h2.ID)
	if got.RTTms != nil || *got.LossPct != 100 || got.LastSeen != nil || !got.LastChange.Equal(time.Unix(200, 0)) {
		t.Fatalf("host 2 = %+v", got)
	}
	if p := repo.hosts[h1.ID]; p.Status != apitypes.StatusUp || p.RTTms == nil {
		t.Fatalf("not persisted: %+v", p)
	}

	// Only the round-trip time changes: saved, no event.
	o := up(h1.ID, "10.0.0.1")
	rtt := 9.0
	o.RTTms = &rtt
	observe(t, s, o)
	if got := r.take(); got != "" {
		t.Fatalf("rtt-only change emitted %s", got)
	}
	if p := repo.hosts[h1.ID]; *p.RTTms != 9 {
		t.Fatalf("rtt not saved: %v", *p.RTTms)
	}

	// Status "" keeps the status; last_change stays.
	keep := down(h1.ID, "10.0.0.1")
	keep.Status = ""
	observe(t, s, keep)
	got, _ = s.Host(h1.ID)
	if got.Status != apitypes.StatusUp || !got.LastChange.Equal(time.Unix(100, 0)) {
		t.Fatalf("keep: %+v", got)
	}

	// Results for a changed IP, an unmonitored or a deleted host are dropped.
	if _, err := s.UpdateHost(context.Background(), h2.ID, apitypes.HostPatch{Monitored: apitypes.Some(false)}); err != nil {
		t.Fatal(err)
	}
	r.take()
	observe(t, s, up(h2.ID, "10.0.0.2"), up(h1.ID, "10.9.9.9"), up(99, "10.0.0.3"))
	if got := r.take(); got != "" {
		t.Fatalf("dropped results emitted %s", got)
	}
}

// TC-602: after a restart hosts are stale; the first round clears the flag
// and emits host.status even though the status value is the same.
func TestStaleClears(t *testing.T) {
	s, repo := newSvc(t)
	h := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1"})
	observe(t, s, up(h.ID, "10.0.0.1"))

	s2, err := NewService(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	s2.SetNotifier(r)
	got, _ := s2.Host(h.ID)
	if !got.Stale || got.Status != apitypes.StatusUp {
		t.Fatalf("after restart: %+v", got)
	}
	observe(t, s2, up(h.ID, "10.0.0.1"))
	if ev := r.take(); ev != "host.status 1 up" {
		t.Fatalf("events: %s", ev)
	}
	if got, _ := s2.Host(h.ID); got.Stale {
		t.Fatal("still stale")
	}
}

// TC-307 (inventory part): unmonitored hosts are not monitor targets.
func TestMonitorTargets(t *testing.T) {
	s, _ := newSvc(t)
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1"})
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.2", Monitored: ptr(false)})
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.3"})
	got := s.MonitorTargets()
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 || got[1].IP != netip.MustParseAddr("10.0.0.3") {
		t.Fatalf("targets = %+v", got)
	}
}

// TC-310: an IP change resets the status and emits host.status.
func TestIPChangeResets(t *testing.T) {
	s, _ := newSvc(t)
	r := &recorder{}
	s.SetNotifier(r)
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g"})
	h := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1", ParentID: &g.ID})
	observe(t, s, up(h.ID, "10.0.0.1"))
	r.take()
	got, err := s.UpdateHost(context.Background(), h.ID, apitypes.HostPatch{IP: apitypes.Some("10.0.0.2")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != apitypes.StatusUnknown || got.RTTms != nil || got.LossPct != nil || got.LastChange.Equal(time.Unix(100, 0)) {
		t.Fatalf("after IP change: %+v", got)
	}
	if ev := r.take(); ev != "host.updated 1 unknown; host.status 1 unknown; group.status 1 unknown" {
		t.Fatalf("events: %s", ev)
	}
	// Same IP again, or another field: no reset, no host.status.
	observe(t, s, up(h.ID, "10.0.0.2"))
	r.take()
	if _, err := s.UpdateHost(context.Background(), h.ID, apitypes.HostPatch{IP: apitypes.Some("10.0.0.2"), Name: apitypes.Some("x")}); err != nil {
		t.Fatal(err)
	}
	if ev := r.take(); ev != "host.updated 1 up" {
		t.Fatalf("no-op IP: %s", ev)
	}
}

// TC-603: unmonitored hosts are never stale.
func TestStaleOnlyMonitored(t *testing.T) {
	s, repo := newSvc(t)
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1"})
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.2", Monitored: ptr(false)})
	s2, err := NewService(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := s2.Host(1); !h.Stale {
		t.Error("monitored host not stale after restart")
	}
	if h, _ := s2.Host(2); h.Stale {
		t.Error("unmonitored host stale after restart")
	}
	h, err := s2.UpdateHost(context.Background(), 1, apitypes.HostPatch{Monitored: apitypes.Some(false)})
	if err != nil || h.Stale {
		t.Fatalf("turning monitoring off: %v %+v", err, h)
	}
	// Turning it back on: stale until the next round (v0.10).
	h, err = s2.UpdateHost(context.Background(), 2, apitypes.HostPatch{Monitored: apitypes.Some(true)})
	if err != nil || !h.Stale {
		t.Fatalf("turning monitoring on: %v %+v", err, h)
	}
	// Another change leaves the flag alone.
	h, _ = s2.UpdateHost(context.Background(), 2, apitypes.HostPatch{Name: apitypes.Some("x")})
	if !h.Stale {
		t.Fatal("unrelated change cleared stale")
	}
}

// TC-315 (inventory part): last_seen comes from SeenAt.
func TestSeenAt(t *testing.T) {
	s, _ := newSvc(t)
	h := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1"})
	o := up(h.ID, "10.0.0.1")
	o.SeenAt = time.Unix(90, 0)
	observe(t, s, o)
	got, _ := s.Host(h.ID)
	if !got.LastSeen.Equal(time.Unix(90, 0)) || !got.LastChange.Equal(time.Unix(100, 0)) {
		t.Fatalf("last_seen %v, last_change %v", got.LastSeen, got.LastChange)
	}
}

// TC-203, TC-209, TC-210 (inventory part): AddDiscovered.
func TestAddDiscovered(t *testing.T) {
	s, repo := newSvc(t)
	r := &recorder{}
	s.SetNotifier(r)
	wide := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.0.0/16"})
	sub := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.1.0/24", ParentID: &wide.ID})
	elsewhere := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.2.0/24"}) // not under wide
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.9", ParentID: &sub.ID})
	r.take()
	applies := repo.applies
	at := time.Unix(300, 0)
	n, err := s.AddDiscovered(context.Background(), wide.ID, []Found{
		{IP: netip.MustParseAddr("10.0.0.5"), RTTms: 1, At: at},
		{IP: netip.MustParseAddr("10.0.1.5"), RTTms: 2, LossPct: 50, At: at},
		{IP: netip.MustParseAddr("10.0.2.5"), RTTms: 3, At: at}, // only inside wide's subtree counts
		{IP: netip.MustParseAddr("10.0.0.9"), RTTms: 4, At: at}, // exists: skipped, not moved
	})
	if err != nil || n != 3 || repo.applies != applies+1 {
		t.Fatalf("n=%d err=%v applies=%d", n, err, repo.applies-applies)
	}
	want := map[string]int64{"10.0.0.5": wide.ID, "10.0.1.5": sub.ID, "10.0.2.5": wide.ID, "10.0.0.9": sub.ID}
	pos := map[[2]float64]bool{}
	for _, h := range s.Hosts() {
		if *h.ParentID != want[h.IP] {
			t.Errorf("%s in %d, want %d", h.IP, *h.ParentID, want[h.IP])
		}
		if h.IP != "10.0.0.9" && (h.Status != apitypes.StatusUp || !h.Monitored || !h.LastSeen.Equal(at) || h.RTTms == nil) {
			t.Errorf("%s: %+v", h.IP, h)
		}
		if *h.ParentID == wide.ID {
			p := [2]float64{h.X, h.Y}
			if pos[p] {
				t.Errorf("two hosts at %v", p)
			}
			pos[p] = true
		}
	}
	// One event per new host. No group.status: both groups contain the
	// still-unknown host 10.0.0.9, so they stay unknown.
	if ev := r.take(); ev != "host.created 2 up; host.created 3 up; host.created 4 up" {
		t.Errorf("events: %s", ev)
	}
	if g, _ := s.Group(wide.ID); g.Status != apitypes.StatusUnknown {
		t.Errorf("wide status %s", g.Status)
	}

	// The group was deleted meanwhile: the most specific network group anywhere.
	if err := s.DeleteGroup(context.Background(), sub.ID, apitypes.ChildrenReparent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDiscovered(context.Background(), 999, []Found{{IP: netip.MustParseAddr("10.0.2.7"), At: at}}); err != nil {
		t.Fatal(err)
	}
	for _, h := range s.Hosts() {
		if h.IP == "10.0.2.7" && *h.ParentID != elsewhere.ID {
			t.Errorf("fallback placement: %+v", h)
		}
	}
	// A failing repository adds nothing.
	repo.fail = errors.New("disk full")
	if _, err := s.AddDiscovered(context.Background(), wide.ID, []Found{{IP: netip.MustParseAddr("10.0.0.77"), At: at}}); err == nil {
		t.Fatal("no error")
	}
	for _, h := range s.Hosts() {
		if h.IP == "10.0.0.77" {
			t.Fatal("host kept after a failed save")
		}
	}
}

// TC-405 (inventory part): SetPTRName and the reset on an IP change.
func TestPTRName(t *testing.T) {
	s, repo := newSvc(t)
	r := &recorder{}
	s.SetNotifier(r)
	h := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1"})
	r.take()
	ip := netip.MustParseAddr("10.0.0.1")
	if err := s.SetPTRName(context.Background(), h.ID, ip, "nas.example."); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Host(h.ID)
	if got.PTRName != "nas.example." || got.DisplayName != "nas.example" || repo.hosts[h.ID].PTRName != "nas.example." {
		t.Fatalf("after set: %+v", got)
	}
	if ev := r.take(); ev != "host.updated 1 unknown" {
		t.Fatalf("events: %s", ev)
	}
	// Same name, a stale IP, or an unknown host: nothing happens.
	s.SetPTRName(context.Background(), h.ID, ip, "nas.example.")
	s.SetPTRName(context.Background(), h.ID, netip.MustParseAddr("10.0.0.2"), "x.")
	s.SetPTRName(context.Background(), 99, ip, "x.")
	if ev := r.take(); ev != "" {
		t.Fatalf("no-ops emitted %s", ev)
	}
	got, _ = s.UpdateHost(context.Background(), h.ID, apitypes.HostPatch{IP: apitypes.Some("10.0.0.2")})
	if got.PTRName != "" || got.DisplayName != "10.0.0.2" {
		t.Fatalf("after IP change: %+v", got)
	}
	if ts := s.DNSTargets(); len(ts) != 1 || ts[0].IP.String() != "10.0.0.2" {
		t.Fatalf("targets %+v", ts)
	}
}
