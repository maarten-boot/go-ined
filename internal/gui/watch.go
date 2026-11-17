package gui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Watched services in the GUI (SPEC-732; Design-service-alerts.md S6, S7).

// watchedText is the details panel's "Watched" row: "ssh up, https down
// since 14:07, tcp:5432 unknown", or "none".
func watchedText(ws []apitypes.WatchedService) string {
	if len(ws) == 0 {
		return "none"
	}
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = apitypes.ServiceLabel(w.Name) + " " + w.Status
		if w.Status == apitypes.StatusDown && w.LastChange != nil {
			parts[i] += " since " + shortTime(*w.LastChange)
		}
	}
	return strings.Join(parts, ", ")
}

// shortTime is 14:07 today, else 2026-11-15 14:07 (local time).
func shortTime(t time.Time) string {
	t = t.Local()
	if y, m, d := time.Now().Date(); t.Year() != y || t.Month() != m || t.Day() != d {
		return t.Format(time.DateTime[:16])
	}
	return t.Format("15:04")
}

// watchItem is the host menu's "Watch services…", or "Watch services on
// N hosts…" when the menu acts on a selection with several hosts.
func (g *GUI) watchItem(hosts []int64) *fyne.MenuItem {
	label := "Watch services…"
	if len(hosts) > 1 {
		label = fmt.Sprintf("Watch services on %d hosts…", len(hosts))
	}
	return fyne.NewMenuItem(label, func() { g.watchDialog(hosts) })
}

// selectedHosts returns the hosts among keys, in order.
func selectedHosts(keys []mapmodel.Key) []int64 {
	var ids []int64
	for _, k := range keys {
		if k.Type == apitypes.TypeHost {
			ids = append(ids, k.ID)
		}
	}
	return ids
}

// watchDialog loads the hosts, then opens the dialog.
func (g *GUI) watchDialog(ids []int64) {
	go func() {
		hosts := make([]apitypes.Host, 0, len(ids))
		for _, id := range ids {
			h, err := g.c.Host(g.ctx, id)
			if err != nil {
				g.do(func() { g.status.message(fmt.Sprintf("host %d: %v", id, err)) })
				return
			}
			hosts = append(hosts, h)
		}
		g.do(func() { g.openWatchDialog(hosts) })
	}()
}

// watchForm is the open "Watch services" dialog (for tests).
type watchForm struct {
	*formDialog
	checks map[string]*widget.Check // by service name
	other  *widget.Entry            // other TCP ports
	note   string                   // what the dialog says about differing hosts
}

// openWatchDialog shows a tick per known service, marked "answered" where
// the last service check found it, and a field for other TCP ports. With
// several hosts, a tick is set only where all hosts watch the service, and
// saving sets the same list on all of them.
func (g *GUI) openWatchDialog(hosts []apitypes.Host) *watchForm {
	n := len(hosts)
	watching := map[string]int{} // name -> hosts watching it
	answered := map[string]int{} // name -> hosts where it answered
	for _, h := range hosts {
		for _, w := range h.Watch {
			watching[w.Name]++
		}
		for _, s := range h.Services {
			answered[s]++
		}
	}
	differ := false
	for _, c := range watching {
		differ = differ || c != n
	}
	f := &watchForm{checks: map[string]*widget.Check{}, other: widget.NewEntry()}
	var ticks []fyne.CanvasObject
	for _, s := range apitypes.KnownServices() {
		label := fmt.Sprintf("%s (%d)", apitypes.ServiceLabel(s.Name), s.Port)
		switch c := answered[s.Name]; {
		case c > 0 && n == 1:
			label += " · answered at the last check"
		case c > 0:
			label += fmt.Sprintf(" · answered on %d of %d", c, n)
		}
		ch := widget.NewCheck(label, nil)
		ch.SetChecked(watching[s.Name] == n)
		f.checks[s.Name] = ch
		ticks = append(ticks, ch)
	}
	var ports []string
	for _, w := range hosts[0].Watch {
		if p, ok := strings.CutPrefix(w.Name, "tcp:"); ok && watching[w.Name] == n {
			ports = append(ports, p)
		}
	}
	f.other.SetText(strings.Join(ports, ", "))
	f.other.SetPlaceHolder("for example 5432, 8080")
	items := []*widget.FormItem{
		widget.NewFormItem("Services", container.NewGridWithColumns(2, ticks...)),
		widget.NewFormItem("Other TCP ports", f.other),
	}
	title := "Watch services on " + hosts[0].DisplayName
	if n > 1 {
		title = fmt.Sprintf("Watch services on %d hosts", n)
		f.note = fmt.Sprintf("Saving sets this list on all %d hosts.", n)
		if differ {
			f.note += " They watch different services now; a service only some of them watch is shown unticked."
		}
		l := widget.NewLabel(f.note)
		l.Wrapping = fyne.TextWrapWord
		items = append(items, widget.NewFormItem("", l))
	}
	ids := make([]int64, n)
	for i, h := range hosts {
		ids[i] = h.ID
	}
	f.formDialog = g.showForm(title, "Save", items, func() error {
		list, err := f.list()
		if err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := g.c.UpdateHost(g.ctx, id, apitypes.HostPatch{Watch: apitypes.Some(list)}); err != nil {
				if n > 1 {
					return fmt.Errorf("%s: %w (saved on %d of %d hosts)", hosts[i].DisplayName, err, i, n)
				}
				return err
			}
		}
		return nil
	})
	f.d.Resize(fyne.NewSize(620, 0))
	g.lastWatch = f
	return f
}

// tcpBlank is "tcp:" with blanks after it, as in "tcp: 5432".
var tcpBlank = regexp.MustCompile(`(?i)tcp:\s+`)

// list is the watch list the dialog shows: the ticked services in their
// usual order, then the other ports as tcp:N; checked as the server would.
func (f *watchForm) list() ([]string, error) {
	list := []string{}
	for _, s := range apitypes.KnownServices() {
		if f.checks[s.Name].Checked {
			list = append(list, s.Name)
		}
	}
	for p := range strings.FieldsFuncSeq(tcpBlank.ReplaceAllString(f.other.Text, "tcp:"), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		p = strings.TrimPrefix(strings.ToLower(p), "tcp:")
		if _, err := strconv.Atoi(p); err != nil {
			return nil, fmt.Errorf("other TCP ports: %q is not a port number", p)
		}
		list = append(list, "tcp:"+p)
	}
	ps, err := apitypes.ParseWatchList(list)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out, nil
}
