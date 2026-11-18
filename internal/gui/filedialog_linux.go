//go:build linux

package gui

import (
	"net/url"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"github.com/rymdport/portal"
	"github.com/rymdport/portal/filechooser"
)

// On Linux the desktop's dialog comes through the XDG desktop portal
// (org.freedesktop.portal.FileChooser), as Fyne does in Flatpak builds:
// GNOME's or KDE's own dialog (Design-file-dialogs.md F1a). Without a
// portal the call fails at once and Fyne's dialog is used.

func init() {
	nativeSave = portalSave
	nativeOpen = portalOpen
	nativeParent = portalParent
}

// portalParent is "x11:<id>" for X11; "" elsewhere (on Wayland the dialog
// opens without a parent).
func portalParent(w fyne.Window) string {
	h := ""
	if nw, ok := w.(driver.NativeWindow); ok {
		nw.RunNative(func(ctx any) {
			if x, ok := ctx.(driver.X11WindowContext); ok {
				h = portal.FormatX11WindowHandle(x.WindowHandle)
			}
		})
	}
	return h
}

func portalFilter(exts []string) []*filechooser.Filter {
	if len(exts) == 0 {
		return nil
	}
	f := &filechooser.Filter{Name: strings.ToUpper(strings.TrimPrefix(exts[0], ".")) + " files"}
	for _, e := range exts {
		f.Rules = append(f.Rules,
			filechooser.Rule{Type: filechooser.GlobPattern, Pattern: "*" + strings.ToLower(e)},
			filechooser.Rule{Type: filechooser.GlobPattern, Pattern: "*" + strings.ToUpper(e)})
	}
	return []*filechooser.Filter{f}
}

func portalSave(parent, title, name string, exts []string) (string, error) {
	fs := portalFilter(exts)
	o := &filechooser.SaveFileOptions{CurrentName: name, Filters: fs}
	if len(fs) > 0 {
		o.CurrentFilter = fs[0]
	}
	uris, err := filechooser.SaveFile(parent, title, o)
	return firstPath(uris, err)
}

func portalOpen(parent, title string, exts []string) (string, error) {
	fs := portalFilter(exts)
	o := &filechooser.OpenFileOptions{Filters: fs}
	if len(fs) > 0 {
		o.CurrentFilter = fs[0]
	}
	uris, err := filechooser.OpenFile(parent, title, o)
	return firstPath(uris, err)
}

// firstPath turns the portal's file:// URI into a path; none = cancelled.
func firstPath(uris []string, err error) (string, error) {
	if err != nil || len(uris) == 0 {
		return "", err
	}
	u, err := url.Parse(uris[0])
	if err != nil {
		return "", err
	}
	return u.Path, nil
}
