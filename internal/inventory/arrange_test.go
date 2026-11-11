package inventory

import (
	"math"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-724: Arrange lays out a group's members on a grid of the given cell,
// in their rough order, with subgroups taking their contents along, in one
// transaction with one map.arranged event (SPEC-510).
func TestArrange(t *testing.T) {
	s, repo := newSvc(t)
	rec := &recorder{}
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", X: ptr(100.0), Y: ptr(100.0)})
	// Members at scattered spots: order by y, then x.
	h1 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &g.ID, X: ptr(300.0), Y: ptr(500.0)})
	h2 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &g.ID, X: ptr(50.0), Y: ptr(500.0)})
	sub := mustGroup(t, s, apitypes.GroupCreate{Name: "sub", ParentID: &g.ID, X: ptr(10.0), Y: ptr(10.0)})
	inner := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.3", ParentID: &sub.ID, X: ptr(10.0), Y: ptr(130.0)})
	h4 := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.4", ParentID: &g.ID, X: ptr(0.0), Y: ptr(900.0)})
	other := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.9", X: ptr(7.0), Y: ptr(7.0)})
	s.SetNotifier(rec)
	applies := repo.applies

	n, err := s.Arrange(ctx, NetworkView, &g.ID, 250, 90)
	if err != nil || n != 4 || repo.applies != applies+1 {
		t.Fatalf("arrange: %d %v, %d transactions", n, err, repo.applies-applies)
	}
	// 4 members: 2 columns, starting below the group (100, 220).
	want := map[int64][2]float64{} // host id -> position
	pos := func(id int64) [2]float64 { h, _ := s.Host(id); return [2]float64{h.X, h.Y} }
	gs, _ := s.Group(sub.ID)
	if gs.X != 100 || gs.Y != 220 {
		t.Errorf("sub (first by y) at %g,%g", gs.X, gs.Y)
	}
	want[h2.ID], want[h1.ID], want[h4.ID] = [2]float64{350, 220}, [2]float64{100, 310}, [2]float64{350, 310}
	for id, w := range want {
		if p := pos(id); p != w {
			t.Errorf("host %d at %v, want %v", id, p, w)
		}
	}
	if p := pos(inner.ID); p != [2]float64{100, 340} {
		t.Errorf("sub's host didn't move along: %v", p)
	}
	if p := pos(other.ID); p != [2]float64{7, 7} {
		t.Errorf("a host outside the group moved: %v", p)
	}
	if ev := rec.take(); ev != "map.arranged apitypes.MapArrangedEvent" {
		t.Errorf("events: %s", ev)
	}
	// Again: nothing moves, no event.
	if n, _ := s.Arrange(ctx, NetworkView, &g.ID, 250, 90); n != 0 || rec.take() != "" {
		t.Errorf("second arrange moved %d", n)
	}
	// The root with the normal grid (0 sizes): g and "other".
	if n, err := s.Arrange(ctx, NetworkView, nil, 0, 0); err != nil || n != 2 {
		t.Errorf("root: %d %v", n, err)
	}
	// "other" (y 7) comes before g (y 100): first cell; g second.
	if p := pos(other.ID); p != [2]float64{40, 40} {
		t.Errorf("root host at %v", p)
	}
	if gg, _ := s.Group(g.ID); gg.X != 40+gridDX || gg.Y != 40 {
		t.Errorf("root group at %g,%g", gg.X, gg.Y)
	}

	// Errors.
	for _, c := range []struct {
		view   int64
		parent *int64
		w, h   float64
		kind   Kind
	}{
		{99, nil, 0, 0, NotFound},
		{NetworkView, ptr(int64(999)), 0, 0, Invalid},
		{NetworkView, nil, 5, 80, Invalid},
		{NetworkView, nil, 100, 9000, Invalid},
		{NetworkView, nil, math.NaN(), 80, Invalid},
	} {
		_, err := s.Arrange(ctx, c.view, c.parent, c.w, c.h)
		wantKind(t, err, c.kind)
	}

	// A custom view: placements move, network positions don't.
	v := mustView(t, s, "Lab")
	cg := mustGroup(t, s, apitypes.GroupCreate{Name: "rack", ViewID: &v.ID, X: ptr(0.0), Y: ptr(0.0)})
	mustPlace(t, s, v.ID, h1.ID, apitypes.PlacementSet{ParentID: &cg.ID, X: ptr(500.0), Y: ptr(500.0)})
	mustPlace(t, s, v.ID, h2.ID, apitypes.PlacementSet{ParentID: &cg.ID, X: ptr(400.0), Y: ptr(500.0)})
	before := pos(h1.ID)
	if n, err := s.Arrange(ctx, v.ID, &cg.ID, 300, 80); err != nil || n != 2 {
		t.Fatalf("custom view: %d %v", n, err)
	}
	m, _ := s.MapOf(v.ID)
	for _, it := range m.Items {
		if it.Type == "host" && it.ID == h1.ID && (it.X != 300 || it.Y != 120) {
			t.Errorf("placement of h1 at %g,%g", it.X, it.Y)
		}
	}
	if pos(h1.ID) != before {
		t.Error("network position changed by a custom-view arrange")
	}
	// A group of another view as parent.
	_, err = s.Arrange(ctx, v.ID, &g.ID, 0, 0)
	wantKind(t, err, Invalid)
}
