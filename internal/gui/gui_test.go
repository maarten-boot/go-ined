package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// uiThread runs functions one at a time on one goroutine, like Fyne's
// event loop does in the real app. After the test, Do drops functions
// (a late reload or timer), as a closed app would.
type uiThread struct {
	mu     sync.Mutex
	closed bool
	ch     chan func()
}

func newUI(t *testing.T) *uiThread {
	u := &uiThread{ch: make(chan func(), 100000)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for f := range u.ch {
			f()
		}
	}()
	t.Cleanup(func() {
		u.mu.Lock()
		u.closed = true
		close(u.ch)
		u.mu.Unlock()
		<-done
	})
	return u
}

func (u *uiThread) Do(f func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.closed {
		u.ch <- f
	}
}

// Wait runs f on the UI goroutine and waits for it.
func (u *uiThread) Wait(f func()) {
	done := make(chan struct{})
	u.Do(func() { defer close(done); f() })
	<-done
}

// eventually polls cond on the UI goroutine until it holds.
func eventually(t *testing.T, u *uiThread, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var ok bool
		u.Wait(func() { ok = cond() })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type env struct {
	t   *testing.T
	srv *testserver.Server
	c   *client.Client
	ui  *uiThread
	g   *GUI
	ctx context.Context

	mu    sync.Mutex
	saved []theme.Contrast
	aaa   int
}

func start(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, srv: testserver.Start(t, testserver.Options{}), ui: newUI(t), ctx: context.Background()}
	var err error
	if e.c, err = client.New(client.Options{URL: e.srv.URL()}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) open() {
	a := test.NewTempApp(e.t)
	e.ui.Wait(func() {
		e.g = New(a, Options{Client: e.c, Contrast: theme.Default, Do: e.ui.Do,
			SaveContrast: func(c theme.Contrast) error {
				e.mu.Lock()
				defer e.mu.Unlock()
				if c.Preset == theme.PresetAAA {
					e.aaa++
					if e.aaa == 2 { // the second AAA save fails
						return errors.New("disk full")
					}
				}
				e.saved = append(e.saved, c)
				return nil
			}})
		e.g.Window().Resize(fyne.NewSize(1000, 700))
	})
	e.g.Start()
	e.t.Cleanup(e.g.Stop)
}

func (e *env) nodes(view int64) []mapmodel.Node {
	if mv := e.g.maps[view]; mv != nil {
		return mv.Nodes()
	}
	return nil
}

func find(nodes []mapmodel.Node, typ string, id int64) *mapmodel.Node {
	for i := range nodes {
		if nodes[i].Item.Type == typ && nodes[i].Item.ID == id {
			return &nodes[i]
		}
	}
	return nil
}

func (e *env) tabNames() string {
	var out []string
	for _, ti := range e.g.tabs.Items {
		out = append(out, ti.Text)
	}
	return strings.Join(out, ",")
}

// must returns v; a setup error panics, which fails the test.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TC-710, TC-711, TC-713, TC-712: tabs, live updates and the changes the
// GUI makes, against a real API server.
func TestGUI(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	net := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.0.0/24"}))
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.0.5", Name: "web"}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "laptop"}))
	pay := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Payroll"}))
	app := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "app", ViewID: &pay.ID}))
	must(c.PlaceHost(ctx, pay.ID, h1.ID, apitypes.PlacementSet{ParentID: &app.ID}))

	e.open()
	g := e.g
	eventually(t, e.ui, "tabs and the network map", func() bool {
		return e.tabNames() == "Networks,Payroll" && len(e.nodes(1)) == 2 && strings.Contains(g.status.Text(), "connected")
	})
	// Only the selected view's map is loaded (SPEC-716).
	e.ui.Wait(func() {
		if g.model.Loaded(pay.ID) {
			t.Error("Payroll loaded before its tab was shown")
		}
		if n := find(e.nodes(1), "group", net.ID); n == nil || n.Kind != mapmodel.KindGroup {
			t.Errorf("network group: %+v", n)
		}
	})

	// Expand by double click: the server's event redraws the map as a frame.
	e.ui.Wait(func() { g.maps[1].OnToggle(net.ID, true) })
	eventually(t, e.ui, "expanded frame with its host", func() bool {
		n := find(e.nodes(1), "group", net.ID)
		return n != nil && n.Kind == mapmodel.KindFrame && find(e.nodes(1), "host", h1.ID) != nil
	})
	if got := must(c.Group(ctx, net.ID)); got.Collapsed {
		t.Error("expand didn't reach the server")
	}

	// A change made elsewhere is applied in place (host.updated).
	must(c.UpdateHost(ctx, h2.ID, apitypes.HostPatch{Monitored: apitypes.Some(false)}))
	eventually(t, e.ui, "host shows off", func() bool {
		n := find(e.nodes(1), "host", h2.ID)
		return n != nil && len(n.Labels) == 2 && n.Labels[1] == "off"
	})

	// Showing the Payroll tab loads its map.
	e.ui.Wait(func() { g.tabs.SelectIndex(1) })
	eventually(t, e.ui, "Payroll map", func() bool {
		return g.current == pay.ID && find(e.nodes(pay.ID), "group", app.ID) != nil
	})

	// Details and the note (SPEC-708, 718).
	k := mapmodel.GroupKey(app.ID)
	e.ui.Wait(func() { g.details.show(pay.ID, &k) })
	eventually(t, e.ui, "group details", func() bool {
		return g.details.title.Text == "Group app" && strings.Contains(formText(g.details), "Payroll")
	})
	e.ui.Wait(func() {
		g.details.note.SetText("owner: finance")
		g.details.saveNote()
	})
	eventually(t, e.ui, "note saved", func() bool { return strings.Contains(g.status.Text(), "note of group") })
	if got := must(c.Group(ctx, app.ID)); got.Note != "owner: finance" {
		t.Errorf("note on the server: %q", got.Note)
	}
	eventually(t, e.ui, "note marker", func() bool {
		n := find(e.nodes(pay.ID), "group", app.ID)
		return n != nil && n.Item.HasNote
	})

	// An API error goes to the status bar; the map keeps the server's state.
	e.ui.Wait(func() { g.setCollapsed(999, false) })
	eventually(t, e.ui, "error in status bar", func() bool { return strings.Contains(g.status.Text(), "group 999") })

	// Views change elsewhere: a new tab, a rename, then the selected view
	// is deleted and the GUI falls back to the network view (SPEC-715).
	lab := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"}))
	must(c.UpdateView(ctx, pay.ID, apitypes.ViewPatch{Name: apitypes.Some("Pay")}))
	eventually(t, e.ui, "tab list", func() bool { return e.tabNames() == "Networks,Pay,Lab" })
	if err := c.DeleteView(ctx, pay.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, e.ui, "fallback to the network view", func() bool {
		return e.tabNames() == "Networks,Lab" && g.current == 1 && g.tabs.SelectedIndex() == 0 &&
			strings.Contains(g.status.Text(), "was deleted") && g.details.key == nil
	})
	_ = lab
}

// formText returns the text of the details rows.
func formText(d *details) string {
	var b strings.Builder
	for _, o := range d.form.Objects {
		if l, ok := o.(*widget.Label); ok {
			b.WriteString(l.Text + "\n")
		}
	}
	return b.String()
}

// TC-709 (GUI part): find host goes to the host, expanding its groups.
func TestGoToHost(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	net := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.0.0/24"}))
	sub := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "servers", ParentID: &net.ID}))
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.0.7", Name: "db", ParentID: &sub.ID}))
	lab := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 1 && e.tabNames() == "Networks,Lab" })
	// From the Lab tab, where the host isn't placed: the network view.
	e.ui.Wait(func() { g.tabs.SelectIndex(1) })
	eventually(t, e.ui, "lab", func() bool { return g.current == lab.ID })
	e.ui.Wait(func() { g.goToHost(h.ID) })
	eventually(t, e.ui, "host shown in the network view", func() bool {
		sel := g.maps[1].Selected()
		return g.current == 1 && sel != nil && *sel == mapmodel.HostKey(h.ID) && find(e.nodes(1), "host", h.ID) != nil
	})
	for _, id := range []int64{net.ID, sub.ID} {
		if must(c.Group(ctx, id)).Collapsed {
			t.Errorf("group %d still collapsed", id)
		}
	}
}

// TC-005, TC-009 (GUI part): a contrast change recolours at once and is
// saved; a failed save is reported.
func TestContrastChange(t *testing.T) {
	e := start(t)
	must(e.c.CreateHost(e.ctx, apitypes.HostCreate{IP: "192.0.2.9"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "map", func() bool { return len(e.nodes(1)) == 1 })
	e.ui.Wait(func() { g.pickContrast("Max (21:1)") })
	eventually(t, e.ui, "saved", func() bool { return strings.Contains(g.status.Text(), "Max (21:1) saved") })
	e.ui.Wait(func() {
		if g.Contrast().Preset != theme.PresetMax || g.contrSel.Selected != "Max (21:1)" {
			t.Errorf("contrast: %+v %q", g.Contrast(), g.contrSel.Selected)
		}
		assertBlackWhite(t, test.WidgetRenderer(g.maps[1]).Objects())
	})
	e.ui.Wait(func() { g.changeContrast(theme.Contrast{Preset: theme.PresetCustom, Min: 9}) })
	eventually(t, e.ui, "custom saved", func() bool { return strings.Contains(g.status.Text(), "custom (9:1) saved") })
	e.ui.Wait(func() {
		if g.contrSel.Selected != "custom (9:1)" || !strings.Contains(strings.Join(g.contrSel.Options, ","), "custom (9:1)") {
			t.Errorf("selector: %q %v", g.contrSel.Selected, g.contrSel.Options)
		}
		g.pickContrast("AAA (7:1)")
	})
	eventually(t, e.ui, "AAA", func() bool { return strings.Contains(g.status.Text(), "AAA (7:1) saved") })
	e.ui.Wait(func() { g.pickContrast("AA (4.5:1)"); g.pickContrast("AAA (7:1)") })
	eventually(t, e.ui, "save error", func() bool { return strings.Contains(g.status.Text(), "contrast not saved: disk full") })
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.saved) != 4 || e.saved[0].Preset != theme.PresetMax || e.saved[1].Min != 9 {
		t.Errorf("saved: %+v", e.saved)
	}
}

// TC-703: after the server restarts, the GUI reconnects and reloads.
func TestReconnect(t *testing.T) {
	e := start(t)
	must(e.c.CreateHost(e.ctx, apitypes.HostCreate{IP: "192.0.2.1"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "connected", func() bool { return len(e.nodes(1)) == 1 && strings.Contains(g.status.Text(), "connected") })
	e.srv.Down()
	eventually(t, e.ui, "reconnecting", func() bool { return strings.Contains(g.status.Text(), "reconnecting in") })
	e.srv.Up()
	must(e.c.CreateHost(e.ctx, apitypes.HostCreate{IP: "192.0.2.2"}))
	eventually(t, e.ui, "reloaded after restart", func() bool {
		return len(e.nodes(1)) == 2 && strings.HasPrefix(g.status.Text(), "connected")
	})
}

// TC-712: the connection state in words, and the token warning.
func TestStatusBar(t *testing.T) {
	for _, tc := range []struct {
		st   client.StreamState
		want string
	}{
		{client.StreamState{Connected: true}, "connected"},
		{client.StreamState{RetryIn: 4 * time.Second}, "reconnecting in 4 s"},
		{client.StreamState{RetryIn: 1500 * time.Millisecond, Err: errors.New("connection refused")}, "reconnecting in 2 s (connection refused)"},
	} {
		if got := ConnText(tc.st); got != tc.want {
			t.Errorf("ConnText(%+v) = %q, want %q", tc.st, got, tc.want)
		}
	}
	test.NewTempApp(t)
	s := newStatusBar("http://example.org:8470", true)
	s.stopped(errors.New("unauthenticated (HTTP 401)"))
	s.message("hello")
	txt := s.Text()
	for _, want := range []string{"stopped: unauthenticated (HTTP 401)", "hello", "token is sent unencrypted"} {
		if !strings.Contains(txt, want) {
			t.Errorf("status bar %q lacks %q", txt, want)
		}
	}
	if s := newStatusBar("https://x", false); strings.Contains(s.Text(), "unencrypted") {
		t.Error("warning without reason")
	}
	// A stream refused with 401 ends as "stopped".
	srv := testserver.Start(t, testserver.Options{Token: "right"})
	c := must(client.New(client.Options{URL: srv.URL(), Token: "wrong"}))
	ui := newUI(t)
	var g *GUI
	a := test.NewTempApp(t)
	ui.Wait(func() { g = New(a, Options{Client: c, Contrast: theme.Default, Do: ui.Do}) })
	g.Start()
	defer g.Stop()
	eventually(t, ui, "stopped", func() bool { return strings.Contains(g.status.Text(), "stopped: ") })
}

// TestScreenshot writes PNGs of the GUI for a visual check when
// INED_SCREENSHOTS names a directory (manual part of TC-701/704/005).
func TestScreenshot(t *testing.T) {
	dir := os.Getenv("INED_SCREENSHOTS")
	if dir == "" {
		t.Skip("set INED_SCREENSHOTS=<dir> to write screenshots")
	}
	e := start(t)
	c, ctx := e.c, e.ctx
	net := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.0.0/16", Collapsed: ptr(false), Note: "core"}))
	srv := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "servers", ParentID: &net.ID, Collapsed: ptr(false)}))
	must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.1.0/24", ParentID: &net.ID}))
	must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "printers"}))
	for i, n := range []string{"web", "db", "mail"} {
		must(c.CreateHost(ctx, apitypes.HostCreate{IP: fmt.Sprintf("10.0.0.%d", i+10), Name: n, ParentID: &srv.ID}))
	}
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.1.5", Name: "nas"}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "laptop", Monitored: ptr(false), Note: "loaner"}))
	must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Payroll"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) >= 8 })
	time.Sleep(300 * time.Millisecond)
	for _, ct := range []theme.Contrast{theme.Default, {Preset: theme.PresetMax, Min: 21}} {
		e.ui.Wait(func() {
			g.setContrast(ct)
			g.fitAll()
			k := mapmodel.GroupKey(srv.ID)
			g.maps[1].Select(&k)
			g.details.show(1, &k)
		})
		time.Sleep(300 * time.Millisecond)
		e.ui.Wait(func() {
			img := g.Window().Canvas().Capture()
			f, err := os.Create(filepath.Join(dir, "gui-"+ct.Preset+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, img); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// TC-714: a burst of 1,000 status events with 5,000 hosts loaded is
// handled in well under a second, with coalesced redraws (SPEC-716,
// crosscheck 2026-11-11-02 C1).
func TestEventBurst(t *testing.T) {
	ui := newUI(t)
	a := test.NewTempApp(t)
	c := must(client.New(client.Options{URL: "http://127.0.0.1:1"})) // never contacted
	var g *GUI
	ui.Wait(func() {
		g = New(a, Options{Client: c, Contrast: theme.Default, Do: ui.Do})
		g.Window().Resize(fyne.NewSize(1200, 800))
		g.model.SetViews([]apitypes.View{{ID: 1, Name: "Networks"}})
		g.model.SetMap(apitypes.Map{ViewID: 1, Items: hosts(5000)})
		g.rebuildTabs()
		g.redraw(1)
		g.layouts = 0
	})
	t.Cleanup(g.Stop)
	evs := make([]client.Event, 1000)
	for i := range evs {
		b, _ := json.Marshal(apitypes.Host{ID: int64(i + 1), DisplayName: fmt.Sprint("h", i), Status: "down", Monitored: true})
		evs[i] = client.Event{Type: apitypes.EventHostStatus, Data: b}
	}
	start := time.Now()
	for _, ev := range evs {
		ui.Do(func() { g.apply(ev) })
	}
	eventually(t, ui, "burst handled and drawn", func() bool { return len(g.dirty) == 0 && !g.flushing && g.layouts > 0 })
	d := time.Since(start)
	var layouts int
	var n *mapmodel.Node
	ui.Wait(func() { layouts, n = g.layouts, find(g.maps[1].Nodes(), "host", 1000) })
	t.Logf("1000 events: %v, %d layouts", d, layouts)
	if d > time.Second || layouts > 3 {
		t.Errorf("1000 events took %v with %d layouts; want < 1 s and at most 3", d, layouts)
	}
	if n == nil || n.Item.Status != "down" || n.Labels[0] != "h999" {
		t.Errorf("last event not drawn: %+v", n)
	}
}

// C5 (crosscheck 2026-11-11-02): the find dialog, the context menu, the
// toolbar's zoom with its label, and the details following events.
func TestFindMenuToolbarDetails(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	net := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.0.0/24"}))
	db := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.0.7", Name: "db"}))
	web := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.0.8", Name: "web"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 1 })

	// Find: type, see the count, pick the result.
	var f *findDialog
	e.ui.Wait(func() { f = g.openFind() })
	eventually(t, e.ui, "hosts loaded", func() bool { return f.info.Text == "0 of 2 hosts" })
	e.ui.Wait(func() { f.entry.SetText("10.0.0.") })
	eventually(t, e.ui, "two found", func() bool { return f.info.Text == "2 of 2 hosts" })
	e.ui.Wait(func() { f.entry.SetText("DB") })
	e.ui.Wait(func() {
		if f.info.Text != "1 of 2 hosts" || f.list.Length() != 1 {
			t.Errorf("find db: %q", f.info.Text)
		}
		f.list.Select(0)
	})
	eventually(t, e.ui, "db shown and selected", func() bool {
		sel := g.maps[1].Selected()
		return sel != nil && *sel == mapmodel.HostKey(db.ID) && find(e.nodes(1), "host", db.ID) != nil &&
			g.details.title.Text == "Host db"
	})

	// Context menu of the (now expanded) network group: Collapse only.
	e.ui.Wait(func() {
		n := find(e.nodes(1), "group", net.ID)
		items := g.menuItems(n)
		if len(items) != 2 || !items[0].Disabled || items[1].Disabled {
			t.Fatalf("frame menu: %+v", items)
		}
		if g.menuItems(find(e.nodes(1), "host", db.ID)) != nil || g.menuItems(nil) != nil {
			t.Error("menu for a host or empty space")
		}
		items[1].Action()
	})
	eventually(t, e.ui, "collapsed by the menu", func() bool {
		n := find(e.nodes(1), "group", net.ID)
		return n != nil && n.Kind == mapmodel.KindGroup
	})
	e.ui.Wait(func() {
		items := g.menuItems(find(e.nodes(1), "group", net.ID))
		if items[0].Disabled || !items[1].Disabled {
			t.Errorf("icon menu: %+v", items)
		}
	})

	// Toolbar zoom and the zoom label (I1).
	e.ui.Wait(func() {
		g.zoomTo(1)
		g.zoomBy(2)
		if g.zoomL.Text != "200 %" || g.maps[1].Viewport().Zoom != 2 {
			t.Errorf("zoom: %q", g.zoomL.Text)
		}
		g.fitAll()
		if g.zoomL.Text != zoomLabel(g.maps[1].Viewport().Zoom) {
			t.Errorf("zoom label after fit: %q", g.zoomL.Text)
		}
	})

	// Details follow events; an unsaved note edit isn't overwritten.
	k := mapmodel.HostKey(web.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "web details", func() bool { return g.details.title.Text == "Host web" })
	must(c.UpdateHost(ctx, web.ID, apitypes.HostPatch{Name: apitypes.Some("www"), Note: apitypes.Some("from elsewhere")}))
	eventually(t, e.ui, "renamed", func() bool {
		return g.details.title.Text == "Host www" && g.details.note.Text == "from elsewhere"
	})
	e.ui.Wait(func() { g.details.note.SetText("my unsaved edit") })
	must(c.UpdateHost(ctx, web.ID, apitypes.HostPatch{Description: apitypes.Some("x")}))
	eventually(t, e.ui, "description shown", func() bool { return strings.Contains(formText(g.details), "x\n") })
	e.ui.Wait(func() {
		if g.details.note.Text != "my unsaved edit" {
			t.Errorf("edit overwritten: %q", g.details.note.Text)
		}
	})
	gk := mapmodel.GroupKey(net.ID)
	e.ui.Wait(func() { g.details.show(1, &gk) })
	eventually(t, e.ui, "group details", func() bool { return g.details.title.Text == "Group 10.0.0.0/24" })
	e.ui.Wait(func() {
		b, _ := json.Marshal(apitypes.GroupStatusEvent{ID: net.ID, Status: "down"})
		g.details.onEvent(client.Event{Type: apitypes.EventGroupStatus, Data: b})
		if !strings.Contains(formText(g.details), "down") {
			t.Error("group.status not shown")
		}
	})
	if err := c.DeleteGroup(ctx, net.ID, apitypes.ChildrenReparent); err != nil {
		t.Fatal(err)
	}
	eventually(t, e.ui, "cleared after delete", func() bool { return g.details.key == nil && g.details.title.Text == "Nothing selected" })
}

// C5, D1: the custom-contrast dialog validates its ratio; Max shows
// button borders.
func TestCustomContrastAndBorders(t *testing.T) {
	e := start(t)
	e.open()
	g := e.g
	var f *customForm
	e.ui.Wait(func() {
		for _, r := range g.borders {
			if r.StrokeColor != color.Transparent {
				t.Fatalf("border visible in AA: %v", r.StrokeColor)
			}
		}
		f = g.customContrastForm()
		f.Show()
		for _, bad := range []string{"3", "22", "x"} {
			if f.entry.Validator(bad) == nil {
				t.Errorf("ratio %q accepted", bad)
			}
		}
		f.entry.SetText("12")
		f.Submit()
	})
	eventually(t, e.ui, "custom applied", func() bool { return g.Contrast() == theme.Contrast{Preset: theme.PresetCustom, Min: 12} })
	e.ui.Wait(func() {
		f = g.customContrastForm()
		f.Show()
		f.Hide() // cancel: the selector shows the setting in use
		if g.contrSel.Selected != "custom (12:1)" {
			t.Errorf("after cancel: %q", g.contrSel.Selected)
		}
		g.pickContrast("Max (21:1)")
		if len(g.borders) < 6 {
			t.Fatalf("%d borders", len(g.borders))
		}
		for _, r := range g.borders {
			if r.StrokeColor != nrgba(theme.Black) {
				t.Errorf("Max border colour %v", r.StrokeColor)
			}
		}
	})
}
