package gui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Explaining the service marker (Design-marker-explained.md): the line in
// the details (E1), the hover box on hosts (E2a) and the legend (E3).

// downLine says which watched services are down: "1 watched service down:
// https (443) since 14:07"; "" when none is.
func downLine(ws []apitypes.WatchedService) string {
	var parts []string
	for _, w := range ws {
		if w.Status != apitypes.StatusDown {
			continue
		}
		s := apitypes.ServiceLabel(w.Name)
		if !strings.HasPrefix(w.Name, "tcp:") {
			s += " (" + strconv.Itoa(w.Port) + ")"
		}
		if w.LastChange != nil {
			s += " since " + shortTime(*w.LastChange)
		}
		parts = append(parts, s)
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return "1 watched service down: " + parts[0]
	}
	return fmt.Sprintf("%d watched services down: %s", len(parts), strings.Join(parts, ", "))
}

// markerSVG is the service marker as the map draws it (serviceMarker): an
// outlined triangle with a bar, in colour c.
func markerSVG(c color.NRGBA) fyne.Resource {
	col := fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
	return fyne.NewStaticResource("marker-"+col[1:]+".svg", []byte(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 14 14" width="14" height="14">`+
			`<path d="M1.5 12.5 L12.5 12.5 L7 1.5 Z M7 5.4 L7 9.2" fill="none" stroke="%s" stroke-width="1.8" stroke-linejoin="round"/></svg>`, col)))
}

// markerImage is the marker in the palette's "down" colour, 14×14.
func (g *GUI) markerImage() *canvas.Image {
	im := canvas.NewImageFromResource(markerSVG(nrgba(g.palette.Status(apitypes.StatusDown))))
	im.FillMode = canvas.ImageFillContain
	im.SetMinSize(fyne.NewSquareSize(14))
	return im
}

// ---- E2a: the hover box ----

// hoverDelay is how long the pointer rests on a host before its box shows.
const hoverDelay = 500 * time.Millisecond

// hoverBox is the box shown for a host the pointer rests on.
type hoverBox struct {
	pop  *widget.PopUp
	key  mapmodel.Key
	text *widget.Label // name, address, status
	down *fyne.Container
	line *widget.Label
}

// showHover opens the box for host node n near the pointer (at, absolute
// in the window). It shows what the map knows at once, then asks the
// server for the host for "since" and the services' names.
func (g *GUI) showHover(view int64, n mapmodel.Node, at fyne.Position) {
	g.hideHover()
	it := n.Item
	b := &hoverBox{key: mapmodel.Key{Type: it.Type, ID: it.ID}, text: widget.NewLabel(""), line: widget.NewLabel("")}
	b.text.Wrapping, b.line.Wrapping = fyne.TextWrapOff, fyne.TextWrapOff
	b.text.SetText(hoverText(it.Name, it.IP, mapmodel.StatusWord(it.Status, it.Stale), it.Monitored, nil))
	b.down = container.NewBorder(nil, nil, container.NewCenter(g.markerImage()), nil, b.line)
	if it.ServicesDown > 0 {
		b.line.SetText(nCount(it.ServicesDown, "watched service") + " down")
	} else {
		b.down.Hide()
	}
	border := canvas.NewRectangle(color.Transparent)
	border.StrokeColor, border.StrokeWidth = nrgba(g.palette.Border), 1
	content := container.NewStack(border, container.NewPadded(container.NewVBox(b.text, b.down)))
	b.pop = widget.NewPopUp(content, g.win.Canvas())
	b.pop.ShowAtPosition(at.AddXY(16, 16))
	g.hover = b
	id := it.ID
	go func() {
		h, err := g.c.Host(g.ctx, id)
		if err != nil {
			return
		}
		g.do(func() {
			if g.hover != b {
				return // gone, or another host by now
			}
			b.text.SetText(hoverText(h.DisplayName, h.IP, mapmodel.StatusWord(h.Status, h.Stale), h.Monitored, h.LastChange))
			if l := downLine(h.Watch); l != "" {
				b.line.SetText(l)
				b.down.Show()
			} else {
				b.down.Hide()
			}
			b.pop.Resize(b.pop.Content.MinSize())
		})
	}()
}

// hoverText: "web\n192.0.2.10\ndown since 14:07".
func hoverText(name, ip, status string, monitored bool, since *time.Time) string {
	if !monitored {
		status = "not monitored"
	} else if status == apitypes.StatusDown && since != nil {
		status += " since " + shortTime(*since)
	}
	lines := []string{name}
	if ip != "" && ip != name {
		lines = append(lines, ip)
	}
	return strings.Join(append(lines, status), "\n")
}

func (g *GUI) hideHover() {
	if g.hover != nil {
		g.hover.pop.Hide()
		g.hover = nil
	}
}

// ---- E3: the legend ----

// legendItems are the example items the legend draws with the map's own
// drawing: every host sign, the service marker, and the kinds of group.
func legendItems() []apitypes.MapItem {
	lan, site, grp := int64(1), int64(2), int64(3)
	host := func(id int64, x, y float64, name, status string, f func(*apitypes.MapItem)) apitypes.MapItem {
		it := apitypes.MapItem{Type: apitypes.TypeHost, ID: id, X: x, Y: y, Name: name, Status: status, Monitored: true}
		if f != nil {
			f(&it)
		}
		return it
	}
	return []apitypes.MapItem{
		host(11, 0, 0, "up", apitypes.StatusUp, nil),
		host(12, 130, 0, "down", apitypes.StatusDown, nil),
		host(13, 260, 0, "unknown", apitypes.StatusUnknown, nil),
		host(14, 390, 0, "not monitored", apitypes.StatusUnknown, func(it *apitypes.MapItem) { it.Monitored = false }),
		host(15, 520, 0, "stale", apitypes.StatusUp, func(it *apitypes.MapItem) { it.Stale = true }),
		host(16, 650, 0, "service down", apitypes.StatusUp, func(it *apitypes.MapItem) { it.ServicesDown = 1 }),
		{Type: apitypes.TypeGroup, ID: grp, X: 0, Y: 170, Name: "a group, collapsed", Status: apitypes.StatusUp, Collapsed: true},
		{Type: apitypes.TypeGroup, ID: site, X: 220, Y: 170, Name: "a group", Status: apitypes.StatusUp},
		host(17, 240, 270, "member", apitypes.StatusUp, func(it *apitypes.MapItem) { it.ParentID = &site }),
		{Type: apitypes.TypeGroup, ID: lan, X: 480, Y: 170, Name: "10.0.1.0/24", CIDR: "10.0.1.0/24", Status: apitypes.StatusUp},
		host(18, 500, 270, "10.0.1.5", apitypes.StatusUp, func(it *apitypes.MapItem) { it.ParentID = &lan; it.IP = "10.0.1.5" }),
	}
}

// legendText explains the drawing.
const legendText = `Hosts: the sign and the word under the name give the ping status: ● up, ✕ down, ? unknown, – not monitored. A hollow sign with "(stale)" means no ping result yet since the server started.
▲ next to a host: at least one of its watched services ("Watch services…") is down; the sign still shows the ping status. Select the host for which ones, or rest the pointer on it.
Groups: a collapsed group is a folder with its status; an expanded group is a frame around its members, with a bar at the bottom to drop items out of it. A network group (with a CIDR) has a dashed frame.`

// showLegend opens Help › "Map symbols…".
func (g *GUI) showLegend() {
	m := mapmodel.New()
	m.SetMap(apitypes.Map{ViewID: legendView, Items: legendItems()})
	mv := NewMapView(legendView, func() theme.Palette { return g.palette })
	mv.SetNodes(m.Layout(legendView, measure))
	mv.legend = true
	text := widget.NewLabel(legendText)
	text.Wrapping = fyne.TextWrapWord
	content := container.NewBorder(nil, text, nil, nil, mv)
	d := dialog.NewCustom("Map symbols", "Close", content, g.win)
	d.Resize(fyne.NewSize(860, 560))
	d.Show()
	g.lastLegend = mv
}

// legendView is the legend's own view id; no real view has it.
const legendView = -1
