package gui

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// drag simulates a pointer drag on a map from a map point by screen
// pixels, in small steps, then releases.
func drag(mv *MapView, mx, my, dx, dy float64) {
	sx, sy := mv.vp.ToScreen(mx, my)
	steps := 5
	for i := 1; i <= steps; i++ {
		ddx, ddy := float32(dx/float64(steps)), float32(dy/float64(steps))
		pos := fyne.NewPos(float32(sx+dx*float64(i)/float64(steps)), float32(sy+dy*float64(i)/float64(steps)))
		mv.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: pos}, Dragged: fyne.Delta{DX: ddx, DY: ddy}})
	}
	mv.DragEnd()
}

// TC-718 (widget): click vs drag, item drags, drop targets, Esc, panning
// inside a frame.
func TestDragWidget(t *testing.T) {
	p := theme.NewPalette(theme.Default)
	mv, _ := newMap(t, p,
		apitypes.MapItem{Type: "group", ID: 1, Name: "office", X: 100, Y: 100, Status: "up"},
		apitypes.MapItem{Type: "host", ID: 1, ParentID: pid(1), Name: "web", X: 100, Y: 200, Status: "up", Monitored: true},
		apitypes.MapItem{Type: "host", ID: 2, ParentID: pid(1), Name: "db", X: 250, Y: 200, Status: "up", Monitored: true},
		apitypes.MapItem{Type: "group", ID: 2, Name: "lab", X: 500, Y: 100, Collapsed: true, Status: "up"},
		apitypes.MapItem{Type: "host", ID: 3, Name: "laptop", X: 500, Y: 400, Status: "up", Monitored: true},
	)
	var log []string
	keyList := func(keys []mapmodel.Key) string {
		var out []string
		for _, k := range keys {
			out = append(out, fmt.Sprintf("%s %d", k.Type, k.ID))
		}
		return strings.Join(out, "+")
	}
	mv.OnMoved = func(keys []mapmodel.Key, dx, dy float64) {
		log = append(log, fmt.Sprintf("move %s %g,%g", keyList(keys), dx, dy))
	}
	mv.OnDropInto = func(keys []mapmodel.Key, g int64, _, _ float64) {
		log = append(log, fmt.Sprintf("into %s → %d", keyList(keys), g))
	}
	mv.Subtree = func(k mapmodel.Key) map[mapmodel.Key]bool {
		if k == mapmodel.GroupKey(1) {
			return map[mapmodel.Key]bool{k: true, mapmodel.HostKey(1): true, mapmodel.HostKey(2): true}
		}
		return map[mapmodel.Key]bool{k: true}
	}
	take := func() string { s := strings.Join(log, "; "); log = nil; return s }

	// Under 4 pixels: nothing moves, the map doesn't pan.
	v0 := mv.vp
	drag(mv, 500, 400, 3, 0)
	if got := take(); got != "" || mv.vp != v0 {
		t.Errorf("tiny drag: %q, viewport %+v", got, mv.vp)
	}
	// A host onto empty space.
	drag(mv, 500, 400, 40, 20)
	if got := take(); got != "move host 3 40,20" {
		t.Errorf("host move: %q", got)
	}
	// A host onto the collapsed group "lab".
	drag(mv, 500, 400, 0, -300)
	if got := take(); got != "into host 3 → 2" {
		t.Errorf("drop into icon: %q", got)
	}
	// The frame of "office" by its label: the group (with its subtree).
	frame := mv.Nodes()[0]
	drag(mv, frame.Icon.X+10, frame.Icon.Y+5, 0, 300)
	if got := take(); got != "move group 1 0,300" {
		t.Errorf("frame drag: %q", got)
	}
	// Inside the frame's body: a pan.
	v1 := mv.vp
	drag(mv, frame.Bounds.X+frame.Bounds.W-4, frame.Bounds.Y+frame.Bounds.H-4, 30, 0)
	if got := take(); got != "" || mv.vp.X != v1.X-30 {
		t.Errorf("frame body drag: %q, %+v", got, mv.vp)
	}
	// A host onto the frame label of a group: into it; not onto itself.
	drag(mv, 500, 400, frame.Icon.X+20-500, frame.Icon.Y+8-400)
	if got := take(); got != "into host 3 → 1" {
		t.Errorf("drop on frame label: %q", got)
	}
	// While dragging, the target is marked; Esc cancels.
	sx, sy := mv.vp.ToScreen(500, 400)
	for i := 1; i <= 10; i++ {
		mv.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(float32(sx), float32(sy)-float32(30*i))}, Dragged: fyne.Delta{DY: -30}})
	}
	if !mv.Dragging() || mv.dropTarget == nil || mv.dropTarget.Item.ID != 2 {
		t.Fatalf("drop target while dragging: %+v", mv.dropTarget)
	}
	if !has(count(testObjects(mv)).texts, "into") {
		t.Error("drop target not marked with \"into\"")
	}
	mv.CancelDrag()
	mv.DragEnd()
	if got := take(); got != "" || mv.Dragging() {
		t.Errorf("after Esc: %q", got)
	}
}

func testObjects(mv *MapView) []fyne.CanvasObject { return test.WidgetRenderer(mv).Objects() }

// Short names for the widgets inside the forms.
type (
	fyneEntry  = widget.Entry
	fyneCheck  = widget.Check
	fyneSelect = widget.Select
	fyneLabel  = widget.Label
	fyneRadio  = widget.RadioGroup
)

func clientFor(url string) (*client.Client, error) { return client.New(client.Options{URL: url}) }

// TC-718 (GUI): drags reach the server: a host's position, a group with
// its contents, a drop into a group, a placement in a custom view.
func TestDragGUI(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	a := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "a", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	b := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "b", X: ptr(600.0), Y: ptr(100.0)}))
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &a.ID, X: ptr(100.0), Y: ptr(220.0)}))
	lone := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", X: ptr(600.0), Y: ptr(400.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 4 })
	e.ui.Wait(func() { g.maps[1].SetViewport(mapmodel.Viewport{Zoom: 1}) })

	e.ui.Wait(func() { drag(g.maps[1], 600, 400, 50, 0) })
	eventually(t, e.ui, "host moved on the server", func() bool { return must(c.Host(ctx, lone.ID)).X == 650 })

	e.ui.Wait(func() {
		f := find(e.nodes(1), "group", a.ID)
		drag(g.maps[1], f.Icon.X+5, f.Icon.Y+5, 0, 100)
	})
	eventually(t, e.ui, "group moved with its host", func() bool {
		return must(c.Group(ctx, a.ID)).Y == 200 && must(c.Host(ctx, h.ID)).Y == 320
	})

	eventually(t, e.ui, "map laid out after the moves", func() bool {
		n := find(e.nodes(1), "host", lone.ID)
		return n != nil && n.Icon.X == 650-mapmodel.HostSize/2
	})
	e.ui.Wait(func() {
		drag(g.maps[1], 650, 400, -50, -300)
	}) // onto group b's icon
	eventually(t, e.ui, "dropped into b", func() bool {
		hh := must(c.Host(ctx, lone.ID))
		return hh.ParentID != nil && *hh.ParentID == b.ID
	})

	// A custom view: the drag changes the placement only.
	v := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"}))
	must(c.PlaceHost(ctx, v.ID, h.ID, apitypes.PlacementSet{X: ptr(50.0), Y: ptr(50.0)}))
	eventually(t, e.ui, "Lab tab", func() bool { return e.tabNames() == "Networks,Lab" })
	e.ui.Wait(func() { g.showView(v.ID) })
	eventually(t, e.ui, "Lab map", func() bool { return find(e.nodes(v.ID), "host", h.ID) != nil })
	e.ui.Wait(func() {
		g.maps[v.ID].SetViewport(mapmodel.Viewport{Zoom: 1})
		drag(g.maps[v.ID], 50, 50, 25, 25)
	})
	eventually(t, e.ui, "placement moved", func() bool {
		m := must(c.ViewMap(ctx, v.ID))
		return m.Items[0].X == 75 && m.Items[0].Y == 75
	})
	if hh := must(c.Host(ctx, h.ID)); hh.Y != 320 {
		t.Errorf("network position changed by a custom-view drag: %+v", hh)
	}
}

// TC-719: create, edit, move and delete through the dialogs; an API error
// stays in the dialog; the Delete key; cascade wording.
func TestDialogs(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	e.open()
	g := e.g
	eventually(t, e.ui, "connected", func() bool { return strings.Contains(g.status.Text(), "connected") })

	// New group: a bad CIDR first, then a good one.
	var f *formDialog
	e.ui.Wait(func() {
		f = g.newGroupDialog(1, nil, 300, 300)
		items := f.form.Items
		items[0].Widget.(*fyneEntry).SetText("lan")
		items[2].Widget.(*fyneEntry).SetText("10.0.0.5/24")
		f.Submit()
	})
	eventually(t, e.ui, "error in the dialog", func() bool { return strings.Contains(f.errL.Text, "Error:") && !f.done })
	e.ui.Wait(func() {
		f.form.Items[2].Widget.(*fyneEntry).SetText("10.0.0.0/24")
		f.Submit()
	})
	eventually(t, e.ui, "group created", func() bool { return f.done && len(e.nodes(1)) == 1 })
	gr := must(c.Groups(ctx))[0]
	if gr.Name != "lan" || gr.CIDR != "10.0.0.0/24" || gr.X != 300 {
		t.Errorf("created group: %+v", gr)
	}

	// New host into the group's frame (expanded), edit it.
	must(c.UpdateGroup(ctx, gr.ID, apitypes.GroupPatch{Collapsed: apitypes.Some(false)}))
	e.ui.Wait(func() {
		f = g.newHostDialog(&gr.ID, 300, 420)
		f.form.Items[0].Widget.(*fyneEntry).SetText("10.0.0.9")
		f.form.Items[1].Widget.(*fyneEntry).SetText("nas")
		f.Submit()
	})
	eventually(t, e.ui, "host created", func() bool { return f.done && len(e.nodes(1)) == 2 })
	h := must(c.Hosts(ctx))[0]
	e.ui.Wait(func() { g.lastForm = nil; g.editHostDialog(h.ID) })
	eventually(t, e.ui, "edit form", func() bool { return g.lastForm != nil })
	e.ui.Wait(func() {
		f = g.lastForm
		f.form.Items[1].Widget.(*fyneEntry).SetText("storage")
		f.form.Items[3].Widget.(*fyneCheck).SetChecked(false)
		f.Submit()
	})
	eventually(t, e.ui, "host edited", func() bool { return f.done })
	if hh := must(c.Host(ctx, h.ID)); hh.Name != "storage" || hh.Monitored {
		t.Errorf("edited host: %+v", hh)
	}
	e.ui.Wait(func() { g.lastForm = nil; g.editGroupDialog(1, gr.ID) })
	eventually(t, e.ui, "group edit form", func() bool { return g.lastForm != nil })
	e.ui.Wait(func() {
		f = g.lastForm
		f.form.Items[1].Widget.(*fyneEntry).SetText("the LAN")
		f.Submit()
	})
	eventually(t, e.ui, "group edited", func() bool { return f.done })
	if gg := must(c.Group(ctx, gr.ID)); gg.Description != "the LAN" || gg.CIDR != "10.0.0.0/24" {
		t.Errorf("edited group: %+v", gg)
	}

	// Move to group: the group can't go into itself; the host goes to Root.
	sub := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "sub", ParentID: &gr.ID}))
	eventually(t, e.ui, "sub shown", func() bool { return find(e.nodes(1), "group", sub.ID) != nil })
	e.ui.Wait(func() {
		f = g.moveToDialog(1, mapmodel.GroupKey(gr.ID))
		if opts := f.form.Items[0].Widget.(*fyneSelect).Options; len(opts) != 1 || opts[0] != "Root" {
			t.Errorf("move options for the group: %v", opts)
		}
		f.d.Hide()
		f = g.moveToDialog(1, mapmodel.HostKey(h.ID))
		sel := f.form.Items[0].Widget.(*fyneSelect)
		if sel.Selected != fmt.Sprintf("lan (%d)", gr.ID) || len(sel.Options) != 3 {
			t.Errorf("host move dialog: %q %v", sel.Selected, sel.Options)
		}
		sel.SetSelected("Root")
		f.Submit()
	})
	eventually(t, e.ui, "host at root", func() bool { return f.done && must(c.Host(ctx, h.ID)).ParentID == nil })

	// Delete: Delete key on the selected host; the group's cascade question.
	must(c.UpdateHost(ctx, h.ID, apitypes.HostPatch{ParentID: apitypes.Some(&sub.ID)}))
	eventually(t, e.ui, "host in sub", func() bool {
		n := find(e.nodes(1), "host", h.ID)
		return n != nil && n.Item.ParentID != nil && *n.Item.ParentID == sub.ID
	})
	e.ui.Wait(func() {
		f = g.deleteDialog(1, mapmodel.GroupKey(gr.ID))
		text := f.form.Items[0].Widget.(*fyneLabel).Text
		if !strings.Contains(text, "1 host and 1 group.") || !strings.Contains(text, "deletes 1 host and 1 group") {
			t.Errorf("cascade wording: %q", text)
		}
		f.form.Items[1].Widget.(*fyneRadio).SetSelected("Delete its contents too")
		f.Submit()
	})
	eventually(t, e.ui, "cascade done", func() bool { return f.done && len(must(c.Hosts(ctx))) == 0 && len(e.nodes(1)) == 0 })

	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.5", Name: "pc"}))
	eventually(t, e.ui, "pc shown", func() bool { return find(e.nodes(1), "host", h2.ID) != nil })
	e.ui.Wait(func() {
		k := mapmodel.HostKey(h2.ID)
		g.maps[1].Select(&k)
		g.lastForm = nil
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyDelete})
		f = g.lastForm
		if f == nil || !strings.Contains(f.form.Items[0].Widget.(*fyneLabel).Text, "removed from every view") {
			t.Fatalf("Delete key: %+v", f)
		}
		f.Submit()
	})
	eventually(t, e.ui, "host deleted", func() bool { return f.done && len(must(c.Hosts(ctx))) == 0 })

	// In a custom view, the cascade keeps the hosts.
	h3 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.6"}))
	v := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"}))
	cg := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "rack", ViewID: &v.ID}))
	must(c.PlaceHost(ctx, v.ID, h3.ID, apitypes.PlacementSet{ParentID: &cg.ID}))
	eventually(t, e.ui, "Lab tab", func() bool { return e.tabNames() == "Networks,Lab" })
	e.ui.Wait(func() { g.showView(v.ID) })
	eventually(t, e.ui, "Lab loaded", func() bool { return find(e.nodes(v.ID), "group", cg.ID) != nil })
	e.ui.Wait(func() {
		f = g.deleteDialog(v.ID, mapmodel.GroupKey(cg.ID))
		if text := f.form.Items[0].Widget.(*fyneLabel).Text; !strings.Contains(text, "stay in the network view") {
			t.Errorf("custom cascade wording: %q", text)
		}
	})
}

// TC-720: the discovery dialog, progress in the dialog and the status bar,
// cancel, "Scan this network" disabled for IPv6.
func TestDiscoveryDialog(t *testing.T) {
	gate := make(chan struct{})
	e := &env{t: t, srv: testserver.Start(t, testserver.Options{Gate: gate}), ui: newUI(t), ctx: t.Context()}
	e.c = must(clientFor(e.srv.URL()))
	c, ctx := e.c, e.ctx
	v6 := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "2001:db8::/64"}))
	v4 := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.9.0.0/30"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 2 })
	e.ui.Wait(func() {
		if it := menuItem(g.menuItems(1, find(e.nodes(1), "group", v6.ID), 0, 0, nil), "Scan this network"); it == nil || !it.Disabled {
			t.Errorf("IPv6 scan item: %+v", it)
		}
		it := menuItem(g.menuItems(1, find(e.nodes(1), "group", v4.ID), 0, 0, nil), "Scan this network")
		if it == nil || it.Disabled {
			t.Fatalf("IPv4 scan item: %+v", it)
		}
		it.Action()
		if g.disc == nil || g.disc.cidr.Text != "10.9.0.0/30" {
			t.Fatalf("dialog: %+v", g.disc)
		}
		g.startDiscovery(g.disc)
	})
	eventually(t, e.ui, "job running", func() bool { return g.disc.job != "" && !g.disc.cancel.Disabled() })
	var job string
	e.ui.Wait(func() { job = g.disc.job; g.disc.cancel.OnTapped() })
	// The cancel request runs in the background: release the held probes
	// only once the server has the job cancelled, or it may finish first.
	deadline := time.Now().Add(5 * time.Second)
	for must(c.Discovery(ctx, job)).State == apitypes.DiscoveryRunning {
		if time.Now().After(deadline) {
			t.Fatal("job not cancelled on the server")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(gate)
	eventually(t, e.ui, "cancelled", func() bool {
		return strings.Contains(g.disc.info.Text, "cancelled") && strings.Contains(g.status.Text(), "discovery 10.9.0.0/30 cancelled")
	})

	// A second job with the dialog closed: progress goes to the status bar.
	var dd *discoveryDialog
	e.ui.Wait(func() {
		dd = g.openDiscovery("")
		dd.cidr.SetText("10.8.0.0/30")
		g.startDiscovery(dd)
	})
	eventually(t, e.ui, "started", func() bool { return dd.job != "" })
	e.ui.Wait(func() { dd.d.Hide() })
	eventually(t, e.ui, "done in the status bar", func() bool {
		return g.disc == nil && strings.Contains(g.status.Text(), "discovery 10.8.0.0/30 done: 2 found")
	})
	e.ui.Wait(func() {
		dd.copyB.OnTapped()
		if got := fyne.CurrentApp().Clipboard().Content(); !strings.Contains(got, "range 10.8.0.0/30, state done: 2 of 2 probed") {
			t.Errorf("copied job details: %q", got)
		}
	})
	// A bad range: the error is in the dialog.
	e.ui.Wait(func() {
		dd = g.openDiscovery("")
		dd.cidr.SetText("2001:db8::/120")
		g.startDiscovery(dd)
	})
	eventually(t, e.ui, "error shown", func() bool { return strings.HasPrefix(dd.info.Text, "Error:") && !dd.start.Disabled() })
	// The message can be selected and copied (user request).
	e.ui.Wait(func() {
		if !dd.info.Selectable || dd.info.Wrapping != fyne.TextWrapWord {
			t.Error("message not selectable or not wrapped")
		}
		dd.copyB.OnTapped()
		if got := fyne.CurrentApp().Clipboard().Content(); !strings.HasPrefix(got, "Error: ") || !strings.Contains(got, "IPv6") {
			t.Errorf("copied: %q", got)
		}
	})
}

// TC-721: the View menu.
func TestViewMenu(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	e.open()
	g := e.g
	eventually(t, e.ui, "tabs", func() bool { return e.tabNames() == "Networks" })
	e.ui.Wait(func() {
		items := g.viewMenuItems()
		if !menuItem(items, "Delete view…").Disabled || !menuItem(items, "Move view left").Disabled {
			t.Errorf("network view menu: %v", labels(items))
		}
	})
	var f *formDialog
	e.ui.Wait(func() {
		f = g.newViewDialog()
		f.form.Items[0].Widget.(*fyneEntry).SetText("Lab")
		f.Submit()
	})
	eventually(t, e.ui, "new view selected", func() bool { return e.tabNames() == "Networks,Lab" && g.current == 2 })
	e.ui.Wait(func() {
		f = g.renameViewDialog()
		f.form.Items[0].Widget.(*fyneEntry).SetText("Rack")
		f.Submit()
	})
	eventually(t, e.ui, "renamed", func() bool { return e.tabNames() == "Networks,Rack" })
	e.ui.Wait(func() { menuItem(g.viewMenuItems(), "Move view left").Action() })
	eventually(t, e.ui, "moved left", func() bool { return e.tabNames() == "Rack,Networks" })
	e.ui.Wait(func() {
		if !menuItem(g.viewMenuItems(), "Move view left").Disabled {
			t.Error("left of the first tab")
		}
		f = g.deleteViewDialog()
		f.Submit()
	})
	eventually(t, e.ui, "deleted", func() bool { return e.tabNames() == "Networks" && g.current == 1 })
	if vs := must(c.Views(ctx)); len(vs) != 1 {
		t.Errorf("views: %+v", vs)
	}
}

// TC-722: "Show in view ▸", "Remove from this view", and the details.
func TestShowInView(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web"}))
	a := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "A"}))
	b := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "B"}))
	must(c.PlaceHost(ctx, a.ID, h.ID, apitypes.PlacementSet{}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 1 })
	host := must(c.Host(ctx, h.ID))
	e.ui.Wait(func() {
		item := menuItem(g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, &host), "Show in view")
		if item == nil || item.ChildMenu == nil {
			t.Fatalf("Show in view: %+v", item)
		}
		sub := item.ChildMenu.Items
		if len(sub) != 2 || !sub[0].Checked || sub[1].Checked {
			t.Fatalf("sub menu: %+v", sub)
		}
		if menuItem(g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, &host), "Remove from this view") != nil {
			t.Error("remove in the network view")
		}
		k := mapmodel.HostKey(h.ID)
		g.details.show(1, &k)
		sub[1].Action() // place in B
	})
	eventually(t, e.ui, "placed in B; details list it", func() bool {
		return slices.Equal(must(c.Host(ctx, h.ID)).Views, []int64{1, a.ID, b.ID}) && strings.Contains(formText(g.details), "Networks, A, B")
	})
	e.ui.Wait(func() { g.showView(a.ID) })
	eventually(t, e.ui, "A loaded", func() bool { return find(e.nodes(a.ID), "host", h.ID) != nil })
	host = must(c.Host(ctx, h.ID))
	e.ui.Wait(func() {
		menuItem(g.menuItems(a.ID, find(e.nodes(a.ID), "host", h.ID), 0, 0, &host), "Remove from this view").Action()
	})
	eventually(t, e.ui, "removed from A", func() bool {
		return find(e.nodes(a.ID), "host", h.ID) == nil && slices.Equal(must(c.Host(ctx, h.ID)).Views, []int64{1, b.ID})
	})
}

// Crosscheck 2026-11-11-03 C1, C5, P1: every tiny discovery job's end
// reaches the dialog; the range field has the focus.
func TestDiscoveryEndsAreSeen(t *testing.T) {
	e := start(t)
	e.open()
	g := e.g
	eventually(t, e.ui, "connected", func() bool { return strings.Contains(g.status.Text(), "connected") })
	for i := 0; i < 20; i++ {
		var dd *discoveryDialog
		e.ui.Wait(func() {
			dd = g.openDiscovery("")
			if g.win.Canvas().Focused() != dd.cidr {
				t.Fatal("range field not focused")
			}
			dd.cidr.SetText(fmt.Sprintf("10.%d.0.0/30", 100+i))
			g.startDiscovery(dd)
		})
		eventually(t, e.ui, fmt.Sprintf("job %d done", i), func() bool { return strings.Contains(dd.info.Text, "done") && dd.cancel.Disabled() })
		e.ui.Wait(func() { dd.d.Hide() })
	}
}

// Crosscheck 2026-11-11-03 C2, P1: the progress bar uses palette colours
// only (black and white in Max) and fills with its value.
func TestProgressBar(t *testing.T) {
	test.NewTempApp(t)
	p := theme.NewPalette(theme.Contrast{Preset: theme.PresetMax, Min: 21})
	b := newProgressBar(func() theme.Palette { return p })
	b.Resize(fyne.NewSize(202, 14))
	b.SetValue(0.5)
	objs := test.WidgetRenderer(b).Objects()
	assertBlackWhite(t, objs)
	if w := objs[1].Size().Width; w != 100 {
		t.Errorf("fill width %g, want 100", w)
	}
	b.SetValue(7)
	if w := objs[1].Size().Width; w != 200 {
		t.Errorf("fill width at > 1: %g", w)
	}
	for _, o := range objs {
		if _, ok := o.(*canvas.Text); ok {
			t.Error("text inside the bar")
		}
	}
}

// Crosscheck 2026-11-11-03 C4, C7: "New host here…" on empty space goes
// next to its network group; the empty-space and frame menus; the host
// menu opens after loading the host; Esc stops a drag.
func TestMenusAndNewHost(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	net := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.5.0.0/24", X: ptr(1000.0), Y: ptr(1000.0), Collapsed: ptr(false)}))
	h0 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "10.5.0.1", X: ptr(1000.0), Y: ptr(1120.0)}))
	v := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 2 })

	var f *formDialog
	e.ui.Wait(func() {
		items := g.menuItems(1, nil, 5, 5, nil)
		if fmt.Sprint(labels(items)) != "[New group here… New host here…]" {
			t.Errorf("empty-space menu: %v", labels(items))
		}
		if fmt.Sprint(labels(g.menuItems(v.ID, nil, 5, 5, nil))) != "[New group here…]" {
			t.Error("custom view offers New host")
		}
		f = g.newHostDialog(nil, 5, 5)
		f.form.Items[0].Widget.(*fyneEntry).SetText("10.5.0.9")
		f.Submit()
	})
	eventually(t, e.ui, "host created", func() bool { return f.done })
	for _, h := range must(c.Hosts(ctx)) {
		if h.IP == "10.5.0.9" && (h.ParentID == nil || *h.ParentID != net.ID || h.X < 1000 || h.Y < 1000) {
			t.Errorf("new host not next to its network group: %+v", h)
		}
	}

	// The frame's body offers new items inside the group.
	e.ui.Wait(func() {
		fr := find(e.nodes(1), "group", net.ID)
		items := g.menuItems(1, fr, fr.Bounds.X+fr.Bounds.W-3, fr.Bounds.Y+fr.Bounds.H-3, nil)
		menuItem(items, "New group here…").Action()
		f = g.lastForm
		f.form.Items[0].Widget.(*fyneEntry).SetText("inner")
		f.Submit()
	})
	eventually(t, e.ui, "group inside the frame", func() bool { return f.done })
	for _, gr := range must(c.Groups(ctx)) {
		if gr.Name == "inner" && (gr.ParentID == nil || *gr.ParentID != net.ID) {
			t.Errorf("inner group: %+v", gr)
		}
	}

	// The host menu: loaded first, then shown as a pop-up.
	eventually(t, e.ui, "host shown", func() bool { return find(e.nodes(1), "host", h0.ID) != nil })
	var before int
	e.ui.Wait(func() {
		before = len(g.win.Canvas().Overlays().List())
		g.openMenu(1, find(e.nodes(1), "host", h0.ID), fyne.NewPos(10, 10), 0, 0)
	})
	eventually(t, e.ui, "pop-up menu", func() bool { return len(g.win.Canvas().Overlays().List()) == before+1 })

	// Esc during a drag.
	e.ui.Wait(func() {
		mv := g.maps[1]
		mv.SetViewport(mapmodel.Viewport{Zoom: 1, X: 900, Y: 900})
		sx, sy := mv.vp.ToScreen(1000, 1120)
		for i := 1; i <= 5; i++ {
			mv.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(float32(sx)+float32(10*i), float32(sy))}, Dragged: fyne.Delta{DX: 10}})
		}
		if !mv.Dragging() {
			t.Fatal("not dragging")
		}
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
		mv.DragEnd()
		if mv.Dragging() {
			t.Error("Esc didn't stop the drag")
		}
	})
	if h := must(c.Host(ctx, h0.ID)); h.X != 1000 {
		t.Errorf("host moved after Esc: %+v", h)
	}
}

// TC-723, TC-724 (GUI): the "Wrap names" toggle redraws at once and is
// saved; "Reflow…" on a group and "Reflow view…" lay out the members on
// the server with room for the longest name.
func TestWrapAndReflow(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	long := "web01.prod.example.org"
	g := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "g", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: long, ParentID: &g.ID, X: ptr(100.0), Y: ptr(220.0)}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Name: long, ParentID: &g.ID, X: ptr(200.0), Y: ptr(220.0)}))
	var savedMu sync.Mutex
	var saved []bool
	a := test.NewTempApp(t)
	e.ui.Wait(func() {
		e.g = New(a, Options{Client: c, Contrast: theme.Default, Do: e.ui.Do, WrapNames: true,
			SaveWrapNames: func(on bool) error { savedMu.Lock(); defer savedMu.Unlock(); saved = append(saved, on); return nil }})
	})
	e.g.Start()
	t.Cleanup(e.g.Stop)
	gu := e.g
	eventually(t, e.ui, "wrapped", func() bool {
		n := find(e.nodes(1), "host", h1.ID)
		return n != nil && n.NameLines > 1
	})
	e.ui.Wait(func() {
		if !gu.wrapChk.Checked {
			t.Error("toggle not checked")
		}
		gu.wrapChk.SetChecked(false)
		if n := find(e.nodes(1), "host", h1.ID); n.NameLines != 1 {
			t.Errorf("still wrapped: %q", n.Labels)
		}
	})
	eventually(t, e.ui, "saved", func() bool {
		savedMu.Lock()
		defer savedMu.Unlock()
		return len(saved) == 1 && !saved[0]
	})

	// Reflow the group: the two hosts move apart far enough.
	var f *formDialog
	e.ui.Wait(func() {
		n := find(e.nodes(1), "group", g.ID)
		menuItem(gu.menuItems(1, n, n.Icon.X+2, n.Icon.Y+2, nil), "Reflow…").Action()
		f = gu.lastForm
		if text := f.form.Items[0].Widget.(*fyneLabel).Text; !strings.Contains(text, "2 members in group g") {
			t.Errorf("question: %q", text)
		}
		f.Submit()
	})
	eventually(t, e.ui, "reflowed", func() bool { return f.done })
	a1, a2 := must(c.Host(ctx, h1.ID)), must(c.Host(ctx, h2.ID))
	if w, _ := measure(long); a2.X-a1.X < w || a1.Y != a2.Y {
		t.Errorf("hosts at %g,%g and %g,%g: not room for %g", a1.X, a1.Y, a2.X, a2.Y, w)
	}
	eventually(t, e.ui, "map follows", func() bool {
		n := find(e.nodes(1), "host", h2.ID)
		return n != nil && n.Item.X == a2.X
	})
	// Reflow view: offered in the View menu, for the root.
	e.ui.Wait(func() {
		menuItem(gu.viewMenuItems(), "Reflow view…").Action()
		if f = gu.lastForm; f == nil || !strings.Contains(f.form.Items[0].Widget.(*fyneLabel).Text, "top level of this view") {
			t.Fatalf("view reflow: %+v", f)
		}
		f.Submit()
	})
	eventually(t, e.ui, "view reflowed", func() bool { return f.done })
}

// TC-725 (GUI): collapsing and expanding keep the group's top-left corner.
func TestCornerStays(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	// The members sit far from the group's own position.
	g := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "g", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web", ParentID: &g.ID, X: ptr(500.0), Y: ptr(400.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Name: "db", ParentID: &g.ID, X: ptr(600.0), Y: ptr(400.0)}))
	e.open()
	gu := e.g
	frameOf := func() *mapmodel.Node {
		n := find(e.nodes(1), "group", g.ID)
		if n == nil || n.Kind != mapmodel.KindFrame {
			return nil
		}
		return n
	}
	eventually(t, e.ui, "frame", func() bool { return frameOf() != nil })
	var fx, fy float64
	e.ui.Wait(func() { f := frameOf(); fx, fy = f.Bounds.X, f.Bounds.Y; gu.maps[1].OnToggle(g.ID, false) })
	eventually(t, e.ui, "collapsed", func() bool {
		n := find(e.nodes(1), "group", g.ID)
		return n != nil && n.Kind == mapmodel.KindGroup
	})
	e.ui.Wait(func() {
		x, y := mapmodel.IconCorner(find(e.nodes(1), "group", g.ID).Item)
		if math.Abs(x-fx) > 0.01 || math.Abs(y-fy) > 0.01 {
			t.Errorf("icon corner %g,%g, frame corner was %g,%g", x, y, fx, fy)
		}
		fx, fy = x, y
		gu.maps[1].OnToggle(g.ID, true)
	})
	eventually(t, e.ui, "expanded at the icon's corner", func() bool {
		f := frameOf()
		return f != nil && math.Abs(f.Bounds.X-fx) < 0.01 && math.Abs(f.Bounds.Y-fy) < 0.01
	})
	// The group's own position didn't move on expand; its contents did.
	if gg := must(c.Group(ctx, g.ID)); gg.Collapsed {
		t.Error("still collapsed")
	}
}

// TC-726 (widget): Ctrl+click adds and removes, Shift+drag selects with a
// rubber band, and a drag on a selected item moves the whole selection.
func TestMultiSelectWidget(t *testing.T) {
	p := theme.NewPalette(theme.Default)
	mv, _ := newMap(t, p,
		apitypes.MapItem{Type: "host", ID: 1, Name: "a", X: 100, Y: 100, Status: "up", Monitored: true},
		apitypes.MapItem{Type: "host", ID: 2, Name: "b", X: 200, Y: 100, Status: "up", Monitored: true},
		apitypes.MapItem{Type: "host", ID: 3, Name: "c", X: 500, Y: 400, Status: "up", Monitored: true},
	)
	var moved []mapmodel.Key
	mv.OnMoved = func(keys []mapmodel.Key, _, _ float64) { moved = keys }
	at := func(x, y float64) *fyne.PointEvent {
		sx, sy := mv.vp.ToScreen(x, y)
		return &fyne.PointEvent{Position: fyne.NewPos(float32(sx), float32(sy))}
	}
	press := func(m fyne.KeyModifier) { mv.MouseDown(&desktop.MouseEvent{Modifier: m}) }
	sel := func() string { return fmt.Sprint(mv.Selection()) }

	press(0)
	mv.Tapped(at(100, 100))
	press(fyne.KeyModifierShortcutDefault)
	mv.Tapped(at(200, 100))
	if sel() != "[{host 1} {host 2}]" || *mv.Selected() != mapmodel.HostKey(2) {
		t.Fatalf("ctrl+click add: %s", sel())
	}
	mv.Tapped(at(100, 100)) // ctrl+click again removes
	mv.Tapped(at(700, 700)) // ctrl+click on empty space keeps the selection
	if sel() != "[{host 2}]" {
		t.Fatalf("ctrl+click remove: %s", sel())
	}
	press(0)
	mv.Tapped(at(700, 700))
	if sel() != "[]" {
		t.Fatalf("plain click on empty space: %s", sel())
	}

	// Shift+drag from empty space around hosts 1 and 2.
	press(fyne.KeyModifierShift)
	drag(mv, 50, 50, 220, 100)
	if sel() != "[{host 1} {host 2}]" || mv.Dragging() {
		t.Fatalf("rubber band: %s", sel())
	}
	// A plain drag on a selected host moves both.
	press(0)
	drag(mv, 100, 100, 0, 60)
	if fmt.Sprint(moved) != "[{host 1} {host 2}]" {
		t.Errorf("multi-drag moved %v", moved)
	}
	// A drag on an unselected host moves only that one.
	drag(mv, 500, 400, 0, 60)
	if fmt.Sprint(moved) != "[{host 3}]" {
		t.Errorf("single drag moved %v", moved)
	}

	// TC-727: a right-click on a selected item keeps the selection and
	// makes it the primary; on another item it selects only that one.
	mv.SetSelection([]mapmodel.Key{mapmodel.HostKey(1), mapmodel.HostKey(2)})
	mv.TappedSecondary(at(100, 100))
	if sel() != "[{host 1} {host 2}]" || *mv.Selected() != mapmodel.HostKey(1) {
		t.Errorf("right-click on a selected host: %s", sel())
	}
	mv.TappedSecondary(at(500, 400))
	if sel() != "[{host 3}]" {
		t.Errorf("right-click on an unselected host: %s", sel())
	}
}

// TC-727 (GUI): with several items selected, the context menu's "Move to
// group…" and "Delete…" act on all of them.
func TestMultiSelectMenu(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	dst := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "dst", Collapsed: ptr(false), X: ptr(600.0), Y: ptr(100.0)}))
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lan.ID, X: ptr(100.0), Y: ptr(220.0)}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &lan.ID, X: ptr(200.0), Y: ptr(220.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h2.ID) != nil })

	var f *formDialog
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)})
		items := g.menuItems(1, find(e.nodes(1), "host", h1.ID), 0, 0, &h1)
		mi := menuItem(items, "Move 2 items to group…")
		if mi == nil || menuItem(items, "Delete 2 items…") == nil {
			t.Fatalf("menu: %v", labels(items))
		}
		g.lastForm = nil
		mi.Action()
		f = g.lastForm
		s := f.form.Items[0].Widget.(*widget.Select)
		if s.Selected != fmt.Sprintf("lan (%d)", lan.ID) {
			t.Fatalf("move dialog preselects %q", s.Selected)
		}
		s.SetSelected(fmt.Sprintf("dst (%d)", dst.ID))
		f.Submit()
	})
	eventually(t, e.ui, "moved", func() bool { return f.done })
	for _, h := range []apitypes.Host{h1, h2} {
		if got := must(c.Host(ctx, h.ID)); got.ParentID == nil || *got.ParentID != dst.ID {
			t.Errorf("host %d not moved: %+v", h.ID, got)
		}
	}

	// One item: the old labels.
	eventually(t, e.ui, "hosts shown", func() bool { return find(e.nodes(1), "host", h1.ID) != nil })
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.HostKey(h1.ID)})
		items := g.menuItems(1, find(e.nodes(1), "host", h1.ID), 0, 0, &h1)
		if menuItem(items, "Move to group…") == nil || menuItem(items, "Delete host…") == nil {
			t.Errorf("single menu: %v", labels(items))
		}
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)})
		items = g.menuItems(1, find(e.nodes(1), "host", h1.ID), 0, 0, &h1)
		g.lastForm = nil
		menuItem(items, "Delete 2 items…").Action()
		f = g.lastForm
		if f == nil || f.form.Items[0].Widget.(*fyneLabel).Text != "Delete 2 hosts? Hosts are removed from every view. This can't be undone." {
			t.Fatalf("delete many: %+v", f)
		}
		f.Submit()
	})
	eventually(t, e.ui, "deleted", func() bool { return f.done && len(must(c.Hosts(ctx))) == 0 })
}

// TC-726 (GUI): g groups the selection, c and e collapse and expand the
// selected groups, Delete asks once for several items.
func TestMultiSelectKeys(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	net := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web", ParentID: &net.ID, X: ptr(100.0), Y: ptr(220.0)}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Name: "db", ParentID: &net.ID, X: ptr(200.0), Y: ptr(220.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h2.ID) != nil })

	// g: a new group "app" inside lan, around web and db, positions kept.
	var f *formDialog
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)})
		g.lastForm = nil
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyG})
		f = g.lastForm
		if f == nil {
			t.Fatal("no dialog for g")
		}
		f.form.Items[0].Widget.(*fyneEntry).SetText("app")
		f.Submit()
	})
	eventually(t, e.ui, "grouped", func() bool { return f.done })
	var app apitypes.Group
	for _, gr := range must(c.Groups(ctx)) {
		if gr.Name == "app" {
			app = gr
		}
	}
	if app.ID == 0 || app.ParentID == nil || *app.ParentID != net.ID || app.Collapsed {
		t.Fatalf("new group: %+v", app)
	}
	for _, h := range []apitypes.Host{h1, h2} {
		got := must(c.Host(ctx, h.ID))
		if got.ParentID == nil || *got.ParentID != app.ID || got.X != h.X || got.Y != h.Y {
			t.Errorf("host %d: %+v", h.ID, got)
		}
	}
	eventually(t, e.ui, "app frame shown and selected", func() bool {
		n := find(e.nodes(1), "group", app.ID)
		return n != nil && n.Kind == mapmodel.KindFrame && g.maps[1].Selected() != nil && *g.maps[1].Selected() == mapmodel.GroupKey(app.ID)
	})

	// c and e on two selected groups.
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.GroupKey(net.ID), mapmodel.GroupKey(app.ID)})
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyC})
	})
	eventually(t, e.ui, "both collapsed", func() bool {
		return must(c.Group(ctx, net.ID)).Collapsed && must(c.Group(ctx, app.ID)).Collapsed
	})
	e.ui.Wait(func() { g.onKey(&fyne.KeyEvent{Name: fyne.KeyE}) })
	eventually(t, e.ui, "both expanded", func() bool {
		return !must(c.Group(ctx, net.ID)).Collapsed && !must(c.Group(ctx, app.ID)).Collapsed
	})

	// Delete with two hosts selected: one question, both deleted.
	eventually(t, e.ui, "hosts shown", func() bool { return find(e.nodes(1), "host", h1.ID) != nil })
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)})
		g.lastForm = nil
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyDelete})
		f = g.lastForm
		if f == nil || f.form.Items[0].Widget.(*fyneLabel).Text != "Delete 2 hosts? Hosts are removed from every view. This can't be undone." {
			t.Fatalf("delete many: %+v", f)
		}
		f.Submit()
	})
	eventually(t, e.ui, "deleted", func() bool { return f.done && len(must(c.Hosts(ctx))) == 0 })
	// Esc clears the selection.
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.GroupKey(app.ID)})
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
		if len(g.maps[1].Selection()) != 0 {
			t.Error("Esc kept the selection")
		}
	})
}
