package inventory

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// memRepo is an in-memory Repository for unit tests.
type memRepo struct {
	groups map[int64]Group
	hosts  map[int64]Host
	fail   error
}

func newMemRepo() *memRepo { return &memRepo{groups: map[int64]Group{}, hosts: map[int64]Host{}} }

func (r *memRepo) Load(context.Context) ([]Group, []Host, error) {
	var gs []Group
	var hs []Host
	for _, g := range r.groups {
		gs = append(gs, g)
	}
	for _, h := range r.hosts {
		hs = append(hs, h)
	}
	return gs, hs, nil
}

func (r *memRepo) Apply(_ context.Context, c Changes) error {
	if r.fail != nil {
		return r.fail
	}
	for _, id := range c.DeleteHosts {
		delete(r.hosts, id)
	}
	for _, id := range c.DeleteGroups {
		delete(r.groups, id)
	}
	for _, g := range c.Groups {
		r.groups[g.ID] = g
	}
	for _, h := range c.Hosts {
		r.hosts[h.ID] = h
	}
	return nil
}

var ctx = context.Background()

func newSvc(t *testing.T) (*Service, *memRepo) {
	t.Helper()
	r := newMemRepo()
	s, err := NewService(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func ptr[T any](v T) *T { return &v }

func mustGroup(t *testing.T, s *Service, in apitypes.GroupCreate) apitypes.Group {
	t.Helper()
	g, err := s.CreateGroup(ctx, in)
	if err != nil {
		t.Fatalf("CreateGroup(%+v): %v", in, err)
	}
	return g
}

func mustHost(t *testing.T, s *Service, in apitypes.HostCreate) apitypes.Host {
	t.Helper()
	h, err := s.CreateHost(ctx, in)
	if err != nil {
		t.Fatalf("CreateHost(%+v): %v", in, err)
	}
	return h
}

func wantKind(t *testing.T, err error, k Kind) {
	t.Helper()
	if KindOf(err) != k {
		t.Fatalf("want error kind %d, got %v", k, err)
	}
}

// TC-101: duplicate IP is a conflict; IPv4-mapped IPv6 counts as the same IP.
func TestDuplicateIP(t *testing.T) {
	s, _ := newSvc(t)
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.1"})
	_, err := s.CreateHost(ctx, apitypes.HostCreate{IP: "::ffff:10.0.0.1"})
	wantKind(t, err, Conflict)
	_, err = s.CreateHost(ctx, apitypes.HostCreate{IP: "not-an-ip"})
	wantKind(t, err, Invalid)
	_, err = s.CreateHost(ctx, apitypes.HostCreate{IP: "fe80::1%eth0"})
	wantKind(t, err, Invalid)
}

// TC-102: an item has one parent; moving changes it; null moves to the root.
func TestMoveHost(t *testing.T) {
	s, _ := newSvc(t)
	a := mustGroup(t, s, apitypes.GroupCreate{Name: "a"})
	b := mustGroup(t, s, apitypes.GroupCreate{Name: "b"})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &a.ID})
	h2, err := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&b.ID)})
	if err != nil || *h2.ParentID != b.ID {
		t.Fatalf("move: %v %+v", err, h2.ParentID)
	}
	h3, err := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some[*int64](nil)})
	if err != nil || h3.ParentID != nil {
		t.Fatalf("move to root: %v %+v", err, h3.ParentID)
	}
	_, err = s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(ptr(int64(999)))})
	wantKind(t, err, Invalid)
}

// TC-103: no cycles.
func TestNoCycles(t *testing.T) {
	s, _ := newSvc(t)
	a := mustGroup(t, s, apitypes.GroupCreate{Name: "a"})
	b := mustGroup(t, s, apitypes.GroupCreate{Name: "b", ParentID: &a.ID})
	c := mustGroup(t, s, apitypes.GroupCreate{Name: "c", ParentID: &b.ID})
	for _, target := range []int64{a.ID, b.ID, c.ID} {
		_, err := s.UpdateGroup(ctx, a.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&target)})
		wantKind(t, err, Conflict)
	}
	if _, err := s.UpdateGroup(ctx, c.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&a.ID)}); err != nil {
		t.Fatalf("legal move: %v", err)
	}
}

// TC-104: delete modes.
func TestDeleteGroupModes(t *testing.T) {
	s, r := newSvc(t)
	top := mustGroup(t, s, apitypes.GroupCreate{Name: "top"})
	mid := mustGroup(t, s, apitypes.GroupCreate{Name: "mid", ParentID: &top.ID})
	sub := mustGroup(t, s, apitypes.GroupCreate{Name: "sub", ParentID: &mid.ID})
	h1 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &mid.ID})
	h2 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &sub.ID})

	wantKind(t, s.DeleteGroup(ctx, mid.ID, ""), Conflict)
	wantKind(t, s.DeleteGroup(ctx, mid.ID, "bogus"), Invalid)
	wantKind(t, s.DeleteGroup(ctx, 999, ""), NotFound)

	if err := s.DeleteGroup(ctx, mid.ID, apitypes.ChildrenReparent); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Host(h1.ID); *h.ParentID != top.ID {
		t.Errorf("reparent host: parent = %v", h.ParentID)
	}
	if g, _ := s.Group(sub.ID); *g.ParentID != top.ID {
		t.Errorf("reparent group: parent = %v", g.ParentID)
	}
	if r.hosts[h1.ID].ParentID != top.ID {
		t.Error("reparent not persisted")
	}

	if err := s.DeleteGroup(ctx, top.ID, apitypes.ChildrenCascade); err != nil {
		t.Fatal(err)
	}
	if len(s.Hosts()) != 0 || len(s.Groups()) != 0 {
		t.Errorf("cascade left %d hosts, %d groups", len(s.Hosts()), len(s.Groups()))
	}
	if len(r.hosts) != 0 || len(r.groups) != 0 {
		t.Error("cascade not persisted")
	}
	_ = h2

	empty := mustGroup(t, s, apitypes.GroupCreate{Name: "empty"})
	if err := s.DeleteGroup(ctx, empty.ID, ""); err != nil {
		t.Errorf("delete empty group: %v", err)
	}
}

// TC-105: status aggregation (SPEC-105, SPEC-306).
func TestAggregate(t *testing.T) {
	m := NewModel()
	m.Groups[1] = &Group{ID: 1, Name: "top"}
	m.Groups[2] = &Group{ID: 2, Name: "net", ParentID: 1, CIDR: netip.MustParsePrefix("10.0.0.0/24")}
	m.Groups[3] = &Group{ID: 3, Name: "empty", ParentID: 1}
	m.Groups[4] = &Group{ID: 4, Name: "offonly"}
	add := func(id, parent int64, st string, mon bool) {
		m.Hosts[id] = &Host{ID: id, ParentID: parent, Status: st, Monitored: mon}
	}
	add(1, 2, apitypes.StatusUp, true)
	add(2, 2, apitypes.StatusUp, true)
	add(3, 1, apitypes.StatusUnknown, true)
	add(4, 2, apitypes.StatusDown, false) // unmonitored: ignored for status
	add(5, 4, apitypes.StatusDown, false)

	agg := m.Aggregate()
	cases := []struct {
		id     int64
		status string
		c      Counts
	}{
		{2, apitypes.StatusUp, Counts{Up: 2, Off: 1}},
		{1, apitypes.StatusUnknown, Counts{Up: 2, Unknown: 1, Off: 1}},
		{3, apitypes.StatusUnknown, Counts{}},
		{4, apitypes.StatusUnknown, Counts{Off: 1}},
	}
	for _, c := range cases {
		if agg[c.id] != c.c || agg[c.id].Status() != c.status {
			t.Errorf("group %d: got %+v %s, want %+v %s", c.id, agg[c.id], agg[c.id].Status(), c.c, c.status)
		}
	}
	m.Hosts[1].Status = apitypes.StatusDown
	if st := m.Aggregate()[1].Status(); st != apitypes.StatusDown {
		t.Errorf("down must win, got %s", st)
	}
}

// TC-106: free positions near the parent, no overlap; explicit positions kept.
func TestFreePosition(t *testing.T) {
	s, _ := newSvc(t)
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", X: ptr(500.0), Y: ptr(300.0)})
	type xy struct{ x, y float64 }
	seen := map[xy]bool{}
	for i := 0; i < 25; i++ {
		h := mustHost(t, s, apitypes.HostCreate{IP: netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}).String(), ParentID: &g.ID})
		p := xy{h.X, h.Y}
		if seen[p] {
			t.Fatalf("position %v used twice", p)
		}
		seen[p] = true
		if h.Y < 300 || h.X < 500 {
			t.Errorf("host %d at %v, expected below/right of parent (500,300)", i, p)
		}
	}
	h := mustHost(t, s, apitypes.HostCreate{IP: "198.51.100.1", X: ptr(1.5), Y: ptr(2.5)})
	if h.X != 1.5 || h.Y != 2.5 {
		t.Errorf("explicit position lost: %v,%v", h.X, h.Y)
	}
	_, err := s.CreateHost(ctx, apitypes.HostCreate{IP: "198.51.100.2", X: ptr(1.0)})
	wantKind(t, err, Invalid)
}

// TC-107: CIDR must be valid, canonical and unique; it can be cleared.
func TestGroupCIDR(t *testing.T) {
	s, _ := newSvc(t)
	g := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.0.0/24"})
	if g.Name != "10.0.0.0/24" {
		t.Errorf("name should default to CIDR, got %q", g.Name)
	}
	_, err := s.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.0.0/24"})
	wantKind(t, err, Conflict)
	_, err = s.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.0.5/24"})
	if KindOf(err) != Invalid || !strings.Contains(err.Error(), "10.0.0.0/24") {
		t.Errorf("non-canonical: %v", err)
	}
	_, err = s.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.0.0/33"})
	wantKind(t, err, Invalid)
	_, err = s.CreateGroup(ctx, apitypes.GroupCreate{})
	wantKind(t, err, Invalid)
	mustGroup(t, s, apitypes.GroupCreate{CIDR: "2001:db8::/32"})

	g2, err := s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{CIDR: apitypes.Some("")})
	if err != nil || g2.CIDR != "" {
		t.Fatalf("clear CIDR: %v %q", err, g2.CIDR)
	}
	mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.0.0/24"}) // free again
}

// TC-108: placement of a manually added host without parent.
func TestPlacement(t *testing.T) {
	s, _ := newSvc(t)
	g8 := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.0.0/8"})
	g16 := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.1.0.0/16", ParentID: &g8.ID})
	g6 := mustGroup(t, s, apitypes.GroupCreate{CIDR: "2001:db8::/32"})
	cases := []struct {
		ip   string
		want *int64
	}{
		{"10.1.2.3", &g16.ID},
		{"10.2.0.1", &g8.ID},
		{"192.0.2.1", nil},
		{"2001:db8::5", &g6.ID},
		{"2001:db9::5", nil},
	}
	for _, c := range cases {
		h := mustHost(t, s, apitypes.HostCreate{IP: c.ip})
		if (h.ParentID == nil) != (c.want == nil) || (c.want != nil && *h.ParentID != *c.want) {
			t.Errorf("%s: parent %v, want %v", c.ip, h.ParentID, c.want)
		}
	}
	// An explicit parent wins over placement.
	other := mustGroup(t, s, apitypes.GroupCreate{Name: "other"})
	if h := mustHost(t, s, apitypes.HostCreate{IP: "10.1.9.9", ParentID: &other.ID}); *h.ParentID != other.ID {
		t.Error("explicit parent ignored")
	}
}

// TC-109: nested network groups must fit (SPEC-114).
func TestNesting(t *testing.T) {
	s, _ := newSvc(t)
	n16 := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.1.0.0/16"})
	plain := mustGroup(t, s, apitypes.GroupCreate{Name: "plain", ParentID: &n16.ID})

	// Inside through an ordinary group: OK. Outside: conflict.
	n24 := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.1.2.0/24", ParentID: &plain.ID})
	_, err := s.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.2.0.0/24", ParentID: &plain.ID})
	wantKind(t, err, Conflict)
	_, err = s.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "2001:db8::/64", ParentID: &n16.ID})
	wantKind(t, err, Conflict)

	// CIDR change of the child that breaks it.
	_, err = s.UpdateGroup(ctx, n24.ID, apitypes.GroupPatch{CIDR: apitypes.Some("10.9.0.0/24")})
	wantKind(t, err, Conflict)
	// CIDR change of the parent that breaks a descendant.
	_, err = s.UpdateGroup(ctx, n16.ID, apitypes.GroupPatch{CIDR: apitypes.Some("10.9.0.0/16")})
	wantKind(t, err, Conflict)
	if g, _ := s.Group(n16.ID); g.CIDR != "10.1.0.0/16" {
		t.Errorf("failed update changed state: %s", g.CIDR)
	}
	// Move a subtree containing a network group under a non-matching network.
	other := mustGroup(t, s, apitypes.GroupCreate{CIDR: "192.168.0.0/16"})
	_, err = s.UpdateGroup(ctx, plain.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&other.ID)})
	wantKind(t, err, Conflict)
	// Hosts may move anywhere.
	h := mustHost(t, s, apitypes.HostCreate{IP: "10.1.2.3"})
	if _, err := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&other.ID)}); err != nil {
		t.Errorf("host move: %v", err)
	}
	// Clearing the parent's CIDR is fine: n24 then has no enclosing network.
	if _, err := s.UpdateGroup(ctx, n16.ID, apitypes.GroupPatch{CIDR: apitypes.Some("")}); err != nil {
		t.Errorf("clear parent CIDR: %v", err)
	}
}

// TC-110: notes, with the 64 KiB limit.
func TestNotes(t *testing.T) {
	s, _ := newSvc(t)
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", Note: "group note"})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1"})
	h2, err := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{Note: apitypes.Some("line1\nline2")})
	if err != nil || h2.Note != "line1\nline2" {
		t.Fatalf("set: %v %q", err, h2.Note)
	}
	h3, _ := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{Note: apitypes.Some("")})
	if h3.Note != "" {
		t.Error("clear failed")
	}
	_, err = s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{Note: apitypes.Some(strings.Repeat("x", MaxNoteBytes+1))})
	wantKind(t, err, TooLarge)
	if _, err = s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{Note: apitypes.Some(strings.Repeat("x", MaxNoteBytes))}); err != nil {
		t.Errorf("exactly 64 KiB: %v", err)
	}
	hasNote := map[string]bool{}
	for _, it := range s.Map().Items {
		hasNote[it.Type+"/"+strconv.FormatInt(it.ID, 10)] = it.HasNote
	}
	if !hasNote["group/"+strconv.FormatInt(g.ID, 10)] || hasNote["host/"+strconv.FormatInt(h.ID, 10)] {
		t.Error("map has_note false for group with note")
	}
}

// TC-111: visibility under collapsed groups; new groups start collapsed.
func TestVisibility(t *testing.T) {
	m := NewModel()
	m.Groups[1] = &Group{ID: 1, Collapsed: false}
	m.Groups[2] = &Group{ID: 2, ParentID: 1, Collapsed: true}
	m.Groups[3] = &Group{ID: 3, ParentID: 2, Collapsed: false}
	m.Groups[4] = &Group{ID: 4, ParentID: 1, Collapsed: false}
	cases := map[int64]bool{Root: true, 1: true, 2: false, 3: false, 4: true}
	for parent, want := range cases {
		if got := m.Visible(parent); got != want {
			t.Errorf("Visible(parent=%d) = %v, want %v", parent, got, want)
		}
	}
	s, _ := newSvc(t)
	if g := mustGroup(t, s, apitypes.GroupCreate{Name: "n"}); !g.Collapsed {
		t.Error("new group not collapsed")
	}
	if g := mustGroup(t, s, apitypes.GroupCreate{Name: "o", Collapsed: ptr(false)}); g.Collapsed {
		t.Error("explicit collapsed=false ignored")
	}
}

// TC-206: outside_cidr flag (SPEC-115).
func TestOutsideCIDR(t *testing.T) {
	s, _ := newSvc(t)
	net := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.0.0/24"})
	plain := mustGroup(t, s, apitypes.GroupCreate{Name: "plain", ParentID: &net.ID})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1"})
	if h.OutsideCIDR {
		t.Error("root host flagged")
	}
	h, _ = s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&plain.ID)})
	if !h.OutsideCIDR {
		t.Error("host under plain group inside non-matching network not flagged")
	}
	h, _ = s.UpdateHost(ctx, h.ID, apitypes.HostPatch{IP: apitypes.Some("10.0.0.7")})
	if h.OutsideCIDR {
		t.Error("flag not cleared after IP fix")
	}
}

// TC-401: display name priority.
func TestDisplayName(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.1")
	cases := []struct {
		h    Host
		want string
	}{
		{Host{IP: ip, Name: "manual", PTRName: "ptr.example."}, "manual"},
		{Host{IP: ip, PTRName: "ptr.example."}, "ptr.example"},
		{Host{IP: ip}, "192.0.2.1"},
	}
	for _, c := range cases {
		if got := c.h.DisplayName(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

// A failing repository leaves the in-memory model unchanged.
func TestRepoFailureKeepsModel(t *testing.T) {
	s, r := newSvc(t)
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g"})
	r.fail = errors.New("disk full")
	if _, err := s.CreateGroup(ctx, apitypes.GroupCreate{Name: "x"}); err == nil {
		t.Fatal("want error")
	}
	if _, err := s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{Name: apitypes.Some("renamed")}); err == nil {
		t.Fatal("want error")
	}
	if len(s.Groups()) != 1 || s.Groups()[0].Name != "g" {
		t.Errorf("model changed: %+v", s.Groups())
	}
	r.fail = nil
	if g2 := mustGroup(t, s, apitypes.GroupCreate{Name: "y"}); g2.ID != g.ID+1 {
		t.Errorf("id skipped after failure: %d", g2.ID)
	}
}

// SPEC-603: loaded hosts are stale until pinged; new hosts are not.
func TestStaleOnLoad(t *testing.T) {
	s, r := newSvc(t)
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1"})
	if h.Stale {
		t.Error("new host stale")
	}
	s2, err := NewService(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if h2, _ := s2.Host(h.ID); !h2.Stale {
		t.Error("loaded host not stale")
	}
}
