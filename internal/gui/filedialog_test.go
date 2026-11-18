package gui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	fynetheme "fyne.io/fyne/v2/theme"

	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-752: export and import use the desktop's file dialog when there is
// one: the file is written with mode 0600 and named in the status bar, a
// chosen file is imported with its report; a cancel does nothing; when the
// desktop has no dialog (or it fails) Fyne's dialog opens instead
// (Design-file-dialogs.md F1a, F2).
func TestFileDialogs(t *testing.T) {
	saved, opened := nativeSave, nativeOpen
	t.Cleanup(func() { nativeSave, nativeOpen = saved, opened })
	dir := t.TempDir()
	file := filepath.Join(dir, "lan.json")
	var asked []string
	nativeSave = func(_, title, name string, exts []string) (string, error) {
		asked = append(asked, title+"|"+name+"|"+strings.Join(exts, ","))
		return file, nil
	}

	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.1.0/24", Name: "lan"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "group", lan.ID) != nil })

	e.ui.Wait(func() { g.exportItem(lan.ID, "lan").Action() })
	eventually(t, e.ui, "exported", func() bool { return strings.Contains(g.status.Text(), "to lan.json") })
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 || st.Size() == 0 {
		t.Fatalf("file: %v %v", st, err)
	}
	if len(asked) != 1 || asked[0] != "Export lan|lan.json|.json" {
		t.Fatalf("asked %q", asked)
	}

	// Import the file: the dry run's report opens.
	nativeOpen = func(_, title string, exts []string) (string, error) { return file, nil }
	e.ui.Wait(func() { g.importItem("Import here…", 0, nil, nil).Action() })
	eventually(t, e.ui, "report", func() bool { return g.lastImport != nil && strings.Contains(g.lastImport.text, "Conflicts (") })

	// Cancelled: nothing.
	nativeSave = func(_, _, _ string, _ []string) (string, error) { return "", nil }
	e.ui.Wait(func() { g.status.message("idle"); g.exportItem(lan.ID, "lan").Action() })
	e.ui.Wait(func() {})
	e.ui.Wait(func() {
		if !strings.Contains(g.status.Text(), "idle") || strings.Contains(g.status.Text(), "Export") || g.lastFyneDialog != nil {
			t.Errorf("after a cancel: %q %v", g.status.Text(), g.lastFyneDialog)
		}
	})

	// No dialog of the desktop's own: Fyne's.
	nativeSave = func(_, _, _ string, _ []string) (string, error) { return "", errNoNative }
	e.ui.Wait(func() { g.exportItem(lan.ID, "lan").Action() })
	eventually(t, e.ui, "Fyne's save dialog", func() bool { return g.lastFyneDialog != nil })
	e.ui.Wait(func() { g.lastFyneDialog.Hide(); g.lastFyneDialog = nil })
	nativeOpen = func(_, _ string, _ []string) (string, error) { return "", errors.New("portal gone") }
	e.ui.Wait(func() { g.importItem("Import here…", 0, nil, nil).Action() })
	eventually(t, e.ui, "Fyne's open dialog", func() bool { return g.lastFyneDialog != nil })
	e.ui.Wait(func() { g.lastFyneDialog.Hide() })
}

// TC-753: folders are an outline in the text colour, in every preset
// (Design-file-dialogs.md F3).
func TestFolderOutline(t *testing.T) {
	if _, ok := folderOutline.(*fynetheme.ThemedResource); !ok {
		t.Fatalf("not themed: %T", folderOutline)
	}
	for _, preset := range []string{theme.PresetAA, theme.PresetAAA, theme.PresetMax} {
		ft := newFyneTheme(theme.NewPalette(theme.Contrast{Preset: preset}))
		for _, n := range []fyne.ThemeIconName{fynetheme.IconNameFolder, fynetheme.IconNameFolderOpen} {
			if ft.Icon(n) != folderOutline {
				t.Errorf("%s %s: %v", preset, n, ft.Icon(n))
			}
		}
		if ft.Icon(fynetheme.IconNameFile) == folderOutline {
			t.Error("other icons are Fyne's")
		}
	}
}
