package gui

import (
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Editing on the map (M6): drag and drop, context menus, and the actions
// behind them (SPEC-704, 706, 707, 719, 721). Requests run in goroutines;
// the map shows the server's state when its events arrive. A drag is
// shown at once (Model.Shift); if the server refuses, the view is
// reloaded.

func (g *GUI) wireEditing(mv *MapView) {
	view := mv.view
	mv.OnMenu = func(n *mapmodel.Node, at fyne.Position, mx, my float64) { g.openMenu(view, n, at, mx, my) }
	mv.OnMoved = func(keys []mapmodel.Key, dx, dy float64) {
		for _, k := range g.topLevel(view, keys) {
			g.moveItem(view, k, dx, dy)
		}
	}
	mv.OnDropInto = func(keys []mapmodel.Key, group int64, dx, dy float64) {
		for _, k := range g.topLevel(view, keys) {
			g.mu.Lock()
			it := g.item(view, k)
			g.mu.Unlock()
			if it != nil && it.ParentID != nil && *it.ParentID == group {
				g.moveItem(view, k, dx, dy) // dropped onto its own group: just a move
				continue
			}
			g.moveInto(view, k, &group)
		}
	}
	mv.Subtree = func(k mapmodel.Key) map[mapmodel.Key]bool {
		if k.Type == apitypes.TypeHost {
			return map[mapmodel.Key]bool{k: true}
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.model.Subtree(view, k.ID)
	}
}

// item returns a loaded item; caller holds g.mu.
func (g *GUI) item(view int64, k mapmodel.Key) *apitypes.MapItem {
	if vm := g.model.Map(view); vm != nil {
		return vm.Items[k]
	}
	return nil
}

// moveItem saves a drag onto empty space: a host's new position (network
// view: PATCH; custom view: PUT placement), or a group with everything in
// it (POST move).
func (g *GUI) moveItem(view int64, k mapmodel.Key, dx, dy float64) {
	g.mu.Lock()
	it := g.item(view, k)
	if it == nil {
		g.mu.Unlock()
		return
	}
	g.model.Shift(view, k, dx, dy)
	x, y, parent := it.X, it.Y, it.ParentID
	g.mu.Unlock()
	g.redraw(view) // at once: the item stays where it was dropped
	go func() {
		var err error
		switch {
		case k.Type == apitypes.TypeGroup:
			_, err = g.c.MoveGroup(g.ctx, k.ID, dx, dy)
		case view == mapmodel.NetworkView:
			_, err = g.c.UpdateHost(g.ctx, k.ID, apitypes.HostPatch{X: apitypes.Some(x), Y: apitypes.Some(y)})
		default:
			_, err = g.c.PlaceHost(g.ctx, view, k.ID, apitypes.PlacementSet{ParentID: parent, X: &x, Y: &y})
		}
		if err != nil {
			g.fail(fmt.Sprintf("move %s %d", k.Type, k.ID), err)
			g.reload.Request(view)
		}
	}()
}

// moveInto gives an item a new parent (nil: the root); the server puts it
// next to the new parent (SPEC-508).
func (g *GUI) moveInto(view int64, k mapmodel.Key, group *int64) {
	go func() {
		if err := g.setParent(view, k, group); err != nil {
			g.fail(fmt.Sprintf("move %s %d", k.Type, k.ID), err)
		}
	}()
}

// ---- context menus (SPEC-703, 706, 707, 719, 721) ----

// openMenu shows the context menu of a node or of empty space. A host's
// menu needs the host's views (SPEC-509), so it is loaded first.
func (g *GUI) openMenu(view int64, n *mapmodel.Node, at fyne.Position, mx, my float64) {
	show := func(items []*fyne.MenuItem) {
		if len(items) > 0 {
			widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), g.win.Canvas(), at)
		}
	}
	if n == nil || n.Item.Type != apitypes.TypeHost {
		show(g.menuItems(view, n, mx, my, nil))
		return
	}
	id := n.Item.ID
	go func() {
		h, err := g.c.Host(g.ctx, id)
		g.do(func() {
			if err != nil {
				g.status.message(fmt.Sprintf("host %d: %v", id, err))
				return
			}
			show(g.menuItems(view, n, mx, my, &h))
		})
	}()
}

// menuItems builds a context menu: for empty space or a frame's body
// (n nil or a frame), new items there; for a group or host, its actions.
// h is the host's record for a host's menu.
func (g *GUI) menuItems(view int64, n *mapmodel.Node, mx, my float64, h *apitypes.Host) []*fyne.MenuItem {
	var parent *int64
	if n != nil && n.Kind == mapmodel.KindFrame {
		x, y := mx, my
		if !n.Icon.Contains(x, y) { // a frame's body: new items go into it
			id := n.Item.ID
			parent = &id
		}
	}
	newItems := []*fyne.MenuItem{fyne.NewMenuItem("New group here…", func() { g.newGroupDialog(view, parent, mx, my) })}
	if view == mapmodel.NetworkView {
		newItems = append(newItems, fyne.NewMenuItem("New host here…", func() { g.newHostDialog(parent, mx, my) }))
	}
	if n == nil {
		return newItems
	}
	k := mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}
	// With several items selected and this one among them, "Move to
	// group…" and "Delete…" act on all of them (SPEC-724).
	targets := []mapmodel.Key{k}
	if mv := g.maps[view]; mv != nil {
		if sel := mv.Selection(); len(sel) > 1 && slices.Contains(sel, k) {
			targets = sel
		}
	}
	moveLabel, delLabel := "Move to group…", "Delete "+k.Type+"…"
	if len(targets) > 1 {
		moveLabel = fmt.Sprintf("Move %d items to group…", len(targets))
		delLabel = fmt.Sprintf("Delete %d items…", len(targets))
	}
	deleteTargets := func() {
		if len(targets) == 1 {
			g.deleteDialog(view, k)
			return
		}
		g.deleteManyDialog(view, g.topLevel(view, targets))
	}
	if n.Item.Type == apitypes.TypeHost {
		items := []*fyne.MenuItem{
			fyne.NewMenuItem("Edit…", func() { g.editHostDialog(k.ID) }),
			fyne.NewMenuItem(moveLabel, func() { g.moveToDialog(view, targets...) }),
		}
		if h != nil {
			items = append(items, g.showInViewItem(h))
		}
		if view != mapmodel.NetworkView {
			items = append(items, fyne.NewMenuItem("Remove from this view", func() { g.unplace(view, k.ID) }))
		}
		return append(items, fyne.NewMenuItemSeparator(), fyne.NewMenuItem(delLabel, deleteTargets))
	}
	id := n.Item.ID
	expand := fyne.NewMenuItem("Expand", func() { g.setCollapsed(id, false) })
	collapse := fyne.NewMenuItem("Collapse", func() { g.setCollapsed(id, true) })
	expand.Disabled, collapse.Disabled = n.Kind == mapmodel.KindFrame, n.Kind != mapmodel.KindFrame
	items := []*fyne.MenuItem{expand, collapse,
		fyne.NewMenuItem("Edit…", func() { g.editGroupDialog(view, id) }),
		fyne.NewMenuItem(moveLabel, func() { g.moveToDialog(view, targets...) }),
	}
	items = append(items, fyne.NewMenuItem("Reflow…", func() { g.reflowDialog(view, &id) }))
	if cidr := n.Item.CIDR; cidr != "" {
		scan := fyne.NewMenuItem("Scan this network", func() { g.openDiscovery(cidr) })
		scan.Disabled = strings.Contains(cidr, ":") // IPv6 (SPEC-707)
		items = append(items, scan)
	}
	if parent != nil {
		items = append(items, fyne.NewMenuItemSeparator())
		items = append(items, newItems...)
	}
	return append(items, fyne.NewMenuItemSeparator(), fyne.NewMenuItem(delLabel, deleteTargets))
}

// showInViewItem is "Show in view ▸" with every custom view, marked where
// the host is placed (SPEC-721).
func (g *GUI) showInViewItem(h *apitypes.Host) *fyne.MenuItem {
	g.mu.Lock()
	views := g.model.Views()
	g.mu.Unlock()
	var sub []*fyne.MenuItem
	for _, v := range views {
		if v.ID == mapmodel.NetworkView {
			continue
		}
		vid, placed := v.ID, slices.Contains(h.Views, v.ID)
		it := fyne.NewMenuItem(v.Name, func() {
			if !placed {
				g.place(vid, h.ID)
			}
		})
		it.Checked = placed
		sub = append(sub, it)
	}
	item := fyne.NewMenuItem("Show in view", nil)
	if len(sub) == 0 {
		item.Disabled = true
		return item
	}
	item.ChildMenu = fyne.NewMenu("", sub...)
	return item
}

func (g *GUI) place(view, host int64) {
	go func() {
		if _, err := g.c.PlaceHost(g.ctx, view, host, apitypes.PlacementSet{}); err != nil {
			g.fail(fmt.Sprintf("show host %d in view %d", host, view), err)
		}
	}()
}

// unplace removes a host from a custom view; no question, as the host
// stays (SPEC-721).
func (g *GUI) unplace(view, host int64) {
	go func() {
		if err := g.c.UnplaceHost(g.ctx, view, host); err != nil {
			g.fail(fmt.Sprintf("remove host %d from view %d", host, view), err)
		}
	}()
}

// deleteSelected asks to delete the selected items of the current tab
// (Delete key, SPEC-719, 724).
func (g *GUI) deleteSelected() {
	mv := g.currentMap()
	if mv == nil {
		return
	}
	switch keys := g.topLevel(mv.view, mv.Selection()); len(keys) {
	case 0:
	case 1:
		g.deleteDialog(mv.view, keys[0])
	default:
		g.deleteManyDialog(mv.view, keys)
	}
}

// topLevel drops the keys that lie inside a selected group: they move or
// go with that group.
func (g *GUI) topLevel(view int64, keys []mapmodel.Key) []mapmodel.Key {
	g.mu.Lock()
	defer g.mu.Unlock()
	sel := map[int64]bool{}
	for _, k := range keys {
		if k.Type == apitypes.TypeGroup {
			sel[k.ID] = true
		}
	}
	var out []mapmodel.Key
	for _, k := range keys {
		it := g.item(view, k)
		if it == nil {
			continue
		}
		inside := false
		for p, n := it.ParentID, 0; p != nil && n < 1000; n++ {
			if sel[*p] {
				inside = true
				break
			}
			pg := g.item(view, mapmodel.GroupKey(*p))
			if pg == nil {
				break
			}
			p = pg.ParentID
		}
		if !inside {
			out = append(out, k)
		}
	}
	return out
}

// setSelectedCollapsed collapses or expands every selected group, each
// keeping its top-left corner (keys c and e, SPEC-724).
func (g *GUI) setSelectedCollapsed(collapsed bool) {
	mv := g.currentMap()
	if mv == nil {
		return
	}
	for _, k := range mv.Selection() {
		if k.Type == apitypes.TypeGroup {
			g.setCollapsed(k.ID, collapsed)
		}
	}
}

// onKey handles keys that no widget took: Esc cancels a drag, Delete
// deletes the selected item.
func (g *GUI) onKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyEscape:
		if mv := g.currentMap(); mv != nil {
			if mv.Dragging() {
				mv.CancelDrag()
			} else {
				mv.Select(nil) // Esc clears the selection (SPEC-724)
				g.details.clear()
			}
		}
	case fyne.KeyDelete:
		g.deleteSelected()
	case fyne.KeyG: // group the selection (SPEC-724, as in Tkined)
		if mv := g.currentMap(); mv != nil {
			g.groupSelectionDialog(mv.view, mv.Selection())
		}
	case fyne.KeyC:
		g.setSelectedCollapsed(true)
	case fyne.KeyE:
		g.setSelectedCollapsed(false)
	}
}
