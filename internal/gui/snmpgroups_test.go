package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
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
}
