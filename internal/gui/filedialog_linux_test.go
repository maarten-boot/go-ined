//go:build linux

package gui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakePortal is a FileChooser on a private bus: it answers each request
// with status and uris, after first sending an answer for another request
// (that one must be ignored).
type fakePortal struct {
	conn   *dbus.Conn
	mu     sync.Mutex
	status uint32
	uris   []string
	got    map[string]dbus.Variant
}

func (p *fakePortal) set(status uint32, uris ...string) {
	p.mu.Lock()
	p.status, p.uris = status, uris
	p.mu.Unlock()
}

func (p *fakePortal) option(name string) any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got[name].Value()
}

func (p *fakePortal) answer(sender dbus.Sender, o map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	p.mu.Lock()
	p.got = o
	status, uris := p.status, p.uris
	p.mu.Unlock()
	token, _ := o["handle_token"].Value().(string)
	path := portalRequestPath(string(sender), token)
	go func() {
		time.Sleep(20 * time.Millisecond)
		other := portalRequestPath(string(sender), "someone_else")
		p.conn.Emit(other, portalRequest+"."+portalResponse, uint32(0), map[string]dbus.Variant{"uris": dbus.MakeVariant([]string{"file:///wrong"})})
		p.conn.Emit(path, portalRequest+"."+portalResponse, status, map[string]dbus.Variant{"uris": dbus.MakeVariant(uris)})
	}()
	return path, nil
}

func (p *fakePortal) SaveFile(sender dbus.Sender, parent, title string, o map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	return p.answer(sender, o)
}

func (p *fakePortal) OpenFile(sender dbus.Sender, parent, title string, o map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	return p.answer(sender, o)
}

// busConfig: a bus without any services to start, so a call to a name
// nobody owns fails instead of starting the desktop's real portal.
const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// privateBus starts a dbus-daemon of the test's own and points portalBus
// at it.
func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("no dbus-daemon")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(conf, []byte(fmt.Sprintf(busConfig, filepath.Join(dir, "bus"))), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("dbus-daemon", "--config-file="+conf, "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(line)
	saved := portalBus
	t.Cleanup(func() { portalBus = saved })
	portalBus = func() (*dbus.Conn, error) { return dbus.Connect(addr) }
	return addr
}

// TC-752: the portal's answer: a file name with "+", "%", "#", "?", a
// space or non-ASCII is the file chosen; "cancelled" is ""; "ended" is an
// error, not a crash; an answer to another request is ignored; without a
// portal the call is errNoNative (crosscheck 2026-11-18-01 C1, C2, C4, C5).
func TestPortalDialog(t *testing.T) {
	addr := privateBus(t)

	// Nobody has the portal's name yet.
	if _, err := portalSave("", "Export", "a.json", []string{".json"}); !errorsIsNoNative(err) {
		t.Fatalf("no portal: %v", err)
	}

	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	p := &fakePortal{conn: conn}
	if err := conn.Export(p, portalPath, portalChooser); err != nil {
		t.Fatal(err)
	}
	if r, err := conn.RequestName(portalName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("name: %v %v", r, err)
	}

	for uri, want := range map[string]string{
		"file:///tmp/a+b.json":         "/tmp/a+b.json",
		"file:///tmp/100%25.json":      "/tmp/100%.json",
		"file:///tmp/a%23b.json":       "/tmp/a#b.json",
		"file:///tmp/why%3F.json":      "/tmp/why?.json",
		"file:///tmp/my%20lan.json":    "/tmp/my lan.json",
		"file:///tmp/r%C3%A9seau.json": "/tmp/réseau.json",
	} {
		p.set(0, uri)
		if got, err := portalSave("x11:1f", "Export", "lan.json", []string{".json"}); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", uri, got, err, want)
		}
		if got, err := portalOpen("", "Import", []string{".json"}); err != nil || got != want {
			t.Errorf("open %s: %q %v", uri, got, err)
		}
	}
	if n, _ := p.option("current_name").(string); n != "" {
		t.Errorf("open sent a name %q", n)
	}
	p.set(0, "file:///tmp/x.json")
	portalSave("", "Export", "lan.json", []string{".json"})
	if n, _ := p.option("current_name").(string); n != "lan.json" {
		t.Errorf("current_name %q", n)
	}
	if fs, ok := p.option("filters").([][]any); !ok || len(fs) != 1 {
		t.Errorf("filters %#v", p.option("filters"))
	}

	p.set(portalCancelled)
	if got, err := portalSave("", "Export", "lan.json", nil); err != nil || got != "" {
		t.Errorf("cancelled: %q %v", got, err)
	}
	p.set(2) // ended: no uris
	if got, err := portalOpen("", "Import", nil); err == nil || errorsIsNoNative(err) || got != "" {
		t.Errorf("ended: %q %v", got, err)
	}
	p.set(0, "https://example.com/a.json")
	if _, err := portalOpen("", "Import", nil); err == nil || errorsIsNoNative(err) {
		t.Errorf("not a local file: %v", err)
	}
}

func errorsIsNoNative(err error) bool {
	return err != nil && strings.Contains(err.Error(), errNoNative.Error())
}
