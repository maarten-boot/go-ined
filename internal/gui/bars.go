package gui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// ---- a group's bottom bar and bar colour (Design-group-bars.md) ----

// dropOut moves items that were dropped onto a group's bottom bar out of
// the group, to its parent (B1), just below the frame at the x where they
// were dropped (B2). Several items keep their places relative to each
// other. SPEC-704 v0.42.
func (g *GUI) dropOut(view int64, keys []mapmodel.Key, group int64, dx float64) {
	mv := g.maps[view]
	if mv == nil {
		return
	}
	keys = g.topLevel(view, keys)
	g.mu.Lock()
	grp := g.item(view, mapmodel.GroupKey(group))
	var frame *mapmodel.Node
	bounds := map[mapmodel.Key]mapmodel.Rect{}
	for i, n := range mv.Nodes() {
		k := mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}
		bounds[k] = n.Bounds
		if n.Kind == mapmodel.KindFrame && n.Item.ID == group {
			frame = &mv.Nodes()[i]
		}
	}
	if grp == nil || frame == nil || len(keys) == 0 {
		g.mu.Unlock()
		return
	}
	top := 0.0
	for i, k := range keys {
		if b, ok := bounds[k]; ok && (i == 0 || b.Y < top) {
			top = b.Y
		}
	}
	dy := frame.Bounds.Y + frame.Bounds.H + mapmodel.OutGap - top
	type move struct {
		k    mapmodel.Key
		x, y float64
		name string
	}
	var moves []move
	for _, k := range keys {
		if it := g.item(view, k); it != nil {
			moves = append(moves, move{k, it.X + dx, it.Y + dy, it.Name})
		}
	}
	var parent *int64
	if grp.ParentID != nil {
		parent = ptrTo(*grp.ParentID)
	}
	gname := grp.Name
	g.mu.Unlock()
	go func() {
		done := 0
		for _, m := range moves {
			if err := g.setParentAt(view, m.k, parent, m.x, m.y); err != nil {
				g.fail(fmt.Sprintf("move %s %d", m.k.Type, m.k.ID), err)
				continue
			}
			done++
		}
		switch {
		case done == 1 && len(moves) == 1:
			g.do(func() { g.status.message(fmt.Sprintf("%s moved out of %q", moves[0].name, gname)) })
		case done > 0:
			g.do(func() { g.status.message(fmt.Sprintf("%d items moved out of %q", done, gname)) })
		}
	}()
}

// colorItem is "Bar colour ▸" of one or more groups (SPEC-727): None, the
// 16 colours of the palette, and Custom… (a colour picker). A choice is
// checked when every group has it; a custom colour they share is listed
// too. Caller holds g.mu.
func (g *GUI) colorItem(groups []*apitypes.MapItem) *fyne.MenuItem {
	ids := make([]int64, len(groups))
	same := true
	for i, it := range groups {
		ids[i] = it.ID
		same = same && it.Color == groups[0].Color
	}
	current := ""
	if same {
		current = groups[0].Color
	}
	setAll := func(hex string) {
		for _, id := range ids {
			g.setColor(id, hex)
		}
	}
	choice := func(label, hex string) *fyne.MenuItem {
		mi := fyne.NewMenuItem(label, func() { setAll(hex) })
		mi.Checked = same && current == hex
		if rgb, ok := theme.ParseHex(hex); ok {
			mi.Icon = swatch(rgb)
		}
		return mi
	}
	items := []*fyne.MenuItem{choice("None", ""), fyne.NewMenuItemSeparator()}
	inPalette := current == ""
	for _, b := range theme.BarPalette {
		items = append(items, choice(b.Name, b.RGB.Hex()))
		inPalette = inPalette || b.RGB.Hex() == current
	}
	items = append(items, fyne.NewMenuItemSeparator())
	if same && !inPalette {
		items = append(items, choice(current, current))
	}
	items = append(items, fyne.NewMenuItem("Custom…", func() { g.colorDialog(ids, current) }))
	mi := fyne.NewMenuItem("Bar colour", nil)
	if len(groups) > 1 {
		mi.Label = fmt.Sprintf("Bar colour (%d groups)", len(groups))
	}
	mi.ChildMenu = fyne.NewMenu("", items...)
	return mi
}

// colorDialog opens Fyne's colour picker (hue, saturation, RGB, hex) for a
// custom bar colour, starting from the current one.
func (g *GUI) colorDialog(ids []int64, current string) {
	g.colorPicked = func(c color.Color) {
		r, gr, b, _ := c.RGBA()
		hex := theme.RGB{R: uint8(r >> 8), G: uint8(gr >> 8), B: uint8(b >> 8)}.Hex()
		for _, id := range ids {
			g.setColor(id, hex)
		}
	}
	p := dialog.NewColorPicker("Bar colour", "A colour for the group's top bar", g.colorPicked, g.win)
	p.Advanced = true
	if rgb, ok := theme.ParseHex(current); ok {
		p.SetColor(nrgba(rgb))
	}
	p.Show()
}

// setColor saves a group's bar colour ("" for none); the group.updated
// event reloads the view.
func (g *GUI) setColor(group int64, hex string) {
	go func() {
		if _, err := g.c.UpdateGroup(g.ctx, group, apitypes.GroupPatch{Color: apitypes.Some(hex)}); err != nil {
			g.fail(fmt.Sprintf("group %d", group), err)
		}
	}()
}

// swatch is a small square in a colour, for the menu.
func swatch(c theme.RGB) fyne.Resource {
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect x="1" y="1" width="14" height="14" fill="%s" stroke="#000000" stroke-width="1"/></svg>`, c.Hex())
	return fyne.NewStaticResource("swatch-"+c.Hex()[1:]+".svg", []byte(svg))
}
