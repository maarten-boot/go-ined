package gui

import (
	"fmt"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

func pid(id int64) *int64 { return &id }

// newMap returns a map widget of 800 × 600 showing one view.
func newMap(t *testing.T, p theme.Palette, items ...apitypes.MapItem) (*MapView, *mapmodel.Model) {
	t.Helper()
	test.NewTempApp(t)
	m := mapmodel.New()
	m.SetMap(apitypes.Map{ViewID: 1, Items: items})
	mv := NewMapView(1, func() theme.Palette { return p })
	mv.Resize(fyne.NewSize(800, 600))
	mv.SetNodes(m.Layout(1, measure))
	mv.SetViewport(mapmodel.Viewport{Zoom: 1})
	return mv, m
}

// objectsIn returns the canvas objects drawn inside a map rectangle.
func objectsIn(mv *MapView, r mapmodel.Rect) []fyne.CanvasObject {
	var out []fyne.CanvasObject
	for _, o := range test.WidgetRenderer(mv).Objects()[1:] { // [0] is the background
		x, y := mv.vp.ToMap(float64(o.Position().X), float64(o.Position().Y))
		if r.Contains(x, y) {
			out = append(out, o)
		}
	}
	return out
}

type shapes struct {
	circles, lines, rects int
	texts                 []string
	filledCircle          bool
}

func count(objs []fyne.CanvasObject) shapes {
	var s shapes
	for _, o := range objs {
		switch o := o.(type) {
		case *canvas.Circle:
			s.circles++
			s.filledCircle = s.filledCircle || o.FillColor != color.Transparent
		case *canvas.Line:
			s.lines++
		case *canvas.Rectangle:
			s.rects++
		case *canvas.Text:
			s.texts = append(s.texts, o.Text)
		}
	}
	return s
}

func has(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TC-701: every status has its own shape and a word, not only a colour;
// stale glyphs differ (SPEC-702, 708).
func TestStatusShapes(t *testing.T) {
	for _, p := range []theme.Palette{theme.NewPalette(theme.Default), theme.NewPalette(theme.Contrast{Preset: theme.PresetMax, Min: 21})} {
		mv, _ := newMap(t, p,
			apitypes.MapItem{Type: "host", ID: 1, Name: "a", X: 100, Y: 100, Status: "up", Monitored: true},
			apitypes.MapItem{Type: "host", ID: 2, Name: "b", X: 300, Y: 100, Status: "down", Monitored: true},
			apitypes.MapItem{Type: "host", ID: 3, Name: "c", X: 500, Y: 100, Status: "unknown", Monitored: true},
			apitypes.MapItem{Type: "host", ID: 4, Name: "d", X: 100, Y: 300, Status: "up", Stale: true, Monitored: true},
			apitypes.MapItem{Type: "host", ID: 5, Name: "e", X: 300, Y: 300, Status: "up"},
		)
		box := func(x, y float64) mapmodel.Rect { return mapmodel.Rect{X: x - 60, Y: y - 30, W: 120, H: 90} }
		up, down, unk := count(objectsIn(mv, box(100, 100))), count(objectsIn(mv, box(300, 100))), count(objectsIn(mv, box(500, 100)))
		stale, off := count(objectsIn(mv, box(100, 300))), count(objectsIn(mv, box(300, 300)))
		if up.circles != 1 || !up.filledCircle || !has(up.texts, "up") {
			t.Errorf("%s up: %+v", p.Contrast, up)
		}
		if down.lines != 2 || down.circles != 0 || !has(down.texts, "down") {
			t.Errorf("%s down: %+v", p.Contrast, down)
		}
		if !has(unk.texts, "?") || unk.circles != 0 || unk.lines != 0 || !has(unk.texts, "unknown") {
			t.Errorf("%s unknown: %+v", p.Contrast, unk)
		}
		if stale.circles != 1 || stale.filledCircle || !has(stale.texts, "up (stale)") {
			t.Errorf("%s stale: %+v", p.Contrast, stale)
		}
		if off.lines != 1 || off.circles != 0 || !has(off.texts, "off") {
			t.Errorf("%s unmonitored: %+v", p.Contrast, off)
		}
	}
}

// TC-704: a network group looks different from an ordinary group, as an
// icon and as a frame, and shows its CIDR; a note shows a marker (SPEC-711).
func TestGroupShapes(t *testing.T) {
	p := theme.NewPalette(theme.Default)
	mv, _ := newMap(t, p,
		apitypes.MapItem{Type: "group", ID: 1, Name: "office", X: 100, Y: 100, Collapsed: true, Status: "up"},
		apitypes.MapItem{Type: "group", ID: 2, Name: "lan", CIDR: "10.0.0.0/24", X: 400, Y: 100, Collapsed: true, Status: "up", HasNote: true},
	)
	plain, netw := count(objectsIn(mv, mapmodel.Rect{X: 40, Y: 60, W: 120, H: 120})), count(objectsIn(mv, mapmodel.Rect{X: 340, Y: 60, W: 120, H: 120}))
	if plain.rects != 2 || netw.rects != 3 {
		t.Errorf("folder rects: plain %d, network %d (double border)", plain.rects, netw.rects)
	}
	if !has(netw.texts, "10.0.0.0/24") || plain.lines != 0 || netw.lines != 3 {
		t.Errorf("network icon: %+v, plain %+v", netw, plain)
	}

	// Frames: solid for an ordinary group, dashed (many lines) for a network group.
	mv2, _ := newMap(t, p,
		apitypes.MapItem{Type: "group", ID: 1, Name: "office", X: 100, Y: 100, Status: "up"},
		apitypes.MapItem{Type: "group", ID: 2, Name: "lan", CIDR: "10.0.0.0/24", X: 400, Y: 100, Status: "up"},
	)
	var solid, dashed shapes
	for _, n := range mv2.Nodes() {
		s := count(objectsIn(mv2, mapmodel.Rect{X: n.Bounds.X - 1, Y: n.Bounds.Y - 1, W: n.Bounds.W + 2, H: n.Bounds.H + 2}))
		if n.Item.CIDR == "" {
			solid = s
		} else {
			dashed = s
		}
	}
	if solid.rects != 1 || dashed.rects != 0 || dashed.lines < 20 || !has(dashed.texts, "lan · 10.0.0.0/24 · up") {
		t.Errorf("frames: solid %+v, dashed %+v", solid, dashed)
	}
}

// TC-008 (GUI part): in Max every drawn colour is black, white or none.
func assertBlackWhite(t *testing.T, objs []fyne.CanvasObject) {
	t.Helper()
	ok := func(c color.Color) bool {
		if c == nil {
			return true
		}
		r, g, b, a := c.RGBA()
		return a == 0 || (r == g && g == b && (r == 0 || r == 0xFFFF))
	}
	for _, o := range objs {
		var cs []color.Color
		switch o := o.(type) {
		case *canvas.Rectangle:
			cs = []color.Color{o.FillColor, o.StrokeColor}
		case *canvas.Circle:
			cs = []color.Color{o.FillColor, o.StrokeColor}
		case *canvas.Line:
			cs = []color.Color{o.StrokeColor}
		case *canvas.Text:
			cs = []color.Color{o.Color}
		}
		for _, c := range cs {
			if !ok(c) {
				t.Errorf("%T uses %v in Max", o, c)
			}
		}
	}
}

// Pointer input: select, expand/collapse, zoom at the pointer, pan, the
// scroll bars and the context menu (SPEC-703, 713).
func TestMapInput(t *testing.T) {
	p := theme.NewPalette(theme.Default)
	mv, m := newMap(t, p,
		apitypes.MapItem{Type: "group", ID: 1, Name: "office", X: 100, Y: 100, Status: "up"},
		apitypes.MapItem{Type: "host", ID: 1, ParentID: pid(1), Name: "web", X: 100, Y: 200, Status: "up", Monitored: true},
		apitypes.MapItem{Type: "group", ID: 2, Name: "lab", X: 400, Y: 100, Collapsed: true, Status: "up"},
	)
	var selected *mapmodel.Key
	var toggled []string
	var menu *mapmodel.Node
	mv.OnSelect = func(k *mapmodel.Key) { selected = k }
	mv.OnToggle = func(g int64, expand bool) { toggled = append(toggled, fmt.Sprint(g, expand)) }
	mv.OnMenu = func(n *mapmodel.Node, _ fyne.Position) { menu = n }
	at := func(x, y float64) *fyne.PointEvent {
		sx, sy := mv.vp.ToScreen(x, y)
		return &fyne.PointEvent{Position: fyne.NewPos(float32(sx), float32(sy))}
	}

	mv.Tapped(at(100, 200))
	if selected == nil || *selected != mapmodel.HostKey(1) || *mv.Selected() != *selected {
		t.Fatalf("tap host: %+v", selected)
	}
	mv.Tapped(at(700, 500))
	if selected != nil {
		t.Errorf("tap empty: %+v", selected)
	}
	frame := mv.Nodes()[0]
	mv.DoubleTapped(at(400, 100))                                          // collapsed group icon: expand
	mv.DoubleTapped(at(frame.Icon.X+5, frame.Icon.Y+5))                    // frame label: collapse
	mv.DoubleTapped(at(frame.Bounds.X+3, frame.Bounds.Y+frame.Bounds.H-3)) // inside the frame: nothing
	mv.DoubleTapped(at(100, 200))                                          // a host: nothing
	if fmt.Sprint(toggled) != "[2 true 1 false]" {
		t.Errorf("toggles: %v", toggled)
	}
	mv.TappedSecondary(at(400, 100))
	if menu == nil || menu.Item.ID != 2 || *selected != mapmodel.GroupKey(2) {
		t.Errorf("context menu: %+v", menu)
	}

	// Wheel zoom keeps the point under the pointer.
	ev := &fyne.ScrollEvent{PointEvent: *at(400, 100), Scrolled: fyne.Delta{DY: 10}}
	mv.Scrolled(ev)
	if mx, my := mv.vp.ToMap(float64(ev.Position.X), float64(ev.Position.Y)); mv.vp.Zoom != zoomStep || mx != 400 || my != 100 {
		t.Errorf("zoom in: %+v at %g,%g", mv.vp, mx, my)
	}
	mv.Scrolled(&fyne.ScrollEvent{PointEvent: ev.PointEvent, Scrolled: fyne.Delta{DY: -10}})
	mv.Scrolled(&fyne.ScrollEvent{PointEvent: ev.PointEvent})
	if mv.vp.Zoom != 1 {
		t.Errorf("zoom out: %+v", mv.vp)
	}

	// Drag on the map pans; drag on a scroll bar moves the other way.
	v0 := mv.vp
	mv.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(300, 300)}, Dragged: fyne.Delta{DX: 50, DY: 20}})
	mv.DragEnd()
	if mv.vp.X != v0.X-50 || mv.vp.Y != v0.Y-20 {
		t.Errorf("pan: %+v from %+v", mv.vp, v0)
	}
	v1 := mv.vp
	mv.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(400, 595)}, Dragged: fyne.Delta{DX: 30}})
	mv.DragEnd()
	if mv.vp.X <= v1.X || mv.vp.Y != v1.Y {
		t.Errorf("horizontal bar: %+v from %+v", mv.vp, v1)
	}
	mv.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(795, 300)}, Dragged: fyne.Delta{DY: 30}})
	mv.DragEnd()
	if mv.vp.Y <= v1.Y {
		t.Errorf("vertical bar: %+v", mv.vp)
	}

	// Toolbar actions.
	mv.ZoomTo(2)
	mv.ZoomBy(2)
	if mv.vp.Zoom != 4 {
		t.Errorf("zoom: %g", mv.vp.Zoom)
	}
	mv.FitAll()
	vis := mv.vp.Visible(800, 600)
	if e := mapmodel.Extent(mv.Nodes()); !vis.Contains(e.X, e.Y) || !vis.Contains(e.X+e.W, e.Y+e.H) {
		t.Errorf("fit all: %+v doesn't show %+v", vis, e)
	}
	mv.CentreOn(1000, 1000)
	if sx, sy := mv.vp.ToScreen(1000, 1000); sx != 400 || sy != 300 {
		t.Errorf("centre: %g %g", sx, sy)
	}
	_ = m
}

// TC-708 (U): only items near the visible area are drawn, so a large map
// stays cheap; tiny text is left out when zoomed far out.
func TestCulling(t *testing.T) {
	items := hosts(5000)
	mv, _ := newMap(t, theme.NewPalette(theme.Default), items...)
	near := len(test.WidgetRenderer(mv).Objects())
	if near > 600 {
		t.Errorf("at 100 %% in 800×600, %d objects for 5000 hosts", near)
	}
	mv.SetViewport(mapmodel.Viewport{Zoom: mapmodel.MinZoom})
	far := count(test.WidgetRenderer(mv).Objects())
	if len(far.texts) != 0 {
		t.Errorf("labels drawn at 10 %%: %d", len(far.texts))
	}
}

// hosts makes n hosts on a 100 × 80 grid, 100 per row.
func hosts(n int) []apitypes.MapItem {
	out := make([]apitypes.MapItem, n)
	st := []string{"up", "down", "unknown"}
	for i := range out {
		out[i] = apitypes.MapItem{Type: "host", ID: int64(i + 1), Name: fmt.Sprintf("host-%d", i+1),
			X: float64(40 + 100*(i%100)), Y: float64(40 + 80*(i/100)), Status: st[i%3], Monitored: true}
	}
	return out
}

// BenchmarkLayoutAndDraw5000: layout of 5,000 hosts plus drawing at 100 %
// and at "fit all" (TC-708, crosscheck 2026-11-11-01 I3).
func BenchmarkLayoutAndDraw5000(b *testing.B) {
	test.NewTempApp(b)
	m := mapmodel.New()
	m.SetMap(apitypes.Map{ViewID: 1, Items: hosts(5000)})
	p := theme.NewPalette(theme.Default)
	mv := NewMapView(1, func() theme.Palette { return p })
	mv.Resize(fyne.NewSize(1200, 800))
	for b.Loop() {
		mv.SetNodes(m.Layout(1, measure))
		mv.SetViewport(mapmodel.Viewport{Zoom: 1})
		mv.FitAll()
	}
}
