package gui

import (
	"fmt"
	"slices"
	"strconv"
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
	mv.OnDropOut = func(keys []mapmodel.Key, group int64, dx, dy float64) { g.dropOut(view, keys, group, dx) }
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
	// New items go into the focus group by default (SPEC-726, F6).
	var parent *int64
	if f := g.focusOf(view); f != 0 {
		parent = &f
	}
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
		x, y := mx, my
		newItems = append(newItems, g.importItem("Import here…", cmpOr(parent), &x, &y))
	}
	if n == nil {
		return newItems
	}
	k := mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}
	// With several items selected and this one among them, "Move to
	// group…" and "Delete…" act on the selection as the keys do: items
	// inside a selected group go with that group (SPEC-724).
	targets := []mapmodel.Key{k}
	var sel []mapmodel.Key // the selection, when the menu acts on it
	if mv := g.maps[view]; mv != nil {
		if s := mv.Selection(); len(s) > 1 && slices.Contains(s, k) {
			sel = s
			if top := g.topLevel(view, sel); len(top) > 0 {
				targets = top
			}
		}
	}
	moveLabel, delLabel := "Move to group…", "Delete "+k.Type+"…"
	removeLabel, showLabel := "Remove from this view", "Show in view"
	switch {
	case len(targets) > 1:
		moveLabel = fmt.Sprintf("Move %d items to group…", len(targets))
		delLabel = fmt.Sprintf("Delete %d items…", len(targets))
		removeLabel = fmt.Sprintf("Remove %d items from this view", len(targets))
		showLabel = fmt.Sprintf("Show %d items in view", len(targets))
	case targets[0] != k: // only the selected group this item is in
		g.mu.Lock()
		name := ""
		if it := g.item(view, targets[0]); it != nil {
			name = it.Name
		}
		g.mu.Unlock()
		what := fmt.Sprintf("%s %q", targets[0].Type, name)
		moveLabel, delLabel = "Move "+what+" to group…", "Delete "+what+"…"
		removeLabel, showLabel = "Remove "+what+" from this view", "Show "+what+" in view"
	}
	// "Show in view ▸" and "Remove from this view" act on the selection
	// too (D4, D5); removing exists in custom views only (D3).
	onSelection := len(targets) > 1 || targets[0] != k
	var remove *fyne.MenuItem
	if view != mapmodel.NetworkView {
		remove = fyne.NewMenuItem(removeLabel, func() { g.removeFromView(view, targets) })
	}
	deleteTargets := func() {
		if len(targets) == 1 {
			g.deleteDialog(view, targets[0])
			return
		}
		g.deleteManyDialog(view, targets)
	}
	if n.Item.Type == apitypes.TypeHost {
		items := []*fyne.MenuItem{
			fyne.NewMenuItem("Edit…", func() { g.editHostDialog(k.ID) }),
			fyne.NewMenuItem(moveLabel, func() { g.moveToDialog(view, targets...) }),
		}
		switch {
		case onSelection:
			items = append(items, g.showManyInViewItem(view, targets, showLabel))
		case h != nil:
			items = append(items, g.showInViewItem(h))
		}
		if hosts := selectedHosts(sel); onSelection && len(hosts) > 1 {
			items = append(items, g.watchItem(hosts))
		} else {
			items = append(items, g.watchItem([]int64{k.ID}))
		}
		if h != nil {
			items = append(items, g.connectItem(h))
		}
		if hosts := selectedHosts(sel); onSelection && len(hosts) > 1 {
			// A check of every selected host, as a job (SPEC-737).
			items = append(items, g.snmpCheckItem(apitypes.SNMPCheckCreate{HostIDs: hosts}, len(hosts)))
		} else if h != nil {
			items = append(items, g.checkSNMPItem(*h), g.communitiesItem("host", h.ID, h.DisplayName)) // one host (crosscheck 2026-11-18-03 L7)
		}
		if remove != nil {
			items = append(items, remove)
		}
		return append(items, fyne.NewMenuItemSeparator(), fyne.NewMenuItem(delLabel, deleteTargets))
	}
	id := n.Item.ID
	// Expand, Collapse, Host labels and Bar colour act on every selected group, as
	// the keys c and e do (crosscheck 2026-11-12-03 C1).
	groups := []*apitypes.MapItem{n.Item}
	anyCollapsed, anyExpanded := n.Kind != mapmodel.KindFrame, n.Kind == mapmodel.KindFrame
	g.mu.Lock()
	for _, s := range sel {
		if s.Type != apitypes.TypeGroup || s == k {
			continue
		}
		if it := g.item(view, s); it != nil {
			groups = append(groups, it)
			anyCollapsed = anyCollapsed || it.Collapsed
			anyExpanded = anyExpanded || !it.Collapsed
		}
	}
	labels := g.labelsItem(view, groups)
	colour := g.colorItem(groups)
	rescan := g.rescanItem(groups)
	g.mu.Unlock()
	var copyTo *fyne.MenuItem
	if onSelection {
		copyTo = g.showManyInViewItem(view, targets, showLabel)
	} else {
		g.mu.Lock()
		copyTo = g.copyToViewItem(view, n.Item)
		g.mu.Unlock()
	}
	setAll := func(collapsed bool) {
		for _, it := range groups {
			g.setCollapsed(it.ID, collapsed)
		}
	}
	expand := fyne.NewMenuItem("Expand", func() { setAll(false) })
	collapse := fyne.NewMenuItem("Collapse", func() { setAll(true) })
	if len(groups) > 1 {
		expand.Label = fmt.Sprintf("Expand %d groups", len(groups))
		collapse.Label = fmt.Sprintf("Collapse %d groups", len(groups))
	}
	expand.Disabled, collapse.Disabled = !anyCollapsed, !anyExpanded
	items := []*fyne.MenuItem{expand, collapse,
		fyne.NewMenuItem("Show only this group", func() { g.setFocus(view, id) }),
		fyne.NewMenuItem("Edit…", func() { g.editGroupDialog(view, id) }),
		fyne.NewMenuItem(moveLabel, func() { g.moveToDialog(view, targets...) }),
		copyTo,
	}
	items = append(items, fyne.NewMenuItem("Reflow…", func() { g.reflowDialog(view, &id) }), labels, colour)
	if view == mapmodel.NetworkView {
		items = append(items, g.exportItem(id, n.Item.Name))
		if parent == nil || *parent != id { // a frame's body has "Import here…" below
			// Not "Import here…": with a focus set, that is in the menu
			// too, for the focus group at the click (C5 of crosscheck 2026-11-17-01).
			items = append(items, g.importItem("Import into this group…", id, nil, nil))
		}
	}
	g.mu.Lock()
	nHosts := g.hostsUnder(view, id)
	g.mu.Unlock()
	items = append(items, g.snmpCheckItem(apitypes.SNMPCheckCreate{GroupID: &id}, nHosts))
	if view == mapmodel.NetworkView {
		items = append(items, g.communitiesItem("group", id, n.Item.Name))
	}
	if cidr := n.Item.CIDR; cidr != "" {
		scan := fyne.NewMenuItem("Scan this network", func() { g.openDiscovery(cidr) })
		scan.Disabled = strings.Contains(cidr, ":") // IPv6 (SPEC-707)
		items = append(items, scan)
		if rescan != nil {
			items = append(items, rescan)
		}
	}
	if remove != nil {
		items = append(items, remove)
	}
	if parent != nil {
		items = append(items, fyne.NewMenuItemSeparator())
		items = append(items, newItems...)
	}
	return append(items, fyne.NewMenuItemSeparator(), fyne.NewMenuItem(delLabel, deleteTargets))
}

// labelsItem is "Host labels ▸" of one or more groups (SPEC-725): what
// the hosts inside them show. A choice is checked when every group has
// it; for one group taking its parent's mode, that mode is shown in
// brackets (crosscheck 2026-11-12-03 C2). Caller holds g.mu.
func (g *GUI) labelsItem(view int64, groups []*apitypes.MapItem) *fyne.MenuItem {
	ids := make([]int64, len(groups))
	same := true
	for i, it := range groups {
		ids[i] = it.ID
		same = same && it.Labels == groups[0].Labels
	}
	choice := func(mode string) *fyne.MenuItem {
		label := mapmodel.LabelModeText(mode)
		if vm := g.model.Map(view); vm != nil && len(groups) == 1 && mode == apitypes.LabelsInherit {
			label = vm.GroupLabelsText(&apitypes.MapItem{ParentID: groups[0].ParentID})
		}
		mi := fyne.NewMenuItem(label, func() {
			for _, id := range ids {
				g.setLabels(id, mode)
			}
		})
		mi.Checked = same && groups[0].Labels == mode
		return mi
	}
	mi := fyne.NewMenuItem("Host labels", nil)
	if len(groups) > 1 {
		mi.Label = fmt.Sprintf("Host labels (%d groups)", len(groups))
	}
	mi.ChildMenu = fyne.NewMenu("",
		choice(apitypes.LabelsName),
		choice(apitypes.LabelsIP),
		choice(apitypes.LabelsBoth),
		fyne.NewMenuItemSeparator(),
		choice(apitypes.LabelsInherit),
	)
	return mi
}

// setLabels saves a group's host label mode; the group.updated event
// reloads the view.
func (g *GUI) setLabels(group int64, mode string) {
	go func() {
		if _, err := g.c.UpdateGroup(g.ctx, group, apitypes.GroupPatch{Labels: apitypes.Some(mode)}); err != nil {
			g.fail(fmt.Sprintf("group %d", group), err)
		}
	}()
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

// copyToViewItem is "Show in view ▸" on a group (SPEC-729): every custom
// view but the group's own; choosing one copies the group with its
// subgroups and host placements there. A view is ticked when its map is
// loaded in this GUI and has every host of the group already. Caller
// holds g.mu.
func (g *GUI) copyToViewItem(view int64, grp *apitypes.MapItem) *fyne.MenuItem {
	var hosts []mapmodel.Key
	if vm := g.model.Map(view); vm != nil {
		for k := range vm.Items {
			if k.Type == apitypes.TypeHost && g.model.InFocus(view, grp.ID, k) {
				hosts = append(hosts, k)
			}
		}
	}
	id, name := grp.ID, grp.Name
	var sub []*fyne.MenuItem
	for _, v := range g.model.Views() {
		if v.ID == mapmodel.NetworkView || v.ID == view {
			continue
		}
		vid, vname := v.ID, v.Name
		it := fyne.NewMenuItem(vname, func() { g.copyGroup(vid, vname, id, name) })
		if tvm := g.model.Map(vid); tvm != nil && len(hosts) > 0 {
			it.Checked = true
			for _, k := range hosts {
				if tvm.Items[k] == nil {
					it.Checked = false
					break
				}
			}
		}
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

// copyGroup copies a group into a custom view and says what it did.
func (g *GUI) copyGroup(view int64, vname string, group int64, gname string) {
	go func() {
		r, err := g.c.CopyGroup(g.ctx, view, group)
		if err != nil {
			g.fail(fmt.Sprintf("copy group %d to view %d", group, view), err)
			return
		}
		msg := fmt.Sprintf("%q copied to %q: %s, %s", gname, vname, plural(r.Groups, "group"), plural(r.Hosts, "host"))
		if r.Moved > 0 {
			msg += fmt.Sprintf(", %d of them moved from elsewhere in %q", r.Moved, vname)
		}
		g.do(func() { g.status.message(msg) })
	}()
}

func (g *GUI) place(view, host int64) {
	go func() {
		if _, err := g.c.PlaceHost(g.ctx, view, host, apitypes.PlacementSet{}); err != nil {
			g.fail(fmt.Sprintf("show host %d in view %d", host, view), err)
		}
	}()
}

// showManyInViewItem is "Show in view ▸" on a selection (D5): every
// custom view but this one. Choosing one places the selected hosts there
// and copies the selected groups (SPEC-729). A view is ticked when its map
// is loaded in this GUI and has every host of the selection, those inside
// the selected groups included.
func (g *GUI) showManyInViewItem(view int64, keys []mapmodel.Key, label string) *fyne.MenuItem {
	g.mu.Lock()
	var hosts []mapmodel.Key
	for _, k := range keys {
		if k.Type == apitypes.TypeHost {
			hosts = append(hosts, k)
			continue
		}
		for s := range g.model.Subtree(view, k.ID) {
			if s.Type == apitypes.TypeHost {
				hosts = append(hosts, s)
			}
		}
	}
	var sub []*fyne.MenuItem
	for _, v := range g.model.Views() {
		if v.ID == mapmodel.NetworkView || v.ID == view {
			continue
		}
		vid, vname := v.ID, v.Name
		it := fyne.NewMenuItem(vname, func() { g.showManyInView(view, vid, vname, keys) })
		if tvm := g.model.Map(vid); tvm != nil && len(hosts) > 0 {
			it.Checked = true
			for _, k := range hosts {
				if tvm.Items[k] == nil {
					it.Checked = false
					break
				}
			}
		}
		sub = append(sub, it)
	}
	g.mu.Unlock()
	item := fyne.NewMenuItem(label, nil)
	if len(sub) == 0 {
		item.Disabled = true
		return item
	}
	item.ChildMenu = fyne.NewMenu("", sub...)
	return item
}

// showManyInView places the hosts among keys in view to (at its root,
// unless they are there already) and copies the groups there, then sums it
// up in the status bar (D5).
func (g *GUI) showManyInView(from, to int64, vname string, keys []mapmodel.Key) {
	go func() {
		var placed, there, copied, groups, hosts int
		var nHosts int
		for _, k := range keys {
			if k.Type != apitypes.TypeHost {
				continue
			}
			nHosts++
			h, err := g.c.Host(g.ctx, k.ID)
			if err == nil && slices.Contains(h.Views, to) {
				there++
				continue
			}
			if err == nil {
				_, err = g.c.PlaceHost(g.ctx, to, k.ID, apitypes.PlacementSet{})
			}
			if err != nil {
				g.fail(fmt.Sprintf("show host %d in view %d", k.ID, to), err)
				return
			}
			placed++
		}
		for _, k := range keys {
			if k.Type != apitypes.TypeGroup {
				continue
			}
			r, err := g.c.CopyGroup(g.ctx, to, k.ID)
			if err != nil {
				g.fail(fmt.Sprintf("copy group %d to view %d", k.ID, to), err)
				return
			}
			copied++
			groups += r.Groups
			hosts += r.Hosts
		}
		var parts []string
		if nHosts > 0 {
			p := "placed " + plural(placed, "host")
			if there > 0 {
				p += fmt.Sprintf(" (%d already there)", there)
			}
			parts = append(parts, p)
		}
		if copied > 0 {
			parts = append(parts, fmt.Sprintf("copied %s (%s, %s in all)", plural(copied, "group"), plural(groups, "group"), plural(hosts, "host")))
		}
		msg := fmt.Sprintf("%q: %s", vname, strings.Join(parts, ", "))
		g.do(func() { g.status.message(msg) })
	}()
}

// removeFromView takes items out of a custom view (D3, D4): hosts are
// unplaced, groups are deleted with everything in them, which in a custom
// view only removes their hosts from it (SPEC-120). The hosts stay in the
// network view and in other views. It asks first when a group has
// contents (the dialog is returned for tests); otherwise it goes at once.
func (g *GUI) removeFromView(view int64, keys []mapmodel.Key) *formDialog {
	g.mu.Lock()
	vname := strconv.FormatInt(view, 10)
	if v, ok := g.model.View(view); ok {
		vname = v.Name
	}
	var hosts, groups, inHosts, inGroups int // selected, and inside the selected groups
	for _, k := range keys {
		if k.Type == apitypes.TypeHost {
			hosts++
			continue
		}
		groups++
		h, gs := g.model.Contents(view, k.ID)
		inHosts, inGroups = inHosts+h, inGroups+gs
	}
	what := fmt.Sprintf("%d items (%s, %s in all)", len(keys), plural(groups+inGroups, "group"), plural(hosts+inHosts, "host"))
	if len(keys) == 1 {
		name := ""
		if it := g.item(view, keys[0]); it != nil {
			name = it.Name
		}
		what = fmt.Sprintf("%s %q", keys[0].Type, name)
		if groups == 1 && inHosts+inGroups > 0 {
			what += fmt.Sprintf(" (%s, %s)", plural(inGroups, "subgroup"), plural(inHosts, "host"))
		}
	}
	g.mu.Unlock()
	done := fmt.Sprintf("Removed %s from %q", what, vname)
	run := func() error {
		for _, k := range keys { // hosts first, then groups
			if k.Type == apitypes.TypeHost {
				if err := g.c.UnplaceHost(g.ctx, view, k.ID); err != nil {
					return fmt.Errorf("host %d: %w", k.ID, err)
				}
			}
		}
		for _, k := range keys {
			if k.Type == apitypes.TypeGroup {
				if err := g.c.DeleteGroup(g.ctx, k.ID, apitypes.ChildrenCascade); err != nil {
					return fmt.Errorf("group %d: %w", k.ID, err)
				}
			}
		}
		g.do(func() { g.status.message(done) })
		return nil
	}
	if inHosts+inGroups == 0 { // only hosts and empty groups: no question
		go func() {
			if err := run(); err != nil {
				g.fail(fmt.Sprintf("remove from view %q", vname), err)
			}
		}()
		return nil
	}
	l := widget.NewLabel(fmt.Sprintf("Remove %s from %q? The hosts stay in the network view and in the other views they are in.", what, vname))
	l.Wrapping = fyne.TextWrapWord
	return g.showForm("Remove from this view", "Remove", []*widget.FormItem{widget.NewFormItem("", l)}, run)
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
// deletes the selected item, o shows only the selected group, Backspace
// goes one level up from it.
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
	case fyne.KeyJ: // join the selected hosts to the selected group (SPEC-724)
		if mv := g.currentMap(); mv != nil {
			g.joinDialog(mv.view, mv.Selection(), mv.Selected())
		}
	case fyne.KeyO: // show only the selected group (SPEC-726)
		if mv := g.currentMap(); mv != nil {
			if k := mv.Selected(); k != nil && k.Type == apitypes.TypeGroup {
				g.setFocus(mv.view, k.ID)
			}
		}
	case fyne.KeyBackspace: // one level up from a focus (SPEC-726)
		g.focusUp()
	case fyne.KeyC:
		g.setSelectedCollapsed(true)
	case fyne.KeyE:
		g.setSelectedCollapsed(false)
	}
}

// cmpOr is the group id parent points to, or 0 for the top level.
func cmpOr(parent *int64) int64 {
	if parent == nil {
		return 0
	}
	return *parent
}
