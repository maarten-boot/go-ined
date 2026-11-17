package gui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

type closeBuf struct {
	bytes.Buffer
	closed bool
}

func (b *closeBuf) Close() error { b.closed = true; return nil }

// TC-751: "Export…" and "Import into this group…" on groups of the network
// view (not in custom views) and "Import here…" on empty space; with a
// focus set, a group's menu has both, each once; export writes the
// file and says so; import shows the dry run's report first and imports
// on "Import"; with conflicts the button is "Update existing and import";
// a file that isn't JSON, or that the server refuses, is reported
// (SPEC-733).
func TestExportImportGUI(t *testing.T) {
	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.1.0/24", Name: "lan"}))
	must(c.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.1.10", Name: "web"}))
	v := must(c.CreateView(ctx, apitypes.ViewCreate{Name: "Lab"}))
	lg := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab group", ViewID: &v.ID}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "group", lan.ID) != nil })

	e.ui.Wait(func() {
		items := labels(g.menuItems(1, find(e.nodes(1), "group", lan.ID), 0, 0, nil))
		if !strings.Contains(strings.Join(items, "|"), "|Export…|Import into this group…|") {
			t.Errorf("network group menu: %q", items)
		}
	})
	must(c.UpdateGroup(ctx, lan.ID, apitypes.GroupPatch{Collapsed: apitypes.Some(false)}))
	sub := must(c.CreateGroup(ctx, apitypes.GroupCreate{Name: "sub", ParentID: &lan.ID}))
	eventually(t, e.ui, "sub loaded", func() bool { return find(e.nodes(1), "group", sub.ID) != nil })
	e.ui.Wait(func() {
		g.setFocus(1, lan.ID)
		items := labels(g.menuItems(1, find(e.nodes(1), "group", sub.ID), 0, 0, nil))
		seen := map[string]int{}
		for _, l := range items {
			seen[l]++
		}
		if seen["Import into this group…"] != 1 || seen["Import here…"] != 1 {
			t.Errorf("group menu with a focus: %q", items)
		}
		g.setFocus(1, 0)
	})
	if err := c.DeleteGroup(ctx, sub.ID, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, e.ui, "sub gone", func() bool { return find(e.nodes(1), "group", sub.ID) == nil })
	eventually(t, e.ui, "custom view shown", func() bool { return g.showView(v.ID) })
	eventually(t, e.ui, "custom view loaded", func() bool { return find(e.nodes(v.ID), "group", lg.ID) != nil })
	e.ui.Wait(func() {
		for _, l := range labels(g.menuItems(v.ID, find(e.nodes(v.ID), "group", lg.ID), 0, 0, nil)) {
			if l == "Export…" || strings.HasPrefix(l, "Import") {
				t.Errorf("custom view group menu has %q", l)
			}
		}
	})

	buf := &closeBuf{}
	e.ui.Wait(func() { g.exportTo(lan.ID, buf, "lan.json") })
	eventually(t, e.ui, "exported", func() bool {
		return strings.Contains(g.status.Text(), "Exported Networks › lan to lan.json: 1 group, 1 host")
	})
	var f apitypes.NetworkExport
	if err := json.Unmarshal(buf.Bytes(), &f); err != nil || !buf.closed || f.Group.CIDR != "10.0.1.0/24" {
		t.Fatalf("file: %v closed %v %+v", err, buf.closed, f.Group)
	}

	// A copy for another range: a new import.
	other := []byte(strings.ReplaceAll(buf.String(), "10.0.1.", "10.0.2."))
	x, y := 900.0, 50.0
	e.ui.Wait(func() { g.lastImport = nil; g.importFrom(other, "lan2.json", 0, &x, &y) })
	eventually(t, e.ui, "report", func() bool { return g.lastImport != nil })
	e.ui.Wait(func() {
		im := g.lastImport
		if im.text != "Would create: 1 group, 1 host.\nConflicts: none." || im.confirm.Text != "Import" {
			t.Errorf("report %q, button %q", im.text, im.confirm.Text)
		}
		im.confirm.OnTapped()
	})
	eventually(t, e.ui, "imported", func() bool {
		return g.lastImport.done && strings.Contains(g.status.Text(), "Imported lan2.json: Created: 1 group, 1 host.")
	})
	if hs := must(c.Hosts(ctx)); len(hs) != 2 {
		t.Fatalf("%d hosts", len(hs))
	}

	// The same file again: conflicts, then update.
	e.ui.Wait(func() { g.lastImport = nil; g.importFrom(buf.Bytes(), "lan.json", lan.ID, nil, nil) })
	eventually(t, e.ui, "conflict report", func() bool { return g.lastImport != nil })
	e.ui.Wait(func() {
		im := g.lastImport
		if !strings.Contains(im.text, "Conflicts (2):") || im.confirm.Text != "Update existing and import" {
			t.Errorf("report %q, button %q", im.text, im.confirm.Text)
		}
		im.confirm.OnTapped()
	})
	eventually(t, e.ui, "updated", func() bool {
		return g.lastImport.done && strings.Contains(g.status.Text(), "Updated: 1 group, 1 host.")
	})

	e.ui.Wait(func() { g.importFrom([]byte("nope"), "x.json", 0, nil, nil) })
	eventually(t, e.ui, "not JSON", func() bool { return strings.Contains(g.status.Text(), "Import: x.json is not a JSON file") })
	bad := []byte(strings.Replace(buf.String(), `"version": 1`, `"version": 7`, 1))
	e.ui.Wait(func() { g.importFrom(bad, "v7.json", 0, nil, nil) })
	eventually(t, e.ui, "refused file", func() bool {
		return strings.Contains(g.status.Text(), "Import of v7.json: invalid_argument (HTTP 400): export file version 7")
	})
}
