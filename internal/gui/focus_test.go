package gui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-733 (SPEC-726): a group as a view. The key o shows only the selected
// group (collapsed on the shared map, and it stays so); the trail and the
// "Show" text follow; Backspace goes up and restores the zoom; the "Show"
// list switches focus and tab, filters, and Enter takes the first match;
// a trail part goes back; each tab keeps its own focus. Typing a label
// letter by letter only filters (crosscheck 2026-11-13-03 C2).
func TestFocusNavigation(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", Collapsed: ptr(true), X: ptr(100.0), Y: ptr(100.0)}))
	srv := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "servers", ParentID: &lan.ID, Collapsed: ptr(false), X: ptr(100.0), Y: ptr(200.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web", ParentID: &srv.ID, X: ptr(100.0), Y: ptr(300.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Name: "printer", ParentID: &lan.ID, X: ptr(400.0), Y: ptr(200.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.3", Name: "laptop", X: ptr(900.0), Y: ptr(100.0)}))
	lab := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"}))
	rack := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "rack", ViewID: &lab.ID, X: ptr(0.0), Y: ptr(0.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool {
		return find(e.nodes(1), "group", lan.ID) != nil && len(g.entries) == 5
	})
	shown := func(view int64) string {
		var out []string
		for _, n := range e.nodes(view) {
			out = append(out, n.Item.Name)
		}
		return strings.Join(out, ",")
	}
	entry := func(group int64) string {
		for _, en := range g.entries {
			if en.Group == group {
				return en.Label
			}
		}
		t.Fatalf("no entry for group %d", group)
		return ""
	}

	// o on lan: its contents, without lan and laptop; lan stays collapsed.
	before := mapmodel.Viewport{X: -123, Y: -45, Zoom: 0.37}
	e.ui.Wait(func() {
		g.maps[1].SetViewport(before)
		k := mapmodel.GroupKey(lan.ID)
		g.maps[1].Select(&k)
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyO})
		if got := shown(1); got != "servers,printer,web" {
			t.Errorf("focus lan shows %q", got)
		}
		if got := g.trailText(); got != "Networks › lan" {
			t.Errorf("trail %q", got)
		}
		if g.showSel.Text != "lan" {
			t.Errorf("show text %q", g.showSel.Text)
		}
		if g.maps[1].Viewport() == before {
			t.Error("not fitted to the group")
		}
		if g.maps[1].Selected() != nil {
			t.Error("selection kept")
		}
	})
	if !must(c.Group(ctx, lan.ID)).Collapsed {
		t.Error("lan expanded on the server")
	}

	// Picking "servers" from the list; Backspace back to lan, then to the
	// whole view with the zoom it had.
	e.ui.Wait(func() {
		g.showSel.SetText(entry(srv.ID))
		if got := g.trailText(); got != "Networks › lan › servers" || shown(1) != "web" {
			t.Errorf("focus servers: trail %q, shows %q", got, shown(1))
		}
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyBackspace})
		if g.focusOf(1) != lan.ID {
			t.Errorf("Backspace: focus %d", g.focusOf(1))
		}
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyBackspace})
		if g.focusOf(1) != 0 || g.trailText() != "Networks" || g.showSel.Text != "Networks" {
			t.Errorf("whole view: focus %d, trail %q, text %q", g.focusOf(1), g.trailText(), g.showSel.Text)
		}
		if vp := g.maps[1].Viewport(); vp != before {
			t.Errorf("zoom not restored: %+v", vp)
		}
		g.onKey(&fyne.KeyEvent{Name: fyne.KeyBackspace}) // at the top: nothing
		if g.focusOf(1) != 0 || !strings.Contains(shown(1), "laptop") {
			t.Error("Backspace at the top")
		}
	})

	// Typing filters; Enter takes the first match. Typing "Lab" letter by
	// letter only filters, although "Lab" is a whole label (crosscheck
	// 2026-11-13-03 C2).
	e.ui.Wait(func() {
		for _, s := range []string{"", "L", "La", "Lab"} {
			g.showSel.SetText(s)
		}
		if g.current != 1 || g.focusOf(1) != 0 {
			t.Errorf("typing \"Lab\" switched: tab %d, focus %d", g.current, g.focusOf(1))
		}
		if opts := g.showOpts; len(opts) != 1 || opts[0] != "Lab" || g.showSel.Text != "Lab" {
			t.Errorf("typing \"Lab\": options %q, text %q", opts, g.showSel.Text)
		}
		g.showSel.SetText("SERV")
		if opts := g.showOpts; len(opts) != 1 || strings.TrimSpace(opts[0]) != "servers" {
			t.Errorf("filtered %q", opts)
		}
		g.showSel.OnSubmitted("SERV")
		if g.focusOf(1) != srv.ID || len(g.showOpts) != 5 {
			t.Errorf("Enter: focus %d, options %d", g.focusOf(1), len(g.showOpts))
		}
	})

	// A group of another view switches the tab; each tab keeps its focus.
	e.ui.Wait(func() {
		g.showSel.SetText(entry(rack.ID))
		if g.current != lab.ID || g.focusOf(lab.ID) != rack.ID || g.focusOf(1) != srv.ID {
			t.Errorf("tab %d, focus lab %d, network %d", g.current, g.focusOf(lab.ID), g.focusOf(1))
		}
	})
	// The Lab map loads now, and the trail fills in when it is there.
	eventually(t, e.ui, "Lab trail", func() bool { return g.trailText() == "Lab › rack" })
	e.ui.Wait(func() {
		g.showView(1)
		if got := g.trailText(); got != "Networks › lan › servers" {
			t.Errorf("back on the network tab: trail %q", got)
		}
		// Rebuilding the trail doesn't grow the list of button borders.
		n := len(g.borders)
		g.setFocus(1, lan.ID)
		g.setFocus(1, srv.ID)
		if len(g.borders) != n {
			t.Errorf("borders %d → %d", n, len(g.borders))
		}
		// A click on the trail's first part shows the whole view.
		g.trailBox.Objects[0].(*fyne.Container).Objects[0].(*widget.Button).OnTapped()
		if g.focusOf(1) != 0 {
			t.Error("trail click")
		}
	})
}

// TC-734 (SPEC-726): inside a focus, new groups go into the focus group,
// the group menu offers "Show only this group", Reflow names the focus
// group, an item moved out disappears with a message, a deleted focus
// group moves the focus up, "Find host" for a host outside the focus
// shows the whole view, and an empty focus says so.
func TestFocusEditing(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lan", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	srv := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "servers", ParentID: &lan.ID, Collapsed: ptr(false), X: ptr(100.0), Y: ptr(200.0)}))
	web := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web", ParentID: &srv.ID, X: ptr(100.0), Y: ptr(300.0)}))
	prn := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Name: "printer", ParentID: &lan.ID, X: ptr(400.0), Y: ptr(200.0)}))
	top := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.3", Name: "laptop", X: ptr(900.0), Y: ptr(100.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", web.ID) != nil })
	label := func(items []*fyne.MenuItem, prefix string) *fyne.MenuItem {
		for _, it := range items {
			if strings.HasPrefix(it.Label, prefix) {
				return it
			}
		}
		return nil
	}

	// The group menu's "Show only this group".
	e.ui.Wait(func() {
		n := find(e.nodes(1), "group", lan.ID)
		show := label(g.menuItems(1, n, n.Icon.X+1, n.Icon.Y+1, nil), "Show only this group")
		if show == nil {
			t.Fatal("no \"Show only this group\"")
		}
		show.Action()
		if g.focusOf(1) != lan.ID {
			t.Errorf("focus %d", g.focusOf(1))
		}
		if it := label(g.viewMenuItems(), "Reflow"); it == nil || it.Label != `Reflow group "lan"…` {
			t.Errorf("reflow item %+v", it)
		}
	})

	// An empty focus says so (crosscheck 2026-11-13-03 L1).
	empty := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "spare", X: ptr(1500.0), Y: ptr(100.0)}))
	eventually(t, e.ui, "spare loaded", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.item(1, mapmodel.GroupKey(empty.ID)) != nil
	})
	e.ui.Wait(func() {
		g.setFocus(1, empty.ID)
		if got := g.maps[1].empty; got != `group "spare" is empty` {
			t.Errorf("empty focus: %q", got)
		}
		g.setFocus(1, lan.ID)
		if got := g.maps[1].empty; got != "" {
			t.Errorf("focus lan: %q", got)
		}
	})

	// "New group here…" on empty space goes into lan.
	var f *formDialog
	e.ui.Wait(func() {
		g.lastForm = nil
		label(g.menuItems(1, nil, 700, 700, nil), "New group here").Action()
		f = g.lastForm
		f.form.Items[0].Widget.(*fyneEntry).SetText("new")
		f.Submit()
	})
	eventually(t, e.ui, "created", func() bool { return f.done })
	for _, gr := range must(c.Groups(ctx)) {
		if gr.Name == "new" && (gr.ParentID == nil || *gr.ParentID != lan.ID) {
			t.Errorf("new group's parent %v", gr.ParentID)
		}
	}

	// printer moved to the root: it leaves the focus, with a message.
	must(c.UpdateHost(ctx, prn.ID, apitypes.HostPatch{ParentID: apitypes.Some[*int64](nil)}))
	eventually(t, e.ui, "printer left", func() bool {
		return find(e.nodes(1), "host", prn.ID) == nil && strings.Contains(g.status.Text(), `printer moved out of "lan"`)
	})

	// Focus on servers, then servers is deleted: the focus goes to lan.
	e.ui.Wait(func() { g.setFocus(1, srv.ID) })
	if err := c.DeleteGroup(ctx, srv.ID, "reparent"); err != nil {
		t.Fatal(err)
	}
	eventually(t, e.ui, "focus up", func() bool {
		return g.focusOf(1) == lan.ID && strings.Contains(g.status.Text(), `group "servers" was deleted; showing group "lan"`) &&
			find(e.nodes(1), "host", web.ID) != nil
	})

	// Find host: web is inside the focus (it stays), laptop is outside.
	e.ui.Wait(func() { g.goToHost(web.ID) })
	eventually(t, e.ui, "web found", func() bool {
		s := g.maps[1].Selected()
		return s != nil && *s == mapmodel.HostKey(web.ID)
	})
	e.ui.Wait(func() {
		if g.focusOf(1) != lan.ID {
			t.Errorf("focus left for a host inside it: %d", g.focusOf(1))
		}
		g.goToHost(top.ID)
	})
	eventually(t, e.ui, "laptop found", func() bool {
		s := g.maps[1].Selected()
		return g.focusOf(1) == 0 && s != nil && *s == mapmodel.HostKey(top.ID) && g.trailText() == "Networks"
	})
}
