package gui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// SNMP checks of groups and selections, and community strings per group
// and host (SPEC-737; Design-snmp-gui.md G1, G2).

// hostsUnder counts the hosts of a group and its subgroups in a view, as
// the map has them. Caller holds g.mu.
func (g *GUI) hostsUnder(view, group int64) int {
	vm := g.model.Map(view)
	if vm == nil {
		return 0
	}
	n := 0
	for k := range vm.Items {
		if k.Type == apitypes.TypeHost && g.model.InFocus(view, group, k) {
			n++
		}
	}
	return n
}

// snmpCheckItem is "Check SNMP now (N hosts)" for a group or a selection.
func (g *GUI) snmpCheckItem(in apitypes.SNMPCheckCreate, n int) *fyne.MenuItem {
	label := fmt.Sprintf("Check SNMP now (%d hosts)", n)
	if n == 1 {
		label = "Check SNMP now (1 host)"
	}
	mi := fyne.NewMenuItem(label, func() { g.startSNMPCheck(in, n) })
	mi.Disabled = n == 0
	return mi
}

// startSNMPCheck starts the job; its progress goes to the status bar,
// which shows "Cancel SNMP check" while it runs.
func (g *GUI) startSNMPCheck(in apitypes.SNMPCheckCreate, n int) {
	g.status.message(fmt.Sprintf("Starting an SNMP check of %d hosts…", n))
	go func() {
		j, err := g.c.StartSNMPCheck(g.ctx, in)
		if err != nil {
			g.fail("SNMP check", err)
			return
		}
		g.do(func() {
			if j.State == apitypes.DiscoveryRunning {
				g.snmpJobs[j.ID] = true
			}
			g.showSNMPCheck(j)
		})
	}()
}

// snmpCheckText is "SNMP check: 12 of 40 hosts checked, 5 answered", and
// at the end "SNMP: 12 of 40 hosts answered (26 no answer, 2 skipped)".
func snmpCheckText(j apitypes.SNMPCheck) string {
	if j.State == apitypes.DiscoveryRunning {
		return fmt.Sprintf("SNMP check: %d of %d hosts checked, %d answered", j.Checked, j.Total, j.Found)
	}
	s := fmt.Sprintf("SNMP: %d of %d hosts answered", j.Found, j.Total)
	var more []string
	if j.NoAnswer > 0 {
		more = append(more, fmt.Sprintf("%d no answer", j.NoAnswer))
	}
	if j.Skipped > 0 {
		more = append(more, fmt.Sprintf("%d skipped", j.Skipped))
	}
	if len(more) > 0 {
		s += " (" + strings.Join(more, ", ") + ")"
	}
	if j.State == apitypes.DiscoveryCancelled {
		s += "; cancelled"
	}
	return s
}

func (g *GUI) showSNMPCheck(j apitypes.SNMPCheck) {
	g.status.message(snmpCheckText(j))
	if j.State != apitypes.DiscoveryRunning {
		delete(g.snmpJobs, j.ID)
	}
	if len(g.snmpJobs) == 0 {
		g.status.cancelB.Hide()
		return
	}
	g.status.cancelB.OnTapped = func() {
		for id := range g.snmpJobs {
			go func() {
				if _, err := g.c.CancelSNMPCheck(g.ctx, id); err != nil {
					g.fail("Cancel SNMP check", err)
				}
			}()
		}
	}
	g.status.cancelB.Show()
}

// onSNMPCheckEvent follows the checks this GUI started.
func (g *GUI) onSNMPCheckEvent(ev client.Event) {
	var j apitypes.SNMPCheck
	if json.Unmarshal(ev.Data, &j) != nil || !g.snmpJobs[j.ID] {
		return
	}
	g.showSNMPCheck(j)
}

// communitiesText is the details row (SPEC-737): "2 set here", "from
// group lan", "the server's list"; for a group without a list "none set
// here (inherited)".
func (g *GUI) communitiesText(set int, from string) string {
	switch {
	case set > 0:
		return fmt.Sprintf("%d set here", set)
	case strings.HasPrefix(from, "group "):
		name := from
		if id, err := strconv.ParseInt(strings.TrimPrefix(from, "group "), 10, 64); err == nil {
			g.mu.Lock()
			if it := g.item(mapmodel.NetworkView, mapmodel.GroupKey(id)); it != nil {
				name = "group " + it.Name
			}
			g.mu.Unlock()
		}
		return "from " + name
	case from == "server":
		return "the server's list"
	}
	return "none set here (inherited)"
}

// communitiesItem is "SNMP communities…" on a host or a network-view
// group.
func (g *GUI) communitiesItem(kind string, id int64, name string) *fyne.MenuItem {
	return fyne.NewMenuItem("SNMP communities…", func() { g.openCommunities(kind, id, name) })
}

// communitiesDialog sets the community strings of a host or group. They
// are write-only: the dialog shows only how many are set and where the
// list comes from, never the strings (SPEC-315, G5a).
type communitiesDialog struct {
	d       dialog.Dialog
	info    *widget.Label
	rows    *fyne.Container
	entries []*widget.Entry
	save    func([]string)
}

func (g *GUI) openCommunities(kind string, id int64, name string) {
	go func() {
		set, from := 0, ""
		if kind == "host" {
			h, err := g.c.Host(g.ctx, id)
			if err != nil {
				g.fail("SNMP communities", err)
				return
			}
			set, from = h.SNMPCommunitiesSet, h.SNMPCommunitiesFrom
		} else {
			gr, err := g.c.Group(g.ctx, id)
			if err != nil {
				g.fail("SNMP communities", err)
				return
			}
			set = gr.SNMPCommunitiesSet
		}
		g.do(func() { g.communitiesDialog(kind, id, name, set, from) })
	}()
}

func (g *GUI) communitiesDialog(kind string, id int64, name string, set int, from string) {
	cd := &communitiesDialog{info: widget.NewLabel(""), rows: container.NewVBox()}
	now := g.communitiesText(set, from)
	cd.info.SetText(fmt.Sprintf("Now: %s. The strings are never shown again; what you type here replaces the list. "+
		"\"Inherit\" removes the list set here.", now))
	cd.info.Wrapping = fyne.TextWrapWord
	addRow := func() {
		e := widget.NewPasswordEntry() // masked, with an eye to show while typing
		e.SetPlaceHolder("community string")
		cd.entries = append(cd.entries, e)
		var row *fyne.Container
		rm := widget.NewButton("Remove", func() {
			for i, x := range cd.entries {
				if x == e {
					cd.entries = append(cd.entries[:i], cd.entries[i+1:]...)
				}
			}
			cd.rows.Remove(row)
		})
		row = container.NewBorder(nil, nil, nil, rm, e)
		cd.rows.Add(row)
	}
	addRow()
	add := widget.NewButton("Add", func() {
		if len(cd.entries) < apitypes.MaxCommunities {
			addRow()
		}
	})
	what := "host " + name
	if kind == "group" {
		what = "group " + name
	}
	cd.save = func(list []string) {
		if err := apitypes.CheckCommunities(list); err != nil {
			cd.info.SetText(err.Error())
			return
		}
		cd.d.Hide()
		g.commDialog = nil
		go func() {
			var err error
			var text string
			if kind == "host" {
				var h apitypes.Host
				if h, err = g.c.UpdateHost(g.ctx, id, apitypes.HostPatch{SNMPCommunities: apitypes.Some(list)}); err == nil {
					text = g.communitiesText(h.SNMPCommunitiesSet, h.SNMPCommunitiesFrom)
				}
			} else {
				var gr apitypes.Group
				if gr, err = g.c.UpdateGroup(g.ctx, id, apitypes.GroupPatch{SNMPCommunities: apitypes.Some(list)}); err == nil {
					text = g.communitiesText(gr.SNMPCommunitiesSet, "")
				}
			}
			if err != nil {
				g.fail("SNMP communities of "+what, err)
				return
			}
			g.do(func() { g.status.message(fmt.Sprintf("SNMP communities of %s: %s", what, text)) })
		}()
	}
	saveB := widget.NewButton("Save", func() {
		var list []string
		for _, e := range cd.entries {
			if s := e.Text; s != "" {
				list = append(list, s)
			}
		}
		if len(list) == 0 {
			cd.info.SetText("Type at least one string, or use \"Inherit\".")
			return
		}
		cd.save(list)
	})
	saveB.Importance = widget.HighImportance
	inherit := widget.NewButton("Inherit", func() { cd.save([]string{}) })
	cancel := widget.NewButton("Cancel", func() { cd.d.Hide(); g.commDialog = nil })
	body := container.NewBorder(cd.info, container.NewHBox(add, inherit, cancel, saveB), nil, nil, container.NewVScroll(cd.rows))
	cd.d = dialog.NewCustomWithoutButtons("SNMP communities of "+what, body, g.win)
	cd.d.Resize(fyne.NewSize(460, 320))
	cd.d.Show()
	g.commDialog = cd
}
