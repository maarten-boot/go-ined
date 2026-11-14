package gui

import (
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetheme "fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-743 (D2): growLayout stacks at minimum heights and gives the rest of
// the height to one object; with too little height nothing grows.
func TestGrowLayout(t *testing.T) {
	a, b, c := widget.NewLabel("rows"), widget.NewLabel("Note"), widget.NewMultiLineEntry()
	l := growLayout{grow: 2}
	objs := []fyne.CanvasObject{a, b, c}
	min := l.MinSize(objs)
	l.Layout(objs, fyne.NewSize(300, min.Height+200))
	pad := fynetheme.Padding()
	if b.Position().Y != a.Size().Height+pad || c.Position().Y != b.Position().Y+b.Size().Height+pad {
		t.Errorf("not stacked: %v %v %v", a.Position(), b.Position(), c.Position())
	}
	if a.Size().Height != a.MinSize().Height || c.Size().Height != c.MinSize().Height+200 || c.Size().Width != 300 {
		t.Errorf("sizes: %v %v", a.Size(), c.Size())
	}
	l.Layout(objs, fyne.NewSize(300, min.Height-50))
	if c.Size().Height != c.MinSize().Height {
		t.Errorf("grew without room: %v", c.Size())
	}
}

// TC-743 (D1, D2): the details title and values can be selected, the row
// names can't; the note follows the rows in the same scroll area.
func TestDetailsSelectableAndNote(t *testing.T) {
	e := start(t)
	h := must(e.c.CreateHost(e.ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "nas"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h.ID) != nil })
	e.ui.Wait(func() {
		k := mapmodel.HostKey(h.ID)
		g.details.show(1, &k)
	})
	eventually(t, e.ui, "details", func() bool { return g.details.title.Text == "Host nas" })
	e.ui.Wait(func() {
		d := g.details
		if !d.title.Selectable {
			t.Error("title not selectable")
		}
		for i, o := range d.form.Objects {
			if l := o.(*widget.Label); l.Selectable != (i%2 == 1) {
				t.Errorf("row %d %q: selectable %v", i, l.Text, l.Selectable)
			}
		}
		if d.body.Objects[0] != d.form || d.body.Objects[2] != d.note {
			t.Errorf("body: %v", d.body.Objects)
		}
		var scroll *container.Scroll
		for _, o := range d.obj.(*fyne.Container).Objects {
			if s, ok := o.(*container.Scroll); ok {
				scroll = s
			}
		}
		if scroll == nil || scroll.Content != d.body {
			t.Errorf("the note is not in the rows' scroll area")
		}
	})
}

// TC-744 (D3): "Remove from this view" on a group of a custom view; not
// offered in the network view. An empty group goes at once; a group with
// contents asks first; its hosts stay in the network view.
func TestRemoveGroupFromView(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", Collapsed: ptr(true), X: ptr(100.0), Y: ptr(100.0)}))
	must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "sub", ParentID: &lan.ID}))
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lan.ID}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &lan.ID}))
	pay := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Payroll"}))
	cp := must(c.CopyGroup(ctx, pay.ID, lan.ID))
	empty := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "empty", ViewID: &pay.ID, X: ptr(500.0), Y: ptr(100.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "group", lan.ID) != nil })
	menu := func(view, id int64) []*fyne.MenuItem {
		fr := find(e.nodes(view), "group", id)
		if fr == nil {
			t.Errorf("group %d not in view %d", id, view)
			return nil
		}
		return g.menuItems(view, fr, fr.Icon.X+2, fr.Icon.Y+2, nil)
	}
	e.ui.Wait(func() {
		if menuItem(menu(1, lan.ID), "Remove from this view") != nil {
			t.Error("offered in the network view")
		}
		g.showView(pay.ID)
	})
	eventually(t, e.ui, "Payroll loaded", func() bool {
		return find(e.nodes(pay.ID), "group", cp.GroupID) != nil && find(e.nodes(pay.ID), "group", empty.ID) != nil
	})
	e.ui.Wait(func() {
		g.lastForm = nil
		menuItem(menu(pay.ID, empty.ID), "Remove from this view").Action()
		if g.lastForm != nil {
			t.Error("asked for an empty group")
		}
	})
	eventually(t, e.ui, "empty group gone", func() bool {
		_, err := c.Group(ctx, empty.ID)
		return err != nil && strings.Contains(g.status.Text(), `Removed group "empty" from "Payroll"`)
	})
	var f *formDialog
	e.ui.Wait(func() {
		g.lastForm = nil
		menuItem(menu(pay.ID, cp.GroupID), "Remove from this view").Action()
		f = g.lastForm
		want := `Remove group "lan" (1 subgroup, 2 hosts) from "Payroll"? The hosts stay in the network view and in the other views they are in.`
		if f == nil || f.form.Items[0].Widget.(*fyneLabel).Text != want {
			t.Errorf("question: %+v", f)
			return
		}
		f.Submit()
	})
	eventually(t, e.ui, "removed", func() bool { return f.done && e.items(pay.ID) == 0 })
	for _, h := range []apitypes.Host{h1, h2} {
		if got := must(c.Host(ctx, h.ID)); !slices.Equal(got.Views, []int64{1}) {
			t.Errorf("host %d views %v", h.ID, got.Views)
		}
	}
	if _, err := c.Group(ctx, lan.ID); err != nil {
		t.Errorf("network group gone: %v", err)
	}
	eventually(t, e.ui, "status", func() bool {
		return strings.Contains(g.status.Text(), `Removed group "lan" (1 subgroup, 2 hosts) from "Payroll"`)
	})
}

// TC-745 (D4): "Remove N items from this view" on a selection of hosts
// and a group; it asks, as the group has contents.
func TestRemoveManyFromView(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", Collapsed: ptr(true)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lan.ID}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2"}))
	h3 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.3"}))
	pay := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Payroll"}))
	cp := must(c.CopyGroup(ctx, pay.ID, lan.ID))
	must(c.PlaceHost(ctx, pay.ID, h2.ID, apitypes.PlacementSet{X: ptr(400.0), Y: ptr(0.0)}))
	must(c.PlaceHost(ctx, pay.ID, h3.ID, apitypes.PlacementSet{X: ptr(500.0), Y: ptr(0.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return e.viewCount() == 2 })
	e.ui.Wait(func() { g.showView(pay.ID) })
	eventually(t, e.ui, "Payroll loaded", func() bool { return e.items(pay.ID) == 4 && find(e.nodes(pay.ID), "host", h2.ID) != nil })
	var f *formDialog
	e.ui.Wait(func() {
		g.maps[pay.ID].SetSelection([]mapmodel.Key{mapmodel.HostKey(h2.ID), mapmodel.HostKey(h3.ID), mapmodel.GroupKey(cp.GroupID)})
		items := g.menuItems(pay.ID, find(e.nodes(pay.ID), "host", h2.ID), 0, 0, &h2)
		mi := menuItem(items, "Remove 3 items from this view")
		if mi == nil {
			t.Errorf("menu: %v", labels(items))
			return
		}
		g.lastForm = nil
		mi.Action()
		f = g.lastForm
		want := `Remove 3 items (1 group, 3 hosts in all) from "Payroll"? The hosts stay in the network view and in the other views they are in.`
		if f == nil || f.form.Items[0].Widget.(*fyneLabel).Text != want {
			t.Errorf("question: %+v", f)
			return
		}
		f.Submit()
	})
	eventually(t, e.ui, "removed", func() bool {
		return f.done && e.items(pay.ID) == 0 && strings.Contains(g.status.Text(), `Removed 3 items (1 group, 3 hosts in all) from "Payroll"`)
	})
	if n := len(must(c.Hosts(ctx))); n != 3 {
		t.Errorf("%d hosts left, want 3", n)
	}
}

// TC-746 (D5): "Show N items in view ▸" on a selection lists the custom
// views but the current one; choosing one places the hosts (keeping those
// already there) and copies the groups, and says so; the view is ticked
// once its map is loaded and has them all.
func TestShowManyInView(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", Collapsed: ptr(true), X: ptr(100.0), Y: ptr(100.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lan.ID}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &lan.ID}))
	h3 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.3", X: ptr(400.0), Y: ptr(100.0)}))
	h4 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.4", X: ptr(500.0), Y: ptr(100.0)}))
	pay := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Payroll"}))
	lab := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"}))
	must(c.PlaceHost(ctx, pay.ID, h3.ID, apitypes.PlacementSet{X: ptr(700.0), Y: ptr(50.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h4.ID) != nil && e.viewCount() == 3 })
	sel := []mapmodel.Key{mapmodel.HostKey(h3.ID), mapmodel.HostKey(h4.ID), mapmodel.GroupKey(lan.ID)}
	item := func(view int64) *fyne.MenuItem {
		g.maps[view].SetSelection(sel)
		return menuItem(g.menuItems(view, find(e.nodes(view), "host", h3.ID), 0, 0, &h3), "Show 3 items in view")
	}
	e.ui.Wait(func() {
		it := item(1)
		if it == nil || it.ChildMenu == nil || !slices.Equal(labels(it.ChildMenu.Items), []string{"Payroll", "Lab"}) {
			t.Errorf("menu: %+v", it)
			return
		}
		if it.ChildMenu.Items[0].Checked {
			t.Error("Payroll ticked before")
		}
		it.ChildMenu.Items[0].Action()
	})
	eventually(t, e.ui, "shown", func() bool {
		return strings.Contains(g.status.Text(), `"Payroll": placed 1 host (1 already there), copied 1 group (1 group, 2 hosts in all)`)
	})
	m := must(c.ViewMap(ctx, pay.ID))
	if len(m.Items) != 5 { // h3, h4, the copy of lan and its 2 hosts
		t.Errorf("%d items in Payroll, want 5: %+v", len(m.Items), m.Items)
	}
	for _, it := range m.Items {
		if it.Type == apitypes.TypeHost && it.ID == h3.ID && (it.X != 700 || it.Y != 50) {
			t.Errorf("host already there moved: %+v", it)
		}
	}
	e.ui.Wait(func() { g.showView(pay.ID) })
	eventually(t, e.ui, "Payroll loaded", func() bool { return e.items(pay.ID) == 5 && find(e.nodes(pay.ID), "host", h3.ID) != nil })
	e.ui.Wait(func() {
		g.showView(1)
		if it := item(1); !it.ChildMenu.Items[0].Checked || it.ChildMenu.Items[1].Checked {
			t.Errorf("ticks: %v %v", it.ChildMenu.Items[0].Checked, it.ChildMenu.Items[1].Checked)
		}
	})
	_ = lab
}

// items counts the items of a view's model, hidden ones included.
func (e *env) items(view int64) int {
	e.g.mu.Lock()
	defer e.g.mu.Unlock()
	if vm := e.g.model.Map(view); vm != nil {
		return len(vm.Items)
	}
	return -1
}

// viewCount is the number of views the GUI knows.
func (e *env) viewCount() int {
	e.g.mu.Lock()
	defer e.g.mu.Unlock()
	return len(e.g.model.Views())
}
