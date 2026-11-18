package gui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// rescanChoices are the intervals of "Rescan ▸" (SPEC-735).
var rescanChoices = []struct {
	label string
	every time.Duration
}{
	{"Every hour", time.Hour},
	{"Every 6 hours", 6 * time.Hour},
	{"Every day", 24 * time.Hour},
	{"Every week", 7 * 24 * time.Hour},
}

// rescanItem is "Rescan ▸" of one or more IPv4 network groups (SPEC-206,
// SPEC-735): how often each is scanned again. A choice is checked when
// every group has it; an interval set elsewhere shows as an extra checked
// choice. nil when a group isn't an IPv4 network group.
func (g *GUI) rescanItem(groups []*apitypes.MapItem) *fyne.MenuItem {
	ids := make([]int64, len(groups))
	same := true
	for i, it := range groups {
		if it.CIDR == "" || strings.Contains(it.CIDR, ":") {
			return nil
		}
		ids[i] = it.ID
		same = same && it.Rescan == groups[0].Rescan
	}
	current := groups[0].Rescan
	choice := func(label, every string) *fyne.MenuItem {
		mi := fyne.NewMenuItem(label, func() {
			for _, id := range ids {
				g.setRescan(id, every)
			}
		})
		mi.Checked = same && current == every
		return mi
	}
	items := []*fyne.MenuItem{choice("Off", ""), fyne.NewMenuItemSeparator()}
	known := current == ""
	for _, c := range rescanChoices {
		every := apitypes.FormatRescan(c.every)
		items = append(items, choice(c.label, every))
		known = known || every == current
	}
	if same && !known {
		items = append(items, choice("Every "+current, current))
	}
	mi := fyne.NewMenuItem("Rescan", nil)
	if len(groups) > 1 {
		mi.Label = fmt.Sprintf("Rescan (%d groups)", len(groups))
	}
	mi.ChildMenu = fyne.NewMenu("", items...)
	return mi
}

// setRescan saves a group's rescan interval ("" for off); the
// group.updated event reloads the view.
func (g *GUI) setRescan(group int64, every string) {
	go func() {
		if _, err := g.c.UpdateGroup(g.ctx, group, apitypes.GroupPatch{Rescan: apitypes.Some(every)}); err != nil {
			g.fail(fmt.Sprintf("group %d", group), err)
		}
	}()
}

// rescanText is the details row of a network group (SPEC-735): "off",
// "every day, not scanned yet", "every day, last 03:15, 2 new".
func rescanText(gr *apitypes.Group) string {
	if gr.Rescan == "" {
		return "off"
	}
	every := "every " + gr.Rescan
	if d, err := apitypes.ParseRescan(gr.Rescan); err == nil {
		for _, c := range rescanChoices {
			if c.every == d {
				every = strings.ToLower(c.label)
			}
		}
	}
	if gr.RescanLast == nil {
		return every + ", not scanned yet"
	}
	return fmt.Sprintf("%s, last %s, %d new", every, shortTime(*gr.RescanLast), gr.RescanFound)
}
