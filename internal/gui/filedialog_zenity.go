//go:build windows || darwin

package gui

import (
	"errors"
	"strings"

	"github.com/ncruces/zenity"
)

// On Windows and macOS the system's dialog comes through zenity: the
// Windows Save/Open dialog, the macOS panel (Design-file-dialogs.md F1a).

func init() {
	nativeSave = zenitySave
	nativeOpen = zenityOpen
}

func zenityFilter(exts []string) zenity.FileFilters {
	if len(exts) == 0 {
		return nil
	}
	f := zenity.FileFilter{Name: strings.ToUpper(strings.TrimPrefix(exts[0], ".")) + " files", CaseFold: true}
	for _, e := range exts {
		f.Patterns = append(f.Patterns, "*"+e)
	}
	return zenity.FileFilters{f}
}

func zenitySave(_, title, name string, exts []string) (string, error) {
	p, err := zenity.SelectFileSave(zenity.Title(title), zenity.Filename(name), zenity.ConfirmOverwrite(), zenityFilter(exts))
	if errors.Is(err, zenity.ErrCanceled) {
		return "", nil
	}
	return p, err
}

func zenityOpen(_, title string, exts []string) (string, error) {
	p, err := zenity.SelectFile(zenity.Title(title), zenityFilter(exts))
	if errors.Is(err, zenity.ErrCanceled) {
		return "", nil
	}
	return p, err
}
