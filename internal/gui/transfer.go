package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// Export and import of a network (SPEC-733; Design-network-export.md N10).
// The menu items open Fyne's file dialogs; exportTo and importFrom do the
// work, so tests can call them without the dialogs.

// exportItem is "Export…" on a group of the network view.
func (g *GUI) exportItem(id int64, name string) *fyne.MenuItem {
	return fyne.NewMenuItem("Export…", func() {
		g.saveFile("Export "+name, fileName(name)+".json", []string{".json"}, func(w io.WriteCloser, file string, err error) {
			if err != nil {
				g.status.message("Export: " + err.Error())
				return
			}
			g.exportTo(id, w, file)
		})
	})
}

// fileName makes a group name usable as a file name: "10.0.1.0/24" is
// "10.0.1.0_24".
func fileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < ' ' {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	if s == "" {
		return "network"
	}
	return s
}

// exportTo writes the export of group id to w and closes it; the status
// bar says how it went.
func (g *GUI) exportTo(id int64, w io.WriteCloser, name string) {
	go func() {
		f, err := g.c.ExportGroup(g.ctx, id)
		if err == nil {
			var b []byte
			b, err = json.MarshalIndent(f, "", "  ")
			if err == nil {
				_, err = w.Write(append(b, '\n'))
			}
		}
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		g.do(func() {
			if err != nil {
				g.status.message(fmt.Sprintf("Export to %s failed: %v", name, err))
				return
			}
			gs, hs := countGroups(f.Group)
			g.status.message(fmt.Sprintf("Exported %s to %s: %s, %s", f.From, name, nCount(gs, "group"), nCount(hs, "host")))
		})
	}()
}

func countGroups(e apitypes.ExportGroup) (groups, hosts int) {
	groups, hosts = 1, len(e.Hosts)
	for _, c := range e.Groups {
		cg, ch := countGroups(c)
		groups, hosts = groups+cg, hosts+ch
	}
	return groups, hosts
}

func nCount(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// importItem imports into parent (0 = the top level) of the network view,
// at x, y when given: "Import here…" at a clicked spot, "Import into this
// group…" on a group.
func (g *GUI) importItem(label string, parent int64, x, y *float64) *fyne.MenuItem {
	return fyne.NewMenuItem(label, func() {
		g.openFile("Import a network", []string{".json"}, 64<<20, func(data []byte, file string, err error) {
			if err != nil {
				g.status.message("Import: " + err.Error())
				return
			}
			g.importFrom(data, file, parent, x, y)
		})
	})
}

// importDialog is the open report of an import (for tests).
type importDialog struct {
	d       dialog.Dialog
	text    string // the report as shown
	confirm *widget.Button
	errL    *widget.Label
	done    bool
}

// importFrom asks the server for a dry run of the file, shows the report
// and imports when the user confirms: "Import", or with conflicts "Update
// existing and import" (N7, N8).
func (g *GUI) importFrom(data []byte, name string, parent int64, x, y *float64) {
	if !json.Valid(data) {
		g.status.message(fmt.Sprintf("Import: %s is not a JSON file", name))
		return
	}
	o := client.ImportOptions{ParentID: parent, X: x, Y: y, DryRun: true}
	go func() {
		rep, err := g.c.Import(g.ctx, json.RawMessage(data), o)
		g.do(func() {
			if err != nil {
				g.status.message(fmt.Sprintf("Import of %s: %v", name, err))
				g.showError("Import "+name, err)
				return
			}
			g.showImportReport(data, name, o, rep)
		})
	}()
}

func (g *GUI) showImportReport(data []byte, name string, o client.ImportOptions, rep apitypes.ImportReport) {
	conflicts := len(rep.Conflicts) > 0
	text := strings.Join(rep.Lines(), "\n")
	where := "at the top level"
	if o.ParentID != 0 {
		where = fmt.Sprintf("into group %d", o.ParentID)
		g.mu.Lock()
		if it := g.item(mapmodel.NetworkView, mapmodel.GroupKey(o.ParentID)); it != nil {
			where = fmt.Sprintf("into %q", it.Name)
		}
		g.mu.Unlock()
	}
	intro := fmt.Sprintf("Importing %s %s:", name, where)
	if conflicts {
		intro += "\nParts of it are there already. \"Update existing and import\" updates them from the file and adds the rest; nothing else is deleted or moved."
	}
	head := widget.NewLabel(intro)
	head.Wrapping = fyne.TextWrapWord
	body := widget.NewLabel(text)
	body.Selectable = true // the conflicts can be copied
	body.Wrapping = fyne.TextWrapWord
	im := &importDialog{text: text, errL: widget.NewLabel("")}
	im.errL.Wrapping = fyne.TextWrapWord
	im.errL.Importance = widget.DangerImportance
	label := "Import"
	if conflicts {
		label = "Update existing and import"
	}
	cancel := widget.NewButton("Cancel", func() { im.d.Hide() })
	im.confirm = widget.NewButton(label, func() {
		im.confirm.Disable()
		im.errL.SetText("")
		real := o
		real.DryRun, real.Update = false, conflicts
		go func() {
			rep, err := g.c.Import(g.ctx, json.RawMessage(data), real)
			g.do(func() {
				im.confirm.Enable()
				if err != nil {
					im.errL.SetText("Error: " + err.Error())
					return
				}
				im.done = true
				im.d.Hide()
				g.status.message(fmt.Sprintf("Imported %s: %s", name, strings.Join(rep.Lines(), " ")))
			})
		}()
	})
	im.confirm.Importance = widget.HighImportance
	content := container.NewBorder(head, container.NewVBox(im.errL, container.NewHBox(cancel, im.confirm)), nil, nil,
		container.NewVScroll(body))
	im.d = dialog.NewCustomWithoutButtons("Import "+name, content, g.win)
	im.d.Resize(fyne.NewSize(640, 420))
	im.d.Show()
	g.lastImport = im
}

// showError shows an error from the server in a dialog.
func (g *GUI) showError(title string, err error) {
	var ae *client.APIError
	msg := err.Error()
	if errors.As(err, &ae) && ae.Status != http.StatusConflict {
		msg = ae.Message
	}
	l := widget.NewLabel(msg)
	l.Wrapping = fyne.TextWrapWord
	l.Selectable = true
	d := dialog.NewCustom(title, "Close", l, g.win)
	d.Resize(fyne.NewSize(520, 0))
	d.Show()
}
