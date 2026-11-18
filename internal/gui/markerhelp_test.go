package gui

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	fynetest "fyne.io/fyne/v2/test"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-754: the line of services down: none, one with its port and time,
// several, tcp:N without a port in brackets.
func TestDownLine(t *testing.T) {
	at := time.Date(2026, 11, 16, 14, 7, 0, 0, time.Local)
	ws := []apitypes.WatchedService{{Name: "ssh", Port: 22, Status: "up"}}
	if l := downLine(ws); l != "" {
		t.Errorf("none down: %q", l)
	}
	ws = append(ws, apitypes.WatchedService{Name: "https", Port: 443, Status: "down", LastChange: &at})
	if l := downLine(ws); l != "1 watched service down: https (443) since "+shortTime(at) {
		t.Errorf("one: %q", l)
	}
	ws = append(ws, apitypes.WatchedService{Name: "tcp:5432", Port: 5432, Status: "down"}, apitypes.WatchedService{Name: "vnc:1", Port: 5901, Status: "down"})
	if l := downLine(ws); l != "3 watched services down: https (443) since "+shortTime(at)+", tcp:5432, vnc :1 (5901)" {
		t.Errorf("three: %q", l)
	}
}

// TC-754: the details show the line with the marker while a watched
// service is down, and drop it when it is up again; resting the pointer on
// the host (or its marker) opens a box with name, address, status and the
// line; moving away, a click or a drag closes it; empty space has none;
// the box takes no input and stays inside the window; it closes when the
// host's status changes or another tab is chosen, and a pending box's
// timer is stopped (Design-marker-explained.md E1, E2a; crosscheck
// 2026-11-18-01 C7, C8, L3, L4).
func TestMarkerExplained(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web"}))
	must(c.UpdateHost(ctx, h.ID, apitypes.HostPatch{Watch: apitypes.Some([]string{"ssh", "https"})}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h.ID) != nil })
	k := mapmodel.HostKey(h.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "details", func() bool { return strings.Contains(formText(g.details), "ssh unknown") })
	e.ui.Wait(func() {
		if g.details.problem.Visible() {
			t.Error("a problem line with nothing down")
		}
	})
	at := time.Date(2026, 11, 16, 14, 7, 0, 0, time.Local)
	watch := func(status string) {
		t.Helper()
		if err := e.srv.Svc.ApplyWatch(ctx, []inventory.WatchResult{
			{HostID: h.ID, IP: netip.MustParseAddr("192.0.2.1"), Name: "https", Port: 443, Status: status, At: at},
		}); err != nil {
			t.Fatal(err)
		}
	}
	watch(apitypes.StatusDown)
	want := "1 watched service down: https (443) since " + shortTime(at)
	eventually(t, e.ui, "problem line", func() bool {
		return g.details.problem.Visible() && g.details.problemText.Text == want && len(g.details.problemIcon.Objects) == 1
	})

	// Hover: the host's icon, after the delay.
	mv := g.maps[1]
	move := func(x, y float64) {
		sx, sy := mv.vp.ToScreen(x, y)
		p := fyne.NewPos(float32(sx), float32(sy))
		mv.MouseMoved(&desktop.MouseEvent{PointEvent: fyne.PointEvent{Position: p, AbsolutePosition: p}})
	}
	var icon mapmodel.Rect
	e.ui.Wait(func() {
		icon = find(e.nodes(1), "host", h.ID).Icon
		move(icon.X+icon.W/2, icon.Y+icon.H/2)
		if g.hover != nil {
			t.Error("the box came at once")
		}
	})
	eventually(t, e.ui, "hover box", func() bool {
		b := g.hover
		return b != nil && b.key == k && strings.Contains(b.text.Text, "web\n192.0.2.1\n") && b.down.Visible() && b.line.Text == want
	})
	e.ui.Wait(func() { move(icon.X-500, icon.Y-500) })
	e.ui.Wait(func() {
		if g.hover != nil {
			t.Error("the box stays on empty space")
		}
	})
	// The marker opens it too; a click closes it.
	e.ui.Wait(func() { m := markerRect(icon); move(m.X+m.W-1.5, m.Y+m.H-1.5) })
	eventually(t, e.ui, "hover box on the marker", func() bool { return g.hover != nil })
	e.ui.Wait(func() {
		sx, sy := mv.vp.ToScreen(icon.X-500, icon.Y-500)
		mv.Tapped(&fyne.PointEvent{Position: fyne.NewPos(float32(sx), float32(sy))})
		if g.hover != nil {
			t.Error("the box stays after a click")
		}
	})
	// The box takes no input (C7) and is placed inside the window (C8).
	e.ui.Wait(func() { m := markerRect(icon); move(m.X+m.W-1.5, m.Y+m.H-1.5) })
	eventually(t, e.ui, "hover box again", func() bool { return g.hover != nil })
	e.ui.Wait(func() {
		b := g.hover
		if len(g.hoverLayer.Objects) != 1 || g.hoverLayer.Objects[0] != b.obj {
			t.Fatalf("layer %v", g.hoverLayer.Objects)
		}
		if o := inputTaker(b.obj); o != nil {
			t.Errorf("the box takes input: %T", o)
		}
		room := g.hoverLayer.Size()
		b.at = fyne.NewPos(room.Width-2, room.Height-2) // the corner
		g.placeHover()
		p, sz := b.obj.Position(), b.obj.Size()
		if p.X < 0 || p.Y < 0 || p.X+sz.Width > room.Width || p.Y+sz.Height > room.Height {
			t.Errorf("box at %v size %v in %v", p, sz, room)
		}
	})
	// A status change closes it (L3).
	watch(apitypes.StatusUp)
	eventually(t, e.ui, "closed on a change", func() bool { return g.hover == nil })
	eventually(t, e.ui, "no problem line", func() bool { return !g.details.problem.Visible() })
	// So does another tab; a pending box is stopped (L4).
	e.ui.Wait(func() { move(icon.X+icon.W/2, icon.Y+icon.H/2) })
	eventually(t, e.ui, "hover box, last", func() bool { return g.hover != nil })
	e.ui.Wait(func() {
		g.selectTab(g.tabs.Items[len(g.tabs.Items)-1])
		if g.hover != nil || len(g.hoverLayer.Objects) != 0 {
			t.Error("the box stays on another tab")
		}
		move(icon.X+icon.W/2, icon.Y+icon.H/2)
		if mv.hoverTimer == nil {
			t.Fatal("no timer")
		}
		mv.endHover()
		if mv.hoverTimer != nil {
			t.Error("timer kept")
		}
	})
}

// inputTaker is an object in o that takes clicks or the pointer, or nil.
func inputTaker(o fyne.CanvasObject) fyne.CanvasObject {
	switch o.(type) {
	case fyne.Tappable, fyne.SecondaryTappable, fyne.DoubleTappable, desktop.Mouseable, desktop.Hoverable, fyne.Draggable:
		return o
	}
	var kids []fyne.CanvasObject
	switch c := o.(type) {
	case *fyne.Container:
		kids = c.Objects
	case fyne.Widget:
		kids = fynetest.WidgetRenderer(c).Objects()
	}
	for _, k := range kids {
		if f := inputTaker(k); f != nil {
			return f
		}
	}
	return nil
}

// TC-754: next to the pointer, or before it when there is no room after
// it, else as far as fits (C8).
func TestFitAt(t *testing.T) {
	for _, c := range []struct{ p, n, room, want float32 }{
		{100, 50, 400, 116},
		{380, 50, 400, 314},
		{30, 50, 60, 10},
		{30, 80, 60, 0},
	} {
		if got := fitAt(c.p, c.n, c.room); got != c.want {
			t.Errorf("fitAt(%v, %v, %v) = %v, want %v", c.p, c.n, c.room, got, c.want)
		}
	}
}

// TC-755: Help › "Map symbols…" opens the legend: the map's own drawing of
// every host sign, the marker and the kinds of group, with the text; the
// legend can't be changed by dragging.
func TestLegend(t *testing.T) {
	e := start(t)
	e.open()
	g := e.g
	e.ui.Wait(func() {
		items := g.helpMenuItems()
		if len(items) != 1 || items[0].Label != "Map symbols…" {
			t.Fatalf("Help menu: %v", labels(items))
		}
		items[0].Action()
	})
	eventually(t, e.ui, "legend", func() bool { return g.lastLegend != nil && len(g.lastLegend.Nodes()) > 0 })
	e.ui.Wait(func() {
		mv := g.lastLegend
		var names []string
		frames, marker := 0, false
		for _, n := range mv.Nodes() {
			names = append(names, n.Item.Name)
			if n.Kind == mapmodel.KindFrame {
				frames++
			}
			marker = marker || n.Item.ServicesDown > 0
		}
		all := strings.Join(names, "|")
		for _, w := range []string{"up", "down", "unknown", "not monitored", "stale", "service down", "a group, collapsed", "10.0.1.0/24"} {
			if !strings.Contains(all, w) {
				t.Errorf("legend lacks %q: %s", w, all)
			}
		}
		if frames != 2 || !marker || !strings.Contains(legendText, "▲") {
			t.Errorf("frames %d marker %v", frames, marker)
		}
		before := mv.Nodes()[0].Bounds
		mv.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(5, 5)}, Dragged: fyne.Delta{DX: 50, DY: 50}})
		mv.DragEnd()
		if mv.Nodes()[0].Bounds != before || mv.drag != dragNone {
			t.Error("the legend moved")
		}
	})
}
