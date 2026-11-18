package gui

import (
	"errors"
	"io"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
)

// File dialogs (Design-file-dialogs.md F1a, F2): the desktop's own dialog
// where there is one, a real window that can be moved and resized; Fyne's
// dialog, drawn inside the main window at 90 % of its size, where there
// isn't or when it fails.

// errNoNative: this platform or desktop has no dialog of its own.
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

// saveFile asks where to save: name is the suggested file name, exts the
// file types offered (".json"). done gets the writer, nil when cancelled,
// or an error; it runs on the UI goroutine.
func (g *GUI) saveFile(title, name string, exts []string, done func(w io.WriteCloser, file string, err error)) {
	parent := nativeParent(g.win)
	go func() {
		path, err := nativeSave(parent, title, name, exts)
		g.do(func() {
			if err != nil {
				g.fyneSave(name, exts, done)
				return
			}
			if path == "" {
				return // cancelled
			}
			f, err := os.Create(path)
			if err == nil {
				err = f.Chmod(0o600) // notes can be private (as inedctl -o)
			}
			done(f, baseName(path), err)
		})
	}()
}

// openFile asks for a file to read; done gets its contents (at most max
// bytes read) or an error, on the UI goroutine.
func (g *GUI) openFile(title string, exts []string, max int64, done func(data []byte, file string, err error)) {
	parent := nativeParent(g.win)
	go func() {
		path, err := nativeOpen(parent, title, exts)
		if err != nil {
			g.do(func() { g.fyneOpen(exts, max, done) })
			return
		}
		if path == "" {
			return // cancelled
		}
		data, err := readLimited(path, max)
		g.do(func() { done(data, baseName(path), err) })
	}()
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

func baseName(path string) string {
	return storage.NewFileURI(path).Name()
}

// fyneSize is the size of Fyne's dialog: most of the main window (F2).
func (g *GUI) fyneSize() fyne.Size {
	s := g.win.Canvas().Size()
	return fyne.NewSize(s.Width*fyneDialogShare, s.Height*fyneDialogShare)
}

func (g *GUI) fyneSave(name string, exts []string, done func(w io.WriteCloser, file string, err error)) {
	d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil {
			done(nil, "", err)
			return
		}
		if w == nil {
			return // cancelled
		}
		done(w, w.URI().Name(), nil)
	}, g.win)
	d.SetFileName(name)
	d.SetFilter(storage.NewExtensionFileFilter(exts))
	d.Resize(g.fyneSize())
	d.Show()
	g.lastFyneDialog = d
}

func (g *GUI) fyneOpen(exts []string, max int64, done func(data []byte, file string, err error)) {
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			done(nil, "", err)
			return
		}
		if r == nil {
			return
		}
		data, err := io.ReadAll(io.LimitReader(r, max))
		r.Close()
		done(data, r.URI().Name(), err)
	}, g.win)
	d.SetFilter(storage.NewExtensionFileFilter(exts))
	d.Resize(g.fyneSize())
	d.Show()
	g.lastFyneDialog = d
}
