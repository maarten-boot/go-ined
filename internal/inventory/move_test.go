package inventory

import (
	"math"
	"slices"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-715: a group moves with everything in it, in one transaction, with
// group.updated plus one group.moved.
func TestMoveGroup(t *testing.T) {
	s, repo := newSvc(t)
	rec := &recorder{}
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", X: ptr(100.0), Y: ptr(100.0)})
	sub := mustGroup(t, s, apitypes.GroupCreate{Name: "sub", ParentID: &g.ID, X: ptr(150.0), Y: ptr(200.0)})
	h1 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &sub.ID, X: ptr(150.0), Y: ptr(300.0)})
	h2 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &g.ID, X: ptr(250.0), Y: ptr(200.0)})
	other := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.3", X: ptr(500.0), Y: ptr(500.0)})
	s.SetNotifier(rec)
	applies := repo.applies

	got, err := s.MoveGroup(ctx, g.ID, 10, -20)
	if err != nil || got.X != 110 || got.Y != 80 {
		t.Fatalf("move: %+v %v", got, err)
	}
	if repo.applies != applies+1 {
		t.Errorf("%d transactions, want 1", repo.applies-applies)
	}
	for _, c := range []struct {
		id   int64
		x, y float64
	}{{h1.ID, 160, 280}, {h2.ID, 260, 180}, {other.ID, 500, 500}} {
		if h, _ := s.Host(c.id); h.X != c.x || h.Y != c.y {
			t.Errorf("host %d at %g,%g, want %g,%g", c.id, h.X, h.Y, c.x, c.y)
		}
	}
	if gr, _ := s.Group(sub.ID); gr.X != 160 || gr.Y != 180 {
		t.Errorf("subgroup at %g,%g", gr.X, gr.Y)
	}
	if ev := rec.take(); ev != "group.updated 1 unknown; group.moved apitypes.GroupMovedEvent" {
		t.Errorf("events: %s", ev)
	}

	_, err = s.MoveGroup(ctx, 99, 1, 1)
	wantKind(t, err, NotFound)
	_, err = s.MoveGroup(ctx, g.ID, math.Inf(1), 0)
	wantKind(t, err, Invalid)

	// In a custom view the placements move; the network positions don't.
	v := mustView(t, s, "Payroll")
	cg := mustGroup(t, s, apitypes.GroupCreate{Name: "app", ViewID: &v.ID, X: ptr(0.0), Y: ptr(0.0)})
	mustPlace(t, s, v.ID, h1.ID, apitypes.PlacementSet{ParentID: &cg.ID, X: ptr(10.0), Y: ptr(10.0)})
	if _, err := s.MoveGroup(ctx, cg.ID, 5, 5); err != nil {
		t.Fatal(err)
	}
	m, _ := s.MapOf(v.ID)
	for _, it := range m.Items {
		if it.Type == "host" && (it.X != 15 || it.Y != 15) {
			t.Errorf("placement at %g,%g", it.X, it.Y)
		}
	}
	if h, _ := s.Host(h1.ID); h.X != 160 {
		t.Errorf("network position changed: %g", h.X)
	}
}

// TC-716: a new parent without a position gives a free position near it,
// and a group's contents move along; with x/y they are kept.
func TestReparentPosition(t *testing.T) {
	s, _ := newSvc(t)
	rec := &recorder{}
	a := mustGroup(t, s, apitypes.GroupCreate{Name: "a", X: ptr(1000.0), Y: ptr(1000.0)})
	b := mustGroup(t, s, apitypes.GroupCreate{Name: "b", X: ptr(100.0), Y: ptr(100.0)})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", X: ptr(5.0), Y: ptr(5.0)})

	h2, err := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&a.ID)})
	if err != nil || h2.X != 1000 || h2.Y != 1000+childDY {
		t.Fatalf("host next to its new parent: %+v %v", h2, err)
	}
	h3, _ := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&b.ID), X: apitypes.Some(7.0), Y: apitypes.Some(8.0)})
	if h3.X != 7 || h3.Y != 8 {
		t.Errorf("given position not kept: %g,%g", h3.X, h3.Y)
	}
	h4, _ := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&b.ID)})
	if h4.X != 7 || h4.Y != 8 {
		t.Errorf("same parent moved the host: %g,%g", h4.X, h4.Y)
	}

	// Group b (with host h at 7,8) moves into a: next to a, h along.
	s.SetNotifier(rec)
	g2, err := s.UpdateGroup(ctx, b.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&a.ID)})
	if err != nil || g2.X != 1000 || g2.Y != 1000+childDY {
		t.Fatalf("group next to its new parent: %+v %v", g2, err)
	}
	dx, dy := g2.X-100, g2.Y-100
	if hh, _ := s.Host(h.ID); hh.X != 7+dx || hh.Y != 8+dy {
		t.Errorf("content didn't move along: %g,%g", hh.X, hh.Y)
	}
	if ev := rec.take(); ev != "group.updated 2 unknown; group.moved apitypes.GroupMovedEvent" {
		t.Errorf("events: %s", ev)
	}
	// An empty group moving: no group.moved.
	e := mustGroup(t, s, apitypes.GroupCreate{Name: "empty"})
	rec.take()
	if _, err := s.UpdateGroup(ctx, e.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&a.ID)}); err != nil {
		t.Fatal(err)
	}
	if ev := rec.take(); ev != "group.updated 3 unknown" {
		t.Errorf("empty group events: %s", ev)
	}
}

// TC-717: host JSON lists the views the host is placed in.
func TestHostViews(t *testing.T) {
	s, _ := newSvc(t)
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1"})
	if got, _ := s.Host(h.ID); !slices.Equal(got.Views, []int64{1}) {
		t.Fatalf("views: %v", got.Views)
	}
	b := mustView(t, s, "B")
	a := mustView(t, s, "A")
	if _, err := s.UpdateView(ctx, a.ID, apitypes.ViewPatch{Order: apitypes.Some(int64(-5))}); err != nil {
		t.Fatal(err)
	}
	mustPlace(t, s, b.ID, h.ID, apitypes.PlacementSet{})
	mustPlace(t, s, a.ID, h.ID, apitypes.PlacementSet{})
	if got, _ := s.Host(h.ID); !slices.Equal(got.Views, []int64{1, a.ID, b.ID}) {
		t.Errorf("views in tab order, network first: %v", got.Views)
	}
	if err := s.UnplaceHost(ctx, b.ID, h.ID); err != nil {
		t.Fatal(err)
	}
	if hs := s.Hosts(); !slices.Equal(hs[0].Views, []int64{1, a.ID}) {
		t.Errorf("after unplace: %v", hs[0].Views)
	}
}

// Crosscheck 2026-11-11-03 D1, C6: a PATCH of the position moves only the
// group; moving an empty group emits no group.moved.
func TestPatchPositionMovesOnlyTheGroup(t *testing.T) {
	s, _ := newSvc(t)
	rec := &recorder{}
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", X: ptr(100.0), Y: ptr(100.0)})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &g.ID, X: ptr(100.0), Y: ptr(220.0)})
	empty := mustGroup(t, s, apitypes.GroupCreate{Name: "e"})
	s.SetNotifier(rec)
	if _, err := s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{X: apitypes.Some(300.0)}); err != nil {
		t.Fatal(err)
	}
	if hh, _ := s.Host(h.ID); hh.X != 100 {
		t.Errorf("contents moved by a position PATCH: %g", hh.X)
	}
	if _, err := s.MoveGroup(ctx, empty.ID, 5, 5); err != nil {
		t.Fatal(err)
	}
	if ev := rec.take(); ev != "group.updated 1 unknown; group.updated 2 unknown" {
		t.Errorf("events: %s", ev)
	}
	// A new parent with an explicit position still takes the contents along.
	other := mustGroup(t, s, apitypes.GroupCreate{Name: "o"})
	if _, err := s.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&other.ID), X: apitypes.Some(310.0), Y: apitypes.Some(100.0)}); err != nil {
		t.Fatal(err)
	}
	if hh, _ := s.Host(h.ID); hh.X != 110 {
		t.Errorf("contents didn't follow a new parent: %g", hh.X)
	}
}

// TC-725: moving only the contents leaves the group where it is.
func TestMoveGroupContents(t *testing.T) {
	s, _ := newSvc(t)
	rec := &recorder{}
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", X: ptr(100.0), Y: ptr(100.0)})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &g.ID, X: ptr(100.0), Y: ptr(220.0)})
	empty := mustGroup(t, s, apitypes.GroupCreate{Name: "e", X: ptr(5.0), Y: ptr(5.0)})
	s.SetNotifier(rec)
	got, err := s.MoveGroupContents(ctx, g.ID, -30, 10)
	if err != nil || got.X != 100 || got.Y != 100 {
		t.Fatalf("group moved: %+v %v", got, err)
	}
	if hh, _ := s.Host(h.ID); hh.X != 70 || hh.Y != 230 {
		t.Errorf("contents at %g,%g", hh.X, hh.Y)
	}
	if ev := rec.take(); ev != "group.moved apitypes.GroupMovedEvent" {
		t.Errorf("events: %s", ev)
	}
	if _, err := s.MoveGroupContents(ctx, empty.ID, 1, 1); err != nil || rec.take() != "" {
		t.Errorf("empty group: %v", err)
	}
	_, err = s.MoveGroupContents(ctx, 99, 1, 1)
	wantKind(t, err, NotFound)
}
