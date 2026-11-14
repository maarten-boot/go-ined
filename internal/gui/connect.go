package gui

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// ConnectCommands are the commands behind a host's "Connect" menu
// (SPEC-731), from gui.toml [connect]. Empty = the platform's default.
type ConnectCommands struct {
	SSH, RDP, VNC string
}

// DefaultConnectCommands returns the built-in commands for an operating
// system (runtime.GOOS values).
func DefaultConnectCommands(goos string) ConnectCommands {
	switch goos {
	case "darwin":
		return ConnectCommands{SSH: "open ssh://{ip}", RDP: "open rdp://full%20address=s:{ip}", VNC: "open vnc://{ip}:{port}"}
	case "windows":
		return ConnectCommands{SSH: "cmd /c start ssh {ip}", RDP: "mstsc /v:{ip}", VNC: "cmd /c start vnc://{ip}:{port}"}
	default:
		return ConnectCommands{SSH: "x-terminal-emulator -e ssh {ip}", RDP: "remmina -c rdp://{ip}", VNC: "remmina -c vnc://{ip}:{port}"}
	}
}

// withDefaults fills the empty commands from the platform's defaults.
func (c ConnectCommands) withDefaults(goos string) ConnectCommands {
	d := DefaultConnectCommands(goos)
	if strings.TrimSpace(c.SSH) == "" {
		c.SSH = d.SSH
	}
	if strings.TrimSpace(c.RDP) == "" {
		c.RDP = d.RDP
	}
	if strings.TrimSpace(c.VNC) == "" {
		c.VNC = d.VNC
	}
	return c
}

// splitWords splits a command into words like a POSIX shell, without
// running one: blanks separate words, '…' keeps everything literally,
// "…" and a backslash outside quotes escape the next character. Nothing
// else is special: no variables, globs, pipes or redirections.
func splitWords(s string) ([]string, error) {
	var (
		words []string
		cur   strings.Builder
		in    bool // inside a word
		quote rune // ' or " while inside quotes
		esc   bool
	)
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				esc = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			esc, in = true, true
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	switch {
	case quote != 0:
		return nil, fmt.Errorf("unterminated %c quote", quote)
	case esc:
		return nil, errors.New("backslash at the end")
	}
	if in {
		words = append(words, cur.String())
	}
	if len(words) == 0 {
		return nil, errors.New("empty command")
	}
	return words, nil
}

// buildCommand splits tmpl into words and then fills in {ip}, {name} and
// {port} in each word, so a value can never become more than one word or
// be read as an option of its own (SPEC-731, C5).
func buildCommand(tmpl string, ip netip.Addr, name string, port int) ([]string, error) {
	words, err := splitWords(tmpl)
	if err != nil {
		return nil, err
	}
	r := strings.NewReplacer("{ip}", ip.String(), "{name}", name, "{port}", strconv.Itoa(port))
	for i, w := range words {
		words[i] = r.Replace(w)
	}
	return words, nil
}

// connectTarget is one entry of the Connect menu.
type connectTarget struct {
	label   string // "SSH (22)"
	service string // the name in Host.Services
	key     string // the gui.toml key, or "" for a web page
	port    int
	scheme  string // "http" or "https" for a web page
}

// connectTargets lists the entries for a host: SSH, RDP, VNC display 0
// and every other VNC display that answered, HTTP and HTTPS (C2).
func connectTargets(h *apitypes.Host) []connectTarget {
	ts := []connectTarget{
		{label: "SSH (22)", service: "ssh", key: "ssh", port: 22},
		{label: "Remote desktop (3389)", service: "rdp", key: "rdp", port: 3389},
		{label: "VNC (5900)", service: "vnc", key: "vnc", port: 5900},
	}
	for n := 1; n <= 9; n++ {
		if s := "vnc:" + strconv.Itoa(n); slices.Contains(h.Services, s) {
			ts = append(ts, connectTarget{label: fmt.Sprintf("VNC :%d (%d)", n, 5900+n), service: s, key: "vnc", port: 5900 + n})
		}
	}
	return append(ts,
		connectTarget{label: "Web (http, 80)", service: "http", port: 80, scheme: "http"},
		connectTarget{label: "Web (https, 443)", service: "https", port: 443, scheme: "https"})
}

// connectItem is "Connect ▸" on a host (SPEC-730): ticked where the port
// answered at the last check, every entry clickable, and "Check services
// now".
func (g *GUI) connectItem(h *apitypes.Host) *fyne.MenuItem {
	var sub []*fyne.MenuItem
	for _, t := range connectTargets(h) {
		it := fyne.NewMenuItem(t.label, func() { g.connect(h, t) })
		it.Checked = slices.Contains(h.Services, t.service)
		sub = append(sub, it)
	}
	host := *h
	sub = append(sub, fyne.NewMenuItemSeparator(), fyne.NewMenuItem("Check services now", func() { g.checkServices(host) }))
	item := fyne.NewMenuItem("Connect", nil)
	item.ChildMenu = fyne.NewMenu("", sub...)
	return item
}

// webURL is the address of a host's web page; IPv6 goes in brackets.
func webURL(scheme string, ip netip.Addr) *url.URL {
	host := ip.String()
	if ip.Is6() {
		host = "[" + host + "]"
	}
	return &url.URL{Scheme: scheme, Host: host, Path: "/"}
}

// connect opens a connection to a host: a web page in the browser, or
// the configured command (SPEC-731).
func (g *GUI) connect(h *apitypes.Host, t connectTarget) {
	ip, err := netip.ParseAddr(h.IP)
	if err != nil {
		g.status.message(fmt.Sprintf("Connect to %s: bad IP address %q", h.DisplayName, h.IP))
		return
	}
	if t.scheme != "" {
		u := webURL(t.scheme, ip)
		if err := g.openURL(u); err != nil {
			g.status.message(fmt.Sprintf("Opening %s failed: %v", u, err))
			return
		}
		g.status.message(fmt.Sprintf("Opened %s in the browser", u))
		return
	}
	cmds := g.opt.Connect.withDefaults(runtime.GOOS)
	tmpl := map[string]string{"ssh": cmds.SSH, "rdp": cmds.RDP, "vnc": cmds.VNC}[t.key]
	argv, err := buildCommand(tmpl, ip, h.DisplayName, t.port)
	if err != nil {
		g.status.message(fmt.Sprintf("Connect: the %s command %q in gui.toml [connect]: %v", t.key, tmpl, err))
		return
	}
	what := fmt.Sprintf("%s to %s", strings.SplitN(t.label, " (", 2)[0], h.DisplayName)
	if err := g.start(argv, func(err error) {
		g.do(func() {
			g.status.message(fmt.Sprintf("%s: %s %v; change %s under [connect] in gui.toml", what, argv[0], err, t.key))
		})
	}); err != nil {
		hint := ""
		if errors.Is(err, exec.ErrNotFound) {
			hint = fmt.Sprintf(": %q is not installed", argv[0])
			if argv[0] == "remmina" {
				hint += " (sudo apt install remmina)"
			}
		}
		g.status.message(fmt.Sprintf("%s failed%s; change %s under [connect] in gui.toml (%v)", what, hint, t.key, err))
		return
	}
	g.status.message(fmt.Sprintf("%s: started %s", what, strings.Join(argv, " ")))
}

// quickExit is how long after the start a failing command is still
// reported in the status bar.
const quickExit = 5 * time.Second

// startCommand runs argv without a shell and without waiting. If it fails
// within quickExit, failed is called with the error and the last line of
// its error output.
func startCommand(argv []string, failed func(error)) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	var stderr tailBuffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		err := cmd.Wait()
		if err != nil && time.Since(start) < quickExit {
			if line := stderr.lastLine(); line != "" {
				err = fmt.Errorf("%w: %s", err, line)
			}
			failed(err)
		}
	}()
	return nil
}

// tailBuffer keeps the last 4 KiB written to it.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := bytes.Split(bytes.TrimSpace(t.b), []byte("\n"))
	return strings.TrimSpace(string(lines[len(lines)-1]))
}

// checkServices asks the server to check a host's services now and shows
// the result (SPEC-730).
func (g *GUI) checkServices(h apitypes.Host) {
	g.status.message(fmt.Sprintf("Checking the services of %s…", h.DisplayName))
	go func() {
		got, err := g.c.CheckServices(g.ctx, h.ID)
		if err != nil {
			g.fail("Checking the services of "+h.DisplayName, err)
			return
		}
		g.do(func() {
			g.status.message(fmt.Sprintf("Services of %s: %s", got.DisplayName, servicesText(got)))
			g.details.hostChanged(got)
		})
	}()
}

// servicesText is "ssh, vnc :1 · checked 14:02", "none · checked 14:02"
// or "not checked yet" (SPEC-730, C6).
func servicesText(h apitypes.Host) string {
	if h.ServicesCheckedAt == nil {
		return "not checked yet"
	}
	names := make([]string, len(h.Services))
	for i, s := range h.Services {
		names[i] = apitypes.ServiceLabel(s)
	}
	list := strings.Join(names, ", ")
	if list == "" {
		list = "none"
	}
	t := h.ServicesCheckedAt.Local()
	layout := "15:04"
	if y, m, d := time.Now().Date(); t.Year() != y || t.Month() != m || t.Day() != d {
		layout = time.DateTime[:16]
	}
	return list + " · checked " + t.Format(layout)
}

// start runs a connect command: Options.Start, or startCommand.
func (g *GUI) start(argv []string, failed func(error)) error {
	if g.opt.Start != nil {
		return g.opt.Start(argv, failed)
	}
	return startCommand(argv, failed)
}

// openURL opens a web page: Options.OpenURL, or the system browser.
func (g *GUI) openURL(u *url.URL) error {
	if g.opt.OpenURL != nil {
		return g.opt.OpenURL(u)
	}
	return g.app.OpenURL(u)
}
