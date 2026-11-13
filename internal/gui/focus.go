package gui

import (
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// ---- a group as a view: the focus, the trail and the "Show" list (SPEC-726) ----

// focusOf returns the focus group of a view's tab (0: the whole view).
func (g *GUI) focusOf(view int64) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.focus[view]
}

// setFocus shows a view's tab narrowed to one group (0: the whole view).
// Going down fits the map to the group; going back up restores the zoom
// and pan that level had (F5). The selection is cleared. UI goroutine.
func (g *GUI) setFocus(view, group int64) {
	if !g.showView(view) {
		return
	}
	mv := g.mapView(view)
	g.mu.Lock()
	old := g.focus[view]
	if group != 0 && g.model.Loaded(view) && g.item(view, mapmodel.GroupKey(group)) == nil {
		g.mu.Unlock()
		g.status.message(fmt.Sprintf("group %d is not in this view", group))
		return
	}
	var trail []apitypes.MapItem
	if group != 0 {
		trail = g.model.Trail(view, group)
		g.focus[view] = group
	} else {
		delete(g.focus, view)
	}
	g.mu.Unlock()
	if old == group {
		g.syncNav()
		return
	}
	mv.saved[old] = mv.Viewport()
	up := group == 0 || slices.ContainsFunc(mv.trail, func(it apitypes.MapItem) bool { return it.ID == group })
	mv.trail = trail
	mv.Select(nil)
	g.details.clear()
	g.redraw(view)
	switch vp, ok := mv.saved[group]; {
	case up && ok:
		mv.SetViewport(vp)
	case len(mv.Nodes()) > 0:
		mv.FitAll()
	default: // not loaded yet, or empty: fit once there is something
		mv.fitPending = true
	}
	g.syncNav()
}

// focusUp goes one level up from the current tab's focus (Backspace, F3).
func (g *GUI) focusUp() {
	mv := g.currentMap()
	if mv == nil || g.focusOf(mv.view) == 0 {
		return
	}
	var parent int64
	if n := len(mv.trail); n >= 2 {
		parent = mv.trail[n-2].ID
	}
	g.setFocus(mv.view, parent)
}

// checkFocus keeps a view's focus valid before a layout (F7): when the
// focus group is gone, the focus moves to the nearest group above it that
// still exists, or to the whole view. It refreshes the trail, which also
// follows renames and moves. It returns a message for the status bar.
// Caller holds g.mu.
func (g *GUI) checkFocus(view int64, mv *MapView) string {
	focus := g.focus[view]
	if focus == 0 || !g.model.Loaded(view) {
		return ""
	}
	if g.item(view, mapmodel.GroupKey(focus)) != nil {
		mv.trail = g.model.Trail(view, focus)
		return ""
	}
	gone := fmt.Sprintf("group %d", focus)
	if n := len(mv.trail); n > 0 {
		gone = fmt.Sprintf("group %q", mv.trail[n-1].Name)
	}
	next := int64(0)
	for i := len(mv.trail) - 2; i >= 0; i-- {
		if g.item(view, mapmodel.GroupKey(mv.trail[i].ID)) != nil {
			next = mv.trail[i].ID
			break
		}
	}
	where := "the whole view"
	if next != 0 {
		g.focus[view] = next
		mv.trail = g.model.Trail(view, next)
		where = fmt.Sprintf("group %q", mv.trail[len(mv.trail)-1].Name)
	} else {
		delete(g.focus, view)
		mv.trail = nil
	}
	mv.fitPending = true
	return fmt.Sprintf("%s was deleted; showing %s", gone, where)
}

// movedOut names the items that were shown in the focus and have left it
// while still being in the view (F6). Caller holds g.mu.
func (g *GUI) movedOut(view, focus int64, before, after []mapmodel.Node) string {
	now := map[mapmodel.Key]bool{}
	for _, n := range after {
		now[mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}] = true
	}
	var names []string
	for _, n := range before {
		k := mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}
		if now[k] || g.item(view, k) == nil || g.model.InFocus(view, focus, k) {
			continue
		}
		names = append(names, n.Item.Name)
	}
	if len(names) == 0 {
		return ""
	}
	name := "this group"
	if it := g.item(view, mapmodel.GroupKey(focus)); it != nil {
		name = fmt.Sprintf("%q", it.Name)
	}
	if len(names) == 1 {
		return fmt.Sprintf("%s moved out of %s", names[0], name)
	}
	return fmt.Sprintf("%d items moved out of %s", len(names), name)
}

// navRow is the row above the map: the "Show" list and the trail.
func (g *GUI) navRow() fyne.CanvasObject {
	g.showSel = widget.NewSelectEntry(nil)
	g.showSel.SetPlaceHolder("view or group (Ctrl+L)")
	g.showSel.OnChanged = func(s string) {
		if !g.navQuiet {
			g.onShowText(s)
		}
	}
	g.showSel.OnSubmitted = func(s string) {
		if m := mapmodel.FilterEntries(g.entries, s); len(m) > 0 {
			g.goEntry(m[0])
		}
	}
	g.trailBox = container.NewHBox()
	sel := container.NewGridWrap(fyne.NewSize(300, g.showSel.MinSize().Height), g.showSel)
	return container.NewBorder(nil, nil, container.NewHBox(widget.NewLabel("Show:"), sel), nil, g.trailBox)
}

// onShowText follows changes to the "Show" text (F9). Typing one more
// character only filters the list, also when the text now happens to be a
// whole label ("Lab" on the way to "Lab east", crosscheck 2026-11-13-03
// C2); Enter takes the first match. Any other change that gives a whole
// label, such as a pick from the list, shows that entry.
func (g *GUI) onShowText(s string) {
	prev := g.showPrev
	g.showPrev = s
	typed := len([]rune(s)) == len([]rune(prev))+1 && strings.HasPrefix(s, prev)
	if !typed {
		for _, e := range g.entries {
			if e.Label == s {
				g.goEntry(e)
				return
			}
		}
	}
	g.setShowOptions(entryLabels(mapmodel.FilterEntries(g.entries, s)))
}

func (g *GUI) goEntry(e mapmodel.ShowEntry) {
	g.setFocus(e.View, e.Group)
	g.navSig = ""
	g.syncNav() // also when nothing changed: the text and the list reset
	g.win.Canvas().Unfocus()
}

// setShowOptions sets the lines the "Show" list offers.
func (g *GUI) setShowOptions(opts []string) {
	g.showOpts = opts
	g.showSel.SetOptions(opts)
}

func entryLabels(es []mapmodel.ShowEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Label
	}
	return out
}

// loadGroups fetches every group of every view for the "Show" list.
func (g *GUI) loadGroups() {
	gs, err := g.c.Groups(g.ctx)
	if err != nil {
		g.fail("load groups", err)
		return
	}
	g.do(func() {
		g.groupsAll = gs
		g.rebuildEntries()
	})
}

// rebuildEntries makes the "Show" list from the views and groups. UI
// goroutine.
func (g *GUI) rebuildEntries() {
	g.mu.Lock()
	views := g.model.Views()
	g.mu.Unlock()
	g.entries = mapmodel.ShowEntries(views, g.groupsAll)
	g.navSig = ""
	g.syncNav()
}

// syncNav shows the current tab's focus in the "Show" list and the trail.
// UI goroutine.
func (g *GUI) syncNav() {
	if g.showSel == nil {
		return
	}
	mv := g.currentMap()
	if mv == nil {
		return
	}
	focus := g.focusOf(mv.view)
	g.mu.Lock()
	vname := fmt.Sprintf("view %d", mv.view)
	if v, ok := g.model.View(mv.view); ok {
		vname = v.Name
	}
	g.mu.Unlock()
	var text string
	for _, e := range g.entries {
		if e.View == mv.view && e.Group == focus {
			text = strings.TrimSpace(e.Label)
		}
	}
	parts := []string{vname}
	for _, it := range mv.trail {
		parts = append(parts, it.Name)
	}
	if focus == 0 {
		parts = parts[:1]
	}
	sig := fmt.Sprint(mv.view, focus, text, parts, len(g.entries))
	if sig == g.navSig {
		return
	}
	g.navSig = sig
	g.navQuiet = true
	g.setShowOptions(entryLabels(g.entries))
	g.showSel.SetText(text)
	g.showPrev = text
	g.navQuiet = false

	view := mv.view
	// The old trail's button borders leave the list colourBorders keeps.
	g.borders = slices.DeleteFunc(g.borders, func(r *canvas.Rectangle) bool { return slices.Contains(g.trailBorders, r) })
	g.trailBorders = nil
	objs := []fyne.CanvasObject{}
	for i, p := range parts {
		if i > 0 {
			objs = append(objs, widget.NewLabel("›"))
		}
		if i == len(parts)-1 {
			objs = append(objs, widget.NewLabelWithStyle(p, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
			continue
		}
		target := int64(0)
		if i > 0 {
			target = mv.trail[i-1].ID
		}
		// Buttons, as in the toolbar, so they look clickable; the current
		// part is plain bold text.
		objs = append(objs, g.bordered(widget.NewButton(p, func() { g.setFocus(view, target) })))
		g.trailBorders = append(g.trailBorders, g.borders[len(g.borders)-1])
	}
	g.trailBox.Objects = objs
	g.trailBox.Refresh()
}

// trailText is the trail as text, for tests: "Networks › lan › servers".
func (g *GUI) trailText() string {
	var parts []string
	for _, o := range g.trailBox.Objects {
		if st, ok := o.(*fyne.Container); ok { // a bordered button
			o = st.Objects[0]
		}
		switch w := o.(type) {
		case *widget.Button:
			parts = append(parts, w.Text)
		case *widget.Label:
			if w.Text != "›" {
				parts = append(parts, w.Text)
			}
		}
	}
	return strings.Join(parts, " › ")
}
