package gui

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-756: an IPv4 network group's menu has "Rescan ▸" with Off and four
// intervals; a choice is saved and checked; an interval set elsewhere is
// listed and checked; two groups selected get it both; plain and IPv6
// groups have no item; the details show the setting and the last scan.
func TestRescanMenu(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.1.0/24", X: ptr(100.0), Y: ptr(100.0)}))
	dmz := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.2.0/24", X: ptr(400.0), Y: ptr(100.0)}))
	plain := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "plain", X: ptr(700.0), Y: ptr(100.0)}))
	v6 := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "2001:db8::/64", X: ptr(1000.0), Y: ptr(100.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "group", v6.ID) != nil })
	item := func(id int64, sel []mapmodel.Key) *fyne.MenuItem {
		g.maps[1].SetSelection(sel)
		n := find(e.nodes(1), "group", id)
		for _, it := range g.menuItems(1, n, n.Icon.X+2, n.Icon.Y+2, nil) {
			if strings.HasPrefix(it.Label, "Rescan") {
				return it
			}
		}
		return nil
	}
	e.ui.Wait(func() {
		if item(plain.ID, nil) != nil || item(v6.ID, nil) != nil {
			t.Error("Rescan on a plain or IPv6 group")
		}
		m := item(lan.ID, nil).ChildMenu
		if l := labels(m.Items); strings.Join(l, "|") != "Off||Every hour|Every 6 hours|Every day|Every week" || !m.Items[0].Checked {
			t.Errorf("menu: %q", l)
		}
		menuItem(m.Items, "Every day").Action()
	})
	eventually(t, e.ui, "saved", func() bool { return must(c.Group(ctx, lan.ID)).Rescan == "24h" })
	k := mapmodel.GroupKey(lan.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "details", func() bool { return strings.Contains(formText(g.details), "every day, not scanned yet") })
	at := time.Date(2026, 11, 18, 3, 15, 0, 0, time.Local)
	if err := e.srv.Svc.RescanDone(ctx, netip.MustParsePrefix("10.0.1.0/24"), at, []netip.Addr{netip.MustParseAddr("10.0.1.7"), netip.MustParseAddr("10.0.1.8")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, e.ui, "details after a scan", func() bool {
		return strings.Contains(formText(g.details), "every day, last "+shortTime(at)+", 2 new")
	})
	eventually(t, e.ui, "checked", func() bool { return menuItem(item(lan.ID, nil).ChildMenu.Items, "Every day").Checked })

	// Set elsewhere: listed and checked.
	must(c.UpdateGroup(ctx, lan.ID, apitypes.GroupPatch{Rescan: apitypes.Some("3h")}))
	eventually(t, e.ui, "3h listed", func() bool {
		it := menuItem(item(lan.ID, nil).ChildMenu.Items, "Every 3h")
		return it != nil && it.Checked
	})
	// Two selected: both.
	e.ui.Wait(func() {
		it := item(lan.ID, []mapmodel.Key{mapmodel.GroupKey(lan.ID), mapmodel.GroupKey(dmz.ID)})
		if it.Label != "Rescan (2 groups)" || menuItem(it.ChildMenu.Items, "Every 3h") != nil {
			t.Errorf("two groups: %q %q", it.Label, labels(it.ChildMenu.Items))
		}
		menuItem(it.ChildMenu.Items, "Every week").Action()
	})
	eventually(t, e.ui, "both", func() bool {
		return must(c.Group(ctx, lan.ID)).Rescan == "168h" && must(c.Group(ctx, dmz.ID)).Rescan == "168h"
	})
	e.ui.Wait(func() { g.maps[1].SetSelection(nil) })
	eventually(t, e.ui, "details: every week", func() bool { return strings.Contains(formText(g.details), "every week, last") })
}
