package inventory

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

func mustView(t *testing.T, s *Service, name string) apitypes.View {
	t.Helper()
	v, err := s.CreateView(ctx, apitypes.ViewCreate{Name: name})
	if err != nil {
		t.Fatalf("CreateView(%q): %v", name, err)
	}
	return v
}

func mustPlace(t *testing.T, s *Service, view, host int64, in apitypes.PlacementSet) apitypes.Placement {
	t.Helper()
	p, err := s.PlaceHost(ctx, view, host, in)
	if err != nil {
		t.Fatalf("PlaceHost(%d, %d): %v", view, host, err)
	}
	return p
}

// TC-120: views: the network view, create, rename, reorder, list order,
// names, delete rules, ids not reused.
func TestViews(t *testing.T) {
	s, r := newSvc(t)
	vs := s.Views()
	if len(vs) != 1 || vs[0].ID != 1 || vs[0].Kind != apitypes.ViewNetwork || vs[0].Name != "Networks" {
		t.Fatalf("initial views: %+v", vs)
	}
	a := mustView(t, s, " Payroll ")
	b := mustView(t, s, "Lab")
	if a.ID != 2 || a.Name != "Payroll" || a.Kind != apitypes.ViewCustom || a.Order != 1 || b.Order != 2 {
		t.Fatalf("created: %+v %+v", a, b)
	}
	_, err := s.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"})
	wantKind(t, err, Conflict)
	_, err = s.CreateView(ctx, apitypes.ViewCreate{Name: "  "})
	wantKind(t, err, Invalid)
	_, err = s.CreateView(ctx, apitypes.ViewCreate{Name: strings.Repeat("x", 256)})
	wantKind(t, err, Invalid)

	// Reorder: Lab first (order -1), then Networks (0), then Payroll (1).
	if _, err := s.UpdateView(ctx, b.ID, apitypes.ViewPatch{Order: apitypes.Some(int64(-1))}); err != nil {
		t.Fatal(err)
	}
	if v, err := s.UpdateView(ctx, 1, apitypes.ViewPatch{Name: apitypes.Some("Nets")}); err != nil || v.Name != "Nets" {
		t.Fatalf("rename network view: %+v %v", v, err)
	}
	_, err = s.UpdateView(ctx, a.ID, apitypes.ViewPatch{Name: apitypes.Some("Nets")})
	wantKind(t, err, Conflict)
	_, err = s.UpdateView(ctx, 99, apitypes.ViewPatch{})
	wantKind(t, err, NotFound)
	var names []string
	for _, v := range s.Views() {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != "Lab,Nets,Payroll" {
		t.Fatalf("order: %v", names)
	}

	wantKind(t, s.DeleteView(ctx, 1), Conflict)
	wantKind(t, s.DeleteView(ctx, 99), NotFound)
	if err := s.DeleteView(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	// Ids are not reused after a restart.
	s2, err := NewService(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if c := mustView(t, s2, "New"); c.ID != 4 {
		t.Fatalf("view id %d, want 4", c.ID)
	}
	if v, _ := s2.View(1); v.Name != "Nets" {
		t.Fatalf("view 1 after restart: %+v", v)
	}
}

// TC-121: groups belong to one view.
func TestGroupsPerView(t *testing.T) {
	s, _ := newSvc(t)
	v := mustView(t, s, "Payroll")
	net := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.0.0/24"})
	if net.ViewID != 1 {
		t.Fatalf("default view: %+v", net)
	}
	app := mustGroup(t, s, apitypes.GroupCreate{Name: "app", ViewID: &v.ID})
	db := mustGroup(t, s, apitypes.GroupCreate{Name: "db", ViewID: &v.ID, ParentID: &app.ID})
	if app.ViewID != v.ID || db.ViewID != v.ID {
		t.Fatalf("view ids: %+v %+v", app, db)
	}
	_, err := s.CreateGroup(ctx, apitypes.GroupCreate{Name: "x", ViewID: &v.ID, ParentID: &net.ID})
	wantKind(t, err, Invalid)
	_, err = s.CreateGroup(ctx, apitypes.GroupCreate{Name: "x", ParentID: &app.ID}) // view 1, parent in view 2
	wantKind(t, err, Invalid)
	_, err = s.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.1.0.0/24", ViewID: &v.ID})
	wantKind(t, err, Invalid)
	_, err = s.CreateGroup(ctx, apitypes.GroupCreate{Name: "x", ViewID: ptr(int64(42))})
	wantKind(t, err, Invalid)
	_, err = s.UpdateGroup(ctx, app.ID, apitypes.GroupPatch{CIDR: apitypes.Some("10.2.0.0/24")})
	wantKind(t, err, Invalid)
	_, err = s.UpdateGroup(ctx, app.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&net.ID)})
	wantKind(t, err, Invalid)
	_, err = s.UpdateGroup(ctx, app.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&db.ID)})
	wantKind(t, err, Conflict) // cycle within the view
	wantKind(t, s.DeleteGroup(ctx, app.ID, ""), Conflict)

	gs, err := s.GroupsIn(v.ID)
	if err != nil || len(gs) != 2 || gs[0].ID != app.ID {
		t.Fatalf("GroupsIn: %+v %v", gs, err)
	}
	if _, err := s.GroupsIn(99); KindOf(err) != NotFound {
		t.Fatalf("GroupsIn(99): %v", err)
	}
	if len(s.Groups()) != 3 {
		t.Fatal("Groups lists every view")
	}
	// A new ordinary group at the root of a custom view isn't placed on top
	// of the network view's root items: positions are per view.
	other := mustGroup(t, s, apitypes.GroupCreate{Name: "web", ViewID: &v.ID})
	if other.X == app.X && other.Y == app.Y {
		t.Fatal("free position ignores the view's own items")
	}
}

// TC-122: placements in custom views.
func TestPlacements(t *testing.T) {
	s, _ := newSvc(t)
	v := mustView(t, s, "Payroll")
	app := mustGroup(t, s, apitypes.GroupCreate{Name: "app", ViewID: &v.ID})
	lab := mustGroup(t, s, apitypes.GroupCreate{Name: "lab", ViewID: &v.ID})
	net := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.0.0/24"})
	h := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.5"})
	h2 := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.6"})
	if m, _ := s.MapOf(v.ID); len(m.Items) != 2 || m.ViewID != v.ID {
		t.Fatalf("new hosts must not appear in a custom view: %+v", m)
	}

	p := mustPlace(t, s, v.ID, h.ID, apitypes.PlacementSet{ParentID: &app.ID})
	if *p.ParentID != app.ID || (p.X == 0 && p.Y == 0) {
		t.Fatalf("placement: %+v", p)
	}
	// Same parent without x/y keeps the position; explicit x/y sets it.
	if q := mustPlace(t, s, v.ID, h.ID, apitypes.PlacementSet{ParentID: &app.ID}); q.X != p.X || q.Y != p.Y {
		t.Fatalf("position not kept: %+v", q)
	}
	q := mustPlace(t, s, v.ID, h.ID, apitypes.PlacementSet{ParentID: &lab.ID, X: ptr(5.0), Y: ptr(6.0)})
	if *q.ParentID != lab.ID || q.X != 5 || q.Y != 6 {
		t.Fatalf("move: %+v", q)
	}
	if r := mustPlace(t, s, v.ID, h2.ID, apitypes.PlacementSet{}); r.ParentID != nil {
		t.Fatalf("root placement: %+v", r)
	}
	m, _ := s.MapOf(v.ID)
	hostItems := 0
	for _, it := range m.Items {
		if it.Type == apitypes.TypeHost {
			hostItems++
			if it.ID == h.ID && (*it.ParentID != lab.ID || it.X != 5) {
				t.Fatalf("map item: %+v", it)
			}
		}
	}
	if hostItems != 2 {
		t.Fatalf("map: %+v", m)
	}
	// The network placement is untouched.
	if hh, _ := s.Host(h.ID); *hh.ParentID != net.ID {
		t.Fatalf("network placement changed: %+v", hh)
	}

	_, err := s.PlaceHost(ctx, v.ID, h.ID, apitypes.PlacementSet{ParentID: &net.ID})
	wantKind(t, err, Invalid)
	_, err = s.PlaceHost(ctx, 1, h.ID, apitypes.PlacementSet{})
	wantKind(t, err, Invalid)
	_, err = s.PlaceHost(ctx, 99, h.ID, apitypes.PlacementSet{})
	wantKind(t, err, NotFound)
	_, err = s.PlaceHost(ctx, v.ID, 99, apitypes.PlacementSet{})
	wantKind(t, err, NotFound)
	_, err = s.PlaceHost(ctx, v.ID, h.ID, apitypes.PlacementSet{X: ptr(1.0)})
	wantKind(t, err, Invalid)
	_, err = s.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.0.7", ParentID: &app.ID})
	wantKind(t, err, Invalid)
	_, err = s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&app.ID)})
	wantKind(t, err, Invalid)

	if err := s.UnplaceHost(ctx, v.ID, h2.ID); err != nil {
		t.Fatal(err)
	}
	wantKind(t, s.UnplaceHost(ctx, v.ID, h2.ID), NotFound)
	wantKind(t, s.UnplaceHost(ctx, 1, h.ID), Invalid)
	if _, err := s.MapOf(99); KindOf(err) != NotFound {
		t.Fatalf("MapOf(99): %v", err)
	}
	// Map() is the network view.
	if m := s.Map(); m.ViewID != 1 || len(m.Items) != 3 {
		t.Fatalf("Map: %+v", m)
	}
}

// TC-123: deleting in custom views never deletes hosts.
func TestCustomViewDeletes(t *testing.T) {
	s, r := newSvc(t)
	rec := &recorder{}
	s.SetNotifier(rec)
	v := mustView(t, s, "Payroll")
	app := mustGroup(t, s, apitypes.GroupCreate{Name: "app", ViewID: &v.ID})
	sub := mustGroup(t, s, apitypes.GroupCreate{Name: "sub", ViewID: &v.ID, ParentID: &app.ID})
	h1 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1"})
	h2 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.2"})
	mustPlace(t, s, v.ID, h1.ID, apitypes.PlacementSet{ParentID: &app.ID})
	mustPlace(t, s, v.ID, h2.ID, apitypes.PlacementSet{ParentID: &sub.ID})
	rec.take()

	// Reparent: sub's placement moves up to app.
	if err := s.DeleteGroup(ctx, sub.ID, apitypes.ChildrenReparent); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); got != "placement.updated 2/2; group.deleted 2" {
		t.Fatalf("reparent events: %s", got)
	}
	if p := r.placements[PlacementKey{v.ID, h2.ID}]; p.ParentID != app.ID {
		t.Fatalf("reparented placement: %+v", p)
	}
	// Cascade: placements go, hosts stay.
	sub = mustGroup(t, s, apitypes.GroupCreate{Name: "sub", ViewID: &v.ID, ParentID: &app.ID})
	rec.take()
	if err := s.DeleteGroup(ctx, app.ID, apitypes.ChildrenCascade); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); got != "placement.deleted 2/1; placement.deleted 2/2; group.deleted 1; group.deleted 3" {
		t.Fatalf("cascade events: %s", got)
	}
	if len(s.Hosts()) != 2 || len(r.placements) != 0 || len(r.hosts) != 2 {
		t.Fatalf("hosts must stay: %d hosts, %d placements", len(s.Hosts()), len(r.placements))
	}

	// Deleting a view removes its groups and placements, with one event.
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", ViewID: &v.ID})
	mustPlace(t, s, v.ID, h1.ID, apitypes.PlacementSet{ParentID: &g.ID})
	rec.take()
	if err := s.DeleteView(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); got != "view.deleted 2" {
		t.Fatalf("view delete events: %s", got)
	}
	if len(r.groups) != 0 || len(r.placements) != 0 || len(r.views) != 1 || len(s.Hosts()) != 2 {
		t.Fatalf("after view delete: %+v %+v %+v", r.groups, r.placements, r.views)
	}

	// Deleting a host removes its placements everywhere, with only host.deleted.
	v2 := mustView(t, s, "A")
	v3 := mustView(t, s, "B")
	mustPlace(t, s, v2.ID, h1.ID, apitypes.PlacementSet{})
	mustPlace(t, s, v3.ID, h1.ID, apitypes.PlacementSet{})
	rec.take()
	if err := s.DeleteHost(ctx, h1.ID); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); got != "host.deleted 1" {
		t.Fatalf("host delete events: %s", got)
	}
	if len(r.placements) != 0 {
		t.Fatalf("placements left: %+v", r.placements)
	}
	if m, _ := s.MapOf(v2.ID); len(m.Items) != 0 {
		t.Fatalf("map after host delete: %+v", m)
	}

	// Cascade in the network view deletes hosts and, silently, their
	// placements.
	net := mustGroup(t, s, apitypes.GroupCreate{CIDR: "198.51.100.0/24"})
	h3 := mustHost(t, s, apitypes.HostCreate{IP: "198.51.100.3"})
	mustPlace(t, s, v2.ID, h3.ID, apitypes.PlacementSet{})
	rec.take()
	if err := s.DeleteGroup(ctx, net.ID, apitypes.ChildrenCascade); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); got != "host.deleted 3; group.deleted 5" {
		t.Fatalf("network cascade events: %s", got)
	}
	if len(r.placements) != 0 {
		t.Fatalf("placements left: %+v", r.placements)
	}
}

// TC-124: status and visibility per view.
func TestStatusPerView(t *testing.T) {
	s, _ := newSvc(t)
	rec := &recorder{}
	s.SetNotifier(rec)
	v := mustView(t, s, "Payroll")
	app := mustGroup(t, s, apitypes.GroupCreate{Name: "app", ViewID: &v.ID})
	inner := mustGroup(t, s, apitypes.GroupCreate{Name: "inner", ViewID: &v.ID, ParentID: &app.ID})
	net := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.0.0.0/24"})
	h := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.5"})
	other := mustHost(t, s, apitypes.HostCreate{IP: "10.0.0.6"})
	observe(t, s, Observation{HostID: h.ID, IP: netip.MustParseAddr(h.IP), Status: apitypes.StatusUp, Replied: true, At: time.Now()},
		Observation{HostID: other.ID, IP: netip.MustParseAddr("10.0.0.6"), Status: apitypes.StatusDown, At: time.Now()})
	rec.take()

	// Only placed hosts count: the custom groups are unknown (empty).
	if g, _ := s.Group(app.ID); g.Status != apitypes.StatusUnknown {
		t.Fatalf("empty custom group: %s", g.Status)
	}
	mustPlace(t, s, v.ID, h.ID, apitypes.PlacementSet{ParentID: &inner.ID})
	if got := rec.take(); got != "placement.updated 2/1; group.status 1 up; group.status 2 up" {
		t.Fatalf("place events: %s", got)
	}
	// h goes down: its groups in both views change.
	observe(t, s, Observation{HostID: h.ID, IP: netip.MustParseAddr(h.IP), Status: apitypes.StatusDown, At: time.Now()})
	if got := rec.take(); got != "host.status 1 down; group.status 1 down; group.status 2 down" {
		t.Fatalf("status events: %s (network group %d was already down)", got, net.ID)
	}
	gs, _ := s.GroupsIn(v.ID)
	if gs[0].Status != apitypes.StatusDown || gs[1].Status != apitypes.StatusDown {
		t.Fatalf("custom statuses: %+v", gs)
	}
	// Visibility per view: collapsing app hides inner's host in view 2 only.
	snap := s.Snapshot()
	if snap.In(v.ID).Visible(inner.ID) {
		t.Fatal("app is collapsed by default; inner's contents must be hidden")
	}
	s.UpdateGroup(ctx, app.ID, apitypes.GroupPatch{Collapsed: apitypes.Some(false)})
	s.UpdateGroup(ctx, inner.ID, apitypes.GroupPatch{Collapsed: apitypes.Some(false)})
	if !s.Snapshot().In(v.ID).Visible(inner.ID) {
		t.Fatal("expanded: visible")
	}
	// outside_cidr is a network-view notion.
	m, _ := s.MapOf(v.ID)
	for _, it := range m.Items {
		if it.OutsideCIDR {
			t.Fatalf("outside_cidr in a custom view: %+v", it)
		}
	}
}

// TC-128: the load repair covers views and placements.
func TestRepairViews(t *testing.T) {
	r := newMemRepo()
	now := time.Now()
	r.views[1] = View{ID: 1, Name: "Networks", Kind: apitypes.ViewNetwork, CreatedAt: now}
	r.views[2] = View{ID: 2, Name: "A", Kind: apitypes.ViewCustom, CreatedAt: now}
	r.groups[1] = Group{ID: 1, Name: "n1", ViewID: 1}
	r.groups[2] = Group{ID: 2, Name: "lost", ViewID: 9}                // view missing
	r.groups[3] = Group{ID: 3, Name: "cross", ViewID: 2, ParentID: 1} // parent in view 1
	r.groups[4] = Group{ID: 4, Name: "c4", ViewID: 2}
	r.groups[5] = Group{ID: 5, Name: "10.0.0.0/8", ViewID: 2, CIDR: netip.MustParsePrefix("10.0.0.0/8")}
	r.hosts[1] = Host{ID: 1, IP: netip.MustParseAddr("192.0.2.1"), ParentID: 4, Status: "unknown"} // custom group
	r.hosts[2] = Host{ID: 2, IP: netip.MustParseAddr("192.0.2.2"), ParentID: 1, Status: "unknown"}
	r.placements[PlacementKey{2, 1}] = Placement{ViewID: 2, HostID: 1, ParentID: 1} // parent in view 1
	r.placements[PlacementKey{2, 2}] = Placement{ViewID: 2, HostID: 2, ParentID: 4} // fine
	r.placements[PlacementKey{2, 7}] = Placement{ViewID: 2, HostID: 7}              // host missing
	r.placements[PlacementKey{8, 2}] = Placement{ViewID: 8, HostID: 2}              // view missing
	s, err := NewService(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(s.LoadWarnings(), "\n")
	for _, want := range []string{"group 2: view 9", "group 5: a network group", "group 3: parent group 1 is in another view",
		"host 1: parent group 4 is not in the network view", "host 1 in view 2: parent group 1",
		"host 7 in view 2", "host 2 in view 8"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q:\n%s", want, w)
		}
	}
	if r.groups[2].ViewID != 1 || r.groups[3].ParentID != 0 || r.groups[5].ViewID != 1 || r.hosts[1].ParentID != 0 ||
		r.placements[PlacementKey{2, 1}].ParentID != 0 || r.placements[PlacementKey{2, 2}].ParentID != 4 || len(r.placements) != 2 {
		t.Fatalf("repairs: groups %+v hosts %+v placements %+v", r.groups, r.hosts, r.placements)
	}
	if s2, _ := NewService(ctx, r); len(s2.LoadWarnings()) != 0 {
		t.Errorf("warnings repeated: %q", s2.LoadWarnings())
	}
}
