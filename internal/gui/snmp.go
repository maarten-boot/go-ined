package gui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// snmpRows are the details rows of a host's SNMP agent (SPEC-736): "SNMP"
// always; Location, CPU, Memory, Disks and Uptime when known.
func snmpRows(h apitypes.Host) [][2]string {
	s := h.SNMP
	switch {
	case s == nil:
		return [][2]string{{"SNMP", "not checked yet"}}
	case s.Version == "":
		return [][2]string{{"SNMP", "no answer · checked " + shortTime(s.CheckedAt)}}
	}
	head := []string{s.Version}
	for _, p := range []string{s.Name, s.Descr} {
		if p != "" {
			head = append(head, p)
		}
	}
	rows := [][2]string{{"SNMP", strings.Join(head, " · ")}}
	if s.Location != "" {
		rows = append(rows, [2]string{"Location", s.Location})
	}
	v := s.Values
	if v == nil {
		return rows
	}
	if v.CPUPct != nil {
		rows = append(rows, [2]string{"CPU", client.CPUText(*v)})
	}
	var mem, disks []string
	for _, st := range v.Storage {
		if st.Kind == apitypes.StorageMemory {
			mem = append(mem, fmt.Sprintf("%d %%", st.UsedPct))
		} else {
			disks = append(disks, fmt.Sprintf("%s %d %%", st.Name, st.UsedPct))
		}
	}
	if len(mem) > 0 {
		rows = append(rows, [2]string{"Memory", strings.Join(mem, ", ")})
	}
	if len(disks) > 0 {
		rows = append(rows, [2]string{"Disks", strings.Join(disks, ", ")})
	}
	return append(rows, [2]string{"Uptime", client.UptimeText(v.UptimeS) + " · read " + shortTime(v.At)})
}

// checkSNMPItem is "Check SNMP now" on a host (SPEC-736).
func (g *GUI) checkSNMPItem(h apitypes.Host) *fyne.MenuItem {
	return fyne.NewMenuItem("Check SNMP now", func() { g.checkSNMP(h) })
}

// checkSNMP finds the host's agent and reads its values; the result goes
// to the status bar and the details.
func (g *GUI) checkSNMP(h apitypes.Host) {
	g.status.message(fmt.Sprintf("Checking SNMP on %s…", h.DisplayName))
	go func() {
		got, err := g.c.CheckSNMP(g.ctx, h.ID)
		if err != nil {
			g.fail("Checking SNMP on "+h.DisplayName, err)
			return
		}
		g.do(func() {
			g.status.message(fmt.Sprintf("SNMP on %s: %s", got.DisplayName, snmpRows(got)[0][1]))
			g.details.hostChanged(got)
		})
	}()
}
