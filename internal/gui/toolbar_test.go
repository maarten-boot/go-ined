package gui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-737 (SPEC-717 v0.44, SPEC-726 v0.44): the toolbar has one "Zoom N % ▾"
// button whose menu has Zoom in, Zoom out, 100 % and Fit all with their
// shortcuts in the label (black, not Fyne's faint shortcut text); zoom in and out are disabled at the limits; the label follows
// the zoom; the "Show" list is in the toolbar, after the Zoom button.
func TestZoomMenu(t *testing.T) {
	e := start(t)
	must(e.c.CreateHost(e.ctx, apitypes.HostCreate{IP: "192.0.2.1", X: ptr(100.0), Y: ptr(100.0)}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 1 })
	e.ui.Wait(func() {
		items := g.zoomMenuItems()
		want := []string{"Zoom in (Ctrl++)", "Zoom out (Ctrl+−)", "100 % (Ctrl+0)", "Fit all (Ctrl+1)"}
		if got := labels(items); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
			t.Errorf("menu: %v", got)
		}
		menuItem(items, "100 % (Ctrl+0)").Action()
		if g.zoomBtn.Text != "Zoom 100 % ▾" {
			t.Errorf("label %q", g.zoomBtn.Text)
		}
		menuItem(g.zoomMenuItems(), "Zoom in (Ctrl++)").Action()
		if z := g.maps[1].Viewport().Zoom; z <= 1 || g.zoomBtn.Text != zoomButtonText(z) {
			t.Errorf("zoom in: %v, %q", z, g.zoomBtn.Text)
		}
		g.zoomTo(mapmodel.MaxZoom)
		if m := g.zoomMenuItems(); !menuItem(m, "Zoom in (Ctrl++)").Disabled || menuItem(m, "Zoom out (Ctrl+−)").Disabled {
			t.Error("at 400 %: zoom in not disabled")
		}
		g.zoomTo(mapmodel.MinZoom)
		if m := g.zoomMenuItems(); menuItem(m, "Zoom in (Ctrl++)").Disabled || !menuItem(m, "Zoom out (Ctrl+−)").Disabled {
			t.Error("at 10 %: zoom out not disabled")
		}
		menuItem(g.zoomMenuItems(), "Fit all (Ctrl+1)").Action()
		if g.zoomBtn.Text != zoomButtonText(g.maps[1].Viewport().Zoom) {
			t.Errorf("after fit: %q", g.zoomBtn.Text)
		}
		// The toolbar (under the hover layer): the Zoom button, then "Show:" and the list.
		bar := g.win.Content().(*fyne.Container).Objects[0].(*fyne.Container).Objects[1].(*fyne.Container).Objects[0].(*fyne.Container)
		zoom := bar.Objects[0].(*fyne.Container).Objects[0]
		show := bar.Objects[1].(*fyne.Container)
		if zoom != g.zoomBtn || show.Objects[0].(*widget.Label).Text != "Show:" ||
			show.Objects[1].(*fyne.Container).Objects[0] != g.showSel {
			t.Error("toolbar order: want the Zoom button, then the Show list")
		}
	})
}
