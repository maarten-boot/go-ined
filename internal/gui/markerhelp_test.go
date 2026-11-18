package gui

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

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
// line; moving away, a click or a drag closes it; empty space has none
// (Design-marker-explained.md E1, E2a).
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
	// Up again: the line goes.
	watch(apitypes.StatusUp)
	eventually(t, e.ui, "no problem line", func() bool { return !g.details.problem.Visible() })
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
