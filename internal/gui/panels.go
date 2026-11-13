package gui

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// ---- status bar (SPEC-705, 717, 802) ----

// ConnText describes the event stream's state in words (SPEC-717).
func ConnText(st client.StreamState) string {
	if st.Connected {
		return "connected"
	}
	s := fmt.Sprintf("reconnecting in %d s", int(math.Ceil(st.RetryIn.Seconds())))
	if st.Err != nil {
		s += " (" + st.Err.Error() + ")"
	}
	return s
}

type statusBar struct {
	obj                       fyne.CanvasObject
	connL, msgL, warnL, pingL *widget.Label
	copyB                     *widget.Button
	clip                      func() fyne.Clipboard
	log                       []string // recent messages with their time, newest last
}

// statusLogSize is how many messages "Copy" keeps.
const statusLogSize = 50

// newStatusBar builds the status bar. Its messages can be selected, and
// "Copy" puts the recent ones on the clipboard, e.g. for a bug report.
func newStatusBar(url string, tokenInClear bool, clip func() fyne.Clipboard) *statusBar {
	s := &statusBar{
		connL: widget.NewLabel("connecting…"),
		msgL:  widget.NewLabel(""),
		warnL: widget.NewLabel(""),
		pingL: widget.NewLabel(""),
		clip:  clip,
	}
	s.msgL.Truncation = fyne.TextTruncateEllipsis
	s.msgL.Selectable = true
	s.pingL.TextStyle.Bold = true
	s.pingL.Truncation = fyne.TextTruncateEllipsis
	s.pingL.Hide()
	if tokenInClear {
		s.warnL.SetText("Warning: the token is sent unencrypted; use https")
		s.warnL.TextStyle.Bold = true
	}
	s.copyB = widget.NewButton("Copy", s.copyLog)
	s.obj = container.NewBorder(s.pingL, nil,
		container.NewHBox(widget.NewLabel(url), widget.NewSeparator(), s.connL, widget.NewSeparator()),
		container.NewHBox(s.warnL, s.copyB), s.msgL)
	return s
}

// copyLog puts the recent messages (and a ping warning) on the clipboard.
func (s *statusBar) copyLog() {
	if s.clip == nil {
		return
	}
	lines := s.log
	if s.pingL.Text != "" {
		lines = append([]string{s.pingL.Text}, lines...)
	}
	s.clip().SetContent(strings.Join(lines, "\n"))
}

// ping shows (or with "" hides) the warning that the server can't ping.
func (s *statusBar) ping(warning string) {
	s.pingL.SetText(warning)
	if warning == "" {
		s.pingL.Hide()
		return
	}
	s.pingL.Show()
}

func (s *statusBar) conn(st client.StreamState) { s.connL.SetText(ConnText(st)) }

func (s *statusBar) stopped(err error) { s.connL.SetText("stopped: " + err.Error()) }

func (s *statusBar) message(m string) {
	line := time.Now().Format("15:04:05") + " " + m
	s.log = append(s.log, line)
	if len(s.log) > statusLogSize {
		s.log = s.log[len(s.log)-statusLogSize:]
	}
	s.msgL.SetText(line)
}

// Text returns everything the status bar shows (for tests).
func (s *statusBar) Text() string {
	return strings.Join([]string{s.connL.Text, s.msgL.Text, s.warnL.Text, s.pingL.Text}, " | ")
}

// pingWarning is the status-bar warning for a server that can't ping, or
// "" (crosscheck 2026-11-11-04, user report).
func pingWarning(h apitypes.Health) string {
	switch {
	case h.ICMPMode == apitypes.ICMPUnavailable:
		return "The server can't ping, so discovery and monitoring don't work: " + h.ICMPError
	case h.Status == "degraded" && h.ICMPMode == "":
		return "The server reports a problem (status degraded); details need a valid token (inedctl health)"
	}
	return ""
}

// ---- details panel (SPEC-708) ----

type details struct {
	g     *GUI
	obj   fyne.CanvasObject
	title *widget.Label
	form  *fyne.Container
	note  *widget.Entry
	save  *widget.Button

	view     int64
	key      *mapmodel.Key
	seq      int // ignores answers for an older selection
	host     *apitypes.Host
	group    *apitypes.Group
	lastNote string // the note as loaded; edits aren't overwritten by events
}

func newDetails(g *GUI) *details {
	d := &details{g: g, title: widget.NewLabel("Nothing selected"), form: container.New(layout.NewFormLayout())}
	d.title.TextStyle.Bold = true
	d.note = widget.NewMultiLineEntry()
	d.note.SetPlaceHolder("Note")
	d.note.Wrapping = fyne.TextWrapWord
	d.note.SetMinRowsVisible(6)
	d.save = widget.NewButton("Save note", d.saveNote)
	d.note.Disable()
	d.save.Disable()
	d.obj = container.NewBorder(d.title, container.NewVBox(widget.NewLabel("Note"), d.note, g.bordered(d.save)), nil, nil,
		container.NewVScroll(d.form))
	return d
}

func (d *details) clear() {
	d.seq++
	d.key, d.host, d.group = nil, nil, nil
	d.title.SetText("Nothing selected")
	d.form.Objects = nil
	d.form.Refresh()
	d.note.SetText("")
	d.note.Disable()
	d.save.Disable()
}

// show selects an item of a view and loads its full record.
func (d *details) show(view int64, k *mapmodel.Key) {
	if k == nil {
		d.clear()
		return
	}
	d.seq++
	seq, g := d.seq, d.g
	d.view, d.key = view, k
	go func() {
		var (
			h   apitypes.Host
			gr  apitypes.Group
			err error
		)
		if k.Type == apitypes.TypeHost {
			h, err = g.c.Host(g.ctx, k.ID)
		} else {
			gr, err = g.c.Group(g.ctx, k.ID)
		}
		g.do(func() {
			if seq != d.seq {
				return
			}
			if err != nil {
				g.status.message(fmt.Sprintf("%s %d: %v", k.Type, k.ID, err))
				return
			}
			if k.Type == apitypes.TypeHost {
				d.setHost(h, true)
			} else {
				d.setGroup(gr, true)
			}
		})
	}()
}

func (d *details) setHost(h apitypes.Host, loadNote bool) {
	d.host, d.group = &h, nil
	d.fill()
	d.setNote(h.Note, loadNote)
}

func (d *details) setGroup(g apitypes.Group, loadNote bool) {
	d.group, d.host = &g, nil
	d.fill()
	d.setNote(g.Note, loadNote)
}

// setNote shows the server's note unless the user has unsaved edits.
func (d *details) setNote(n string, force bool) {
	if force || d.note.Text == d.lastNote {
		d.note.SetText(n)
	}
	d.lastNote = n
	d.note.Enable()
	d.save.Enable()
}

// parentName is the item's parent in the current view (SPEC-708).
func (d *details) parentName() string {
	d.g.mu.Lock()
	defer d.g.mu.Unlock()
	vm := d.g.model.Map(d.view)
	if vm == nil || d.key == nil {
		return "-"
	}
	it := vm.Items[*d.key]
	if it == nil {
		return "-"
	}
	if it.ParentID == nil {
		return "root"
	}
	if p := vm.Items[mapmodel.GroupKey(*it.ParentID)]; p != nil {
		return fmt.Sprintf("%s (%d)", p.Name, p.ID)
	}
	return strconv.FormatInt(*it.ParentID, 10)
}

// viewNames lists the names of the views a host is in (SPEC-721).
func (d *details) viewNames(ids []int64) string {
	d.g.mu.Lock()
	defer d.g.mu.Unlock()
	var names []string
	for _, id := range ids {
		if v, ok := d.g.model.View(id); ok {
			names = append(names, v.Name)
		}
	}
	return dash(strings.Join(names, ", "))
}

// reload loads the selected item's record again, keeping an unsaved note.
func (d *details) reload() {
	if d.key == nil {
		return
	}
	k, g, seq := *d.key, d.g, d.seq
	go func() {
		h, err := g.c.Host(g.ctx, k.ID)
		g.do(func() {
			if err == nil && seq == d.seq {
				d.setHost(h, false)
			}
		})
	}()
}

func (d *details) refreshParent() {
	if d.host != nil || d.group != nil {
		d.fill()
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func when(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format(time.DateTime)
}

// fill writes the rows of the selected item.
func (d *details) fill() {
	var rows [][2]string
	switch {
	case d.host != nil:
		h := d.host
		d.title.SetText("Host " + h.DisplayName)
		rtt, loss := "-", "-"
		if h.RTTms != nil {
			rtt = strconv.FormatFloat(*h.RTTms, 'f', 2, 64) + " ms"
		}
		if h.LossPct != nil {
			loss = strconv.FormatFloat(*h.LossPct, 'f', 0, 64) + " %"
		}
		rows = [][2]string{
			{"ID", strconv.FormatInt(h.ID, 10)}, {"IP address", h.IP}, {"PTR name", dash(h.PTRName)},
			{"Manual name", dash(h.Name)}, {"Status", mapmodel.StatusWord(h.Status, h.Stale)},
			{"Stale", yesNo(h.Stale)}, {"Monitored", yesNo(h.Monitored)}, {"Round-trip time", rtt},
			{"Packet loss", loss}, {"Last seen", when(h.LastSeen)}, {"Last change", when(h.LastChange)},
			{"Parent (this view)", d.parentName()}, {"Views", d.viewNames(h.Views)},
			{"Description", dash(h.Description)},
		}
		if d.view == mapmodel.NetworkView {
			rows = append(rows, [2]string{"Outside its CIDR", yesNo(h.OutsideCIDR)})
		}
	case d.group != nil:
		gr := d.group
		d.title.SetText("Group " + gr.Name)
		d.g.mu.Lock()
		view, _ := d.g.model.View(gr.ViewID)
		d.g.mu.Unlock()
		counts, labels := "-", mapmodel.LabelModeText(gr.Labels)
		d.g.mu.Lock()
		if vm := d.g.model.Map(d.view); vm != nil {
			if it := vm.Items[mapmodel.GroupKey(gr.ID)]; it != nil {
				labels = vm.GroupLabelsText(it)
			}
		}
		d.g.mu.Unlock()
		var nodes []mapmodel.Node
		if mv := d.g.maps[d.view]; mv != nil { // nil once the view is gone (C3)
			nodes = mv.Nodes()
		}
		for _, n := range nodes {
			if n.Item.Type == apitypes.TypeGroup && n.Item.ID == gr.ID {
				counts = dash(n.Counts.String())
			}
		}
		rows = [][2]string{
			{"ID", strconv.FormatInt(gr.ID, 10)}, {"Name", gr.Name}, {"CIDR", dash(gr.CIDR)},
			{"Status", mapmodel.StatusWord(gr.Status, false)}, {"Hosts", counts},
			{"Collapsed", yesNo(gr.Collapsed)}, {"Host labels", labels}, {"Parent (this view)", d.parentName()},
			{"View", dash(view.Name)}, {"Description", dash(gr.Description)},
		}
	}
	objs := make([]fyne.CanvasObject, 0, 2*len(rows))
	for _, r := range rows {
		k := widget.NewLabel(r[0])
		k.TextStyle.Bold = true
		v := widget.NewLabel(r[1])
		v.Wrapping = fyne.TextWrapWord
		objs = append(objs, k, v)
	}
	d.form.Objects = objs
	d.form.Refresh()
}

// onEvent keeps the shown record current from the event stream.
func (d *details) onEvent(ev client.Event) {
	if d.key == nil {
		return
	}
	k := *d.key
	switch ev.Type {
	case apitypes.EventHostStatus, apitypes.EventHostUpdated:
		var h apitypes.Host
		if json.Unmarshal(ev.Data, &h) == nil && k == mapmodel.HostKey(h.ID) {
			d.setHost(h, false)
		}
	case apitypes.EventGroupUpdated:
		var g apitypes.Group
		if json.Unmarshal(ev.Data, &g) == nil && k == mapmodel.GroupKey(g.ID) {
			d.setGroup(g, false)
		}
	case apitypes.EventGroupStatus:
		var s apitypes.GroupStatusEvent
		if json.Unmarshal(ev.Data, &s) == nil && d.group != nil && k == mapmodel.GroupKey(s.ID) {
			d.group.Status = s.Status
			d.fill()
		}
	case apitypes.EventPlacementUpdated, apitypes.EventPlacementDeleted:
		// The host's views changed (SPEC-509): load it again.
		var p apitypes.PlacementDeletedEvent
		if json.Unmarshal(ev.Data, &p) == nil && k == mapmodel.HostKey(p.HostID) {
			d.reload()
		}
	case apitypes.EventHostDeleted, apitypes.EventGroupDeleted:
		var del apitypes.DeletedEvent
		typ := apitypes.TypeHost
		if ev.Type == apitypes.EventGroupDeleted {
			typ = apitypes.TypeGroup
		}
		if json.Unmarshal(ev.Data, &del) == nil && k == (mapmodel.Key{Type: typ, ID: del.ID}) {
			d.clear()
		}
	}
}

// saveNote writes the note (SPEC-708, SPEC-718).
func (d *details) saveNote() {
	if d.key == nil {
		return
	}
	k, text, g := *d.key, d.note.Text, d.g
	go func() {
		var err error
		if k.Type == apitypes.TypeHost {
			_, err = g.c.UpdateHost(g.ctx, k.ID, apitypes.HostPatch{Note: apitypes.Some(text)})
		} else {
			_, err = g.c.UpdateGroup(g.ctx, k.ID, apitypes.GroupPatch{Note: apitypes.Some(text)})
		}
		g.do(func() {
			if err != nil {
				g.status.message(fmt.Sprintf("note of %s %d not saved: %v", k.Type, k.ID, err))
				return
			}
			d.lastNote = text
			g.status.message(fmt.Sprintf("note of %s %d saved", k.Type, k.ID))
		})
	}()
}

// ---- find host (SPEC-714) ----

// findDialog is the open find dialog (kept for tests).
type findDialog struct {
	entry *widget.Entry
	list  *widget.List
	info  *widget.Label
}

func (g *GUI) showFind() { g.openFind() }

// openFind opens the find dialog; it loads the hosts in the background.
func (g *GUI) openFind() *findDialog {
	var hosts, found []apitypes.Host
	entry := widget.NewEntry()
	entry.SetPlaceHolder("Name, PTR name or IP address")
	list := widget.NewList(
		func() int { return len(found) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			h := found[i]
			s := h.DisplayName + " — " + h.IP
			if h.PTRName != "" && !strings.HasPrefix(h.PTRName, h.DisplayName) {
				s += " (" + strings.TrimSuffix(h.PTRName, ".") + ")"
			}
			o.(*widget.Label).SetText(s)
		})
	info := widget.NewLabel("Loading hosts…")
	update := func() {
		found = mapmodel.Find(hosts, entry.Text)
		info.SetText(fmt.Sprintf("%d of %d hosts", len(found), len(hosts)))
		list.UnselectAll()
		list.Refresh()
	}
	entry.OnChanged = func(string) { update() }
	content := container.NewBorder(container.NewVBox(entry, info), nil, nil, nil, list)
	d := dialog.NewCustom("Find host", "Close", content, g.win)
	list.OnSelected = func(i widget.ListItemID) {
		h := found[i]
		d.Hide()
		g.goToHost(h.ID)
	}
	d.Resize(fyne.NewSize(520, 420))
	d.Show()
	g.win.Canvas().Focus(entry)
	go func() {
		hs, err := g.c.Hosts(g.ctx)
		g.do(func() {
			if err != nil {
				info.SetText("Can't load hosts: " + err.Error())
				return
			}
			hosts = hs
			update()
		})
	}()
	return &findDialog{entry: entry, list: list, info: info}
}

// goToHost shows a host (SPEC-714): in the current tab if it is placed in
// that view, else in the network view; collapsed groups above it are
// expanded, and the map is centred on it.
func (g *GUI) goToHost(host int64) {
	cur := g.current
	go func() {
		for _, v := range []int64{mapmodel.NetworkView, cur} {
			g.mu.Lock()
			loaded := g.model.Loaded(v)
			g.mu.Unlock()
			if !loaded {
				g.loadMap(v)
			}
		}
		g.mu.Lock()
		view, expand, ok := g.model.Locate(host, cur)
		var x, y float64
		leave := false // the host is outside the tab's focus (SPEC-726, F7)
		if ok {
			it := g.model.Map(view).Items[mapmodel.HostKey(host)]
			x, y = it.X, it.Y
			if f := g.focus[view]; f != 0 {
				if !g.model.InFocus(view, f, mapmodel.HostKey(host)) {
					leave = true
				} else { // groups from the focus up needn't be expanded
					sub := g.model.Subtree(view, f)
					expand = slices.DeleteFunc(expand, func(id int64) bool {
						return id == f || !sub[mapmodel.GroupKey(id)]
					})
				}
			}
		}
		g.mu.Unlock()
		if !ok {
			g.fail("find", fmt.Errorf("host %d is not on the map", host))
			return
		}
		for _, gid := range expand {
			if _, err := g.c.UpdateGroup(g.ctx, gid, apitypes.GroupPatch{Collapsed: apitypes.Some(false)}); err != nil {
				g.fail(fmt.Sprintf("expand group %d", gid), err)
			}
		}
		g.do(func() {
			g.showView(view)
			mv := g.maps[view]
			if mv == nil {
				return
			}
			if leave {
				g.setFocus(view, 0)
				g.status.message("find: the host is outside the shown group; showing the whole view")
			}
			k := mapmodel.HostKey(host)
			mv.Select(&k)
			mv.CentreOn(x, y)
			g.details.show(view, &k)
		})
	}()
}

// showView selects a view's tab.
// showView selects a view's tab; false if there is none (yet).
func (g *GUI) showView(view int64) bool {
	for i, ti := range g.tabs.Items {
		if mv, ok := ti.Content.(*MapView); ok && mv.view == view {
			g.tabs.SelectIndex(i)
			g.setCurrent(view)
			return true
		}
	}
	return false
}

// ---- contrast (SPEC-004, 005, 709) ----

const customOption = "custom…"

func contrastOptions() []string {
	var out []string
	for _, p := range []string{theme.PresetAA, theme.PresetAAA, theme.PresetMax} {
		c, _ := theme.Parse(p, 0)
		out = append(out, c.String())
	}
	return append(out, customOption)
}

// setContrast applies a contrast setting at once (no restart).
func (g *GUI) setContrast(c theme.Contrast) {
	g.contrast, g.palette = c, theme.NewPalette(c)
	g.app.Settings().SetTheme(newFyneTheme(g.palette))
	g.colourBorders()
	if g.disc != nil {
		g.disc.bar.Refresh()
	}
	for _, mv := range g.maps {
		mv.Refresh()
	}
}

func (g *GUI) pickContrast(s string) {
	if s == g.contrast.String() {
		return
	}
	if s == customOption {
		g.askCustomContrast()
		return
	}
	preset, _, _ := strings.Cut(s, " ")
	c, err := theme.Parse(preset, 0)
	if err != nil {
		g.status.message(err.Error())
		return
	}
	g.changeContrast(c)
}

func (g *GUI) askCustomContrast() { g.customContrastForm().Show() }

// customContrastForm builds the dialog for a custom ratio; tests use its
// entry and Submit.
func (g *GUI) customContrastForm() *customForm {
	e := widget.NewEntry()
	e.SetText(strconv.FormatFloat(g.contrast.Min, 'f', -1, 64))
	e.Validator = func(s string) error {
		r, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return fmt.Errorf("not a number")
		}
		_, err = theme.Parse(theme.PresetCustom, r)
		return err
	}
	d := dialog.NewForm("Custom contrast", "Apply", "Cancel",
		[]*widget.FormItem{widget.NewFormItem("Minimum ratio (4.5–21)", e)},
		func(ok bool) {
			if !ok {
				g.contrSel.SetSelected(g.contrast.String())
				return
			}
			r, _ := strconv.ParseFloat(strings.TrimSpace(e.Text), 64)
			c, err := theme.Parse(theme.PresetCustom, r)
			if err != nil {
				g.status.message(err.Error())
				return
			}
			g.changeContrast(c)
		}, g.win)
	return &customForm{FormDialog: d, entry: e}
}

type customForm struct {
	*dialog.FormDialog
	entry *widget.Entry
}

// changeContrast applies and saves a contrast setting chosen in the GUI.
func (g *GUI) changeContrast(c theme.Contrast) {
	g.setContrast(c)
	opts := contrastOptions()
	if c.Preset == theme.PresetCustom {
		opts = append(opts[:len(opts)-1], c.String(), customOption)
	}
	g.contrSel.Options = opts
	g.contrSel.SetSelected(c.String())
	if g.opt.SaveContrast == nil {
		return
	}
	go func() {
		err := g.opt.SaveContrast(c)
		g.do(func() {
			if err != nil {
				g.status.message("contrast not saved: " + err.Error())
				return
			}
			g.status.message("contrast " + c.String() + " saved")
		})
	}()
}

// Contrast returns the contrast setting in use.
func (g *GUI) Contrast() theme.Contrast { return g.contrast }
