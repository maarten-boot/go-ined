package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
func (g *GUI) hostsUnder(view, group int64) int { return len(g.hostIDsUnder(view, group)) }

// hostIDsUnder lists them. The subtree is built once, not per host
// (crosscheck 2026-11-19-01 C4). Caller holds g.mu.
func (g *GUI) hostIDsUnder(view, group int64) []int64 {
	var ids []int64
	for k := range g.model.Subtree(view, group) {
		if k.Type == apitypes.TypeHost {
			ids = append(ids, k.ID)
		}
	}
	return ids
}

// checkTarget is what "Check SNMP now" checks for a click on k with the
// selection sel (onSelection: k is in it): a lone group by its id, else
// every selected host and every host below the selected groups (L10).
// Caller holds g.mu.
func (g *GUI) checkTarget(view int64, k mapmodel.Key, sel []mapmodel.Key, onSelection bool) (apitypes.SNMPCheckCreate, int) {
	keys := []mapmodel.Key{k}
	if onSelection {
		keys = sel
	}
	if len(keys) == 1 && keys[0].Type == apitypes.TypeGroup {
		id := keys[0].ID
		return apitypes.SNMPCheckCreate{GroupID: &id}, g.hostsUnder(view, id)
	}
	seen := map[int64]bool{}
	var ids []int64
	add := func(id int64) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, s := range keys {
		if s.Type == apitypes.TypeHost {
			add(s.ID)
			continue
		}
		for _, id := range g.hostIDsUnder(view, s.ID) {
			add(id)
		}
	}
	slices.Sort(ids)
	return apitypes.SNMPCheckCreate{HostIDs: ids}, len(ids)
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
		g.do(func() { g.registerSNMPCheck(j) })
	}()
}

// registerSNMPCheck follows a job this GUI started. An event for it may
// have come before the start's answer: the newest one seen is used, and
// the job is read once more (crosscheck 2026-11-19-01 C1).
func (g *GUI) registerSNMPCheck(j apitypes.SNMPCheck) {
	if seen, ok := g.snmpSeen[j.ID]; ok && (seen.Checked > j.Checked || seen.State != apitypes.DiscoveryRunning) {
		j = seen
	}
	g.forgetSeen(j.ID)
	if j.State == apitypes.DiscoveryRunning {
		g.snmpJobs[j.ID] = true
		g.refreshSNMPCheck(j.ID)
	}
	g.showSNMPCheck(j)
}

// refreshSNMPCheck reads a job and shows it, if this GUI still follows it.
// An answer that the job is gone ends following it: 404 (the server
// restarted or dropped it) and 503 "unavailable" (SNMP is off)
// (crosscheck 2026-11-19-03 C2, L5). After any other answer, such as 401
// for a changed token (crosscheck 2026-11-19-04 C2), a network error or a
// timeout, the job may still run, so Cancel stays and the next event or
// refresh settles it (crosscheck 2026-11-19-02 C2).
func (g *GUI) refreshSNMPCheck(id string) {
	go func() {
		j, err := g.c.SNMPCheck(g.ctx, id)
		g.do(func() {
			var ae *client.APIError
			isAE := errors.As(err, &ae)
			switch {
			case !g.snmpJobs[id]:
			case isAE && (ae.Status == http.StatusNotFound || ae.Status == http.StatusServiceUnavailable && ae.Code == apitypes.CodeUnavailable):
				delete(g.snmpJobs, id)
				g.showSNMPCheck(apitypes.SNMPCheck{ID: id, State: apitypes.DiscoveryCancelled}) // the button
				why := "no longer known to the server"
				if ae.Status == http.StatusServiceUnavailable {
					why = "SNMP is off on the server"
				}
				g.status.message(fmt.Sprintf("SNMP check %s: %s; no longer followed", id, why))
			case err != nil:
				g.status.message(fmt.Sprintf("SNMP check %s: not read: %v", id, err))
			default:
				g.showSNMPCheck(j)
			}
		})
	}()
}

// refreshSNMPChecks reads every job this GUI follows (after a resync,
// when events may have been missed).
func (g *GUI) refreshSNMPChecks() {
	for id := range g.snmpJobs {
		g.refreshSNMPCheck(id)
	}
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
	g.status.cancelB.SetText("Cancel SNMP check")
	if len(g.snmpJobs) > 1 { // L11
		g.status.cancelB.SetText("Cancel SNMP checks")
	}
	g.status.cancelB.OnTapped = func() {
		for id := range g.snmpJobs {
			go func() {
				if _, err := g.c.CancelSNMPCheck(g.ctx, id); err != nil {
					g.fail("Cancel SNMP check", err)
				}
				g.refreshSNMPCheck(id) // a finished job sends no event (C1)
			}()
		}
	}
	g.status.cancelB.Show()
}

// onSNMPCheckEvent follows the checks this GUI started.
func (g *GUI) onSNMPCheckEvent(ev client.Event) {
	var j apitypes.SNMPCheck
	if json.Unmarshal(ev.Data, &j) != nil {
		return
	}
	if !g.snmpJobs[j.ID] {
		// Perhaps one this GUI just started, whose answer is still on its
		// way: kept for registerSNMPCheck (C1).
		g.keepSeen(j)
		return
	}
	g.showSNMPCheck(j)
}

// maxSeen is how many events of jobs not followed here are kept.
const maxSeen = 50

// keepSeen keeps the newest event of a job: events are sent outside the
// job's lock, so an older one can come last (crosscheck 2026-11-19-02 L1).
// When full, the oldest job is dropped, not all (L2).
func (g *GUI) keepSeen(j apitypes.SNMPCheck) {
	if old, ok := g.snmpSeen[j.ID]; ok {
		if old.State == apitypes.DiscoveryRunning && (j.State != apitypes.DiscoveryRunning || j.Checked >= old.Checked) {
			g.snmpSeen[j.ID] = j
		}
		return
	}
	if len(g.snmpSeenIDs) >= maxSeen {
		delete(g.snmpSeen, g.snmpSeenIDs[0])
		g.snmpSeenIDs = slices.Delete(g.snmpSeenIDs, 0, 1)
	}
	g.snmpSeen[j.ID] = j
	g.snmpSeenIDs = append(g.snmpSeenIDs, j.ID)
}

func (g *GUI) forgetSeen(id string) {
	if _, ok := g.snmpSeen[id]; ok {
		delete(g.snmpSeen, id)
		g.snmpSeenIDs = slices.DeleteFunc(g.snmpSeenIDs, func(s string) bool { return s == id })
	}
}

// communitiesText is the details row (SPEC-737): "2 set here", "from
// view switches", "from group lan", "the server's list"; for a group or
// view without a list "none set here (inherited)".
func (g *GUI) communitiesText(set int, from string) string {
	switch {
	case set > 0:
		return fmt.Sprintf("%d set here", set)
	case strings.HasPrefix(from, "view "):
		name := from
		if id, err := strconv.ParseInt(strings.TrimPrefix(from, "view "), 10, 64); err == nil {
			g.mu.Lock()
			if v, ok := g.model.View(id); ok {
				name = "view " + v.Name
			}
			g.mu.Unlock()
		}
		return "from " + name
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

// communitiesItem is "SNMP communities…" on a host, a network-view
// group or (in the View menu) a custom view.
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
	busy    []fyne.Disableable // disabled while saving (crosscheck 2026-11-19-02 C3)
	closed  bool               // closed with Cancel, perhaps while saving
	kind    string             // "host", "group" or "view", and its id
	id      int64
}

// closeCommunitiesFor closes the dialog when its host, group or view is
// deleted (crosscheck 2026-11-19-04 L4).
func (g *GUI) closeCommunitiesFor(ev client.Event) {
	cd := g.commDialog
	if cd == nil {
		return
	}
	kind := map[string]string{apitypes.EventHostDeleted: "host", apitypes.EventGroupDeleted: "group", apitypes.EventViewDeleted: "view"}[ev.Type]
	var del apitypes.DeletedEvent
	if kind == "" || kind != cd.kind || json.Unmarshal(ev.Data, &del) != nil || del.ID != cd.id {
		return
	}
	cd.closed = true
	cd.d.Hide()
	g.commDialog = nil
	g.status.message(fmt.Sprintf("SNMP communities: the %s was deleted", kind))
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
		} else if kind == "view" {
			v, err := g.c.View(g.ctx, id)
			if err != nil {
				g.fail("SNMP communities", err)
				return
			}
			set = v.SNMPCommunitiesSet
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
	cd := &communitiesDialog{info: widget.NewLabel(""), rows: container.NewVBox(), kind: kind, id: id}
	now := g.communitiesText(set, from)
	if kind == "view" && set == 0 { // L3
		now = "none set here; its hosts use their group's or the server's list"
	}
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
		if len(cd.entries) >= apitypes.MaxCommunities {
			cd.info.SetText(fmt.Sprintf("At most %d strings.", apitypes.MaxCommunities)) // L9
			return
		}
		addRow()
	})
	what := kind + " " + name
	cd.save = func(list []string) {
		if err := apitypes.CheckCommunities(list); err != nil {
			cd.info.SetText(err.Error())
			return
		}
		// The dialog stays open until the server took the list, so nothing
		// typed is lost on an error (L9). Save, Inherit and Add wait for
		// the answer: no second save (crosscheck 2026-11-19-02 C3).
		cd.info.SetText("Saving…")
		for _, b := range cd.busy {
			b.Disable()
		}
		go func() {
			var err error
			var text string
			if kind == "host" {
				var h apitypes.Host
				if h, err = g.c.UpdateHost(g.ctx, id, apitypes.HostPatch{SNMPCommunities: apitypes.Some(list)}); err == nil {
					text = g.communitiesText(h.SNMPCommunitiesSet, h.SNMPCommunitiesFrom)
				}
			} else if kind == "view" {
				var v apitypes.View
				if v, err = g.c.UpdateView(g.ctx, id, apitypes.ViewPatch{SNMPCommunities: apitypes.Some(list)}); err == nil {
					text = g.communitiesText(v.SNMPCommunitiesSet, "")
				}
			} else {
				var gr apitypes.Group
				if gr, err = g.c.UpdateGroup(g.ctx, id, apitypes.GroupPatch{SNMPCommunities: apitypes.Some(list)}); err == nil {
					text = g.communitiesText(gr.SNMPCommunitiesSet, "")
				}
			}
			g.do(func() {
				for _, b := range cd.busy {
					b.Enable()
				}
				switch {
				case err != nil && cd.closed: // Cancel came first: say it where it is seen (C3)
					g.status.message(fmt.Sprintf("SNMP communities of %s not saved: %v", what, err))
					return
				case err != nil:
					cd.info.SetText("Not saved: " + err.Error())
					return
				}
				cd.d.Hide()
				if g.commDialog == cd {
					g.commDialog = nil
				}
				g.status.message(fmt.Sprintf("SNMP communities of %s: %s", what, text))
			})
		}()
	}
	saveB := widget.NewButton("Save", func() {
		var list []string
		for _, e := range cd.entries {
			// As typed: spaces at the ends may belong to the string; only
			// empty rows are left out (crosscheck 2026-11-19-02 L3).
			if strings.TrimSpace(e.Text) != "" {
				list = append(list, e.Text)
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
	cancel := widget.NewButton("Cancel", func() { cd.closed = true; cd.d.Hide(); g.commDialog = nil })
	cd.busy = []fyne.Disableable{add, inherit, saveB}
	body := container.NewBorder(cd.info, container.NewHBox(add, inherit, cancel, saveB), nil, nil, container.NewVScroll(cd.rows))
	cd.d = dialog.NewCustomWithoutButtons("SNMP communities of "+what, body, g.win)
	cd.d.Resize(fyne.NewSize(460, 320))
	cd.d.Show()
	g.commDialog = cd
}
