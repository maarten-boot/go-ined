package gui

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/snmp"
	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-757: a host's details have "SNMP: not checked yet"; "Check SNMP now"
// in the host menu finds the agent and fills SNMP, Location, CPU, Memory,
// Disks and Uptime, with the status bar; an snmp.values event updates
// them; with SNMP off the server's reason is in the status bar.
func TestSNMPDetails(t *testing.T) {
	cpu, busiest := 6, 100
	e := startWith(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"public"}, Client: snmp.Static{
		Community: "public", Version: snmp.V2c,
		Sys: inventory.SNMPSystem{Name: "web", Descr: "Linux web 6.8.0", Location: "rack 4"},
		Vals: apitypes.SNMPValues{UptimeS: 1053224, CPUPct: &cpu, CPUMaxPct: &busiest, CPUs: 16, Storage: []apitypes.SNMPStorage{
			{Name: "Physical memory", Kind: apitypes.StorageMemory, SizeBytes: 1 << 33, UsedPct: 63},
			{Name: "/", Kind: apitypes.StorageDisk, SizeBytes: 1 << 35, UsedPct: 91},
			{Name: "/data", Kind: apitypes.StorageDisk, SizeBytes: 1 << 36, UsedPct: 40}}},
	}}})
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 1 })
	k := mapmodel.HostKey(h.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "not checked", func() bool { return strings.Contains(formText(g.details), "SNMP\nnot checked yet") })
	e.ui.Wait(func() {
		host := must(c.Host(ctx, h.ID))
		it := menuItem(g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, &host), "Check SNMP now")
		if it == nil {
			t.Fatal("no Check SNMP now")
		}
		it.Action()
	})
	eventually(t, e.ui, "details after the check", func() bool {
		f := formText(g.details)
		return strings.Contains(g.status.Text(), "SNMP on web: v2c · web · Linux web 6.8.0") &&
			strings.Contains(f, "SNMP\nv2c · web · Linux web 6.8.0") && strings.Contains(f, "Location\nrack 4") &&
			strings.Contains(f, "CPU\n6 % (16 CPUs, busiest 100 %)") && strings.Contains(f, "Memory\n63 %") &&
			strings.Contains(f, "Disks\n/ 91 %, /data 40 %") && strings.Contains(f, "Uptime\n12 d 4 h · read ")
	})
	// New values from the server: shown.
	cpu2 := 88
	e.srv.Svc.SetSNMPValues(h.ID, netip.MustParseAddr("192.0.2.1"), inventory.SNMPAgent{Version: snmp.V2c, Community: "public"},
		apitypes.SNMPValues{At: time.Now(), UptimeS: 60, CPUPct: &cpu2}, false)
	eventually(t, e.ui, "updated values", func() bool {
		f := formText(g.details)
		return strings.Contains(f, "CPU\n88 %") && !strings.Contains(f, "Disks") && strings.Contains(f, "Uptime\n1 min")
	})
}

// TC-757: with SNMP off on the server, "Check SNMP now" reports why.
func TestSNMPOff(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 1 })
	e.ui.Wait(func() {
		host := must(c.Host(ctx, h.ID))
		menuItem(g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, &host), "Check SNMP now").Action()
	})
	eventually(t, e.ui, "reason", func() bool { return strings.Contains(g.status.Text(), "snmp.enabled = false") })
}

// TC-757 (crosscheck 2026-11-18-03 C1, L7): an agent text with "\r\n"
// (Cisco) goes to the status bar as one line without a crash; with two
// hosts selected there is no "Check SNMP now".
func TestSNMPOddTextAndSelection(t *testing.T) {
	e := startWith(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"public"}, Client: snmp.Static{
		Community: "public", Version: snmp.V2c, Sys: inventory.SNMPSystem{Name: "core-1", Descr: "Cisco IOS\r\nTAC support"},
	}}})
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "core"}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Name: "edge"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 2 })
	e.ui.Wait(func() {
		host := must(c.Host(ctx, h.ID))
		menuItem(g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, &host), "Check SNMP now").Action()
	})
	// Static doesn't clean its text as the UDP client does: the status bar
	// gets the "\r\n" and makes one line of it.
	eventually(t, e.ui, "status", func() bool {
		return strings.Contains(g.status.Text(), "SNMP on core: v2c · core-1 · Cisco IOS TAC support |")
	})
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.HostKey(h.ID), mapmodel.HostKey(h2.ID)})
		host := must(c.Host(ctx, h.ID))
		if menuItem(g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, &host), "Check SNMP now") != nil {
			t.Error("Check SNMP now on two selected hosts")
		}
	})
}
