package main

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Tables and detail views (SPEC-902). Status is always written out as a
// word, never shown by colour alone.

func (a *app) table(header string, rows [][]string) {
	tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func (a *app) hostTable(hs []apitypes.Host) {
	slices.SortFunc(hs, func(x, y apitypes.Host) int { return cmp.Compare(x.ID, y.ID) })
	rows := make([][]string, len(hs))
	for i, h := range hs {
		rows[i] = []string{id(h.ID), h.IP, h.DisplayName, status(h.Status, h.Stale), ms(h.RTTms), parent(h.ParentID), yesNo(h.Monitored)}
	}
	a.table("ID\tIP\tNAME\tSTATUS\tRTT\tPARENT\tMON", rows)
}

func (a *app) groupTable(gs []apitypes.Group) {
	slices.SortFunc(gs, func(x, y apitypes.Group) int { return cmp.Compare(x.ID, y.ID) })
	rows := make([][]string, len(gs))
	for i, g := range gs {
		rows[i] = []string{id(g.ID), g.Name, dash(g.CIDR), g.Status, parent(g.ParentID), yesNo(g.Collapsed), id(g.ViewID)}
	}
	a.table("ID\tNAME\tCIDR\tSTATUS\tPARENT\tCOLLAPSED\tVIEW", rows)
}

func (a *app) hostDetails(h apitypes.Host) {
	loss := "-"
	if h.LossPct != nil {
		loss = strconv.FormatFloat(*h.LossPct, 'f', 0, 64) + " %"
	}
	a.details([][2]string{
		{"ID", id(h.ID)}, {"IP", h.IP}, {"Name", h.Name}, {"PTR name", h.PTRName}, {"Display name", h.DisplayName},
		{"Description", h.Description}, {"Parent", parent(h.ParentID)}, {"Monitored", yesNo(h.Monitored)},
		{"Status", status(h.Status, h.Stale)}, {"RTT", ms(h.RTTms)}, {"Loss", loss},
		{"Last seen", when(h.LastSeen)}, {"Last change", when(h.LastChange)},
		{"Outside CIDR", yesNo(h.OutsideCIDR)}, {"Position", pos(h.X, h.Y)},
		{"Created", h.CreatedAt.Local().Format(time.DateTime)}, {"Updated", h.UpdatedAt.Local().Format(time.DateTime)},
		{"Note", noteInline(h.Note)},
	}, false)
	a.noteBlock(h.Note)
}

func (a *app) groupDetails(g apitypes.Group) {
	a.details([][2]string{
		{"ID", id(g.ID)}, {"Name", g.Name}, {"CIDR", dash(g.CIDR)}, {"Description", g.Description},
		{"View", id(g.ViewID)}, {"Parent", parent(g.ParentID)}, {"Status", g.Status}, {"Collapsed", yesNo(g.Collapsed)},
		{"Labels", labelsText(g.Labels)}, {"Bar colour", colorText(g.Color)}, {"Position", pos(g.X, g.Y)},
		{"Created", g.CreatedAt.Local().Format(time.DateTime)}, {"Updated", g.UpdatedAt.Local().Format(time.DateTime)},
		{"Note", noteInline(g.Note)},
	}, false)
	a.noteBlock(g.Note)
}

// details prints "Key: value" lines; skipEmpty leaves out empty values.
func (a *app) details(kv [][2]string, skipEmpty bool) {
	tw := tabwriter.NewWriter(a.out, 0, 0, 1, ' ', 0)
	for _, p := range kv {
		switch {
		case p[1] != "":
			fmt.Fprintf(tw, "%s:\t%s\n", p[0], p[1])
		case skipEmpty:
		case p[0] == "Note": // a multi-line note follows (noteBlock)
			fmt.Fprintf(tw, "%s:\n", p[0])
		default:
			fmt.Fprintf(tw, "%s:\t-\n", p[0])
		}
	}
	tw.Flush()
}

// noteInline is the note's value on the "Note:" line: the note itself if
// it is one line, else empty (noteBlock prints it below).
func noteInline(n string) string {
	n = strings.TrimRight(n, "\n")
	switch {
	case n == "":
		return "-"
	case strings.Contains(n, "\n"):
		return ""
	}
	return n
}

func (a *app) noteBlock(n string) {
	if noteInline(n) != "" {
		return
	}
	for _, l := range strings.Split(strings.TrimRight(n, "\n"), "\n") {
		fmt.Fprintln(a.out, "  "+l)
	}
}

func id(n int64) string { return strconv.FormatInt(n, 10) }

func parent(p *int64) string {
	if p == nil {
		return "root"
	}
	return id(*p)
}

func status(s string, stale bool) string {
	if stale {
		return s + " (stale)"
	}
	return s
}

func ms(v *float64) string {
	if v == nil {
		return "-"
	}
	prec := 1
	if *v < 1 {
		prec = 2 // loopback and LAN times are often below 0.1 ms
	}
	return strconv.FormatFloat(*v, 'f', prec, 64) + " ms"
}

func when(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format(time.DateTime)
}

func pos(x, y float64) string {
	return strconv.FormatFloat(x, 'f', -1, 64) + ", " + strconv.FormatFloat(y, 'f', -1, 64)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *app) viewTable(vs []apitypes.View) {
	rows := make([][]string, len(vs))
	for i, v := range vs {
		rows[i] = []string{id(v.ID), v.Name, v.Kind, strconv.FormatInt(v.Order, 10)}
	}
	a.table("ID\tNAME\tKIND\tORDER", rows) // in tab order, as the API lists them
}

func (a *app) viewDetails(v apitypes.View) {
	a.details([][2]string{
		{"ID", id(v.ID)}, {"Name", v.Name}, {"Kind", v.Kind}, {"Order", strconv.FormatInt(v.Order, 10)},
		{"Created", v.CreatedAt.Local().Format(time.DateTime)}, {"Updated", v.UpdatedAt.Local().Format(time.DateTime)},
	}, false)
}

// viewHosts prints the hosts of a view's map, sorted by ID.
func (a *app) viewHosts(m apitypes.Map) {
	var rows [][]string
	for _, it := range m.Items {
		if it.Type == apitypes.TypeHost {
			rows = append(rows, []string{id(it.ID), it.IP, it.Name, status(it.Status, it.Stale), parent(it.ParentID)})
		}
	}
	slices.SortFunc(rows, func(x, y []string) int {
		n, _ := strconv.Atoi(x[0])
		k, _ := strconv.Atoi(y[0])
		return cmp.Compare(n, k)
	})
	a.table("ID\tIP\tNAME\tSTATUS\tPARENT", rows)
}

// labelsText describes a group's host label mode (SPEC-725).
func labelsText(mode string) string {
	switch mode {
	case apitypes.LabelsInherit:
		return "inherit"
	case apitypes.LabelsBoth:
		return "name and IP"
	default:
		return mode
	}
}

// colorText describes a group's bar colour (SPEC-727).
func colorText(c string) string {
	if c == "" {
		return "none"
	}
	return c
}
