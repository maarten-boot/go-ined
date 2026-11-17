package gui

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/services"
	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

func watchNames(h apitypes.Host) []string {
	var out []string
	for _, w := range h.Watch {
		out = append(out, w.Name)
	}
	return out
}

// TC-749: "Watch services…" on one host shows a tick per known service,
// marked where it answered at the last check, and a field for other TCP
// ports; saving sets the list, a mistake stays in the dialog. The details
// panel has a Watched row, and a service going down puts the service
// marker on the map and "down since" in the row (SPEC-732).
func TestWatchServicesGUI(t *testing.T) {
	e := startWith(t, testserver.Options{Services: services.Config{
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			if addr == "192.0.2.1:22" {
				c, _ := net.Pipe()
				return c, nil
			}
			return nil, errors.New("connection refused")
		},
	}})
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web"}))
	must(c.CheckServices(ctx, h.ID))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 1 })

	open := func() *watchForm {
		t.Helper()
		e.ui.Wait(func() {
			g.lastWatch = nil
			host := must(c.Host(ctx, h.ID))
			item := menuItem(g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, &host), "Watch services…")
			if item == nil {
				t.Fatal("no Watch services… in the host menu")
			}
			item.Action()
		})
		eventually(t, e.ui, "watch dialog", func() bool { return g.lastWatch != nil })
		var f *watchForm
		e.ui.Wait(func() { f = g.lastWatch })
		return f
	}

	f := open()
	e.ui.Wait(func() {
		if l := f.checks["ssh"].Text; l != "ssh (22) · answered at the last check" {
			t.Errorf("ssh tick: %q", l)
		}
		if l := f.checks["vnc:1"].Text; l != "vnc :1 (5901)" {
			t.Errorf("vnc:1 tick: %q", l)
		}
		for name, ch := range f.checks {
			if ch.Checked {
				t.Errorf("%s ticked before anything is watched", name)
			}
		}
		f.checks["https"].SetChecked(true)
		f.checks["ssh"].SetChecked(true)
		f.other.SetText("5432, tcp:8080")
		f.Submit()
	})
	eventually(t, e.ui, "list saved", func() bool {
		return slices.Equal(watchNames(must(c.Host(ctx, h.ID))), []string{"ssh", "https", "tcp:5432", "tcp:8080"})
	})

	// Shown again: the ticks and ports as saved. A port given twice and a
	// word are refused in the dialog.
	f = open()
	e.ui.Wait(func() {
		if !f.checks["ssh"].Checked || !f.checks["https"].Checked || f.checks["http"].Checked || f.other.Text != "5432, 8080" {
			t.Errorf("shown again: ssh %v https %v other %q", f.checks["ssh"].Checked, f.checks["https"].Checked, f.other.Text)
		}
		f.other.SetText("22")
		f.Submit()
	})
	eventually(t, e.ui, "error in the dialog", func() bool { return strings.Contains(f.errL.Text, `"ssh" and "tcp:22" are the same port 22`) })
	e.ui.Wait(func() { f.other.SetText("5432 pg"); f.Submit() })
	eventually(t, e.ui, "word refused", func() bool { return strings.Contains(f.errL.Text, `"pg" is not a port number`) })
	e.ui.Wait(func() { // blanks after "tcp:" are fine (L6 of crosscheck 2026-11-17-01)
		f.other.SetText("tcp: 5432, TCP:  8080")
		if l, err := f.list(); err != nil || strings.Join(l, ",") != "ssh,https,tcp:5432,tcp:8080" {
			t.Errorf("tcp: with blanks: %q %v", l, err)
		}
	})
	if f.done {
		t.Fatal("the dialog closed after an error")
	}

	// The details row, then https goes down: the row and the map follow.
	k := mapmodel.HostKey(h.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "Watched row", func() bool {
		return strings.Contains(formText(g.details), "ssh unknown, https unknown, tcp:5432 unknown, tcp:8080 unknown")
	})
	at := time.Date(2026, 11, 16, 14, 7, 0, 0, time.Local)
	if err := e.srv.Svc.ApplyWatch(ctx, []inventory.WatchResult{
		{HostID: h.ID, IP: netip.MustParseAddr("192.0.2.1"), Name: "https", Port: 443, Status: apitypes.StatusDown, At: at},
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, e.ui, "down in the row and on the map", func() bool {
		n := find(e.nodes(1), "host", h.ID)
		return strings.Contains(formText(g.details), "https down since "+shortTime(at)) && n != nil && n.Item.ServicesDown == 1
	})
}

// TC-749: with several hosts selected the menu says "Watch services on N
// hosts…"; ticks only where all watch a service, a note when they differ,
// and saving sets the same list on all of them.
func TestWatchServicesSelection(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	h1 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "a"}))
	h2 := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2", Name: "b"}))
	must(c.UpdateHost(ctx, h1.ID, apitypes.HostPatch{Watch: apitypes.Some([]string{"ssh", "https"})}))
	must(c.UpdateHost(ctx, h2.ID, apitypes.HostPatch{Watch: apitypes.Some([]string{"ssh", "tcp:5432"})}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) >= 2 })
	e.ui.Wait(func() {
		g.maps[1].SetSelection([]mapmodel.Key{mapmodel.HostKey(h1.ID), mapmodel.HostKey(h2.ID)})
		item := menuItem(g.menuItems(1, find(e.nodes(1), "host", h1.ID), 0, 0, nil), "Watch services on 2 hosts…")
		if item == nil {
			t.Fatal("no Watch services on 2 hosts…")
		}
		item.Action()
	})
	eventually(t, e.ui, "watch dialog", func() bool { return g.lastWatch != nil })
	e.ui.Wait(func() {
		f := g.lastWatch
		if !f.checks["ssh"].Checked || f.checks["https"].Checked || f.other.Text != "" {
			t.Errorf("ticks: ssh %v https %v other %q", f.checks["ssh"].Checked, f.checks["https"].Checked, f.other.Text)
		}
		if !strings.Contains(f.note, "Saving sets this list on all 2 hosts. They watch different services now") {
			t.Errorf("note: %q", f.note)
		}
		f.checks["http"].SetChecked(true)
		f.Submit()
	})
	eventually(t, e.ui, "both saved", func() bool {
		return slices.Equal(watchNames(must(c.Host(ctx, h1.ID))), []string{"ssh", "http"}) &&
			slices.Equal(watchNames(must(c.Host(ctx, h2.ID))), []string{"ssh", "http"})
	})
}
