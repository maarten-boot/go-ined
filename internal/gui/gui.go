// Package gui is the Fyne desktop client of go-ined (SPEC-701..718). The
// GUI's picture of the server lives in mapmodel; this package shows it
// and talks to the server through pkg/client.
package gui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"math"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// reloadDelay coalesces map reloads, redrawEvery redraws (SPEC-716).
const (
	reloadDelay = 250 * time.Millisecond
	redrawEvery = 100 * time.Millisecond
)

// Options configure the GUI.
type Options struct {
	Client   *client.Client
	Contrast theme.Contrast
	// SaveContrast stores a changed contrast setting (SPEC-005); nil
	// means it isn't saved.
	SaveContrast func(theme.Contrast) error
	// WrapNames is the start value of the "Wrap names" toggle (SPEC-722);
	// SaveWrapNames, if set, stores a change.
	WrapNames     bool
	SaveWrapNames func(bool) error
	// Warnings are shown in the status bar at start (config problems).
	Warnings []string
	// Title of the window; default "go-ined".
	Title string
	// Do runs a function on the UI goroutine; default fyne.Do. Tests pass
	// a serial queue, because Fyne's test driver runs fyne.Do at once on
	// the calling goroutine.
	Do func(func())
}

// GUI is one running desktop client.
type GUI struct {
	app fyne.App
	win fyne.Window
	c   *client.Client
	opt Options

	mu    sync.Mutex // guards model
	model *mapmodel.Model

	do     func(func())
	ctx    context.Context
	cancel context.CancelFunc
	reload *mapmodel.Reloader
	stream *client.Stream

	contrast theme.Contrast
	palette  theme.Palette

	tabs    *container.AppTabs
	maps    map[int64]*MapView
	current int64 // view of the selected tab

	// Coalesced redraws (UI goroutine only): views waiting for a redraw,
	// whether a flush is scheduled, when the last one ran, and how many
	// layouts were made (for tests).
	dirty     map[int64]bool
	flushing  bool
	lastFlush time.Time
	layouts   int
	details   *details
	status    *statusBar
	contrSel  *widget.Select
	zoomL     *widget.Label       // the current map's zoom (crosscheck 2026-11-11-02 I1)
	borders   []*canvas.Rectangle // button borders, shown in Max contrast (D1)

	// Editing (M6).
	lastForm *formDialog      // the form opened last (for tests)
	disc     *discoveryDialog // the open discovery dialog
	jobs     map[string]bool  // discovery jobs started here, still running
	wantView int64            // a new view to select once its tab exists
	viewBtn  *widget.Button
	wrapChk  *widget.Check

	// A group as a view (SPEC-726): each tab's focus group (guarded by
	// mu; absent: the whole view), and on the UI goroutine every group of
	// every view, the "Show" list, the trail and its last state.
	focus        map[int64]int64
	groupsReload *mapmodel.Reloader
	groupsAll    []apitypes.Group
	entries      []mapmodel.ShowEntry
	showSel      *widget.SelectEntry
	showOpts     []string // what the "Show" list offers now
	trailBox     *fyne.Container
	navQuiet     bool   // the "Show" text is being set, not typed
	showPrev     string // the "Show" text before the last change, to tell typing from a pick
	navSig       string
	trailBorders []*canvas.Rectangle // the trail buttons' borders, in borders
}

// New builds the window. Call Start to connect, then ShowAndRun (or use
// the window directly in tests).
func New(a fyne.App, opt Options) *GUI {
	ctx, cancel := context.WithCancel(context.Background())
	g := &GUI{
		app: a, c: opt.Client, opt: opt, model: mapmodel.New(),
		ctx: ctx, cancel: cancel, maps: map[int64]*MapView{}, current: mapmodel.NetworkView,
		dirty: map[int64]bool{}, jobs: map[string]bool{}, focus: map[int64]int64{},
	}
	g.do = opt.Do
	if g.do == nil {
		g.do = fyne.Do
	}
	g.setContrast(opt.Contrast)
	g.model.WrapNames = opt.WrapNames
	g.reload = mapmodel.NewReloader(reloadDelay, g.loadMap)
	g.groupsReload = mapmodel.NewReloader(reloadDelay, func(int64) { g.loadGroups() })

	title := opt.Title
	if title == "" {
		title = "go-ined"
	}
	g.win = a.NewWindow(title)
	g.status = newStatusBar(g.c.URL(), g.c.TokenInClear(), a.Clipboard)
	for _, w := range opt.Warnings {
		g.status.message(w)
	}
	g.details = newDetails(g)
	g.tabs = container.NewAppTabs()
	g.tabs.OnSelected = func(ti *container.TabItem) { g.selectTab(ti) }
	g.rebuildTabs()

	split := container.NewHSplit(g.tabs, g.details.obj)
	split.Offset = 0.75
	top := container.NewVBox(g.toolbar(), g.navRow())
	g.win.SetContent(container.NewBorder(top, g.status.obj, nil, nil, split))
	g.win.Resize(fyne.NewSize(1200, 800))
	g.shortcuts()
	g.win.Canvas().SetOnTypedKey(g.onKey)
	g.win.SetOnClosed(g.Stop)
	return g
}

// Window returns the main window.
func (g *GUI) Window() fyne.Window { return g.win }

// Start connects to the server: it opens the event stream, which loads
// the views and maps on every (re)connect (SPEC-705, SPEC-716).
func (g *GUI) Start() {
	g.stream = g.c.Events(g.ctx, client.StreamOptions{OnState: func(st client.StreamState) {
		g.do(func() { g.status.conn(st) })
		if st.Connected {
			go g.loadAll()
		}
	}})
	go func() {
		t := time.NewTicker(healthEvery)
		defer t.Stop()
		for {
			select {
			case <-g.ctx.Done():
				return
			case <-t.C:
				g.checkHealth()
			}
		}
	}()
	go func() {
		for ev := range g.stream.C() {
			g.do(func() { g.apply(ev) })
		}
		if err := g.stream.Err(); err != nil {
			g.do(func() { g.status.stopped(err) })
		}
	}()
}

// Stop ends the event stream and pending reloads.
func (g *GUI) Stop() {
	g.reload.Stop()
	g.groupsReload.Stop()
	g.cancel()
}

// healthEvery is how often the GUI reads /health for the ping warning, so
// the warning goes away once the server finds ICMP (v0.27).
const healthEvery = 30 * time.Second

// checkHealth updates the ping warning from /health.
func (g *GUI) checkHealth() {
	if h, err := g.c.Health(g.ctx); err == nil {
		g.do(func() { g.status.ping(pingWarning(h)) })
	}
}

// ShowAndRun starts the GUI and runs the Fyne event loop.
func (g *GUI) ShowAndRun() {
	g.Start()
	g.win.ShowAndRun()
	g.Stop()
}

// ---- loading ----

// loadAll reloads the views, the selected view's map and every other
// loaded map (start, reconnect, resync).
func (g *GUI) loadAll() {
	g.checkHealth()
	var views []int64
	for try := 0; ; try++ {
		g.mu.Lock()
		gen := g.model.ViewsGen()
		g.mu.Unlock()
		vs, err := g.c.Views(g.ctx)
		if err != nil {
			g.fail("load views", err)
			return
		}
		g.mu.Lock()
		// A view event applied during the fetch may be missing from vs.
		if g.model.ViewsGen() != gen && try < 3 {
			g.mu.Unlock()
			continue
		}
		g.model.SetViews(vs)
		views = g.model.LoadedViews()
		g.mu.Unlock()
		break
	}
	g.do(g.rebuildTabs)
	g.loadGroups()
	loaded := map[int64]bool{}
	for _, v := range views {
		loaded[v] = true
	}
	cur := g.currentView()
	loaded[cur] = true
	for v := range loaded {
		g.loadMap(v)
	}
}

// loadMap fetches one view's map. If the map changed while it was being
// fetched, the fresh copy may miss an event, so it is fetched again.
func (g *GUI) loadMap(view int64) {
	g.mu.Lock()
	gen := g.model.Gen(view)
	g.mu.Unlock()
	m, err := g.c.ViewMap(g.ctx, view)
	if err != nil {
		var ae *client.APIError
		if errors.As(err, &ae) && ae.Status == 404 {
			return // the view was deleted meanwhile; view.deleted follows
		}
		g.fail(fmt.Sprintf("load view %d", view), err)
		return
	}
	g.mu.Lock()
	again := g.model.Loaded(view) && g.model.Gen(view) != gen
	g.model.SetMap(m)
	g.mu.Unlock()
	if again {
		g.reload.Request(view)
	}
	g.do(func() { g.markDirty(view) })
}

func (g *GUI) fail(what string, err error) {
	if g.ctx.Err() != nil {
		return
	}
	g.do(func() { g.status.message(what + ": " + err.Error()) })
}

func (g *GUI) currentView() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current
}

// ---- events (UI goroutine) ----

func (g *GUI) apply(ev client.Event) {
	g.mu.Lock()
	eff, err := g.model.Apply(ev.Type, ev.Data)
	g.mu.Unlock()
	if err != nil {
		g.status.message(err.Error())
		return
	}
	for _, v := range eff.Redraw {
		g.markDirty(v)
	}
	for _, v := range eff.Reload {
		g.reload.Request(v)
	}
	if eff.ViewsChanged {
		g.rebuildTabs()
	}
	switch ev.Type { // the "Show" list (SPEC-726)
	case apitypes.EventGroupCreated, apitypes.EventGroupUpdated, apitypes.EventGroupDeleted,
		apitypes.EventViewCreated, apitypes.EventViewUpdated, apitypes.EventViewDeleted:
		g.groupsReload.Request(0)
	}
	if eff.Resync {
		go g.loadAll()
	}
	g.details.onEvent(ev)
	if ev.Type == apitypes.EventDiscoveryProgress || ev.Type == apitypes.EventDiscoveryFinished {
		g.onDiscoveryEvent(ev)
	}
}

// markDirty asks for a redraw of a view. Redraws are coalesced: the flush
// runs after the events already queued, and at most every redrawEvery, so
// a burst of events costs one layout per view (SPEC-716, crosscheck
// 2026-11-11-02 C1). UI goroutine only.
func (g *GUI) markDirty(view int64) {
	g.dirty[view] = true
	if g.flushing {
		return
	}
	g.flushing = true
	if wait := redrawEvery - time.Since(g.lastFlush); wait > 0 {
		time.AfterFunc(wait, func() {
			if g.ctx.Err() == nil { // not after Stop
				g.do(g.flush)
			}
		})
		return
	}
	g.do(g.flush)
}

// flush redraws the views marked dirty.
func (g *GUI) flush() {
	g.flushing = false
	g.lastFlush = time.Now()
	for v := range g.dirty {
		delete(g.dirty, v)
		g.redraw(v)
	}
}

// redraw lays out a loaded view again, narrowed to the tab's focus group
// if it has one (SPEC-726).
func (g *GUI) redraw(view int64) {
	mv := g.maps[view]
	if mv == nil {
		return
	}
	g.mu.Lock()
	msg := g.checkFocus(view, mv)
	focus := g.focus[view]
	nodes := g.model.LayoutIn(view, focus, measure)
	if msg == "" && focus != 0 && focus == mv.shownFocus {
		msg = g.movedOut(view, focus, mv.Nodes(), nodes)
	}
	g.mu.Unlock()
	g.layouts++
	mv.empty = ""
	if n := len(mv.trail); focus != 0 && n > 0 && len(nodes) == 0 {
		mv.empty = fmt.Sprintf("group %q is empty", mv.trail[n-1].Name)
	}
	mv.SetNodes(nodes)
	mv.shownFocus = focus
	if mv.fitPending && len(nodes) > 0 {
		mv.fitPending = false
		mv.FitAll()
	}
	if msg != "" {
		g.status.message(msg)
	}
	if view == g.current {
		g.details.refreshParent()
		g.syncNav()
	}
}

// ---- tabs (SPEC-715) ----

// rebuildTabs makes the tabs match the views, keeping each view's map.
func (g *GUI) rebuildTabs() {
	g.mu.Lock()
	views := g.model.Views()
	g.mu.Unlock()
	if len(views) == 0 { // not loaded yet: a placeholder network tab
		views = []apitypes.View{{ID: mapmodel.NetworkView, Name: "Networks"}}
	}
	items := make([]*container.TabItem, 0, len(views))
	sel := -1
	for i, v := range views {
		mv := g.mapView(v.ID)
		items = append(items, &container.TabItem{Text: v.Name, Content: mv})
		if v.ID == g.current {
			sel = i
		}
	}
	for id := range g.maps {
		if !containsView(views, id) {
			delete(g.maps, id)
			g.mu.Lock()
			delete(g.focus, id)
			g.mu.Unlock()
		}
	}
	g.tabs.OnSelected = nil
	g.tabs.SetItems(items)
	if sel < 0 {
		if g.current != mapmodel.NetworkView {
			g.status.message(fmt.Sprintf("view %d was deleted; showing the network view", g.current))
		}
		g.setCurrent(mapmodel.NetworkView)
		g.details.clear()
		for i, v := range views {
			if v.ID == mapmodel.NetworkView {
				sel = i
			}
		}
	}
	if sel >= 0 {
		g.tabs.SelectIndex(sel)
	}
	g.tabs.OnSelected = func(ti *container.TabItem) { g.selectTab(ti) }
	if id := g.wantView; id != 0 && containsView(views, id) {
		g.wantView = 0
		g.showView(id)
	}
	if g.showSel != nil {
		g.rebuildEntries()
	}
}

func containsView(vs []apitypes.View, id int64) bool {
	for _, v := range vs {
		if v.ID == id {
			return true
		}
	}
	return false
}

func (g *GUI) setCurrent(v int64) {
	g.mu.Lock()
	g.current = v
	g.mu.Unlock()
}

// mapView returns (creating it once) the map widget of a view.
func (g *GUI) mapView(view int64) *MapView {
	if mv := g.maps[view]; mv != nil {
		return mv
	}
	mv := NewMapView(view, func() theme.Palette { return g.palette })
	mv.saved = map[int64]mapmodel.Viewport{}
	mv.OnSelect = func(k *mapmodel.Key) { g.details.show(view, k) }
	mv.OnToggle = func(group int64, expand bool) { g.setCollapsed(group, !expand) }
	g.wireEditing(mv)
	mv.OnViewport = func(mapmodel.Viewport) {
		if view == g.current {
			g.showZoom()
		}
	}
	g.maps[view] = mv
	return mv
}

func (g *GUI) selectTab(ti *container.TabItem) {
	mv, ok := ti.Content.(*MapView)
	if !ok {
		return
	}
	g.setCurrent(mv.view)
	g.showZoom()
	g.details.show(mv.view, mv.Selected())
	g.syncNav()
	g.mu.Lock()
	loaded := g.model.Loaded(mv.view)
	g.mu.Unlock()
	if !loaded {
		go g.loadMap(mv.view)
	}
}

func (g *GUI) currentMap() *MapView { return g.maps[g.current] }

// ---- changes the GUI may make (SPEC-718) ----

// setCollapsed expands or collapses a group on the server. The map shows
// the change when the server's event arrives.
//
// The group's top-left corner stays where it is (v0.29): on collapse the
// icon goes to the frame's top-left corner; on expand the contents move
// first so that the frame starts at the icon's top-left corner. A group
// already in that state is left alone: for an expanded group the stored
// icon position isn't the frame's corner, so "expanding" it again would
// move its contents (crosscheck 2026-11-12-04 C1). UI goroutine only.
func (g *GUI) setCollapsed(group int64, collapsed bool) {
	patch := apitypes.GroupPatch{Collapsed: apitypes.Some(collapsed)}
	var dx, dy float64
	g.mu.Lock()
	view, ok := g.model.GroupView(group)
	it := g.item(view, mapmodel.GroupKey(group))
	if ok && it != nil && it.Collapsed == collapsed {
		g.mu.Unlock()
		return
	}
	if ok && it != nil {
		if collapsed {
			for _, n := range g.maps[view].Nodes() {
				if n.Kind == mapmodel.KindFrame && n.Item.ID == group {
					x, y := mapmodel.CenterForCorner(n.Bounds.X, n.Bounds.Y)
					patch.X, patch.Y = apitypes.Some(x), apitypes.Some(y)
				}
			}
		} else if fr, visible := g.model.FrameIfExpanded(view, group, measure); visible {
			ax, ay := mapmodel.IconCorner(it)
			dx, dy = ax-fr.X, ay-fr.Y
		}
	}
	g.mu.Unlock()
	go func() {
		if dx != 0 || dy != 0 {
			if _, err := g.c.MoveGroupContents(g.ctx, group, dx, dy); err != nil {
				g.fail(fmt.Sprintf("group %d", group), err)
				return
			}
		}
		if _, err := g.c.UpdateGroup(g.ctx, group, patch); err != nil {
			g.fail(fmt.Sprintf("group %d", group), err)
		}
	}()
}

// ---- toolbar and shortcuts (SPEC-713, 717) ----

func (g *GUI) zoomBy(f float64) {
	if mv := g.currentMap(); mv != nil {
		mv.ZoomBy(f)
	}
}

func (g *GUI) zoomTo(z float64) {
	if mv := g.currentMap(); mv != nil {
		mv.ZoomTo(z)
	}
}

func (g *GUI) fitAll() {
	if mv := g.currentMap(); mv != nil {
		mv.FitAll()
	}
}

func zoomLabel(z float64) string { return fmt.Sprintf("%d %%", int(math.Round(z*100))) }

// bordered draws a border around a button. In Max contrast buttons have
// the window's white background, so the border shows where they are; in
// the other presets their grey background does that and the border is
// transparent (crosscheck 2026-11-11-02 D1).
func (g *GUI) bordered(o fyne.CanvasObject) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.StrokeWidth = 1
	g.borders = append(g.borders, r)
	g.colourBorders()
	return container.NewStack(o, r)
}

func (g *GUI) colourBorders() {
	c := color.Color(color.Transparent)
	if g.contrast.Preset == theme.PresetMax {
		c = nrgba(g.palette.Text)
	}
	for _, r := range g.borders {
		r.StrokeColor = c
		r.Refresh()
	}
}

// showZoom shows the zoom of the current tab's map.
func (g *GUI) showZoom() {
	if mv := g.currentMap(); mv != nil && g.zoomL != nil {
		g.zoomL.SetText(zoomLabel(mv.Viewport().Zoom))
	}
}

func (g *GUI) toolbar() fyne.CanvasObject {
	btn := func(label string, f func()) fyne.CanvasObject { return g.bordered(widget.NewButton(label, f)) }
	g.contrSel = widget.NewSelect(contrastOptions(), func(s string) { g.pickContrast(s) })
	g.contrSel.SetSelected(g.contrast.String())
	g.zoomL = widget.NewLabel(zoomLabel(1))
	return container.NewHBox(
		btn("Zoom −", func() { g.zoomBy(1 / zoomStep) }),
		g.zoomL,
		btn("Zoom +", func() { g.zoomBy(zoomStep) }),
		btn("100 %", func() { g.zoomTo(1) }),
		btn("Fit all", g.fitAll),
		widget.NewSeparator(),
		btn("Find host…", g.showFind),
		btn("Discover…", func() { g.openDiscovery("") }),
		g.viewMenuButton(),
		widget.NewSeparator(),
		widget.NewSeparator(),
		g.wrapToggle(),
		widget.NewLabel("Contrast:"), g.bordered(g.contrSel),
	)
}

// wrapToggle is the "Wrap names" check box (SPEC-722).
func (g *GUI) wrapToggle() fyne.CanvasObject {
	g.wrapChk = widget.NewCheck("Wrap names", nil)
	g.wrapChk.SetChecked(g.model.WrapNames)
	g.wrapChk.OnChanged = g.setWrap
	return g.wrapChk
}

// setWrap turns name wrapping on or off at once, and saves the choice.
func (g *GUI) setWrap(on bool) {
	g.mu.Lock()
	g.model.WrapNames = on
	views := g.model.LoadedViews()
	g.mu.Unlock()
	for _, v := range views {
		g.redraw(v)
	}
	if g.opt.SaveWrapNames == nil {
		return
	}
	go func() {
		if err := g.opt.SaveWrapNames(on); err != nil {
			g.fail("wrap names not saved", err)
		}
	}()
}

// viewMenuButton opens the View menu below itself (SPEC-720).
func (g *GUI) viewMenuButton() fyne.CanvasObject {
	g.viewBtn = widget.NewButton("View ▾", nil)
	g.viewBtn.OnTapped = func() {
		pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(g.viewBtn)
		pos.Y += g.viewBtn.Size().Height
		widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", g.viewMenuItems()...), g.win.Canvas(), pos)
	}
	return g.bordered(g.viewBtn)
}

// shortcuts: Ctrl+= / Ctrl++ zoom in, Ctrl+- zoom out, Ctrl+0 100 %,
// Ctrl+1 fit all, Ctrl+F find host, Ctrl+L the "Show" list (SPEC-726).
func (g *GUI) shortcuts() {
	c := g.win.Canvas()
	add := func(k fyne.KeyName, f func()) {
		c.AddShortcut(&desktop.CustomShortcut{KeyName: k, Modifier: fyne.KeyModifierShortcutDefault}, func(fyne.Shortcut) { f() })
	}
	add(fyne.KeyEqual, func() { g.zoomBy(zoomStep) })
	add(fyne.KeyPlus, func() { g.zoomBy(zoomStep) })
	add(fyne.KeyMinus, func() { g.zoomBy(1 / zoomStep) })
	add(fyne.Key0, func() { g.zoomTo(1) })
	add(fyne.Key1, g.fitAll)
	add(fyne.KeyF, g.showFind)
	add(fyne.KeyL, func() { g.win.Canvas().Focus(g.showSel) })
}
