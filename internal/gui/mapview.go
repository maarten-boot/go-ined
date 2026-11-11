package gui

import (
	"cmp"
	"image/color"
	"math"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Text sizes on the map, in map units (zoom 100 %).
const (
	mapTextSize   = 12.0
	minTextPixels = 6.0  // labels smaller than this on screen are left out
	scrollBarW    = 10.0 // pixels
	cullMargin    = 40.0 // map units around the visible area that are still drawn
	zoomStep      = 1.25
)

// MapView draws one view's map (SPEC-702, 703, 711, 712, 713) and turns
// pointer input into pan, zoom, selection and expand/collapse requests.
type MapView struct {
	widget.BaseWidget

	view    int64
	nodes   []mapmodel.Node
	vp      mapmodel.Viewport
	placed  bool                  // the viewport has been set ("fit all" on the first layout)
	sel     *mapmodel.Key         // the primary selection (shown in the details)
	sels    map[mapmodel.Key]bool // everything selected (SPEC-724)
	mods    fyne.KeyModifier      // modifier keys held at the last mouse press
	palette func() theme.Palette

	// Callbacks, set by the GUI.
	OnSelect func(k *mapmodel.Key)          // nil: nothing selected
	OnToggle func(group int64, expand bool) // expand or collapse a group
	// OnMenu opens the context menu of a node (nil: empty space) at a
	// screen position; (mx, my) is the map point clicked.
	OnMenu func(n *mapmodel.Node, at fyne.Position, mx, my float64)
	// OnViewport is called when the viewport changes (zoom display).
	OnViewport func(mapmodel.Viewport)
	// OnMoved: items were dragged by (dx, dy) map units onto empty space.
	OnMoved func(keys []mapmodel.Key, dx, dy float64)
	// OnDropInto: items were dropped onto a group (SPEC-704).
	OnDropInto func(keys []mapmodel.Key, group int64, dx, dy float64)
	// Subtree returns the keys that move with a dragged item: the item,
	// and for a group everything below it.
	Subtree func(k mapmodel.Key) map[mapmodel.Key]bool

	drag dragMode
	// A press that may become an item drag or a pan (M6-9: 4 pixels).
	pendingKey   *mapmodel.Key
	pendingTotal fyne.Delta
	// An item drag in progress: the items dragged (the selection, when the
	// drag starts on a selected item), and everything moving with them.
	dragKeys []mapmodel.Key
	// A rubber band (Shift+drag), in map units.
	bandX0, bandY0, bandX1, bandY1 float64
	dragSet                        map[mapmodel.Key]bool
	dragDX                         float64 // map units
	dragDY                         float64
	dropTarget                     *mapmodel.Node
}

type dragMode int

const (
	dragNone dragMode = iota
	dragPending
	dragPan
	dragHBar
	dragVBar
	dragItem
	dragBand
)

// dragThreshold is how far the pointer moves before a press becomes a
// drag, so a click stays a click (M6-9).
const dragThreshold = 4.0

// NewMapView returns an empty map for a view.
func NewMapView(view int64, palette func() theme.Palette) *MapView {
	mv := &MapView{view: view, vp: mapmodel.Viewport{Zoom: 1}, palette: palette}
	mv.ExtendBaseWidget(mv)
	return mv
}

// measure gives text sizes at zoom 100 % for the layout.
func measure(s string) (float64, float64) {
	sz := fyne.MeasureText(s, mapTextSize, fyne.TextStyle{})
	return float64(sz.Width), float64(sz.Height)
}

// SetNodes replaces what the map shows. The first time, the viewport is
// fitted to the content once the widget has a size.
func (mv *MapView) SetNodes(nodes []mapmodel.Node) {
	mv.nodes = nodes
	mv.Refresh()
}

// Nodes returns what the map shows.
func (mv *MapView) Nodes() []mapmodel.Node { return mv.nodes }

// Viewport returns the current viewport.
func (mv *MapView) Viewport() mapmodel.Viewport { return mv.vp }

// SetViewport sets the viewport.
func (mv *MapView) SetViewport(v mapmodel.Viewport) {
	mv.vp, mv.placed = v, true
	mv.Refresh()
	mv.viewportChanged()
}

func (mv *MapView) viewportChanged() {
	if mv.OnViewport != nil {
		mv.OnViewport(mv.vp)
	}
}

// Select makes one item the selection (nil clears it).
func (mv *MapView) Select(k *mapmodel.Key) {
	mv.sel, mv.sels = k, map[mapmodel.Key]bool{}
	if k != nil {
		mv.sels[*k] = true
	}
	mv.Refresh()
}

// SetSelection selects these items; the last one is the primary.
func (mv *MapView) SetSelection(keys []mapmodel.Key) {
	mv.sel, mv.sels = nil, map[mapmodel.Key]bool{}
	for _, k := range keys {
		mv.sels[k] = true
		mv.sel = &k
	}
	mv.Refresh()
}

// toggle adds an item to the selection or removes it (Ctrl+click).
func (mv *MapView) toggle(k mapmodel.Key) {
	if mv.sels == nil {
		mv.sels = map[mapmodel.Key]bool{}
	}
	if mv.sels[k] {
		delete(mv.sels, k)
		if mv.sel != nil && *mv.sel == k {
			mv.sel = nil
			if rest := mv.Selection(); len(rest) > 0 { // another one becomes primary
				mv.sel = &rest[len(rest)-1]
			}
		}
	} else {
		mv.sels[k] = true
		mv.sel = &k
	}
	mv.Refresh()
}

// Selected returns the primary selected item.
func (mv *MapView) Selected() *mapmodel.Key { return mv.sel }

// Selection returns every selected item, in a stable order.
func (mv *MapView) Selection() []mapmodel.Key {
	out := make([]mapmodel.Key, 0, len(mv.sels))
	for k := range mv.sels {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b mapmodel.Key) int {
		return cmp.Or(strings.Compare(a.Type, b.Type), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// MouseDown records the modifier keys of a press, for the tap or drag that
// follows (Ctrl+click, Shift+drag; SPEC-724).
func (mv *MapView) MouseDown(ev *desktop.MouseEvent) { mv.mods = ev.Modifier }

// MouseUp implements desktop.Mouseable.
func (mv *MapView) MouseUp(*desktop.MouseEvent) {}

func (mv *MapView) ctrlHeld() bool  { return mv.mods&fyne.KeyModifierShortcutDefault != 0 }
func (mv *MapView) shiftHeld() bool { return mv.mods&fyne.KeyModifierShift != 0 }

// ZoomBy zooms around the centre of the widget.
func (mv *MapView) ZoomBy(f float64) {
	s := mv.Size()
	mv.SetViewport(mv.vp.ZoomAt(mv.vp.Zoom*f, float64(s.Width)/2, float64(s.Height)/2))
}

// ZoomTo sets an absolute zoom around the centre.
func (mv *MapView) ZoomTo(z float64) {
	s := mv.Size()
	mv.SetViewport(mv.vp.ZoomAt(z, float64(s.Width)/2, float64(s.Height)/2))
}

// FitAll shows everything (SPEC-713).
func (mv *MapView) FitAll() {
	s := mv.Size()
	mv.SetViewport(mapmodel.Fit(mapmodel.Extent(mv.nodes), float64(s.Width), float64(s.Height)))
}

// CentreOn centres the map on a map point.
func (mv *MapView) CentreOn(x, y float64) {
	s := mv.Size()
	mv.SetViewport(mv.vp.CentreOn(x, y, float64(s.Width), float64(s.Height)))
}

func (mv *MapView) toMap(p fyne.Position) (float64, float64) {
	return mv.vp.ToMap(float64(p.X), float64(p.Y))
}

func (mv *MapView) hit(p fyne.Position) *mapmodel.Node {
	x, y := mv.toMap(p)
	return mapmodel.Hit(mv.nodes, x, y)
}

// Tapped selects the item under the pointer, or clears the selection.
// With Ctrl held it adds the item to the selection or removes it.
func (mv *MapView) Tapped(ev *fyne.PointEvent) {
	var k *mapmodel.Key
	if n := mv.hit(ev.Position); n != nil {
		k = &mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}
	}
	switch {
	case mv.ctrlHeld() && k != nil:
		mv.toggle(*k)
	case mv.ctrlHeld():
		return // Ctrl+click on empty space keeps the selection
	default:
		mv.Select(k)
	}
	if mv.OnSelect != nil {
		mv.OnSelect(mv.sel)
	}
}

// DoubleTapped expands a collapsed group icon or collapses an expanded
// group by its frame label (SPEC-703).
func (mv *MapView) DoubleTapped(ev *fyne.PointEvent) {
	n := mv.hit(ev.Position)
	if n == nil || mv.OnToggle == nil {
		return
	}
	x, y := mv.toMap(ev.Position)
	switch {
	case n.Kind == mapmodel.KindGroup:
		mv.OnToggle(n.Item.ID, true)
	case n.Kind == mapmodel.KindFrame && n.Icon.Contains(x, y):
		mv.OnToggle(n.Item.ID, false)
	}
}

// TappedSecondary opens the context menu of the item under the pointer.
func (mv *MapView) TappedSecondary(ev *fyne.PointEvent) {
	n := mv.hit(ev.Position)
	if n != nil {
		k := mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}
		if mv.sels[k] { // part of the selection: keep it (SPEC-724)
			mv.sel = &k
			mv.Refresh()
		} else {
			mv.Select(&k)
		}
		if mv.OnSelect != nil {
			mv.OnSelect(&k)
		}
	}
	if mv.OnMenu != nil {
		x, y := mv.toMap(ev.Position)
		mv.OnMenu(n, ev.AbsolutePosition, x, y)
	}
}

// Scrolled zooms around the pointer (SPEC-713).
func (mv *MapView) Scrolled(ev *fyne.ScrollEvent) {
	f := zoomStep
	switch {
	case ev.Scrolled.DY < 0:
		f = 1 / zoomStep
	case ev.Scrolled.DY == 0:
		return
	}
	mv.SetViewport(mv.vp.ZoomAt(mv.vp.Zoom*f, float64(ev.Position.X), float64(ev.Position.Y)))
}

// Dragged pans the map (on empty space or inside a frame), moves it by a
// scroll bar (SPEC-713), or drags an item: a host, a collapsed group, or a
// frame by its label (SPEC-704).
func (mv *MapView) Dragged(ev *fyne.DragEvent) {
	if mv.drag == dragNone {
		start := fyne.NewPos(ev.Position.X-ev.Dragged.DX, ev.Position.Y-ev.Dragged.DY)
		mv.drag = mv.barAt(start)
		if mv.drag == dragPan && mv.shiftHeld() { // a rubber band (SPEC-724)
			mv.drag = dragBand
			mv.bandX0, mv.bandY0 = mv.toMap(start)
			mv.bandX1, mv.bandY1 = mv.bandX0, mv.bandY0
		}
		if mv.drag == dragPan {
			mv.drag, mv.pendingKey, mv.pendingTotal = dragPending, mv.draggable(start), fyne.Delta{}
		}
	}
	dx, dy := float64(ev.Dragged.DX), float64(ev.Dragged.DY)
	if mv.drag == dragBand {
		mv.bandX1, mv.bandY1 = mv.toMap(ev.Position)
		mv.Refresh()
		return
	}
	if mv.drag == dragPending {
		mv.pendingTotal.DX += ev.Dragged.DX
		mv.pendingTotal.DY += ev.Dragged.DY
		if math.Hypot(float64(mv.pendingTotal.DX), float64(mv.pendingTotal.DY)) < dragThreshold {
			return
		}
		dx, dy = float64(mv.pendingTotal.DX), float64(mv.pendingTotal.DY)
		mv.drag = dragPan
		if k := mv.pendingKey; k != nil {
			// A drag on a selected item moves the whole selection.
			mv.dragKeys = []mapmodel.Key{*k}
			if mv.sels[*k] && len(mv.sels) > 1 {
				mv.dragKeys = mv.Selection()
			}
			mv.drag, mv.dragDX, mv.dragDY = dragItem, 0, 0
			mv.dragSet = map[mapmodel.Key]bool{}
			for _, dk := range mv.dragKeys {
				mv.dragSet[dk] = true
				if mv.Subtree != nil {
					for sk := range mv.Subtree(dk) {
						mv.dragSet[sk] = true
					}
				}
			}
		}
	}
	if mv.drag == dragItem {
		mv.dragDX += dx / mv.vp.Zoom
		mv.dragDY += dy / mv.vp.Zoom
		x, y := mv.toMap(ev.Position)
		mv.dropTarget = dropTarget(mv.nodes, x, y, mv.dragSet)
		mv.Refresh()
		return
	}
	v := mv.vp
	switch mv.drag {
	case dragHBar, dragVBar:
		ext, _ := mv.scrollExtent()
		s := mv.Size()
		if mv.drag == dragHBar {
			v.X += dx * ext.W / math.Max(1, float64(s.Width)-scrollBarW)
		} else {
			v.Y += dy * ext.H / math.Max(1, float64(s.Height)-scrollBarW)
		}
	default:
		v.X -= dx / v.Zoom
		v.Y -= dy / v.Zoom
	}
	mv.SetViewport(v)
}

// DragEnd ends a drag. An item drag ends with a drop into a group or a
// move (SPEC-704).
func (mv *MapView) DragEnd() {
	if mv.drag == dragItem {
		keys, dx, dy, target := mv.dragKeys, mv.dragDX, mv.dragDY, mv.dropTarget
		mv.endItemDrag()
		switch {
		case target != nil && mv.OnDropInto != nil:
			mv.OnDropInto(keys, target.Item.ID, dx, dy)
		case (dx != 0 || dy != 0) && mv.OnMoved != nil:
			mv.OnMoved(keys, dx, dy)
		}
	}
	if mv.drag == dragBand {
		mv.drag = dragNone
		mv.selectBand()
	}
	mv.drag, mv.pendingKey = dragNone, nil
}

// CancelDrag stops an item drag without a change (Esc, SPEC-704).
func (mv *MapView) CancelDrag() {
	if mv.drag == dragItem || mv.drag == dragPending || mv.drag == dragBand {
		mv.endItemDrag()
		mv.drag = dragPan // the rest of this pointer drag pans
	}
}

// Dragging reports whether an item is being dragged.
func (mv *MapView) Dragging() bool { return mv.drag == dragItem }

func (mv *MapView) endItemDrag() {
	mv.dragSet, mv.dropTarget, mv.dragDX, mv.dragDY, mv.pendingKey, mv.dragKeys = nil, nil, 0, 0, nil, nil
	mv.Refresh()
}

// selectBand selects every item whose icon (or frame label) centre lies
// inside the rubber band; with Ctrl held it adds them to the selection.
func (mv *MapView) selectBand() {
	x0, x1 := math.Min(mv.bandX0, mv.bandX1), math.Max(mv.bandX0, mv.bandX1)
	y0, y1 := math.Min(mv.bandY0, mv.bandY1), math.Max(mv.bandY0, mv.bandY1)
	band := mapmodel.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	var keys []mapmodel.Key
	if mv.ctrlHeld() {
		keys = mv.Selection()
	}
	for _, n := range mv.nodes {
		if band.Contains(n.Icon.X+n.Icon.W/2, n.Icon.Y+n.Icon.H/2) {
			keys = append(keys, mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID})
		}
	}
	mv.SetSelection(keys)
	if mv.OnSelect != nil {
		mv.OnSelect(mv.sel)
	}
}

// draggable returns the item a drag starting at p would move: a host or
// collapsed group icon, or a frame by its label strip. Inside a frame's
// body a drag pans.
func (mv *MapView) draggable(p fyne.Position) *mapmodel.Key {
	n := mv.hit(p)
	if n == nil {
		return nil
	}
	x, y := mv.toMap(p)
	if n.Kind == mapmodel.KindFrame && !n.Icon.Contains(x, y) {
		return nil
	}
	return &mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}
}

// dropTarget is the group a dragged item would go into at (x, y): a
// collapsed group icon or a frame's label strip, not one being dragged.
func dropTarget(nodes []mapmodel.Node, x, y float64, exclude map[mapmodel.Key]bool) *mapmodel.Node {
	for i := len(nodes) - 1; i >= 0; i-- {
		n := &nodes[i]
		if n.Item.Type != apitypes.TypeGroup || exclude[mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}] {
			continue
		}
		if (n.Kind == mapmodel.KindGroup && n.Bounds.Contains(x, y)) || (n.Kind == mapmodel.KindFrame && n.Icon.Contains(x, y)) {
			return n
		}
	}
	return nil
}

func (mv *MapView) barAt(p fyne.Position) dragMode {
	s := mv.Size()
	switch {
	case float64(p.Y) >= float64(s.Height)-scrollBarW:
		return dragHBar
	case float64(p.X) >= float64(s.Width)-scrollBarW:
		return dragVBar
	}
	return dragPan
}

// scrollExtent is the area the scroll bars cover: the content plus what
// is visible now.
func (mv *MapView) scrollExtent() (ext, vis mapmodel.Rect) {
	s := mv.Size()
	vis = mv.vp.Visible(float64(s.Width), float64(s.Height))
	return mapmodel.Extent(mv.nodes).Union(vis), vis
}

// MinSize keeps the map usable in a small window.
func (mv *MapView) MinSize() fyne.Size { return fyne.NewSize(200, 150) }

// CreateRenderer implements fyne.Widget.
func (mv *MapView) CreateRenderer() fyne.WidgetRenderer { return &mapRenderer{mv: mv} }

type mapRenderer struct {
	mv   *MapView
	objs []fyne.CanvasObject
	pool pool
}

func (r *mapRenderer) Destroy()                     {}
func (r *mapRenderer) MinSize() fyne.Size           { return r.mv.MinSize() }
func (r *mapRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *mapRenderer) Layout(fyne.Size)             { r.build() }
func (r *mapRenderer) Refresh()                     { r.build(); canvas.Refresh(r.mv) }

// build creates the canvas objects for everything near the visible area.
func (r *mapRenderer) build() {
	mv := r.mv
	s := mv.Size()
	if s.Width <= 0 || s.Height <= 0 {
		return
	}
	if !mv.placed && len(mv.nodes) > 0 {
		mv.vp = mapmodel.Fit(mapmodel.Extent(mv.nodes), float64(s.Width), float64(s.Height))
		mv.placed = true
		defer mv.viewportChanged()
	}
	p := mv.palette()
	r.pool.reset()
	d := &drawer{vp: mv.vp, p: p, pool: &r.pool, objs: r.objs[:0]}
	bg := d.pool.rect()
	bg.FillColor, bg.StrokeColor, bg.StrokeWidth = nrgba(p.Background), nil, 0
	bg.Move(fyne.NewPos(0, 0))
	bg.Resize(s)
	d.objs = append(d.objs, bg)

	vis := mv.vp.Visible(float64(s.Width), float64(s.Height))
	vis = mapmodel.Rect{X: vis.X - cullMargin, Y: vis.Y - cullMargin, W: vis.W + 2*cullMargin, H: vis.H + 2*cullMargin}
	// Dragged items are drawn last, on top of everything else.
	order := make([]*mapmodel.Node, 0, len(mv.nodes))
	var dragged []*mapmodel.Node
	for i := range mv.nodes {
		n := &mv.nodes[i]
		if mv.dragSet[mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}] {
			dragged = append(dragged, shifted(n, mv.dragDX, mv.dragDY))
			continue
		}
		order = append(order, n)
	}
	for _, n := range append(order, dragged...) {
		if !n.Bounds.Intersects(vis) {
			continue
		}
		selected := mv.sels[mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}]
		switch n.Kind {
		case mapmodel.KindFrame:
			d.frame(n, vis, selected)
		default:
			d.icon(n, selected)
		}
	}
	if mv.drag == dragBand { // the rubber band (SPEC-724)
		x0, x1 := math.Min(mv.bandX0, mv.bandX1), math.Max(mv.bandX0, mv.bandX1)
		y0, y1 := math.Min(mv.bandY0, mv.bandY1), math.Max(mv.bandY0, mv.bandY1)
		d.rect(mapmodel.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, color.Transparent, nrgba(p.Primary), 1.5)
	}
	if t := mv.dropTarget; t != nil { // SPEC-704: mark the drop target
		r := t.Icon
		if t.Kind == mapmodel.KindGroup {
			r = t.Bounds
		}
		d.rect(mapmodel.Rect{X: r.X - 6, Y: r.Y - 6, W: r.W + 12, H: r.H + 12}, color.Transparent, nrgba(p.Primary), 4)
		d.text("into", r.X+r.W+10, r.Y-6, nrgba(p.Primary), false, true)
	}
	d.scrollBars(mv, s)
	r.objs = d.objs
}

// shifted returns a copy of a node moved by (dx, dy), for drawing a drag.
func shifted(n *mapmodel.Node, dx, dy float64) *mapmodel.Node {
	c := *n
	it := *n.Item
	it.X, it.Y = it.X+dx, it.Y+dy
	c.Item = &it
	c.Icon.X, c.Icon.Y = c.Icon.X+dx, c.Icon.Y+dy
	c.Bounds.X, c.Bounds.Y = c.Bounds.X+dx, c.Bounds.Y+dy
	return &c
}

// drawer turns nodes into canvas objects in screen coordinates. Objects
// come from a pool and are reused by the next build, so a redraw doesn't
// allocate new objects and text textures stay cached (crosscheck
// 2026-11-11-02 C1).
type drawer struct {
	vp   mapmodel.Viewport
	p    theme.Palette
	pool *pool
	objs []fyne.CanvasObject
}

// pool keeps the canvas objects of the previous build for reuse.
type pool struct {
	rects          []*canvas.Rectangle
	lines          []*canvas.Line
	circles        []*canvas.Circle
	texts          []*pooledText
	nr, nl, nc, nt int
}

// pooledText remembers what a text object shows, so an unchanged text
// keeps its texture and size.
type pooledText struct {
	t    *canvas.Text
	key  textKey
	size fyne.Size
}

type textKey struct {
	s    string
	px   float32
	bold bool
}

func (p *pool) reset() { p.nr, p.nl, p.nc, p.nt = 0, 0, 0, 0 }

func (p *pool) rect() *canvas.Rectangle {
	if p.nr == len(p.rects) {
		p.rects = append(p.rects, canvas.NewRectangle(nil))
	}
	p.nr++
	return p.rects[p.nr-1]
}

func (p *pool) line() *canvas.Line {
	if p.nl == len(p.lines) {
		p.lines = append(p.lines, canvas.NewLine(nil))
	}
	p.nl++
	return p.lines[p.nl-1]
}

func (p *pool) circle() *canvas.Circle {
	if p.nc == len(p.circles) {
		p.circles = append(p.circles, canvas.NewCircle(nil))
	}
	p.nc++
	return p.circles[p.nc-1]
}

// text returns a text object showing s; only a changed text is refreshed
// and measured again.
func (p *pool) text(s string, px float32, bold bool, c color.Color) (*canvas.Text, fyne.Size) {
	if p.nt == len(p.texts) {
		p.texts = append(p.texts, &pooledText{t: canvas.NewText("", c)})
	}
	pt := p.texts[p.nt]
	p.nt++
	pt.t.Color = c
	if k := (textKey{s, px, bold}); k != pt.key {
		pt.key = k
		pt.t.Text, pt.t.TextSize, pt.t.TextStyle.Bold = s, px, bold
		pt.size = pt.t.MinSize()
		pt.t.Refresh()
	}
	return pt.t, pt.size
}

func (d *drawer) pt(x, y float64) fyne.Position {
	sx, sy := d.vp.ToScreen(x, y)
	return fyne.NewPos(float32(sx), float32(sy))
}

func (d *drawer) stroke(w float64) float32 { return float32(math.Max(1, w*d.vp.Zoom)) }

func (d *drawer) rect(r mapmodel.Rect, fill, stroke color.Color, w float64) {
	o := d.pool.rect()
	o.FillColor, o.StrokeColor, o.StrokeWidth = fill, stroke, d.stroke(w)
	o.Move(d.pt(r.X, r.Y))
	o.Resize(fyne.NewSize(float32(r.W*d.vp.Zoom), float32(r.H*d.vp.Zoom)))
	d.objs = append(d.objs, o)
}

func (d *drawer) line(x0, y0, x1, y1 float64, c color.Color, w float64) {
	o := d.pool.line()
	o.StrokeColor, o.StrokeWidth = c, d.stroke(w)
	o.Position1, o.Position2 = d.pt(x0, y0), d.pt(x1, y1)
	d.objs = append(d.objs, o)
}

// text draws a line of text with its top centre (or top left) at a map
// point. It returns false when the text is too small to read.
func (d *drawer) text(s string, x, y float64, c color.Color, centre, bold bool) bool {
	size := mapTextSize * d.vp.Zoom
	if size < minTextPixels {
		return false
	}
	t, m := d.pool.text(s, float32(size), bold, c)
	pos := d.pt(x, y)
	if centre {
		pos.X -= m.Width / 2
	}
	t.Move(pos)
	t.Resize(m)
	d.objs = append(d.objs, t)
	return true
}

// glyph draws the status sign (SPEC-702): ● up, ✕ down, ? unknown; a
// dash for an unmonitored host. Stale glyphs are hollow or thin (SPEC-708).
func (d *drawer) glyph(it *apitypes.MapItem, cx, cy, size float64) {
	r := size / 2
	if it.Type == apitypes.TypeHost && !it.Monitored {
		d.line(cx-r, cy, cx+r, cy, nrgba(d.p.Off), 2.5)
		return
	}
	col := nrgba(d.p.Status(it.Status))
	switch it.Status {
	case apitypes.StatusUp:
		c := d.pool.circle()
		c.FillColor, c.StrokeColor, c.StrokeWidth = col, nil, 0
		if it.Stale {
			c.FillColor, c.StrokeColor, c.StrokeWidth = color.Transparent, col, d.stroke(2)
		}
		c.Move(d.pt(cx-r, cy-r))
		c.Resize(fyne.NewSize(float32(size*d.vp.Zoom), float32(size*d.vp.Zoom)))
		d.objs = append(d.objs, c)
	case apitypes.StatusDown:
		w := 3.0
		if it.Stale {
			w = 1.2
		}
		d.line(cx-r, cy-r, cx+r, cy+r, col, w)
		d.line(cx-r, cy+r, cx+r, cy-r, col, w)
	default:
		if size*1.3*d.vp.Zoom < minTextPixels {
			return // too small to read: the empty box is the sign
		}
		t, m := d.pool.text("?", float32(size*1.3*d.vp.Zoom), !it.Stale, col)
		p := d.pt(cx, cy)
		t.Move(fyne.NewPos(p.X-m.Width/2, p.Y-m.Height/2))
		t.Resize(m)
		d.objs = append(d.objs, t)
	}
}

func (d *drawer) frameColor(it *apitypes.MapItem) color.Color {
	if it.CIDR != "" {
		return nrgba(d.p.NetFrame)
	}
	return nrgba(d.p.Frame)
}

// icon draws a host or a collapsed group with its labels.
func (d *drawer) icon(n *mapmodel.Node, selected bool) {
	it, ic := n.Item, n.Icon
	text := nrgba(d.p.Text)
	bg := nrgba(d.p.Background)
	if n.Kind == mapmodel.KindHost {
		d.rect(ic, bg, text, 1.5)
		d.glyph(it, ic.X+ic.W/2, ic.Y+ic.H/2, ic.H*0.55)
	} else {
		fc := d.frameColor(it)
		// A folder: the body and a tab on its top left.
		tabH := mapmodel.GroupTabH
		d.rect(mapmodel.Rect{X: ic.X, Y: ic.Y - tabH, W: ic.W * 0.42, H: tabH}, bg, fc, 1.5)
		d.rect(ic, bg, fc, 1.5)
		if it.CIDR != "" { // a network group: double border
			d.rect(mapmodel.Rect{X: ic.X + 3, Y: ic.Y + 3, W: ic.W - 6, H: ic.H - 6}, color.Transparent, fc, 1)
		}
		d.glyph(it, ic.X+ic.W/2, ic.Y+ic.H/2, ic.H*0.45)
	}
	if it.HasNote { // a folded top-right corner (SPEC-711)
		const k = 7.0
		x1, y0 := ic.X+ic.W, ic.Y
		d.line(x1-k, y0, x1, y0+k, text, 1.2)
		d.line(x1-k, y0, x1-k, y0+k, text, 1.2)
		d.line(x1-k, y0+k, x1, y0+k, text, 1.2)
	}
	y := ic.Y + ic.H + mapmodel.LabelGap
	for i, s := range n.Labels {
		if !d.text(s, it.X, y, text, true, i < max(1, n.NameLines)) {
			break
		}
		_, h := measure(s)
		y += h
	}
	if selected {
		b := n.Bounds
		d.rect(mapmodel.Rect{X: b.X - 4, Y: b.Y - 8, W: b.W + 8, H: b.H + 12}, color.Transparent, nrgba(d.p.Primary), 2.5)
	}
}

// frame draws an expanded group: a solid (ordinary) or dashed (network)
// border, the label strip with the status glyph, name and status word.
func (d *drawer) frame(n *mapmodel.Node, vis mapmodel.Rect, selected bool) {
	b, it := n.Bounds, n.Item
	fc := d.frameColor(it)
	if it.CIDR != "" {
		d.dashedRect(b, vis, fc)
	} else {
		d.rect(b, color.Transparent, fc, 1.5)
	}
	d.line(b.X, b.Y+mapmodel.FrameLabelH, b.X+b.W, b.Y+mapmodel.FrameLabelH, fc, 1)
	g := mapmodel.FrameLabelH * 0.5
	d.glyph(it, b.X+8+g/2, b.Y+mapmodel.FrameLabelH/2, g)
	_, th := measure(n.Labels[0])
	d.text(n.Labels[0], b.X+14+g, b.Y+(mapmodel.FrameLabelH-th)/2, nrgba(d.p.Text), false, true)
	if selected {
		d.rect(mapmodel.Rect{X: b.X - 4, Y: b.Y - 4, W: b.W + 8, H: b.H + 8}, color.Transparent, nrgba(d.p.Primary), 2.5)
	}
}

// dashedRect draws the visible part of a dashed border.
func (d *drawer) dashedRect(b, vis mapmodel.Rect, c color.Color) {
	const dash, gap = 8.0, 5.0
	seg := func(x0, y0, x1, y1 float64) {
		horiz := y0 == y1
		lo, hi := x0, x1
		if !horiz {
			lo, hi = y0, y1
			if x0 < vis.X || x0 > vis.X+vis.W {
				return
			}
			lo, hi = math.Max(lo, vis.Y), math.Min(hi, vis.Y+vis.H)
		} else {
			if y0 < vis.Y || y0 > vis.Y+vis.H {
				return
			}
			lo, hi = math.Max(lo, vis.X), math.Min(hi, vis.X+vis.W)
		}
		start := x0
		if !horiz {
			start = y0
		}
		// Keep the dash pattern anchored to the corner while clipping.
		for p := start + math.Floor((lo-start)/(dash+gap))*(dash+gap); p < hi; p += dash + gap {
			e := math.Min(p+dash, hi)
			if horiz {
				d.line(math.Max(p, lo), y0, e, y0, c, 1.5)
			} else {
				d.line(x0, math.Max(p, lo), x0, e, c, 1.5)
			}
		}
	}
	x1, y1 := b.X+b.W, b.Y+b.H
	seg(b.X, b.Y, x1, b.Y)
	seg(b.X, y1, x1, y1)
	seg(b.X, b.Y, b.X, y1)
	seg(x1, b.Y, x1, y1)
}

// scrollBars draws a horizontal and a vertical bar with a thumb for the
// visible part (SPEC-713).
func (d *drawer) scrollBars(mv *MapView, s fyne.Size) {
	ext, vis := mv.scrollExtent()
	if ext.W <= 0 || ext.H <= 0 {
		return
	}
	w, h := float64(s.Width), float64(s.Height)
	track, thumb := nrgba(d.p.Surface), nrgba(d.p.Border)
	add := func(x, y, rw, rh float64, c color.Color) {
		o := d.pool.rect()
		o.FillColor, o.StrokeColor, o.StrokeWidth = c, nil, 0
		o.Move(fyne.NewPos(float32(x), float32(y)))
		o.Resize(fyne.NewSize(float32(rw), float32(rh)))
		d.objs = append(d.objs, o)
	}
	lw := w - scrollBarW
	add(0, h-scrollBarW, lw, scrollBarW, track)
	add(lw*(vis.X-ext.X)/ext.W, h-scrollBarW+2, math.Max(8, lw*vis.W/ext.W), scrollBarW-4, thumb)
	lh := h - scrollBarW
	add(w-scrollBarW, 0, scrollBarW, lh, track)
	add(w-scrollBarW+2, lh*(vis.Y-ext.Y)/ext.H, scrollBarW-4, math.Max(8, lh*vis.H/ext.H), thumb)
}
