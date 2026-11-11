package gui

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// ---- forms (M6-10: an API error stays in the dialog) ----

// formDialog is a dialog with a form. Submitting runs the request in the
// background; on an error the message appears under the form and the
// dialog stays open, so the input can be fixed (SPEC-706).
type formDialog struct {
	d    dialog.Dialog
	form *widget.Form
	errL *widget.Label
	done bool // closed after a successful submit
}

// showForm opens a form dialog; run does the request.
func (g *GUI) showForm(title, submit string, items []*widget.FormItem, run func() error) *formDialog {
	f := &formDialog{errL: widget.NewLabel("")}
	f.errL.Wrapping = fyne.TextWrapWord
	f.errL.Importance = widget.DangerImportance
	f.errL.Selectable = true // can be copied
	f.form = &widget.Form{Items: items, SubmitText: submit, CancelText: "Cancel"}
	f.form.OnCancel = func() { f.d.Hide() }
	f.form.OnSubmit = func() {
		f.errL.SetText("")
		go func() {
			err := run()
			g.do(func() {
				if err != nil {
					f.errL.SetText("Error: " + err.Error())
					return
				}
				f.done = true
				f.d.Hide()
			})
		}()
	}
	f.d = dialog.NewCustomWithoutButtons(title, container.NewVBox(f.form, f.errL), g.win)
	f.d.Resize(fyne.NewSize(460, 0))
	f.d.Show()
	for _, it := range items { // type at once into the first field
		if e, ok := it.Widget.(*widget.Entry); ok {
			g.win.Canvas().Focus(e)
			break
		}
	}
	g.lastForm = f
	return f
}

// Submit presses the form's submit button (for tests).
func (f *formDialog) Submit() { f.form.OnSubmit() }

func entry(text string) *widget.Entry {
	e := widget.NewEntry()
	e.SetText(text)
	return e
}

// ---- groups and hosts (SPEC-706) ----

func (g *GUI) newGroupDialog(view int64, parent *int64, x, y float64) *formDialog {
	name, desc, cidr := entry(""), entry(""), entry("")
	cidr.SetPlaceHolder("for example 10.0.1.0/24 (optional)")
	items := []*widget.FormItem{widget.NewFormItem("Name", name), widget.NewFormItem("Description", desc)}
	if view == mapmodel.NetworkView {
		items = append(items, widget.NewFormItem("CIDR", cidr))
	}
	return g.showForm("New group", "Create", items, func() error {
		_, err := g.c.CreateGroup(g.ctx, apitypes.GroupCreate{
			Name: name.Text, Description: desc.Text, CIDR: strings.TrimSpace(cidr.Text),
			ViewID: &view, ParentID: parent, X: &x, Y: &y,
		})
		return err
	})
}

func (g *GUI) editGroupDialog(view, id int64) {
	go func() {
		gr, err := g.c.Group(g.ctx, id)
		g.do(func() {
			if err != nil {
				g.status.message(fmt.Sprintf("group %d: %v", id, err))
				return
			}
			name, desc, cidr := entry(gr.Name), entry(gr.Description), entry(gr.CIDR)
			items := []*widget.FormItem{widget.NewFormItem("Name", name), widget.NewFormItem("Description", desc)}
			if view == mapmodel.NetworkView {
				items = append(items, widget.NewFormItem("CIDR (empty: none)", cidr))
			}
			g.showForm("Edit group "+gr.Name, "Save", items, func() error {
				p := apitypes.GroupPatch{Name: apitypes.Some(name.Text), Description: apitypes.Some(desc.Text)}
				if view == mapmodel.NetworkView {
					p.CIDR = apitypes.Some(strings.TrimSpace(cidr.Text))
				}
				_, err := g.c.UpdateGroup(g.ctx, id, p)
				return err
			})
		})
	}()
}

func (g *GUI) newHostDialog(parent *int64, x, y float64) *formDialog {
	ip, name, desc := entry(""), entry(""), entry("")
	ip.SetPlaceHolder("IPv4 or IPv6 address")
	return g.showForm("New host", "Create", []*widget.FormItem{
		widget.NewFormItem("IP address", ip), widget.NewFormItem("Name", name), widget.NewFormItem("Description", desc),
	}, func() error {
		in := apitypes.HostCreate{IP: strings.TrimSpace(ip.Text), Name: name.Text, Description: desc.Text, ParentID: parent}
		// Inside a frame the host goes where the user clicked. On empty
		// space the server picks its network group (SPEC-107) and puts it
		// next to that group (crosscheck 2026-11-11-03 D2).
		if parent != nil {
			in.X, in.Y = &x, &y
		}
		_, err := g.c.CreateHost(g.ctx, in)
		return err
	})
}

func (g *GUI) editHostDialog(id int64) {
	go func() {
		h, err := g.c.Host(g.ctx, id)
		g.do(func() {
			if err != nil {
				g.status.message(fmt.Sprintf("host %d: %v", id, err))
				return
			}
			ip, name, desc := entry(h.IP), entry(h.Name), entry(h.Description)
			mon := widget.NewCheck("pinged by the server", nil)
			mon.SetChecked(h.Monitored)
			g.showForm("Edit host "+h.DisplayName, "Save", []*widget.FormItem{
				widget.NewFormItem("IP address", ip), widget.NewFormItem("Name", name),
				widget.NewFormItem("Description", desc), widget.NewFormItem("Monitored", mon),
			}, func() error {
				_, err := g.c.UpdateHost(g.ctx, id, apitypes.HostPatch{
					IP: apitypes.Some(strings.TrimSpace(ip.Text)), Name: apitypes.Some(name.Text),
					Description: apitypes.Some(desc.Text), Monitored: apitypes.Some(mon.Checked),
				})
				return err
			})
		})
	}()
}

// moveToDialog: "Move to group…" with the view's groups and "Root"
// (SPEC-704). A group can't move into itself or below itself.
func (g *GUI) moveToDialog(view int64, k mapmodel.Key) *formDialog {
	g.mu.Lock()
	groups := g.model.Groups(view)
	skip := map[mapmodel.Key]bool{}
	if k.Type == apitypes.TypeGroup {
		skip = g.model.Subtree(view, k.ID)
	}
	var current string
	if it := g.item(view, k); it != nil && it.ParentID != nil {
		if p := g.item(view, mapmodel.GroupKey(*it.ParentID)); p != nil {
			current = groupOption(*p)
		}
	}
	g.mu.Unlock()
	opts := []string{"Root"}
	ids := map[string]*int64{"Root": nil}
	for _, gr := range groups {
		if skip[mapmodel.GroupKey(gr.ID)] {
			continue
		}
		o, id := groupOption(gr), gr.ID
		opts = append(opts, o)
		ids[o] = &id
	}
	sel := widget.NewSelect(opts, nil)
	if current == "" {
		current = "Root"
	}
	sel.SetSelected(current)
	return g.showForm(fmt.Sprintf("Move %s %d", k.Type, k.ID), "Move", []*widget.FormItem{widget.NewFormItem("Into", sel)},
		func() error { return g.setParent(view, k, ids[sel.Selected]) })
}

// plural writes a count with its noun: "no hosts", "1 host", "2 hosts".
func plural(n int, noun string) string {
	switch n {
	case 0:
		return "no " + noun + "s"
	case 1:
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func groupOption(gr apitypes.MapItem) string { return fmt.Sprintf("%s (%d)", gr.Name, gr.ID) }

// setParent changes an item's parent (nil: root) in a view (SPEC-508).
func (g *GUI) setParent(view int64, k mapmodel.Key, group *int64) error {
	var err error
	switch {
	case k.Type == apitypes.TypeGroup:
		_, err = g.c.UpdateGroup(g.ctx, k.ID, apitypes.GroupPatch{ParentID: apitypes.Some(group)})
	case view == mapmodel.NetworkView:
		_, err = g.c.UpdateHost(g.ctx, k.ID, apitypes.HostPatch{ParentID: apitypes.Some(group)})
	default:
		_, err = g.c.PlaceHost(g.ctx, view, k.ID, apitypes.PlacementSet{ParentID: group})
	}
	return err
}

// ---- deleting (SPEC-719) ----

// deleteDialog asks before deleting and says what goes. A non-empty group
// offers reparent or cascade; in the network view the cascade deletes
// hosts, in a custom view it only takes them out of the view.
func (g *GUI) deleteDialog(view int64, k mapmodel.Key) *formDialog {
	g.mu.Lock()
	it := g.item(view, k)
	var hosts, groups int
	if k.Type == apitypes.TypeGroup {
		hosts, groups = g.model.Contents(view, k.ID)
	}
	g.mu.Unlock()
	if it == nil {
		return nil
	}
	label := func(s string) *widget.FormItem {
		l := widget.NewLabel(s)
		l.Wrapping = fyne.TextWrapWord
		return widget.NewFormItem("", l)
	}
	if k.Type == apitypes.TypeHost {
		return g.showForm("Delete host", "Delete", []*widget.FormItem{
			label(fmt.Sprintf("Delete host %s (%s)? It is removed from every view. This can't be undone.", it.Name, it.IP)),
		}, func() error { return g.c.DeleteHost(g.ctx, k.ID) })
	}
	if hosts+groups == 0 {
		return g.showForm("Delete group", "Delete", []*widget.FormItem{
			label(fmt.Sprintf("Delete the empty group %s? This can't be undone.", it.Name)),
		}, func() error { return g.c.DeleteGroup(g.ctx, k.ID, "") })
	}
	const up, all = "Move its contents up", "Delete its contents too"
	what := plural(hosts, "host") + " and " + plural(groups, "group")
	cascade := "deletes " + what + "."
	if view != mapmodel.NetworkView {
		cascade = fmt.Sprintf("removes %s from this view (they stay in the network view) and deletes %s.",
			plural(hosts, "host"), plural(groups, "group"))
	}
	radio := widget.NewRadioGroup([]string{up, all}, nil)
	radio.SetSelected(up)
	return g.showForm("Delete group", "Delete", []*widget.FormItem{
		label(fmt.Sprintf("Group %s holds %s. \"%s\" puts them in its parent; \"%s\" %s This can't be undone.",
			it.Name, what, up, all, cascade)),
		widget.NewFormItem("Contents", radio),
	}, func() error {
		mode := apitypes.ChildrenReparent
		if radio.Selected == all {
			mode = apitypes.ChildrenCascade
		}
		return g.c.DeleteGroup(g.ctx, k.ID, mode)
	})
}

// ---- discovery (SPEC-707) ----

// discoveryDialog starts a discovery job and follows it.
type discoveryDialog struct {
	d       dialog.Dialog
	cidr    *widget.Entry
	target  *widget.Select
	start   *widget.Button
	cancel  *widget.Button
	bar     *progressBar
	info    *widget.Label
	copyB   *widget.Button
	last    *apitypes.Discovery // the job's latest state, for "Copy"
	job     string
	targets map[string]*int64
}

// openDiscovery opens the discovery dialog, with a range filled in for
// "Scan this network".
func (g *GUI) openDiscovery(cidr string) *discoveryDialog {
	dd := &discoveryDialog{cidr: entry(cidr), info: widget.NewLabel("Not started"), bar: newProgressBar(func() theme.Palette { return g.palette }),
		targets: map[string]*int64{"Root": nil}}
	dd.cidr.SetPlaceHolder("IPv4 range, for example 192.168.1.0/24")
	opts := []string{"Root"}
	g.mu.Lock()
	for _, gr := range g.model.Groups(mapmodel.NetworkView) {
		o, id := groupOption(gr), gr.ID
		opts = append(opts, o)
		dd.targets[o] = &id
	}
	g.mu.Unlock()
	// Messages wrap, can be selected, and "Copy" puts them with the job's
	// details on the clipboard (user request).
	dd.info.Wrapping = fyne.TextWrapWord
	dd.info.Selectable = true
	dd.copyB = widget.NewButton("Copy", func() { g.app.Clipboard().SetContent(dd.details()) })
	dd.target = widget.NewSelect(opts, nil)
	dd.target.SetSelected("Root")
	dd.start = widget.NewButton("Start", func() { g.startDiscovery(dd) })
	dd.cancel = widget.NewButton("Cancel job", func() {
		job := dd.job
		go func() {
			if _, err := g.c.CancelDiscovery(g.ctx, job); err != nil {
				g.fail("cancel discovery", err)
			}
		}()
	})
	dd.cancel.Disable()
	form := widget.NewForm(widget.NewFormItem("Range", dd.cidr),
		widget.NewFormItem("New network group under", dd.target))
	content := container.NewVBox(form,
		widget.NewLabel("Hosts that answer go into the network group for the range, which is created if needed."),
		dd.bar, dd.info, container.NewHBox(dd.start, dd.cancel, dd.copyB))
	dd.d = dialog.NewCustom("Discover hosts", "Close", content, g.win)
	dd.d.SetOnClosed(func() {
		if g.disc == dd {
			g.disc = nil // a running job keeps reporting in the status bar
		}
	})
	dd.d.Resize(fyne.NewSize(480, 0))
	dd.d.Show()
	if cidr == "" { // type the range at once (C5)
		g.win.Canvas().Focus(dd.cidr)
	}
	g.disc = dd
	return dd
}

func (g *GUI) startDiscovery(dd *discoveryDialog) {
	in := apitypes.DiscoveryCreate{CIDR: strings.TrimSpace(dd.cidr.Text), ParentID: dd.targets[dd.target.Selected]}
	dd.start.Disable()
	dd.info.SetText("Starting…")
	go func() {
		job, err := g.c.StartDiscovery(g.ctx, in)
		g.do(func() {
			if err != nil {
				dd.info.SetText("Error: " + err.Error())
				dd.start.Enable()
				return
			}
			dd.job = job.ID
			g.jobs[job.ID] = true
			dd.cancel.Enable()
			g.showJob(job)
			// A small job can end before this answer arrived; its
			// discovery.finished was then ignored. Ask once more (C1).
			go func() {
				now, err := g.c.Discovery(g.ctx, job.ID)
				g.do(func() {
					if err != nil || !g.jobs[now.ID] {
						return
					}
					g.showJob(now)
					if now.State != apitypes.DiscoveryRunning {
						delete(g.jobs, now.ID)
					}
				})
			}()
		})
	}()
}

// progressBar shows progress in palette colours only: a bordered track
// and a filled part, with no text inside (Fyne's bar draws white text on
// a translucent colour; SPEC-002, 709, crosscheck 2026-11-11-03 C2).
type progressBar struct {
	widget.BaseWidget
	value   float64
	palette func() theme.Palette
}

func newProgressBar(p func() theme.Palette) *progressBar {
	b := &progressBar{palette: p}
	b.ExtendBaseWidget(b)
	return b
}

// SetValue sets the progress, 0 to 1.
func (b *progressBar) SetValue(v float64) {
	b.value = math.Min(1, math.Max(0, v))
	b.Refresh()
}

func (b *progressBar) CreateRenderer() fyne.WidgetRenderer {
	r := &barRenderer{b: b, track: canvas.NewRectangle(nil), fill: canvas.NewRectangle(nil)}
	r.Refresh()
	return r
}

type barRenderer struct {
	b           *progressBar
	track, fill *canvas.Rectangle
}

func (r *barRenderer) Destroy()                     {}
func (r *barRenderer) MinSize() fyne.Size           { return fyne.NewSize(100, 14) }
func (r *barRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.track, r.fill} }

func (r *barRenderer) Layout(s fyne.Size) {
	r.track.Resize(s)
	r.fill.Move(fyne.NewPos(1, 1))
	r.fill.Resize(fyne.NewSize(float32(float64(s.Width-2)*r.b.value), s.Height-2))
}

func (r *barRenderer) Refresh() {
	p := r.b.palette()
	r.track.FillColor, r.track.StrokeColor, r.track.StrokeWidth = nrgba(p.Background), nrgba(p.Border), 1
	r.fill.FillColor = nrgba(p.Primary)
	r.Layout(r.b.Size())
	r.track.Refresh()
	r.fill.Refresh()
}

// onDiscoveryEvent follows the jobs this GUI started: in the dialog if it
// is open, and in the status bar.
func (g *GUI) onDiscoveryEvent(ev client.Event) {
	var job apitypes.Discovery
	if json.Unmarshal(ev.Data, &job) != nil || !g.jobs[job.ID] {
		return
	}
	g.showJob(job)
	if job.State != apitypes.DiscoveryRunning {
		delete(g.jobs, job.ID)
	}
}

// details is what "Copy" copies: the message, and the job's fields.
func (dd *discoveryDialog) details() string {
	s := dd.info.Text
	if j := dd.last; j != nil {
		s += fmt.Sprintf("\njob %s, range %s, state %s: %d of %d probed, %d found, %d new hosts, %d probes failed",
			j.ID, j.CIDR, j.State, j.Probed, j.Total, j.Found, j.Added, j.Errors)
		if j.Error != "" {
			s += "\nfirst error: " + j.Error
		}
	}
	return s
}

func jobText(j apitypes.Discovery) string {
	s := fmt.Sprintf("discovery %s: %d of %d probed, %d found", j.CIDR, j.Probed, j.Total, j.Found)
	if j.Errors > 0 && j.State == apitypes.DiscoveryRunning {
		s += fmt.Sprintf(", %d probes failed", j.Errors)
	}
	if j.State != apitypes.DiscoveryRunning {
		s = fmt.Sprintf("discovery %s %s: %d found, %d new hosts", j.CIDR, j.State, j.Found, j.Added)
		if j.Error != "" {
			s += " (" + j.Error + ")"
		}
	}
	return s
}

func (g *GUI) showJob(j apitypes.Discovery) {
	g.status.message(jobText(j))
	dd := g.disc
	if dd == nil || dd.job != j.ID {
		return
	}
	if j.Total > 0 {
		dd.bar.SetValue(float64(j.Probed) / float64(j.Total))
	}
	dd.info.SetText(jobText(j))
	jj := j
	dd.last = &jj
	if j.State != apitypes.DiscoveryRunning {
		dd.cancel.Disable()
		dd.start.Enable()
	}
}

// ---- views (SPEC-720) ----

// viewMenuItems is the toolbar's View menu for the current tab.
func (g *GUI) viewMenuItems() []*fyne.MenuItem {
	g.mu.Lock()
	views := g.model.Views()
	cur, _ := g.model.View(g.current)
	g.mu.Unlock()
	i := 0
	for j, v := range views {
		if v.ID == cur.ID {
			i = j
		}
	}
	left := fyne.NewMenuItem("Move view left", func() { g.moveView(-1) })
	right := fyne.NewMenuItem("Move view right", func() { g.moveView(+1) })
	del := fyne.NewMenuItem("Delete view…", func() { g.deleteViewDialog() })
	left.Disabled, right.Disabled = i == 0, i >= len(views)-1
	del.Disabled = cur.ID == mapmodel.NetworkView || cur.ID == 0
	cur2 := cur.ID
	return []*fyne.MenuItem{
		fyne.NewMenuItem("New view…", func() { g.newViewDialog() }),
		fyne.NewMenuItem("Rename view…", func() { g.renameViewDialog() }),
		left, right,
		fyne.NewMenuItem("Reflow view…", func() { g.reflowDialog(cur2, nil) }),
		fyne.NewMenuItemSeparator(), del,
	}
}

func (g *GUI) newViewDialog() *formDialog {
	name := entry("")
	return g.showForm("New view", "Create", []*widget.FormItem{widget.NewFormItem("Name", name)}, func() error {
		v, err := g.c.CreateView(g.ctx, apitypes.ViewCreate{Name: name.Text})
		if err == nil {
			g.do(func() { g.selectView(v.ID) })
		}
		return err
	})
}

// selectView selects a view's tab, now or when its view.created arrives.
func (g *GUI) selectView(id int64) {
	if g.showView(id) {
		return
	}
	g.wantView = id
}

func (g *GUI) renameViewDialog() *formDialog {
	g.mu.Lock()
	v, _ := g.model.View(g.current)
	g.mu.Unlock()
	name := entry(v.Name)
	return g.showForm("Rename view", "Rename", []*widget.FormItem{widget.NewFormItem("Name", name)}, func() error {
		_, err := g.c.UpdateView(g.ctx, v.ID, apitypes.ViewPatch{Name: apitypes.Some(name.Text)})
		return err
	})
}

// moveView moves the current tab one place (dir -1 or +1) by giving the
// views the orders 0, 1, 2, … in the new sequence; only changed ones are
// saved.
func (g *GUI) moveView(dir int) {
	g.mu.Lock()
	views := g.model.Views()
	cur := g.current
	g.mu.Unlock()
	i := -1
	for j, v := range views {
		if v.ID == cur {
			i = j
		}
	}
	j := i + dir
	if i < 0 || j < 0 || j >= len(views) {
		return
	}
	views[i], views[j] = views[j], views[i]
	go func() {
		for n, v := range views {
			if v.Order == int64(n) {
				continue
			}
			if _, err := g.c.UpdateView(g.ctx, v.ID, apitypes.ViewPatch{Order: apitypes.Some(int64(n))}); err != nil {
				g.fail("move view", err)
				return
			}
		}
	}()
}

func (g *GUI) deleteViewDialog() *formDialog {
	g.mu.Lock()
	v, _ := g.model.View(g.current)
	g.mu.Unlock()
	if v.ID == mapmodel.NetworkView || v.ID == 0 {
		return nil
	}
	l := widget.NewLabel(fmt.Sprintf("Delete view %s? Its groups and placements go; its hosts stay in the network view. This can't be undone.", v.Name))
	l.Wrapping = fyne.TextWrapWord
	return g.showForm("Delete view", "Delete", []*widget.FormItem{widget.NewFormItem("", l)},
		func() error { return g.c.DeleteView(g.ctx, v.ID) })
}

// ---- reflow (SPEC-723) ----

// reflowDialog asks before laying out a group's members (parent nil: the
// view's root) on a grid where the longest full name fits; it changes the
// positions for everyone.
func (g *GUI) reflowDialog(view int64, parent *int64) *formDialog {
	g.mu.Lock()
	w, h, n := g.model.ReflowCell(view, parent, measure)
	what := "the top level of this view"
	if parent != nil {
		if it := g.item(view, mapmodel.GroupKey(*parent)); it != nil {
			what = "group " + it.Name
		}
	}
	g.mu.Unlock()
	if n == 0 {
		g.status.message("nothing to reflow in " + what)
		return nil
	}
	l := widget.NewLabel(fmt.Sprintf("Lay out the %s in %s on a grid of %.0f × %.0f map units, so the longest name fits? "+
		"This changes their positions for everyone.", plural(n, "member"), what, w, h))
	l.Wrapping = fyne.TextWrapWord
	return g.showForm("Reflow", "Reflow", []*widget.FormItem{widget.NewFormItem("", l)}, func() error {
		_, err := g.c.Arrange(g.ctx, view, apitypes.Arrange{ParentID: parent, CellWidth: w, CellHeight: h})
		return err
	})
}

// ---- several items at once (SPEC-724) ----

// deleteManyDialog asks once before deleting several items. Groups go
// with "move contents up", so nothing else is deleted by surprise.
func (g *GUI) deleteManyDialog(view int64, keys []mapmodel.Key) *formDialog {
	var hosts, groups int
	for _, k := range keys {
		if k.Type == apitypes.TypeHost {
			hosts++
		} else {
			groups++
		}
	}
	var what, notes []string
	if hosts > 0 {
		what, notes = append(what, plural(hosts, "host")), append(notes, "hosts are removed from every view")
	}
	if groups > 0 {
		what, notes = append(what, plural(groups, "group")), append(notes, "the groups' contents move up to their parents")
	}
	text := fmt.Sprintf("Delete %s? The %s. This can't be undone.", strings.Join(what, " and "), strings.Join(notes, "; "))
	text = strings.Replace(text, "The hosts are", "Hosts are", 1)
	text = strings.Replace(text, "The the groups'", "The groups'", 1)
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return g.showForm(fmt.Sprintf("Delete %d items", len(keys)), "Delete", []*widget.FormItem{widget.NewFormItem("", l)}, func() error {
		for _, k := range keys { // hosts first, then groups
			if k.Type == apitypes.TypeHost {
				if err := g.c.DeleteHost(g.ctx, k.ID); err != nil {
					return err
				}
			}
		}
		for _, k := range keys {
			if k.Type == apitypes.TypeGroup {
				if err := g.c.DeleteGroup(g.ctx, k.ID, apitypes.ChildrenReparent); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// groupSelectionDialog asks for a name and makes a new group of the
// selected items (key g, as in Tkined). The group goes into the items'
// deepest common parent and is shown expanded around them; the items keep
// their positions, and the group's own position is the frame's top-left
// corner, so collapsing it later puts the icon there.
func (g *GUI) groupSelectionDialog(view int64, keys []mapmodel.Key) *formDialog {
	keys = g.topLevel(view, keys)
	if len(keys) == 0 {
		g.status.message("select hosts or groups first (Ctrl+click, Shift+drag), then press g")
		return nil
	}
	g.mu.Lock()
	parent := g.commonParent(view, keys)
	var box mapmodel.Rect
	pos := map[mapmodel.Key][2]float64{}
	for _, k := range keys {
		if it := g.item(view, k); it != nil {
			pos[k] = [2]float64{it.X, it.Y}
		}
	}
	g.mu.Unlock()
	for _, n := range g.maps[view].Nodes() {
		if _, ok := pos[mapmodel.Key{Type: n.Item.Type, ID: n.Item.ID}]; ok {
			box = box.Union(n.Bounds)
		}
	}
	cx, cy := mapmodel.CenterForCorner(box.X-mapmodel.FramePad, box.Y-mapmodel.FramePad-mapmodel.FrameLabelH)
	name := entry("group")
	return g.showForm(fmt.Sprintf("Group %d items", len(keys)), "Create", []*widget.FormItem{widget.NewFormItem("Name", name)}, func() error {
		gr, err := g.c.CreateGroup(g.ctx, apitypes.GroupCreate{
			Name: name.Text, ViewID: &view, ParentID: parent, Collapsed: ptrTo(false), X: &cx, Y: &cy,
		})
		if err != nil {
			return err
		}
		for _, k := range keys { // into the group, at the same place
			p := pos[k]
			if err := g.setParentAt(view, k, gr.ID, p[0], p[1]); err != nil {
				return err
			}
		}
		g.do(func() {
			if mv := g.maps[view]; mv != nil {
				k := mapmodel.GroupKey(gr.ID)
				mv.Select(&k)
				g.details.show(view, &k)
			}
		})
		return nil
	})
}

// commonParent is the deepest group above all the items (nil: the root).
// Caller holds g.mu.
func (g *GUI) commonParent(view int64, keys []mapmodel.Key) *int64 {
	var common []int64 // ancestors of the first item, nearest first
	for i, k := range keys {
		it := g.item(view, k)
		if it == nil {
			continue
		}
		var chain []int64
		for p, n := it.ParentID, 0; p != nil && n < 1000; n++ {
			chain = append(chain, *p)
			pg := g.item(view, mapmodel.GroupKey(*p))
			if pg == nil {
				break
			}
			p = pg.ParentID
		}
		if i == 0 {
			common = chain
			continue
		}
		in := map[int64]bool{}
		for _, c := range chain {
			in[c] = true
		}
		var keep []int64
		for _, c := range common {
			if in[c] {
				keep = append(keep, c)
			}
		}
		common = keep
	}
	if len(common) == 0 {
		return nil
	}
	return &common[0]
}

// setParentAt gives an item a new parent and keeps its position.
func (g *GUI) setParentAt(view int64, k mapmodel.Key, group int64, x, y float64) error {
	var err error
	switch {
	case k.Type == apitypes.TypeGroup:
		_, err = g.c.UpdateGroup(g.ctx, k.ID, apitypes.GroupPatch{ParentID: apitypes.Some(&group), X: apitypes.Some(x), Y: apitypes.Some(y)})
	case view == mapmodel.NetworkView:
		_, err = g.c.UpdateHost(g.ctx, k.ID, apitypes.HostPatch{ParentID: apitypes.Some(&group), X: apitypes.Some(x), Y: apitypes.Some(y)})
	default:
		_, err = g.c.PlaceHost(g.ctx, view, k.ID, apitypes.PlacementSet{ParentID: &group, X: &x, Y: &y})
	}
	return err
}

func ptrTo[T any](v T) *T { return &v }
