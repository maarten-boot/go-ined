package gui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/gui/mapmodel"
	"github.com/maarten-boot/go-ined/internal/services"
	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-742: a command is split into words like a shell would, without one;
// the placeholders are filled in per word, so a value stays one word.
func TestBuildCommand(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.1")
	for _, c := range []struct {
		tmpl, name string
		want       []string
	}{
		{"x-terminal-emulator -e ssh {ip}", "web", []string{"x-terminal-emulator", "-e", "ssh", "192.0.2.1"}},
		{"remmina -c vnc://{ip}:{port}", "web", []string{"remmina", "-c", "vnc://192.0.2.1:5901"}},
		{`ssh  admin@{ip}  -o "Name={name}"`, "a b", []string{"ssh", "admin@192.0.2.1", "-o", "Name=a b"}},
		{"xterm -T {name} -e ssh {ip}", "x; rm -rf ~", []string{"xterm", "-T", "x; rm -rf ~", "-e", "ssh", "192.0.2.1"}},
		{`echo '$HOME {ip}' a\ b "q\"uote"`, "", []string{"echo", "$HOME 192.0.2.1", "a b", `q"uote`}},
		{`prog ""`, "", []string{"prog", ""}},
	} {
		got, err := buildCommand(c.tmpl, ip, c.name, 5901)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q (%v), want %q", c.tmpl, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "   ", `ssh "{ip}`, "ssh '{ip}", `ssh \`} {
		if got, err := buildCommand(bad, ip, "", 22); err == nil {
			t.Errorf("%q: no error, got %q", bad, got)
		}
	}
	d := ConnectCommands{SSH: "my-ssh {ip}"}.withDefaults("linux")
	if d.SSH != "my-ssh {ip}" || d.RDP != "remmina -c rdp://{ip}" || d.VNC != "remmina -c vnc://{ip}:{port}" {
		t.Errorf("defaults: %+v", d)
	}
	for _, goos := range []string{"darwin", "windows", "linux", "freebsd"} {
		c := DefaultConnectCommands(goos)
		if c.SSH == "" || c.RDP == "" || c.VNC == "" {
			t.Errorf("%s: %+v", goos, c)
		}
	}
	if u := webURL("https", netip.MustParseAddr("2001:db8::1")).String(); u != "https://[2001:db8::1]/" {
		t.Errorf("IPv6 URL: %s", u)
	}
}

// recorder stands in for Options.Start and Options.OpenURL.
type recorder struct {
	mu      sync.Mutex
	started [][]string
	opened  []string
	failed  func(error) // the last command's failure callback
	err     error       // what Start returns
}

func (r *recorder) start(argv []string, failed func(error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, argv)
	r.failed = failed
	return r.err
}

func (r *recorder) openURL(u *url.URL) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, u.String())
	return nil
}

func (r *recorder) last() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var s []string
	if len(r.started) > 0 {
		s = r.started[len(r.started)-1]
	}
	return s, slices.Clone(r.opened)
}

// TC-741: "Connect ▸" on a host ticks the services that answered, lists
// VNC displays beyond :0 only when they answered, keeps every entry
// clickable, runs the configured command or opens the browser, and
// "Check services now" updates the ticks, the status bar and the details
// (SPEC-730, SPEC-731).
func TestConnectMenu(t *testing.T) {
	open := map[string]bool{"192.0.2.1:22": true, "192.0.2.1:5901": true, "192.0.2.1:443": true}
	var mu sync.Mutex
	e := startWith(t, testserver.Options{Services: services.Config{
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			mu.Lock()
			defer mu.Unlock()
			if open[addr] {
				c, _ := net.Pipe()
				return c, nil
			}
			return nil, errors.New("connection refused")
		},
	}})
	rec := &recorder{}
	e.options = func(o *Options) {
		o.Connect = ConnectCommands{SSH: "term -e ssh {ip}"}
		o.Start, o.OpenURL = rec.start, rec.openURL
	}
	c, ctx := e.c, e.ctx
	h := must(c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", Name: "web"}))
	e.open()
	g := e.g
	eventually(t, e.ui, "loaded", func() bool { return len(e.nodes(1)) == 1 })

	connect := func() []*apitypes.Host {
		host := must(c.Host(ctx, h.ID))
		return []*apitypes.Host{&host}
	}
	sub := func(host *apitypes.Host) (labels []string, ticked []string, items map[string]func()) {
		items = map[string]func(){}
		e.ui.Wait(func() {
			item := menuItem(g.menuItems(1, find(e.nodes(1), "host", h.ID), 0, 0, host), "Connect")
			if item == nil || item.ChildMenu == nil || item.Disabled {
				t.Fatalf("Connect: %+v", item)
			}
			for _, it := range item.ChildMenu.Items {
				if it.IsSeparator {
					labels = append(labels, "—")
					continue
				}
				if it.Disabled {
					t.Errorf("%q is disabled", it.Label)
				}
				labels = append(labels, it.Label)
				if it.Checked {
					ticked = append(ticked, it.Label)
				}
				items[it.Label] = it.Action
			}
		})
		return
	}

	// Never checked: no ticks, all entries.
	labels, ticked, items := sub(connect()[0])
	want := []string{"SSH (22)", "Remote desktop (3389)", "VNC (5900)", "Web (http, 80)", "Web (https, 443)", "—", "Check services now"}
	if !slices.Equal(labels, want) || len(ticked) != 0 {
		t.Fatalf("before a check: %q ticked %q", labels, ticked)
	}
	k := mapmodel.HostKey(h.ID)
	e.ui.Wait(func() { g.details.show(1, &k) })
	eventually(t, e.ui, "details: not checked", func() bool { return strings.Contains(formText(g.details), "not checked yet") })

	e.ui.Wait(items["Check services now"])
	eventually(t, e.ui, "status and details after the check", func() bool {
		return strings.Contains(g.status.Text(), "Services of web: ssh, vnc :1, https · checked ") &&
			strings.Contains(formText(g.details), "ssh, vnc :1, https · checked ")
	})
	labels, ticked, items = sub(connect()[0])
	want = []string{"SSH (22)", "Remote desktop (3389)", "VNC (5900)", "VNC :1 (5901)", "Web (http, 80)", "Web (https, 443)", "—", "Check services now"}
	if !slices.Equal(labels, want) || !slices.Equal(ticked, []string{"SSH (22)", "VNC :1 (5901)", "Web (https, 443)"}) {
		t.Fatalf("after a check: %q ticked %q", labels, ticked)
	}

	// The commands: configured SSH, default VNC with the display's port,
	// the browser for the web; an unticked entry works too.
	e.ui.Wait(items["SSH (22)"])
	if s, _ := rec.last(); !slices.Equal(s, []string{"term", "-e", "ssh", "192.0.2.1"}) {
		t.Errorf("ssh: %q", s)
	}
	e.ui.Wait(items["VNC :1 (5901)"])
	if s, _ := rec.last(); !slices.Equal(s, DefaultConnectCommandsArgv(t, "vnc", "192.0.2.1", 5901)) {
		t.Errorf("vnc: %q", s)
	}
	e.ui.Wait(items["Web (http, 80)"])
	if _, o := rec.last(); !slices.Equal(o, []string{"http://192.0.2.1/"}) {
		t.Errorf("web: %q", o)
	}
	if s := g.status.Text(); !strings.Contains(s, "Opened http://192.0.2.1/ in the browser") {
		t.Errorf("status: %q", s)
	}

	// A missing program, and a program that fails at once.
	rec.mu.Lock()
	rec.err = fmt.Errorf("exec: %q: %w", "remmina", exec.ErrNotFound)
	rec.mu.Unlock()
	e.ui.Wait(items["Remote desktop (3389)"])
	if s := g.status.Text(); !strings.Contains(s, `Remote desktop to web failed: "remmina" is not installed (sudo apt install remmina); change rdp under [connect] in gui.toml`) {
		t.Errorf("missing program: %q", s)
	}
	rec.mu.Lock()
	rec.err = nil
	rec.mu.Unlock()
	e.ui.Wait(items["SSH (22)"])
	rec.mu.Lock()
	failed := rec.failed
	rec.mu.Unlock()
	failed(errors.New("exit status 1: no DISPLAY"))
	eventually(t, e.ui, "failure reported", func() bool {
		return strings.Contains(g.status.Text(), "SSH to web: term exit status 1: no DISPLAY; change ssh under [connect] in gui.toml")
	})

	// A port closes; the next check removes the tick and the entry.
	mu.Lock()
	delete(open, "192.0.2.1:5901")
	mu.Unlock()
	e.ui.Wait(items["Check services now"])
	eventually(t, e.ui, "vnc :1 gone", func() bool { return strings.Contains(g.status.Text(), "Services of web: ssh, https · checked ") })
	if labels, _, _ := sub(connect()[0]); slices.Contains(labels, "VNC :1 (5901)") {
		t.Errorf("VNC :1 still listed: %q", labels)
	}
}

// DefaultConnectCommandsArgv is the default command for key on Linux,
// as built for ip and port.
func DefaultConnectCommandsArgv(t *testing.T, key, ip string, port int) []string {
	t.Helper()
	d := DefaultConnectCommands("linux")
	argv, err := buildCommand(map[string]string{"ssh": d.SSH, "rdp": d.RDP, "vnc": d.VNC}[key], netip.MustParseAddr(ip), "", port)
	if err != nil {
		t.Fatal(err)
	}
	return argv
}

// TC-742: startCommand runs without a shell, reports a missing program at
// once, and a quick failure with its last line of error output.
func TestStartCommand(t *testing.T) {
	err := startCommand([]string{"go-ined-no-such-program-xyz"}, func(error) {})
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing program: %v", err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run a failing command")
	}
	got := make(chan error, 1)
	if err := startCommand([]string{sh, "-c", "echo first >&2; echo 'no DISPLAY' >&2; exit 3"}, func(err error) { got <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if !strings.Contains(err.Error(), "exit status 3: no DISPLAY") {
			t.Errorf("failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no failure reported")
	}
	ok := make(chan error, 1)
	if err := startCommand([]string{sh, "-c", "exit 0"}, func(err error) { ok <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ok:
		t.Errorf("success reported as failure: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}
