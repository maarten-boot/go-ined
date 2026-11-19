package gui

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/snmp"
	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
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
