package gui

import (
	"image/color"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-736 (SPEC-704 v0.42): dropping an item from inside a group onto the
// group's bottom bar moves it out to the group's parent, just below the
// frame at the x where it was dropped; while dragging, the bar is marked
// "out"; an item from deeper down leaves the whole group; an item from
// outside only moves.
func TestDropOut(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	site := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "site", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", ParentID: &site.ID, Collapsed: ptr(false), X: ptr(100.0), Y: ptr(200.0)}))
	web := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web", ParentID: &lan.ID, X: ptr(100.0), Y: ptr(300.0)}))
	db := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Name: "db", ParentID: &lan.ID, X: ptr(300.0), Y: ptr(300.0)}))
	other := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.3", Name: "other", X: ptr(600.0), Y: ptr(100.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", db.ID) != nil })
	e.ui.Wait(func() { g.maps[1].SetViewport(mapmodel.Viewport{Zoom: 1}) })
	footOf := func(group int64) (x, y, bottom float64) {
		f := find(e.nodes(1), "group", group)
		return f.Foot.X + f.Foot.W/2, f.Foot.Y + f.Foot.H/2, f.Bounds.Y + f.Bounds.H
	}

	// While dragging web over lan's bottom bar: marked "out".
	var fx, fy, bottom, top float64
	e.ui.Wait(func() {
		mv := g.maps[1]
		fx, fy, bottom = footOf(lan.ID)
		top = find(e.nodes(1), "host", web.ID).Bounds.Y
		sx, sy := mv.vp.ToScreen(100, 300)
		ex, ey := mv.vp.ToScreen(fx, fy)
		for i := 1; i <= 5; i++ {
			mv.Dragged(&fyne.DragEvent{
				PointEvent: fyne.PointEvent{Position: fyne.NewPos(float32(sx+(ex-sx)*float64(i)/5), float32(sy+(ey-sy)*float64(i)/5))},
				Dragged:    fyne.Delta{DX: float32((ex - sx) / 5), DY: float32((ey - sy) / 5)},
			})
		}
		if mv.dropTarget == nil || mv.dropTarget.Item.ID != lan.ID || !mv.dropOut {
			t.Errorf("target %+v, out %v", mv.dropTarget, mv.dropOut)
		}
		if !has(count(testObjects(mv)).texts, "out") {
			t.Error("bottom bar not marked \"out\"")
		}
		mv.DragEnd()
	})
	eventually(t, e.ui, "web out of lan", func() bool {
		h := must(c.Host(ctx, web.ID))
		return h.ParentID != nil && *h.ParentID == site.ID && h.X == fx && h.Y == 300+bottom+mapmodel.OutGap-top &&
			strings.Contains(g.status.Text(), `web moved out of "lan"`)
	})

	// db lies in lan inside site: dropped onto site's bottom bar it leaves
	// site, to the root.
	e.ui.Wait(func() {
		x, y, _ := footOf(site.ID)
		drag(g.maps[1], 300, 300, x-300, y-300)
	})
	eventually(t, e.ui, "db at the root", func() bool { return must(c.Host(ctx, db.ID)).ParentID == nil })

	// other isn't in lan: on lan's bottom bar it only moves.
	e.ui.Wait(func() {
		x, y, _ := footOf(lan.ID)
		mv := g.maps[1]
		drag(mv, 600, 100, x-600, y-100)
	})
	eventually(t, e.ui, "other moved, still at the root", func() bool {
		h := must(c.Host(ctx, other.ID))
		return h.ParentID == nil && h.X != 600
	})
}

// TC-735 (GUI, SPEC-727): "Bar colour ▸" offers None, 16 colours and
// Custom…; a choice is saved and checked, the top bar is filled with it
// (not under Max); a custom colour comes from the picker and is listed;
// with two groups selected it sets both.
func TestBarColorMenu(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lan.ID, X: ptr(100.0), Y: ptr(220.0)}))
	lab := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab", X: ptr(700.0), Y: ptr(100.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "group", lab.ID) != nil })
	sub := func(sel []mapmodel.Key) *fyne.MenuItem {
		g.maps[1].SetSelection(sel)
		fr := find(e.nodes(1), "group", lan.ID)
		for _, it := range g.menuItems(1, fr, fr.Icon.X+2, fr.Icon.Y+2, nil) {
			if strings.HasPrefix(it.Label, "Bar colour") {
				return it
			}
		}
		t.Error("no Bar colour item")
		return &fyne.MenuItem{ChildMenu: fyne.NewMenu("")}
	}
	blue := nrgba(theme.RGB{R: 0x43, G: 0x63, B: 0xD8})
	filled := func(col color.Color) bool {
		for _, o := range testObjects(g.maps[1]) {
			if r, ok := o.(*canvas.Rectangle); ok && r.FillColor == col {
				return true
			}
		}
		return false
	}
	e.ui.Wait(func() {
		m := sub(nil).ChildMenu
		if n := len(m.Items); n != 20 || !menuItem(m.Items, "None").Checked || menuItem(m.Items, "Custom…") == nil {
			t.Errorf("menu (%d): %v", n, labels(m.Items))
			return
		}
		if menuItem(m.Items, "Blue").Icon == nil {
			t.Error("no swatch")
		}
		menuItem(m.Items, "Blue").Action()
	})
	eventually(t, e.ui, "blue saved and drawn", func() bool {
		return must(c.Group(ctx, lan.ID)).Color == "#4363D8" && filled(blue)
	})
	k := mapmodel.GroupKey(lan.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "details", func() bool { return strings.Contains(formText(g.details), "Blue") })
	e.ui.Wait(func() {
		if m := sub(nil).ChildMenu; !menuItem(m.Items, "Blue").Checked || menuItem(m.Items, "None").Checked {
			t.Error("Blue not checked")
		}
		g.setContrast(theme.Contrast{Preset: theme.PresetMax, Min: 21})
		if filled(blue) {
			t.Error("colour drawn under Max")
		}
		g.setContrast(theme.Default)
		// Custom…: the picker's colour is saved and listed.
		menuItem(sub(nil).ChildMenu.Items, "Custom…").Action()
		g.colorPicked(color.NRGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xFF})
	})
	eventually(t, e.ui, "custom colour", func() bool {
		g.mu.Lock()
		it := g.item(1, mapmodel.GroupKey(lan.ID))
		g.mu.Unlock()
		return must(c.Group(ctx, lan.ID)).Color == "#123456" && it != nil && it.Color == "#123456"
	})
	e.ui.Wait(func() {
		if it := menuItem(sub(nil).ChildMenu.Items, "#123456"); it == nil || !it.Checked {
			t.Error("custom colour not listed")
		}
		// Two groups selected: both get the colour.
		mi := sub([]mapmodel.Key{mapmodel.GroupKey(lan.ID), mapmodel.GroupKey(lab.ID)})
		if mi.Label != "Bar colour (2 groups)" {
			t.Errorf("label %q", mi.Label)
		}
		menuItem(mi.ChildMenu.Items, "Green").Action()
	})
	eventually(t, e.ui, "both green", func() bool {
		return must(c.Group(ctx, lan.ID)).Color == "#3CB44B" && must(c.Group(ctx, lab.ID)).Color == "#3CB44B"
	})
}
