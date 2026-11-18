package gui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fyne.io/fyne/v2"
	fynetheme "fyne.io/fyne/v2/theme"

	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// useNative makes the desktop's save dialog answer file (for the test).
func useNative(t *testing.T, file string) {
	saved := nativeSave
	t.Cleanup(func() { nativeSave = saved })
	nativeSave = func(_, _, _ string, _ []string) (string, error) { return file, nil }
}

// TC-752: export and import use the desktop's file dialog when there is
// one: the network is read from the server before the dialog opens and a
// failing server leaves the file as it was; the file is written with mode
// 0600 (also over an existing file) and named in the status bar; a chosen
// file is imported with its report; a cancel does nothing; a second file
// dialog while one is open is refused; an error of the desktop's dialog
// (also a panic in it) is reported, and only a desktop without a dialog of
// its own gets Fyne's dialog (Design-file-dialogs.md F1a, F2; crosscheck
// 2026-11-18-01 C2–C6, L1, L11).
func TestFileDialogs(t *testing.T) {
	saved, opened := nativeSave, nativeOpen
	t.Cleanup(func() { nativeSave, nativeOpen = saved, opened })
	dir := t.TempDir()
	file := filepath.Join(dir, "lan.json")
	var mu sync.Mutex
	var asked []string
	nativeSave = func(_, title, name string, exts []string) (string, error) {
		mu.Lock()
		asked = append(asked, title+"|"+name+"|"+strings.Join(exts, ","))
		mu.Unlock()
		return file, nil
	}

	e := start(t)
	c, ctx := e.c, e.ctx
	lan := must(c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.1.0/24", Name: "lan"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return find(e.nodes(1), "group", lan.ID) != nil })

	// An existing file of mode 0644 is replaced by one of mode 0600.
	if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ui.Wait(func() { g.exportItem(lan.ID, "lan").Action() })
	eventually(t, e.ui, "exported", func() bool { return strings.Contains(g.status.Text(), "to lan.json") })
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 || st.Size() < 10 {
		t.Fatalf("file: %v %v", st, err)
	}
	mu.Lock()
	if len(asked) != 1 || asked[0] != "Export lan|lan.json|.json" {
		t.Fatalf("asked %q", asked)
	}
	mu.Unlock()
	written := must(os.ReadFile(file))
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Fatalf("temporary files left: %q", left)
	}

	// The server fails: no dialog, the file is as it was (C3).
	e.ui.Wait(func() { g.exportItem(999999, "gone").Action() })
	eventually(t, e.ui, "export failed", func() bool { return strings.Contains(g.status.Text(), "Export of gone failed") })
	mu.Lock()
	if len(asked) != 1 {
		t.Errorf("a dialog for a failed export: %q", asked)
	}
	mu.Unlock()
	if b := must(os.ReadFile(file)); string(b) != string(written) {
		t.Error("the file changed")
	}

	// Import the file: the dry run's report opens.
	nativeOpen = func(_, title string, exts []string) (string, error) { return file, nil }
	e.ui.Wait(func() { g.importItem("Import here…", 0, nil, nil).Action() })
	eventually(t, e.ui, "report", func() bool { return g.lastImport != nil && strings.Contains(g.lastImport.text, "Conflicts (") })

	// Cancelled: nothing.
	nativeSave = func(_, _, _ string, _ []string) (string, error) { return "", nil }
	e.ui.Wait(func() { g.exportItem(lan.ID, "lan").Action() })
	eventually(t, e.ui, "cancelled", func() bool { return !g.fileDialog })
	e.ui.Wait(func() {
		if strings.Contains(g.status.Text(), "Export") || g.lastFyneDialog != nil {
			t.Errorf("after a cancel: %q %v", g.status.Text(), g.lastFyneDialog)
		}
	})

	// One file dialog at a time (C5).
	release := make(chan struct{})
	nativeSave = func(_, _, _ string, _ []string) (string, error) { <-release; return "", nil }
	e.ui.Wait(func() { g.exportItem(lan.ID, "lan").Action() })
	eventually(t, e.ui, "dialog open", func() bool { return g.fileDialog })
	e.ui.Wait(func() { g.importItem("Import here…", 0, nil, nil).Action() })
	eventually(t, e.ui, "refused", func() bool { return strings.Contains(g.status.Text(), "A file dialog is open") })
	close(release)
	eventually(t, e.ui, "closed", func() bool { return !g.fileDialog })

	// An error after the dialog opened, or a panic in it: reported, no
	// second dialog (C2, C4).
	nativeOpen = func(_, _ string, _ []string) (string, error) { return "", errors.New("the portal said no") }
	e.ui.Wait(func() { g.importItem("Import here…", 0, nil, nil).Action() })
	eventually(t, e.ui, "error", func() bool { return strings.Contains(g.status.Text(), "Import: the portal said no") })
	nativeSave = func(_, _, _ string, _ []string) (string, error) { panic("no uris") }
	e.ui.Wait(func() { g.exportItem(lan.ID, "lan").Action() })
	eventually(t, e.ui, "panic", func() bool {
		return strings.Contains(g.status.Text(), "Export to lan failed: the file dialog failed: no uris")
	})
	e.ui.Wait(func() {
		if g.lastFyneDialog != nil || g.fileDialog {
			t.Errorf("Fyne's dialog %v, open %v", g.lastFyneDialog, g.fileDialog)
		}
	})

	// No dialog of the desktop's own: Fyne's; it holds the claim until
	// closed.
	nativeSave = func(_, _, _ string, _ []string) (string, error) { return "", errNoNative }
	e.ui.Wait(func() { g.exportItem(lan.ID, "lan").Action() })
	eventually(t, e.ui, "Fyne's save dialog", func() bool { return g.lastFyneDialog != nil })
	e.ui.Wait(func() {
		if !g.fileDialog {
			t.Error("not claimed")
		}
		g.lastFyneDialog.Hide()
		g.lastFyneDialog = nil
		if g.fileDialog {
			t.Error("still claimed after closing")
		}
	})
	nativeOpen = func(_, _ string, _ []string) (string, error) { return "", fmt.Errorf("no portal: %w", errNoNative) }
	e.ui.Wait(func() { g.importItem("Import here…", 0, nil, nil).Action() })
	eventually(t, e.ui, "Fyne's open dialog", func() bool { return g.lastFyneDialog != nil })
	e.ui.Wait(func() { g.lastFyneDialog.Hide() })
}

// TC-752: a file larger than the limit is an error that says so, not a
// cut-off file (crosscheck 2026-11-18-01 L1); a device is written in
// place.
func TestReadAtMost(t *testing.T) {
	if b, err := readAtMost(strings.NewReader("1234"), 4); err != nil || string(b) != "1234" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := readAtMost(strings.NewReader(strings.Repeat("x", 3<<20)), 2<<20); err == nil || err.Error() != "the file is larger than 2 MiB" {
		t.Fatalf("%v", err)
	}
	if err := writeFileSafely(os.DevNull, []byte("x")); err != nil {
		t.Fatalf("%s: %v", os.DevNull, err)
	}
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
