// Package gui is the Fyne desktop client of go-ined (SPEC-701..718). The
// GUI's picture of the server lives in mapmodel; this package shows it
// and talks to the server through pkg/client.
package gui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// reloadDelay coalesces map reloads (SPEC-716).
const reloadDelay = 250 * time.Millisecond

// Options configure the GUI.
type Options struct {
	Client   *client.Client
	Contrast theme.Contrast
	// SaveContrast stores a changed contrast setting (SPEC-005); nil
	// means it isn't saved.
	SaveContrast func(theme.Contrast) error
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

	tabs     *container.AppTabs
	maps     map[int64]*MapView
	current  int64 // view of the selected tab
	details  *details
	status   *statusBar
	contrSel *widget.Select
}

// New builds the window. Call Start to connect, then ShowAndRun (or use
// the window directly in tests).
func New(a fyne.App, opt Options) *GUI {
	ctx, cancel := context.WithCancel(context.Background())
	g := &GUI{
		app: a, c: opt.Client, opt: opt, model: mapmodel.New(),
		ctx: ctx, cancel: cancel, maps: map[int64]*MapView{}, current: mapmodel.NetworkView,
	}
	g.do = opt.Do
	if g.do == nil {
		g.do = fyne.Do
	}
	g.setContrast(opt.Contrast)
	g.reload = mapmodel.NewReloader(reloadDelay, g.loadMap)

	title := opt.Title
	if title == "" {
		title = "go-ined"
	}
	g.win = a.NewWindow(title)
	g.status = newStatusBar(g.c.URL(), g.c.TokenInClear())
	for _, w := range opt.Warnings {
		g.status.message(w)
	}
	g.details = newDetails(g)
	g.tabs = container.NewAppTabs()
	g.tabs.OnSelected = func(ti *container.TabItem) { g.selectTab(ti) }
	g.rebuildTabs()

	split := container.NewHSplit(g.tabs, g.details.obj)
	split.Offset = 0.75
	g.win.SetContent(container.NewBorder(g.toolbar(), g.status.obj, nil, nil, split))
	g.win.Resize(fyne.NewSize(1200, 800))
	g.shortcuts()
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
	g.cancel()
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
	vs, err := g.c.Views(g.ctx)
	if err != nil {
		g.fail("load views", err)
		return
	}
	g.mu.Lock()
	g.model.SetViews(vs)
	views := g.model.LoadedViews()
	g.mu.Unlock()
	g.do(g.rebuildTabs)
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
	g.do(func() { g.redraw(view) })
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
		g.redraw(v)
	}
	for _, v := range eff.Reload {
		g.reload.Request(v)
	}
	if eff.ViewsChanged {
		g.rebuildTabs()
	}
	if eff.Resync {
		go g.loadAll()
	}
	g.details.onEvent(ev)
}

// redraw lays out a loaded view again.
func (g *GUI) redraw(view int64) {
	mv := g.maps[view]
	if mv == nil {
		return
	}
	g.mu.Lock()
	nodes := g.model.Layout(view, measure)
	g.mu.Unlock()
	mv.SetNodes(nodes)
	if view == g.current {
		g.details.refreshParent()
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
	mv.OnSelect = func(k *mapmodel.Key) { g.details.show(view, k) }
	mv.OnToggle = func(group int64, expand bool) { g.setCollapsed(group, !expand) }
	mv.OnMenu = func(n *mapmodel.Node, at fyne.Position) { g.contextMenu(n, at) }
	g.maps[view] = mv
	return mv
}

func (g *GUI) selectTab(ti *container.TabItem) {
	mv, ok := ti.Content.(*MapView)
	if !ok {
		return
	}
	g.setCurrent(mv.view)
	g.details.show(mv.view, mv.Selected())
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
func (g *GUI) setCollapsed(group int64, collapsed bool) {
	go func() {
		if _, err := g.c.UpdateGroup(g.ctx, group, apitypes.GroupPatch{Collapsed: apitypes.Some(collapsed)}); err != nil {
			g.fail(fmt.Sprintf("group %d", group), err)
		}
	}()
}

func (g *GUI) contextMenu(n *mapmodel.Node, at fyne.Position) {
	if n == nil || n.Item.Type != apitypes.TypeGroup {
		return
	}
	id := n.Item.ID
	items := []*fyne.MenuItem{
		fyne.NewMenuItem("Expand", func() { g.setCollapsed(id, false) }),
		fyne.NewMenuItem("Collapse", func() { g.setCollapsed(id, true) }),
	}
	if n.Kind == mapmodel.KindFrame {
		items[0].Disabled = true
	} else {
		items[1].Disabled = true
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), g.win.Canvas(), at)
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

func (g *GUI) toolbar() fyne.CanvasObject {
	btn := func(label string, f func()) *widget.Button { return widget.NewButton(label, f) }
	g.contrSel = widget.NewSelect(contrastOptions(), func(s string) { g.pickContrast(s) })
	g.contrSel.SetSelected(contrastOption(g.contrast))
	return container.NewHBox(
		btn("Zoom −", func() { g.zoomBy(1 / zoomStep) }),
		btn("Zoom +", func() { g.zoomBy(zoomStep) }),
		btn("100 %", func() { g.zoomTo(1) }),
		btn("Fit all", g.fitAll),
		widget.NewSeparator(),
		btn("Find host…", g.showFind),
		widget.NewSeparator(),
		widget.NewLabel("Contrast:"), g.contrSel,
	)
}

// shortcuts: Ctrl+= / Ctrl++ zoom in, Ctrl+- zoom out, Ctrl+0 100 %,
// Ctrl+1 fit all, Ctrl+F find host.
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
}
