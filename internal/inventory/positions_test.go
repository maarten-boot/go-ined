package inventory

import (
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-135: every saved position is rounded to one decimal, half away from
// zero (Design-positions.md P1): create, PATCH, a placement, a move of a
// group (its members keep their offsets, the event carries the rounded
// move), and the export rounds a position stored before (P2).
func TestRoundPositions(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{24.666666, 24.7}, {24.65, 24.7}, {-24.65, -24.7}, {-0.04, 0}, {0.05, 0.1}, {1e300, 1e300}, {3, 3},
	} {
		if got := roundPos(c.in); got != c.want {
			t.Errorf("roundPos(%g) = %g, want %g", c.in, got, c.want)
		}
	}
	s, _ := newSvc(t)
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "lan", X: ptr(100.04), Y: ptr(200.0 / 3)})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &g.ID, X: ptr(110.123), Y: ptr(-0.01)})
	if g.X != 100 || g.Y != 66.7 || h.X != 110.1 || h.Y != 0 {
		t.Fatalf("created: group %g,%g host %g,%g", g.X, g.Y, h.X, h.Y)
	}
	h, err := s.UpdateHost(ctx, h.ID, apitypes.HostPatch{X: apitypes.Some(37.0 / 1.5), Y: apitypes.Some(12.34)})
	if err != nil || h.X != 24.7 || h.Y != 12.3 {
		t.Fatalf("patch: %v %g,%g", err, h.X, h.Y)
	}
	v := mustView(t, s, "Payroll")
	if p := mustPlace(t, s, v.ID, h.ID, apitypes.PlacementSet{X: ptr(5.55555), Y: ptr(6.0)}); p.X != 5.6 {
		t.Fatalf("placement: %+v", p)
	}
	rec := &recorder{}
	s.SetNotifier(rec)
	moved, err := s.MoveGroup(ctx, g.ID, 0.333, 0.25)
	if err != nil || moved.X != 100.3 || moved.Y != 67 {
		t.Fatalf("move: %v %+v", err, moved)
	}
	h, _ = s.Host(h.ID)
	if h.X != 25 || h.Y != 12.6 {
		t.Fatalf("host after the move: %g,%g", h.X, h.Y)
	}
	// A position saved before rounding comes out rounded in an export.
	s.mu.Lock()
	s.m.Hosts[h.ID].X = 25.123456
	s.mu.Unlock()
	f, err := s.ExportGroup(g.ID)
	if err != nil || f.Group.Hosts[0].X != -75.2 {
		t.Fatalf("export: %v %+v", err, f.Group.Hosts)
	}
}

// TC-135: reflow rounds groups too, and the shift of their contents
// (crosscheck 2026-11-18-01 C9).
func TestArrangeRounds(t *testing.T) {
	s, _ := newSvc(t)
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", X: ptr(40.0), Y: ptr(0.0)})
	a := mustGroup(t, s, apitypes.GroupCreate{Name: "a", ParentID: &g.ID, X: ptr(500.0), Y: ptr(500.0)})
	b := mustGroup(t, s, apitypes.GroupCreate{Name: "b", ParentID: &g.ID, X: ptr(900.0), Y: ptr(500.0)})
	in := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &b.ID, X: ptr(910.0), Y: ptr(530.0)})
	if _, err := s.Arrange(ctx, NetworkView, &g.ID, 123.456, 80); err != nil {
		t.Fatal(err)
	}
	ga, _ := s.Group(a.ID)
	gb, _ := s.Group(b.ID)
	h, _ := s.Host(in.ID)
	if ga.X != 40 || gb.X != 163.5 || h.X != 173.5 || h.Y-gb.Y != 30 {
		t.Fatalf("a %g, b %g,%g, host in b %g,%g", ga.X, gb.X, gb.Y, h.X, h.Y)
	}
}

// TC-135: a position beyond ±1e9 is refused, also as the result of a
// move, and nothing is saved (crosscheck 2026-11-18-01 C10).
func TestPositionRange(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", X: ptr(1.7e308), Y: ptr(0.0)}); KindOf(err) != Invalid || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("create: %v", err)
	}
	g := mustGroup(t, s, apitypes.GroupCreate{Name: "g", X: ptr(9e8), Y: ptr(0.0)})
	h := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &g.ID, X: ptr(1e9), Y: ptr(0.0)})
	if _, err := s.MoveGroup(ctx, g.ID, 5e8, 0); KindOf(err) != Invalid {
		t.Fatalf("move: %v", err)
	}
	if _, err := s.MoveGroupContents(ctx, g.ID, 1, 0); KindOf(err) != Invalid {
		t.Fatalf("move contents: %v", err)
	}
	if gg, _ := s.Group(g.ID); gg.X != 9e8 {
		t.Errorf("group moved to %g", gg.X)
	}
	if hh, _ := s.Host(h.ID); hh.X != 1e9 {
		t.Errorf("host moved to %g", hh.X)
	}
	if _, err := s.MoveGroup(ctx, g.ID, -1, 0); err != nil {
		t.Fatalf("a move back: %v", err)
	}
}
