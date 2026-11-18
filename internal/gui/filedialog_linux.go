//go:build linux

package gui

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"github.com/godbus/dbus/v5"
)

// On Linux the desktop's dialog comes through the XDG desktop portal
// (org.freedesktop.portal.FileChooser), as Fyne does in Flatpak builds:
// GNOME's or KDE's own dialog (Design-file-dialogs.md F1a). Without a
// session bus or a portal the call fails at once and Fyne's dialog is
// used.
//
// The portal is called here directly, not through a library (crosscheck
// 2026-11-18-01): the answer's URI is decoded once, so names with "+",
// "%", "#" or "?" stay as chosen (C1); every status is handled, "ended"
// too (C2); and only the answer to this dialog's own request counts (C5).

const (
	portalName      = "org.freedesktop.portal.Desktop"
	portalPath      = "/org/freedesktop/portal/desktop"
	portalChooser   = "org.freedesktop.portal.FileChooser"
	portalRequest   = "org.freedesktop.portal.Request"
	portalResponse  = "Response"
	portalCancelled = 1
)

// portalBus connects to the session bus; tests point it at their own.
var portalBus = func() (*dbus.Conn, error) { return dbus.ConnectSessionBus() }

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
				h = "x11:" + strconv.FormatUint(uint64(x.WindowHandle), 16)
			}
		})
	}
	return h
}

// portalRule and portalFilter are the portal's filter, a(sa(us)).
type portalRule struct {
	Type    uint32 // 0: a glob pattern
	Pattern string
}

type portalFilterT struct {
	Name  string
	Rules []portalRule
}

func portalFilters(exts []string) []portalFilterT {
	if len(exts) == 0 {
		return nil
	}
	f := portalFilterT{Name: strings.ToUpper(strings.TrimPrefix(exts[0], ".")) + " files"}
	for _, e := range exts {
		f.Rules = append(f.Rules, portalRule{0, "*" + strings.ToLower(e)}, portalRule{0, "*" + strings.ToUpper(e)})
	}
	return []portalFilterT{f}
}

func portalOptions(exts []string) map[string]dbus.Variant {
	o := map[string]dbus.Variant{"modal": dbus.MakeVariant(true)}
	if fs := portalFilters(exts); fs != nil {
		o["filters"] = dbus.MakeVariant(fs)
		o["current_filter"] = dbus.MakeVariant(fs[0])
	}
	return o
}

func portalSave(parent, title, name string, exts []string) (string, error) {
	o := portalOptions(exts)
	o["current_name"] = dbus.MakeVariant(name)
	return portalAsk("SaveFile", parent, title, o)
}

func portalOpen(parent, title string, exts []string) (string, error) {
	o := portalOptions(exts)
	o["multiple"] = dbus.MakeVariant(false)
	return portalAsk("OpenFile", parent, title, o)
}

// portalAsk calls FileChooser.<method> and waits for the answer to this
// request. Errors before the portal accepted the call are errNoNative
// (Fyne's dialog is used); later ones are reported.
func portalAsk(method, parent, title string, o map[string]dbus.Variant) (string, error) {
	conn, err := portalBus()
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoNative, err)
	}
	defer conn.Close()
	names := conn.Names()
	if len(names) == 0 {
		return "", fmt.Errorf("%w: no name on the session bus", errNoNative)
	}
	token := portalToken()
	o["handle_token"] = dbus.MakeVariant(token)
	// The request's path is known before the call: listen first, so the
	// answer can't come before we do.
	want := map[dbus.ObjectPath]bool{portalRequestPath(names[0], token): true}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	match := func(p dbus.ObjectPath) error {
		return conn.AddMatchSignal(dbus.WithMatchObjectPath(p), dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember(portalResponse))
	}
	for p := range want {
		if err := match(p); err != nil {
			return "", fmt.Errorf("%w: %v", errNoNative, err)
		}
	}
	var handle dbus.ObjectPath
	if err := conn.Object(portalName, portalPath).Call(portalChooser+"."+method, 0, parent, title, o).Store(&handle); err != nil {
		return "", fmt.Errorf("%w: %v", errNoNative, err)
	}
	if !want[handle] { // a portal older than handle_token
		want[handle] = true
		if err := match(handle); err != nil {
			return "", err
		}
	}
	for s := range signals {
		if !want[s.Path] || s.Name != portalRequest+"."+portalResponse {
			continue
		}
		return portalAnswer(s.Body)
	}
	return "", errors.New("the connection to the file dialog was lost")
}

// portalRequestPath is the request object the portal makes for sender
// and token.
func portalRequestPath(sender, token string) dbus.ObjectPath {
	s := strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + s + "/" + token)
}

func portalToken() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "ined_" + hex.EncodeToString(b)
}

// portalAnswer reads a Response signal's body (u status, a{sv} results):
// the chosen file's path, "" for cancelled.
func portalAnswer(body []any) (string, error) {
	if len(body) != 2 {
		return "", errors.New("the file dialog gave an unexpected answer")
	}
	status, ok := body[0].(uint32)
	results, ok2 := body[1].(map[string]dbus.Variant)
	if !ok || !ok2 {
		return "", errors.New("the file dialog gave an unexpected answer")
	}
	switch status {
	case 0:
	case portalCancelled:
		return "", nil
	default:
		return "", errors.New("the file dialog ended without an answer")
	}
	uris, _ := results["uris"].Value().([]string)
	if len(uris) == 0 {
		return "", nil
	}
	return uriPath(uris[0])
}

// uriPath turns a file:// URI into a path, decoding it once.
func uriPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("the file dialog gave a bad file name: %v", err)
	}
	if u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || u.Path == "" {
		return "", fmt.Errorf("the file dialog gave a file that isn't local: %s", uri)
	}
	return u.Path, nil
}
