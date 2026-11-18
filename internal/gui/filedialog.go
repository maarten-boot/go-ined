package gui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
)

// File dialogs (Design-file-dialogs.md F1a, F2; SPEC-733): the desktop's
// own dialog where there is one, a real window that can be moved and
// resized; Fyne's dialog, drawn inside the main window at 90 % of its
// size, where the desktop's can't be opened. One file dialog at a time.

// errNoNative: this platform or desktop has no dialog of its own, or it
// can't be opened. Only this error makes Fyne's dialog open instead; any
// other error of the desktop's dialog is reported (crosscheck
// 2026-11-18-01 C4).
var errNoNative = errors.New("no native file dialog")

// nativeSave and nativeOpen ask the desktop for a file. They block until
// the user is done; "" with a nil error means cancelled. parent is the
// window handle the desktop needs (see nativeParent). Set per platform;
// tests replace them.
var (
	nativeSave = func(parent, title, name string, exts []string) (string, error) { return "", errNoNative }
	nativeOpen = func(parent, title string, exts []string) (string, error) { return "", errNoNative }
	// nativeParent is called on the UI goroutine for the main window's handle.
	nativeParent = func(fyne.Window) string { return "" }
)

// fyneDialogShare is the part of the main window Fyne's dialog takes (F2).
const fyneDialogShare = 0.9

// writeFunc writes the chosen file in one go.
type writeFunc func(data []byte) error

// claimFileDialog marks a file dialog open; false when one is open
// already, which the status bar then says (C5). UI goroutine.
func (g *GUI) claimFileDialog() bool {
	if g.fileDialog {
		g.status.message("A file dialog is open")
		return false
	}
	g.fileDialog = true
	return true
}

// saveFile asks where to save: name is the suggested file name, exts the
// file types offered (".json"). done gets a function that writes the
// chosen file (nothing is written or emptied before it is called), or an
// error; it isn't called when the user cancels. UI goroutine, also for
// done.
func (g *GUI) saveFile(title, name string, exts []string, done func(write writeFunc, file string, err error)) {
	if !g.claimFileDialog() {
		return
	}
	parent, ask := nativeParent(g.win), nativeSave // taken here: tests replace them (L11)
	go func() {
		path, err := safeNative(func() (string, error) { return ask(parent, title, name, exts) })
		g.do(func() {
			if errors.Is(err, errNoNative) {
				g.fyneSave(name, exts, done) // the dialog stays claimed until it closes
				return
			}
			g.fileDialog = false
			switch {
			case err != nil:
				done(nil, "", err)
			case path != "": // "" is cancelled
				done(func(data []byte) error { return writeFileSafely(path, data) }, baseName(path), nil)
			}
		})
	}()
}

// openFile asks for a file to read; done gets its contents (at most max
// bytes, more is an error) or an error, on the UI goroutine.
func (g *GUI) openFile(title string, exts []string, max int64, done func(data []byte, file string, err error)) {
	if !g.claimFileDialog() {
		return
	}
	parent, ask := nativeParent(g.win), nativeOpen
	go func() {
		path, err := safeNative(func() (string, error) { return ask(parent, title, exts) })
		var data []byte
		if err == nil && path != "" {
			data, err = readLimited(path, max)
		}
		g.do(func() {
			if errors.Is(err, errNoNative) {
				g.fyneOpen(exts, max, done)
				return
			}
			g.fileDialog = false
			switch {
			case err != nil:
				done(nil, "", err)
			case path != "":
				done(data, baseName(path), nil)
			}
		})
	}()
}

// safeNative runs the desktop's dialog; a panic in it (a library that
// trips over an answer it doesn't expect) is an error, not the end of the
// GUI (C2).
func safeNative(ask func() (string, error)) (path string, err error) {
	defer func() {
		if r := recover(); r != nil {
			path, err = "", fmt.Errorf("the file dialog failed: %v", r)
		}
	}()
	return ask()
}

// writeFileSafely writes data to path through a temporary file in the
// same folder, renamed over path when it is complete, so an earlier file
// stays as it was when the write fails (C3). The file gets mode 0600, as
// inedctl's -o (SPEC-905; C6). Where no temporary file can be made (a
// folder without write access) path is written directly.
func writeFileSafely(path string, data []byte) error {
	if st, err := os.Stat(path); err == nil && !st.Mode().IsRegular() {
		return writeDirect(path, data) // a device or pipe: no rename over it
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp") // mode 0600
	if err != nil {
		return writeDirect(path, data)
	}
	_, err = tmp.Write(data)
	if serr := tmp.Sync(); err == nil {
		err = serr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func writeDirect(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// readLimited reads at most max bytes; a larger file is an error that
// says so (crosscheck 2026-11-18-01 L1).
func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAtMost(f, max)
}

func readAtMost(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err == nil && int64(len(data)) > max {
		return nil, fmt.Errorf("the file is larger than %d MiB", max>>20)
	}
	return data, err
}

func baseName(path string) string {
	return storage.NewFileURI(path).Name()
}

// fyneSize is the size of Fyne's dialog: most of the main window (F2).
func (g *GUI) fyneSize() fyne.Size {
	s := g.win.Canvas().Size()
	return fyne.NewSize(s.Width*fyneDialogShare, s.Height*fyneDialogShare)
}

func (g *GUI) fyneSave(name string, exts []string, done func(write writeFunc, file string, err error)) {
	d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		g.fileDialog = false
		if err != nil {
			done(nil, "", err)
			return
		}
		if w == nil {
			return // cancelled
		}
		u := w.URI()
		if u.Scheme() == "file" {
			// Fyne has opened (and emptied) the file already; it gets the
			// same write and mode as with the desktop's dialog (C6, L2).
			w.Close()
			done(func(data []byte) error { return writeFileSafely(u.Path(), data) }, u.Name(), nil)
			return
		}
		done(func(data []byte) error {
			_, err := w.Write(data)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
			return err
		}, u.Name(), nil)
	}, g.win)
	d.SetFileName(name)
	g.showFyneDialog(d, exts)
}

func (g *GUI) fyneOpen(exts []string, max int64, done func(data []byte, file string, err error)) {
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		g.fileDialog = false
		if err != nil {
			done(nil, "", err)
			return
		}
		if r == nil {
			return
		}
		data, err := readAtMost(r, max)
		r.Close()
		done(data, r.URI().Name(), err)
	}, g.win)
	g.showFyneDialog(d, exts)
}

func (g *GUI) showFyneDialog(d *dialog.FileDialog, exts []string) {
	d.SetFilter(storage.NewExtensionFileFilter(exts))
	d.SetOnClosed(func() { g.fileDialog = false })
	d.Resize(g.fyneSize())
	d.Show()
	g.lastFyneDialog = d
}
