package gui

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/snmp"
	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// gated holds every answer until released or the check is cancelled.
type gated struct{ gate chan struct{} }

func (g gated) System(ctx context.Context, _ netip.Addr, _, _ string) (inventory.SNMPSystem, error) {
	select {
	case <-g.gate:
		return inventory.SNMPSystem{Name: "x"}, nil
	case <-ctx.Done():
		return inventory.SNMPSystem{}, ctx.Err()
	}
}

func (g gated) Values(context.Context, netip.Addr, string, string) (apitypes.SNMPValues, error) {
	return apitypes.SNMPValues{}, errors.New("no values")
}

// TC-758: a group's menu has "Check SNMP now (2 hosts)" and, in the network
// view, "SNMP communities…"; two selected hosts get "Check SNMP now (2
// hosts)"; a check shows its progress with "Cancel SNMP check", which
// stops it; the dialog sets a group's list (masked entries), the details
// show "2 set here" and on a host "from group lab", and "Inherit" clears
// it; the strings are never shown.
func TestSNMPGroupCheckAndCommunities(t *testing.T) {
	gate := make(chan struct{})
	e := startWith(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"public"}, Client: gated{gate}}})
	c, ctx := e.c, e.ctx
	lab := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lab.ID, X: ptr(100.0), Y: ptr(200.0)}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &lab.ID, X: ptr(200.0), Y: ptr(200.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h2.ID) != nil })
	groupItem := func(label string) func() {
		var act func()
		e.ui.Wait(func() {
			g.maps[1].SetSelection(nil)
			fr := find(e.nodes(1), "group", lab.ID)
			if it := menuItem(g.menuItems(1, fr, fr.Icon.X+2, fr.Icon.Y+2, nil), label); it != nil {
				act = it.Action
			}
		})
		if act == nil {
			t.Fatalf("no %q", label)
		}
		return act
	}
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)})
		host := must(c.Host(ctx, h1.ID))
		if menuItem(g.menuItems(1, find(e.nodes(1), "host", h1.ID), 0, 0, &host), "Check SNMP now (2 hosts)") == nil {
			t.Error("no check on the selection")
		}
	})
	// A group check: progress, Cancel, cancelled.
	e.ui.Wait(groupItem("Check SNMP now (2 hosts)"))
	eventually(t, e.ui, "running", func() bool {
		return strings.Contains(g.status.Text(), "SNMP check: 0 of 2 hosts checked") && g.status.cancelB.Visible()
	})
	e.ui.Wait(func() { test.Tap(g.status.cancelB) })
	eventually(t, e.ui, "cancelled", func() bool {
		return strings.Contains(g.status.Text(), "; cancelled") && !g.status.cancelB.Visible()
	})
	close(gate)

	// The communities dialog.
	e.ui.Wait(groupItem("SNMP communities…"))
	eventually(t, e.ui, "dialog", func() bool { return g.commDialog != nil })
	e.ui.Wait(func() {
		cd := g.commDialog
		if !strings.Contains(cd.info.Text, "none set here") || !cd.entries[0].Password {
			t.Errorf("dialog: %q masked %v", cd.info.Text, cd.entries[0].Password)
		}
		cd.entries[0].SetText("lab-ro")
		cd.save([]string{"lab-ro", "second"})
	})
	eventually(t, e.ui, "saved", func() bool { return must(c.Group(ctx, lab.ID)).SNMPCommunitiesSet == 2 })
	k := mapmodel.HostKey(h1.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "host row", func() bool { return strings.Contains(formText(g.details), "SNMP communities\nfrom group lab") })
	gk := mapmodel.GroupKey(lab.ID)
	e.ui.Wait(func() { g.details.show(1, &gk) })
	eventually(t, e.ui, "group row", func() bool { return strings.Contains(formText(g.details), "SNMP communities\n2 set here") })
	if strings.Contains(formText(g.details)+g.status.Text(), "lab-ro") {
		t.Error("a string is shown")
	}
	e.ui.Wait(groupItem("SNMP communities…"))
	eventually(t, e.ui, "dialog again", func() bool { return g.commDialog != nil && strings.Contains(g.commDialog.info.Text, "2 set here") })
	e.ui.Wait(func() { g.commDialog.save([]string{}) })
	eventually(t, e.ui, "inherit", func() bool { return must(c.Group(ctx, lab.ID)).SNMPCommunitiesSet == 0 })
}

// TC-758 (crosscheck 2026-11-19-01 C1, L9, L10): a finished event that
// comes before the start's answer is used, so no Cancel button stays; a
// mixed selection checks its hosts and the hosts below its groups; a save
// the server refuses keeps the dialog open with the reason.
func TestSNMPCheckRaceSelectionAndSaveError(t *testing.T) {
	e := startWith(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"public"}, Client: snmp.Static{Community: "public", Version: snmp.V2c}}})
	c, ctx := e.c, e.ctx
	lab := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lab.ID, X: ptr(100.0), Y: ptr(200.0)}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", ParentID: &lab.ID, X: ptr(200.0), Y: ptr(200.0)}))
	lone := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.3", X: ptr(600.0), Y: ptr(200.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", lone.ID) != nil })
	e.ui.Wait(func() {
		// The finished event first, then the start's answer.
		done := apitypes.SNMPCheck{ID: "x-1", State: apitypes.DiscoveryDone, Total: 1, Checked: 1, Found: 1}
		b, _ := json.Marshal(done)
		g.onSNMPCheckEvent(client.Event{Type: apitypes.EventSNMPCheckFinished, Data: b})
		g.registerSNMPCheck(apitypes.SNMPCheck{ID: "x-1", State: apitypes.DiscoveryRunning, Total: 1})
		if g.status.cancelB.Visible() || len(g.snmpJobs) != 0 || !strings.Contains(g.status.Text(), "SNMP: 1 of 1 hosts answered") {
			t.Errorf("after an early finished event: cancel %v jobs %v status %q", g.status.cancelB.Visible(), g.snmpJobs, g.status.Text())
		}
		// A group and a lone host selected: 3 hosts.
		sel := []mapmodel.Key{mapmodel.GroupKey(lab.ID), mapmodel.HostKey(lone.ID)}
		g.maps[1].SetSelection(sel)
		host := must(c.Host(ctx, lone.ID))
		if menuItem(g.menuItems(1, find(e.nodes(1), "host", lone.ID), 0, 0, &host), "Check SNMP now (3 hosts)") == nil {
			t.Error("mixed selection from a host")
		}
		fr := find(e.nodes(1), "group", lab.ID)
		if menuItem(g.menuItems(1, fr, fr.Icon.X+2, fr.Icon.Y+2, nil), "Check SNMP now (3 hosts)") == nil {
			t.Error("mixed selection from a group")
		}
		g.maps[1].SetSelection(nil)
		g.communitiesDialog("host", 999, "gone", 0, "server") // the server refuses: no such host
		g.commDialog.save([]string{"x"})
	})
	eventually(t, e.ui, "the reason in the dialog", func() bool {
		return g.commDialog != nil && strings.HasPrefix(g.commDialog.info.Text, "Not saved: ")
	})
}

// TC-759 (crosscheck 2026-11-19-02 C1-C3, L1-L5): the details of a host
// follow its groups' lists without host events, also when a group moves; a
// refresh error other than "not found" keeps following the check; a save
// disables its buttons and, after Cancel, reports a failure in the status
// bar; the dialog keeps spaces; the early-event store keeps the newest
// event and drops the oldest job; one host with a group keeps its items.
func TestSNMPFollowDetailsAndDialogBusy(t *testing.T) {
	e := startWith(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"public"}, Client: snmp.Static{Community: "public", Version: snmp.V2c}}})
	c, ctx := e.c, e.ctx
	lab := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	top := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "top", Collapsed: ptr(false), X: ptr(600.0), Y: ptr(600.0)}))
	empty := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "empty", X: ptr(900.0), Y: ptr(100.0)}))
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lab.ID, X: ptr(100.0), Y: ptr(200.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h.ID) != nil })
	k := mapmodel.HostKey(h.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "server's list", func() bool { return strings.Contains(formText(g.details), "SNMP communities\nthe server's list") })

	// C1: a group's list reaches the shown host through group.updated.
	must(c.UpdateGroup(ctx, lab.ID, apitypes.GroupPatch{SNMPCommunities: apitypes.Some([]string{"a"})}))
	eventually(t, e.ui, "from lab", func() bool { return strings.Contains(formText(g.details), "SNMP communities\nfrom group lab") })
	// L4: lab without a list moves below top, which has one.
	must(c.UpdateGroup(ctx, top.ID, apitypes.GroupPatch{SNMPCommunities: apitypes.Some([]string{"b"})}))
	must(c.UpdateGroup(ctx, lab.ID, apitypes.GroupPatch{SNMPCommunities: apitypes.Some([]string{})}))
	must(c.UpdateGroup(ctx, lab.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&top.ID)}))
	eventually(t, e.ui, "from top", func() bool { return strings.Contains(formText(g.details), "SNMP communities\nfrom group top") })

	e.ui.Wait(func() {
		// L5: one host and an empty group: the one-host items, no job item.
		g.maps[1].SetSelection([]mapmodel.Key{k, mapmodel.GroupKey(empty.ID)})
		host := must(c.Host(ctx, h.ID))
		items := g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, &host)
		if menuItem(items, "Check SNMP now") == nil || menuItem(items, "SNMP communities…") == nil || menuItem(items, "Check SNMP now (1 host)") != nil {
			t.Errorf("one host and an empty group: %v", labels(items))
		}
		g.maps[1].SetSelection(nil)

		// L1: an older event after a newer one is not kept.
		ev := func(j apitypes.SNMPCheck) {
			b, _ := json.Marshal(j)
			g.onSNMPCheckEvent(client.Event{Type: apitypes.EventSNMPCheckProgress, Data: b})
		}
		ev(apitypes.SNMPCheck{ID: "y-1", State: apitypes.DiscoveryRunning, Total: 9, Checked: 5})
		ev(apitypes.SNMPCheck{ID: "y-1", State: apitypes.DiscoveryRunning, Total: 9, Checked: 3})
		if got := g.snmpSeen["y-1"].Checked; got != 5 {
			t.Errorf("kept checked %d, want 5", got)
		}
		// L2: 50 other jobs push out only the oldest.
		for i := range maxSeen {
			ev(apitypes.SNMPCheck{ID: "z-" + strconv.Itoa(i), State: apitypes.DiscoveryRunning})
		}
		if _, ok := g.snmpSeen["y-1"]; ok || len(g.snmpSeen) != maxSeen || len(g.snmpSeenIDs) != maxSeen {
			t.Errorf("store: %d events, %d ids, y-1 kept %v", len(g.snmpSeen), len(g.snmpSeenIDs), ok)
		}
		if _, ok := g.snmpSeen["z-0"]; !ok {
			t.Error("z-0 dropped")
		}
	})
	// C2: "not found" ends following a check.
	e.ui.Wait(func() { g.registerSNMPCheck(apitypes.SNMPCheck{ID: "gone-1", State: apitypes.DiscoveryRunning}) })
	eventually(t, e.ui, "not found unfollows", func() bool { return !g.snmpJobs["gone-1"] && !g.status.cancelB.Visible() })
	e.ui.Wait(func() {

		// L3: what is typed is saved as typed, empty rows left out.
		g.communitiesDialog("host", h.ID, "h", 0, "server")
		cd := g.commDialog
		var got []string
		cd.save = func(l []string) { got = l }
		cd.entries[0].SetText(" with space ")
		cd.busy[0].(*widget.Button).OnTapped() // Add
		save := cd.busy[2].(*widget.Button)
		save.OnTapped()
		if !slices.Equal(got, []string{" with space "}) {
			t.Errorf("saved %q", got)
		}
		cd.d.Hide()

		// C3: a save disables Save, Inherit and Add; Cancel during it, and
		// the refusal goes to the status bar.
		g.communitiesDialog("host", 999, "gone", 0, "server")
		cd = g.commDialog
		cd.save([]string{"x"})
		for _, b := range cd.busy {
			if !b.Disabled() {
				t.Error("a button is enabled while saving")
			}
		}
		cd.closed = true
		cd.d.Hide()
		g.commDialog = nil
	})
	eventually(t, e.ui, "refusal in the status bar", func() bool {
		return strings.Contains(g.status.Text(), "SNMP communities of host gone not saved:")
	})
}

// TC-759 (crosscheck 2026-11-19-03 C2): a 503 "unavailable" (SNMP is off,
// as after a restart with it turned off) ends following the check and
// hides Cancel; a network error (the server down) keeps it, with Cancel.
func TestSNMPCheckRefreshErrorKeepsJob(t *testing.T) {
	e := startWith(t, testserver.Options{})
	e.open()
	g := e.g
	e.ui.Wait(func() {
		g.registerSNMPCheck(apitypes.SNMPCheck{ID: "run-1", State: apitypes.DiscoveryRunning, Total: 2})
	})
	eventually(t, e.ui, "503 unfollows", func() bool {
		return strings.Contains(g.status.Text(), "SNMP check run-1: SNMP is off on the server; no longer followed") &&
			!g.snmpJobs["run-1"] && !g.status.cancelB.Visible()
	})
	e.srv.Down()
	defer e.srv.Up()
	e.ui.Wait(func() {
		g.registerSNMPCheck(apitypes.SNMPCheck{ID: "run-2", State: apitypes.DiscoveryRunning, Total: 2})
	})
	eventually(t, e.ui, "network error kept", func() bool { return strings.Contains(g.status.Text(), "SNMP check run-2: not read") })
	e.ui.Wait(func() {
		if !g.snmpJobs["run-2"] || !g.status.cancelB.Visible() {
			t.Errorf("after a network error: followed %v, cancel %v", g.snmpJobs["run-2"], g.status.cancelB.Visible())
		}
	})
}

// TC-759 (crosscheck 2026-11-19-03 C1): an event of a group above the
// shown host while the pane loads a group doesn't put a host in the pane.
func TestDetailsGroupEventWhileLoading(t *testing.T) {
	e := startWith(t, testserver.Options{})
	c, ctx := e.c, e.ctx
	lab := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)}))
	other := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "other", X: ptr(600.0), Y: ptr(100.0)}))
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &lab.ID, X: ptr(100.0), Y: ptr(200.0)}))
	for i := range 3 { // so a host has the group's id
		must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2." + strconv.Itoa(10+i), X: ptr(900.0), Y: ptr(float64(100 * i))}))
	}
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h.ID) != nil })
	k, gk := mapmodel.HostKey(h.ID), mapmodel.GroupKey(other.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "host shown", func() bool { return g.details.host != nil && g.details.host.ID == h.ID })
	b, _ := json.Marshal(lab)
	e.ui.Wait(func() {
		g.details.show(1, &gk)
		g.details.onEvent(client.Event{Type: apitypes.EventGroupUpdated, Data: b})
	})
	eventually(t, e.ui, "group shown", func() bool { return g.details.group != nil && g.details.group.ID == other.ID })
	time.Sleep(300 * time.Millisecond) // a wrong reload would have landed by now
	e.ui.Wait(func() {
		if g.details.host != nil || g.details.group == nil {
			t.Errorf("key %v, host %v, group %v", *g.details.key, g.details.host != nil, g.details.group != nil)
		}
	})
}

// TC-760 (SPEC-737 v0.69, Design-snmp-views.md): a custom view's View menu
// has "SNMP communities…", the network view's not; the dialog sets the
// view's list; a host placed there says "from view switches" and follows
// the view's list without a host event; "Inherit" clears it.
func TestViewCommunitiesGUI(t *testing.T) {
	e := startWith(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"public"}, Client: snmp.Static{Community: "public", Version: snmp.V2c}}})
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", X: ptr(100.0), Y: ptr(100.0)}))
	v := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "switches"}))
	must(c.PlaceHost(ctx, v.ID, h.ID, apitypes.PlacementSet{}))
	e.open()
	g := e.g
	eventually(t, e.ui, "tabs", func() bool { return e.tabNames() == "Networks,switches" })
	e.ui.Wait(func() {
		if menuItem(g.viewMenuItems(), "SNMP communities…") != nil {
			t.Error("the network view has SNMP communities…")
		}
	})
	k := mapmodel.HostKey(h.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "server's list", func() bool { return strings.Contains(formText(g.details), "SNMP communities\nthe server's list") })
	e.ui.Wait(func() { g.selectView(v.ID) })
	eventually(t, e.ui, "view selected", func() bool { return g.currentView() == v.ID })
	e.ui.Wait(func() { g.details.show(v.ID, &k) }) // a new tab shows its own selection
	eventually(t, e.ui, "host in the view", func() bool { return strings.Contains(formText(g.details), "SNMP communities\nthe server's list") })
	e.ui.Wait(func() {
		it := menuItem(g.viewMenuItems(), "SNMP communities…")
		if it == nil {
			t.Fatal("no SNMP communities… in the custom view's menu")
		}
		it.Action()
	})
	eventually(t, e.ui, "dialog", func() bool { return g.commDialog != nil })
	e.ui.Wait(func() { g.commDialog.save([]string{"sw-ro", "sw-2"}) })
	eventually(t, e.ui, "saved", func() bool { return must(c.View(ctx, v.ID)).SNMPCommunitiesSet == 2 })
	eventually(t, e.ui, "host row", func() bool { return strings.Contains(formText(g.details), "SNMP communities\nfrom view switches") })
	eventually(t, e.ui, "status", func() bool { return strings.Contains(g.status.Text(), "SNMP communities of view switches: 2 set here") })
	if strings.Contains(formText(g.details)+g.status.Text(), "sw-ro") {
		t.Error("a string is shown")
	}
	e.ui.Wait(func() {
		g.commDialog = nil
		menuItem(g.viewMenuItems(), "SNMP communities…").Action()
	})
	eventually(t, e.ui, "dialog again", func() bool { return g.commDialog != nil && strings.Contains(g.commDialog.info.Text, "2 set here") })
	e.ui.Wait(func() { g.commDialog.save([]string{}) })
	eventually(t, e.ui, "inherit", func() bool { return strings.Contains(formText(g.details), "SNMP communities\nthe server's list") })
	// A view without a list says what its hosts use (crosscheck 2026-11-19-04
	// L3); the dialog closes when the view is deleted (L4).
	e.ui.Wait(func() {
		g.commDialog = nil
		menuItem(g.viewMenuItems(), "SNMP communities…").Action()
	})
	eventually(t, e.ui, "dialog for an empty view", func() bool {
		return g.commDialog != nil && strings.Contains(g.commDialog.info.Text, "none set here; its hosts use their group's or the server's list")
	})
	if err := c.DeleteView(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, e.ui, "dialog closed", func() bool {
		return g.commDialog == nil && strings.Contains(g.status.Text(), "SNMP communities: the view was deleted")
	})
}

// hookTransport holds the server's answers to GET holdPath while hold is
// set, until release is closed; seen gets a value per held answer and has
// room for 4. The tests hold one (crosscheck 2026-11-19-05 L3), and sends requests to
// /snmp/checks/ without the token while noAuth is set.
type hookTransport struct {
	holdPath   string
	holdMethod string // default GET
	hold       atomic.Bool
	noAuth     atomic.Bool
	seen       chan struct{}
	release    chan struct{}
}

func (h *hookTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.noAuth.Load() && strings.Contains(r.URL.Path, "/snmp/checks/") {
		r = r.Clone(r.Context())
		r.Header.Del("Authorization")
	}
	held := r.Method == cmp.Or(h.holdMethod, http.MethodGet) && r.URL.Path == h.holdPath && h.hold.Load()
	resp, err := http.DefaultTransport.RoundTrip(r)
	if held && err == nil {
		// The server has answered; the answer is read only later.
		body, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		err = rerr
		h.seen <- struct{}{}
		<-h.release
	}
	return resp, err
}

// hookedEnv starts a server with a token and a GUI whose client goes
// through tr.
func hookedEnv(t *testing.T, opts testserver.Options, tr *hookTransport) *env {
	opts.Token = "tok"
	e := startWith(t, opts)
	var err error
	if e.c, err = client.New(client.Options{URL: e.srv.URL(), Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	e.options = func(o *Options) {
		cl, err := client.New(client.Options{URL: e.srv.URL(), Token: "tok", Transport: tr})
		if err != nil {
			t.Fatal(err)
		}
		o.Client = cl
	}
	return e
}

// TC-759 (crosscheck 2026-11-19-04 C1): a placement while the pane loads
// the host isn't lost: when the (older) answer comes, the host is read
// again and its Views row is right.
func TestDetailsEventWhileLoadingHost(t *testing.T) {
	tr := &hookTransport{seen: make(chan struct{}, 4), release: make(chan struct{})}
	e := hookedEnv(t, testserver.Options{}, tr)
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", X: ptr(100.0), Y: ptr(100.0)}))
	v := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "switches"}))
	tr.holdPath = "/api/v1/hosts/" + strconv.FormatInt(h.ID, 10)
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h.ID) != nil })
	k := mapmodel.HostKey(h.ID)
	tr.hold.Store(true)
	e.ui.Wait(func() { g.details.show(1, &k) })
	<-tr.seen // the GET is held: its answer will be from before the placement
	tr.hold.Store(false)
	must(c.PlaceHost(ctx, v.ID, h.ID, apitypes.PlacementSet{}))
	eventually(t, e.ui, "placement event seen", func() bool { return g.details.missed })
	close(tr.release)
	eventually(t, e.ui, "views row", func() bool { return strings.Contains(formText(g.details), "Views\nNetworks, switches") })
}

// TC-759 (crosscheck 2026-11-19-04 C2): a 401 while refreshing a check
// (a changed token) keeps following it, with Cancel.
func TestSNMPCheckRefresh401KeepsJob(t *testing.T) {
	tr := &hookTransport{}
	e := hookedEnv(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"public"}, Client: snmp.Static{Community: "public", Version: snmp.V2c}}}, tr)
	e.open()
	g := e.g
	tr.noAuth.Store(true)
	e.ui.Wait(func() {
		g.registerSNMPCheck(apitypes.SNMPCheck{ID: "run-1", State: apitypes.DiscoveryRunning, Total: 2})
	})
	eventually(t, e.ui, "401 shown", func() bool {
		return strings.Contains(g.status.Text(), "SNMP check run-1: not read") && strings.Contains(g.status.Text(), "401")
	})
	e.ui.Wait(func() {
		if !g.snmpJobs["run-1"] || !g.status.cancelB.Visible() {
			t.Errorf("after a 401: followed %v, cancel %v", g.snmpJobs["run-1"], g.status.cancelB.Visible())
		}
	})
}

// TC-759 (crosscheck 2026-11-19-05 C1): while show()'s answer is held, a
// host.updated and a placement show newer data; the older answer of
// show() then doesn't replace it.
func TestDetailsOlderShowAnswerDropped(t *testing.T) {
	tr := &hookTransport{seen: make(chan struct{}, 4), release: make(chan struct{})}
	e := hookedEnv(t, testserver.Options{}, tr)
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", X: ptr(100.0), Y: ptr(100.0)}))
	v := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "switches"}))
	tr.holdPath = "/api/v1/hosts/" + strconv.FormatInt(h.ID, 10)
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h.ID) != nil })
	k := mapmodel.HostKey(h.ID)
	tr.hold.Store(true)
	e.ui.Wait(func() { g.details.show(1, &k) })
	<-tr.seen // show()'s answer is held: it has no description and one view
	tr.hold.Store(false)
	must(c.UpdateHost(ctx, h.ID, apitypes.HostPatch{Description: apitypes.Some("core switch")}))
	eventually(t, e.ui, "description from the event", func() bool { return strings.Contains(formText(g.details), "core switch") })
	must(c.PlaceHost(ctx, v.ID, h.ID, apitypes.PlacementSet{}))
	eventually(t, e.ui, "views from the reload", func() bool { return strings.Contains(formText(g.details), "Views\nNetworks, switches") })
	var before int
	e.ui.Wait(func() {
		before = g.details.answers
		g.details.note.SetText("typed after the event") // kept (crosscheck 2026-11-19-06 L1)
	})
	close(tr.release)
	eventually(t, e.ui, "held answer handled", func() bool { return g.details.answers > before }) // no fixed wait (L2)
	e.ui.Wait(func() {
		if g.details.note.Text != "typed after the event" {
			t.Errorf("the note typed while loading is gone: %q", g.details.note.Text)
		}
		if txt := formText(g.details); !strings.Contains(txt, "core switch") || !strings.Contains(txt, "Views\nNetworks, switches") {
			t.Errorf("an older answer replaced newer data:\n%s", txt)
		}
	})
}

// TC-760 (crosscheck 2026-11-19-05 L1, L5): a second dialog closes the
// first; a group's dialog closes when its view is deleted, which sends no
// group.deleted.
func TestCommunitiesDialogOneAndGroupOfDeletedView(t *testing.T) {
	e := startWith(t, testserver.Options{})
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", X: ptr(100.0), Y: ptr(100.0)}))
	v := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "lab"}))
	cg := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "rack", ViewID: &v.ID}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h.ID) != nil })
	e.ui.Wait(func() { g.selectView(v.ID) }) // load the custom view's map
	eventually(t, e.ui, "view loaded", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		_, ok := g.model.GroupView(cg.ID)
		return ok
	})
	var first *communitiesDialog
	e.ui.Wait(func() {
		g.communitiesDialog("host", h.ID, "h", 0, "server")
		first = g.commDialog
		g.communitiesDialog("group", cg.ID, "rack", 0, "")
		if !first.closed || g.commDialog == first {
			t.Error("the first dialog stays open")
		}
	})
	if err := c.DeleteView(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, e.ui, "group dialog closed", func() bool {
		return g.commDialog == nil && strings.Contains(g.status.Text(), "SNMP communities: the group was deleted")
	})
}

// TC-759 (crosscheck 2026-11-19-07 C1): while a newly selected host loads,
// the note field is empty and disabled and Save does nothing, so the
// previous host's note can't be saved onto it.
func TestNoteNotSavedOntoNextItem(t *testing.T) {
	tr := &hookTransport{seen: make(chan struct{}, 4), release: make(chan struct{})}
	e := hookedEnv(t, testserver.Options{}, tr)
	c, ctx := e.c, e.ctx
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Note: "note of one", X: ptr(100.0), Y: ptr(100.0)}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Note: "note of two", X: ptr(300.0), Y: ptr(100.0)}))
	tr.holdPath = "/api/v1/hosts/" + strconv.FormatInt(h2.ID, 10)
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h2.ID) != nil })
	k1, k2 := mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)
	e.ui.Wait(func() { g.details.show(1, &k1) })
	eventually(t, e.ui, "one shown", func() bool { return g.details.note.Text == "note of one" })
	tr.hold.Store(true)
	e.ui.Wait(func() { g.details.show(1, &k2) })
	<-tr.seen
	tr.hold.Store(false)
	var before int
	e.ui.Wait(func() {
		if g.details.note.Text != "" || !g.details.note.Disabled() || !g.details.save.Disabled() {
			t.Errorf("while loading: note %q disabled %v, save disabled %v", g.details.note.Text, g.details.note.Disabled(), g.details.save.Disabled())
		}
		g.details.saveNote() // as a click that came through anyway
		before = g.details.answers
	})
	close(tr.release)
	eventually(t, e.ui, "answer", func() bool { return g.details.answers > before })
	e.ui.Wait(func() {
		if g.details.note.Text != "note of two" || g.details.save.Disabled() {
			t.Errorf("after loading: note %q, save disabled %v", g.details.note.Text, g.details.save.Disabled())
		}
	})
	if got := must(c.Host(ctx, h2.ID)).Note; got != "note of two" {
		t.Errorf("host two's note is %q", got)
	}
}

// TC-759 (crosscheck 2026-11-19-07 L1): a group.updated while the group's
// answer is held is not replaced by the older answer.
func TestDetailsOlderGroupAnswerDropped(t *testing.T) {
	tr := &hookTransport{seen: make(chan struct{}, 4), release: make(chan struct{})}
	e := hookedEnv(t, testserver.Options{}, tr)
	c, ctx := e.c, e.ctx
	gr := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab", X: ptr(100.0), Y: ptr(100.0)}))
	tr.holdPath = "/api/v1/groups/" + strconv.FormatInt(gr.ID, 10)
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "group", gr.ID) != nil })
	k := mapmodel.GroupKey(gr.ID)
	tr.hold.Store(true)
	e.ui.Wait(func() { g.details.show(1, &k) })
	<-tr.seen
	tr.hold.Store(false)
	must(c.UpdateGroup(ctx, gr.ID, apitypes.GroupPatch{Description: apitypes.Some("second floor")}))
	eventually(t, e.ui, "description from the event", func() bool { return strings.Contains(formText(g.details), "second floor") })
	var before int
	e.ui.Wait(func() {
		before = g.details.answers
		g.details.note.SetText("typed") // kept
	})
	close(tr.release)
	eventually(t, e.ui, "answer", func() bool { return g.details.answers > before })
	e.ui.Wait(func() {
		if !strings.Contains(formText(g.details), "second floor") || g.details.note.Text != "typed" {
			t.Errorf("the older answer replaced newer data: note %q\n%s", g.details.note.Text, formText(g.details))
		}
	})
}

// TC-759 (crosscheck 2026-11-19-08 C1, D3a, L1): selecting the same item
// again (a tab switch) keeps unsaved typing; leaving it for another item
// says "Note of host N not saved"; a failed load says so in the field.
func TestNoteKeptOnReshowAndWarned(t *testing.T) {
	e := startWith(t, testserver.Options{})
	c, ctx := e.c, e.ctx
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Note: "server note", X: ptr(100.0), Y: ptr(100.0)}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", X: ptr(300.0), Y: ptr(100.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h2.ID) != nil })
	k1, k2 := mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)
	e.ui.Wait(func() { g.details.show(1, &k1) })
	eventually(t, e.ui, "shown", func() bool { return g.details.note.Text == "server note" })
	var before int
	e.ui.Wait(func() {
		g.details.note.SetText("unsaved typing")
		before = g.details.answers
		g.details.show(1, &k1) // as a tab switch does
		if g.details.note.Text != "unsaved typing" || g.details.note.Disabled() {
			t.Errorf("at once: %q disabled %v", g.details.note.Text, g.details.note.Disabled())
		}
	})
	eventually(t, e.ui, "answer", func() bool { return g.details.answers > before })
	e.ui.Wait(func() {
		if g.details.note.Text != "unsaved typing" {
			t.Errorf("after the answer: %q", g.details.note.Text)
		}
		g.details.show(1, &k2)
	})
	eventually(t, e.ui, "warned", func() bool {
		return strings.Contains(g.status.Text(), fmt.Sprintf("Note of host %d not saved", h1.ID))
	})
	k9 := mapmodel.HostKey(999)
	e.ui.Wait(func() { g.details.show(1, &k9) })
	eventually(t, e.ui, "not loaded", func() bool {
		return strings.HasPrefix(g.details.note.PlaceHolder, "Note not loaded") && g.details.note.Disabled()
	})
}

// TC-759 (crosscheck 2026-11-19-08 C2): a save still on its way when
// another item is selected doesn't mark that item's note as edited: an
// event still updates it.
func TestNoteSaveLandsAfterSwitch(t *testing.T) {
	tr := &hookTransport{holdMethod: http.MethodPatch, seen: make(chan struct{}, 4), release: make(chan struct{})}
	e := hookedEnv(t, testserver.Options{}, tr)
	c, ctx := e.c, e.ctx
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Note: "one", X: ptr(100.0), Y: ptr(100.0)}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Note: "two", X: ptr(300.0), Y: ptr(100.0)}))
	tr.holdPath = "/api/v1/hosts/" + strconv.FormatInt(h1.ID, 10)
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h2.ID) != nil })
	k1, k2 := mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)
	e.ui.Wait(func() { g.details.show(1, &k1) })
	eventually(t, e.ui, "one shown", func() bool { return g.details.note.Text == "one" })
	tr.hold.Store(true)
	e.ui.Wait(func() {
		g.details.note.SetText("edit")
		g.details.saveNote()
	})
	<-tr.seen // the save's answer is held
	tr.hold.Store(false)
	e.ui.Wait(func() { g.details.show(1, &k2) })
	eventually(t, e.ui, "two shown", func() bool { return g.details.note.Text == "two" && !g.details.note.Disabled() })
	close(tr.release)
	eventually(t, e.ui, "saved", func() bool { return strings.Contains(g.status.Text(), fmt.Sprintf("note of host %d saved", h1.ID)) })
	e.ui.Wait(func() {
		if g.details.note.Text != "two" || g.details.lastNote != "two" {
			t.Errorf("host two after host one's save: field %q, loaded %q", g.details.note.Text, g.details.lastNote)
		}
	})
	must(c.UpdateHost(ctx, h2.ID, apitypes.HostPatch{Note: apitypes.Some("two, changed elsewhere")}))
	eventually(t, e.ui, "event updates the note", func() bool { return g.details.note.Text == "two, changed elsewhere" })
	if got := must(c.Host(ctx, h1.ID)).Note; got != "edit" {
		t.Errorf("host one's note is %q", got)
	}
}

// TC-759 (crosscheck 2026-11-19-09 C1, L1, L2): a save's late answer
// doesn't bring its text back over a newer note from another client; a
// switch while the save is on its way gives no "not saved"; a note set
// after "Note not loaded" gets the placeholder "Note" back.
func TestNoteOlderSaveAnswer(t *testing.T) {
	tr := &hookTransport{holdMethod: http.MethodPatch, seen: make(chan struct{}, 4), release: make(chan struct{})}
	e := hookedEnv(t, testserver.Options{}, tr)
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Note: "A", X: ptr(100.0), Y: ptr(100.0)}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", X: ptr(300.0), Y: ptr(100.0)}))
	tr.holdPath = "/api/v1/hosts/" + strconv.FormatInt(h.ID, 10)
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h2.ID) != nil })
	k, k2 := mapmodel.HostKey(h.ID), mapmodel.HostKey(h2.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "shown", func() bool { return g.details.note.Text == "A" })
	tr.hold.Store(true)
	e.ui.Wait(func() { g.details.note.SetText("X"); g.details.saveNote() })
	<-tr.seen // the save's answer is held; the server has X
	tr.hold.Store(false)
	must(c.UpdateHost(ctx, h.ID, apitypes.HostPatch{Note: apitypes.Some("B")})) // another client
	eventually(t, e.ui, "B shown", func() bool { return g.details.note.Text == "B" })
	close(tr.release)
	eventually(t, e.ui, "saved", func() bool { return strings.Contains(g.status.Text(), fmt.Sprintf("note of host %d saved", h.ID)) })
	e.ui.Wait(func() {
		if g.details.note.Text != "B" || g.details.lastNote != "B" {
			t.Errorf("an older save's answer came back: field %q, loaded %q", g.details.note.Text, g.details.lastNote)
		}
	})

	// L1: save, then another host at once: no "not saved".
	tr2 := make(chan struct{})
	tr.release, tr.seen = tr2, make(chan struct{}, 4)
	tr.hold.Store(true)
	e.ui.Wait(func() { g.details.note.SetText("Y"); g.details.saveNote() })
	<-tr.seen
	tr.hold.Store(false)
	e.ui.Wait(func() {
		g.details.show(1, &k2)
		if strings.Contains(g.status.Text(), "not saved") {
			t.Errorf("warned while saving: %q", g.status.Text())
		}
	})
	close(tr2)
	eventually(t, e.ui, "Y saved", func() bool { return must(c.Host(ctx, h.ID)).Note == "Y" })

	// L2: the placeholder comes back with a note.
	e.ui.Wait(func() {
		g.details.note.SetPlaceHolder("Note not loaded; select the item again")
		g.details.setNote("n", true)
		if g.details.note.PlaceHolder != "Note" {
			t.Errorf("placeholder %q", g.details.note.PlaceHolder)
		}
	})
}

// TC-759 (crosscheck 2026-11-19-10 L1): a note changed and changed back to
// the old text while a save is on its way counts as changed: the save's
// late answer doesn't bring its text back.
func TestNoteSaveAnswerAfterChangeBack(t *testing.T) {
	tr := &hookTransport{holdMethod: http.MethodPatch, seen: make(chan struct{}, 4), release: make(chan struct{})}
	e := hookedEnv(t, testserver.Options{}, tr)
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Note: "A", X: ptr(100.0), Y: ptr(100.0)}))
	tr.holdPath = "/api/v1/hosts/" + strconv.FormatInt(h.ID, 10)
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "host", h.ID) != nil })
	k := mapmodel.HostKey(h.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "shown", func() bool { return g.details.note.Text == "A" })
	tr.hold.Store(true)
	e.ui.Wait(func() { g.details.note.SetText("X"); g.details.saveNote() })
	<-tr.seen // the server has X; the answer is held
	tr.hold.Store(false)
	eventually(t, e.ui, "own event", func() bool { return g.details.lastNote == "X" })
	must(c.UpdateHost(ctx, h.ID, apitypes.HostPatch{Note: apitypes.Some("A")})) // back to the old text
	eventually(t, e.ui, "A again", func() bool { return g.details.note.Text == "A" && g.details.lastNote == "A" })
	close(tr.release)
	eventually(t, e.ui, "saved", func() bool { return strings.Contains(g.status.Text(), fmt.Sprintf("note of host %d saved", h.ID)) })
	e.ui.Wait(func() {
		if g.details.note.Text != "A" || g.details.lastNote != "A" {
			t.Errorf("the save's late answer came back: field %q, loaded %q", g.details.note.Text, g.details.lastNote)
		}
	})
}
