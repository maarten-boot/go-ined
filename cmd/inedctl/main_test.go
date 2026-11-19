package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/services"
	"github.com/maarten-boot/go-ined/internal/snmp"
	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/internal/version"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// isolate keeps the user's config and environment out of the tests.
func isolate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("INED_URL", "")
	t.Setenv("INED_TOKEN", "")
}

type result struct {
	code        int
	out, errOut string
}

// ctlIn runs inedctl with stdin.
func ctlIn(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return result{code, out.String(), errOut.String()}
}

func ctl(t *testing.T, args ...string) result { t.Helper(); return ctlIn(t, "", args...) }

// ok runs a command that must succeed.
func ok(t *testing.T, args ...string) string {
	t.Helper()
	r := ctl(t, args...)
	if r.code != 0 {
		t.Fatalf("inedctl %s: exit %d\n%s%s", strings.Join(args, " "), r.code, r.out, r.errOut)
	}
	return r.out
}

func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

func get(t *testing.T, s *testserver.Server, path string) string {
	t.Helper()
	resp, err := http.Get(s.URL() + "/api/v1" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TC-901: every subcommand against a test server.
func TestCommands(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	u := "--url=" + s.URL()

	contains(t, ok(t, u, "group", "add", "--cidr", "10.0.0.0/24"), "ID:", "1", "CIDR:", "10.0.0.0/24", "Collapsed:", "yes")
	// Flags after the positional argument work too.
	contains(t, ok(t, u, "host", "add", "10.0.0.5", "--name", "web", "--note", "hi"),
		"IP:", "10.0.0.5", "Parent:", "Monitored:", "yes", "Note:", "hi")
	ok(t, u, "host", "add", "--unmonitored", "10.0.0.6")
	list := ok(t, u, "host", "list")
	contains(t, list, "ID", "IP", "NAME", "STATUS", "RTT", "PARENT", "MON", "SVC")
	lines := strings.Split(strings.TrimSpace(list), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "1 ") || !strings.Contains(lines[1], "web") ||
		!strings.Contains(lines[2], "unknown") || !strings.HasSuffix(strings.Join(strings.Fields(lines[2]), " "), "no -") {
		t.Fatalf("host list:\n%s", list)
	}
	contains(t, ok(t, u, "host", "show", "1"), "Display name:", "web", "Status:", "unknown")
	contains(t, ok(t, u, "host", "edit", "1", "--name", "db", "--monitored=false", "--description", "d"),
		"Name:", "db", "Monitored:    no", "Description:  d")
	contains(t, ok(t, u, "host", "move", "1", "root"), "Parent:       root")
	contains(t, ok(t, u, "host", "move", "1", "1"), "Parent:       1")

	if out := ok(t, u, "note", "get", "host", "1"); out != "hi\n" {
		t.Fatalf("note get: %q", out)
	}
	if r := ctlIn(t, "line 1\nline 2\n\n", u, "note", "set", "host", "1"); r.code != 0 {
		t.Fatalf("note set from stdin: %+v", r)
	}
	if out := ok(t, u, "note", "get", "host", "1"); out != "line 1\nline 2\n" {
		t.Fatalf("note from stdin: %q", out)
	}
	contains(t, ok(t, u, "host", "show", "1"), "\nNote:", "\n  line 1\n  line 2\n")
	ok(t, u, "note", "set", "group", "1", "two", "words")
	if out := ok(t, u, "note", "get", "group", "1"); out != "two words\n" {
		t.Fatalf("note set args: %q", out)
	}
	ok(t, u, "note", "clear", "group", "1")
	if out := ok(t, u, "note", "get", "group", "1"); out != "" {
		t.Fatalf("note clear: %q", out)
	}

	contains(t, ok(t, u, "group", "add", "--name", "lab", "--expanded", "--parent", "1"), "Name:", "lab", "Collapsed:   no", "Parent:      1")
	contains(t, ok(t, u, "group", "collapse", "2"), "Collapsed:   yes")
	contains(t, ok(t, u, "group", "expand", "2"), "Collapsed:   no", "Labels:      inherit")
	contains(t, ok(t, u, "group", "labels", "2", "ip"), "Labels:      ip")
	contains(t, ok(t, u, "group", "labels", "2", "both"), "Labels:      name and IP")
	contains(t, ok(t, u, "group", "labels", "2", "inherit"), "Labels:      inherit", "Bar colour:  none")
	contains(t, ok(t, u, "group", "color", "2", "#f58231"), "Bar colour:  #F58231")
	contains(t, ok(t, u, "group", "color", "2", "none"), "Bar colour:  none", "Rescan:      off")
	// TC-219: rescan on a network group; a plain group gets 400 (exit 1).
	contains(t, ok(t, u, "group", "rescan", "1", "24h"), "Rescan:      every 24h, not scanned yet")
	contains(t, ok(t, u, "group", "rescan", "1", "90m"), "Rescan:      every 1h30m, not scanned yet")
	contains(t, ok(t, u, "group", "rescan", "1", "off"), "Rescan:      off")
	if r := ctl(t, u, "group", "rescan", "2", "1h"); r.code != 1 || !strings.Contains(r.errOut, "network group") {
		t.Fatalf("rescan on a plain group: %+v", r)
	}
	contains(t, ok(t, u, "group", "add", "--name", "red", "--color", "#e6194b"), "Bar colour:  #E6194B")
	contains(t, ok(t, u, "group", "add", "--name", "ips", "--labels", "ip"), "Name:        ips", "Labels:      ip")
	contains(t, ok(t, u, "group", "edit", "2", "--name", "lab2"), "Name:        lab2")
	contains(t, ok(t, u, "group", "move", "2", "root"), "Parent:      root")
	contains(t, ok(t, u, "group", "show", "1"), "CIDR:        10.0.0.0/24")
	gl := ok(t, u, "group", "list")
	contains(t, gl, "ID", "NAME", "CIDR", "STATUS", "PARENT", "COLLAPSED", "lab2")
	contains(t, ok(t, u, "group", "edit", "1", "--cidr", ""), "CIDR:        -")

	ok(t, u, "host", "rm", "2")
	if r := ctl(t, u, "group", "rm", "1"); r.code != 1 || !strings.Contains(r.errOut, "conflict") {
		t.Fatalf("non-empty rm: %+v", r)
	}
	ok(t, u, "group", "rm", "1", "--cascade")
	ok(t, u, "group", "rm", "--reparent", "2")
	if out := ok(t, u, "host", "list"); strings.Count(out, "\n") != 1 {
		t.Fatalf("not empty:\n%s", out)
	}
	contains(t, ok(t, u, "health"), "Status:", "ok", "ICMP mode:", "unprivileged", "Run id:", "run1")
}

// TC-902: --json prints the API's response unchanged, before or after the
// command.
func TestJSON(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	u := "--url=" + s.URL()
	ok(t, u, "host", "add", "192.0.2.1", "--note", "n")
	for _, c := range []struct {
		args []string
		path string
	}{
		{[]string{u, "--json", "host", "show", "1"}, "/hosts/1"},
		{[]string{u, "host", "list", "--json"}, "/hosts"},
		{[]string{u, "--json", "group", "list"}, "/groups"},
		{[]string{u, "--json", "health"}, "/health"},
	} {
		if out := ok(t, c.args...); out != get(t, s, c.path) {
			t.Errorf("%v:\n%s\nwant\n%s", c.args, out, get(t, s, c.path))
		}
	}
	var h apitypes.Host
	if err := json.Unmarshal([]byte(ok(t, u, "--json", "host", "add", "192.0.2.2")), &h); err != nil || h.IP != "192.0.2.2" {
		t.Fatalf("add --json: %+v %v", h, err)
	}
	if out := ok(t, u, "--json", "host", "rm", "2"); out != "" {
		t.Fatalf("rm --json: %q", out)
	}
	if out := ok(t, u, "host", "list"); strings.HasPrefix(out, "[") {
		t.Fatalf("default output is not a table:\n%s", out)
	}
}

func deadURL(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr
}

// TC-903: exit codes.
func TestExitCodes(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{Token: "secret"})
	u, tok := "--url="+s.URL(), "--token=secret"
	for _, c := range []struct {
		code int
		args []string
	}{
		{0, []string{u, tok, "host", "list"}},
		{1, []string{u, tok, "host", "show", "99"}},
		{1, []string{u, tok, "host", "add", "not-an-ip"}},
		{1, []string{u, tok, "discover", "status", "nope"}},
		{2, []string{}},
		{2, []string{u, "frobnicate"}},
		{2, []string{u, "host"}},
		{2, []string{u, "host", "fly"}},
		{2, []string{u, "host", "show"}},
		{2, []string{u, "host", "show", "1", "2"}},
		{2, []string{u, "host", "show", "x"}},
		{2, []string{u, "host", "show", "0"}},
		{2, []string{u, "host", "list", "--bogus"}},
		{2, []string{u, "host", "move", "1", "nowhere"}},
		{2, []string{u, "host", "edit", "1"}},
		{2, []string{u, "group", "add"}},
		{2, []string{u, "group", "add", "--name", "x", "--labels", "dns"}},
		{2, []string{u, "group", "labels", "1"}},
		{2, []string{u, "group", "labels", "1", "dns"}},
		{2, []string{u, "group", "color", "1"}},
		{2, []string{u, "group", "rescan", "1", "daily"}},
		{2, []string{u, "group", "rescan", "1", "5m"}},
		{2, []string{u, "group", "color", "1", "red"}},
		{2, []string{u, "group", "add", "--name", "x", "--color", "#12345"}},
		{2, []string{u, "group", "rm", "1", "--reparent", "--cascade"}},
		{2, []string{u, "note", "get", "router", "1"}},
		{2, []string{u, "discover", "start"}},
		{2, []string{u, "discover", "start", "10.0.0.0/24", "--group", "1"}},
		{2, []string{"--url=ftp://x", "health"}},
		{2, []string{u, "view", "order", "2", "first"}},
		{2, []string{u, "view", "place", "2"}},
		{2, []string{u, "view", "place", "2", "1", "--x", "5"}},
		{2, []string{u, "group", "list", "--view", "0"}},
		{1, []string{u, tok, "view", "show", "9"}},
		{2, []string{"--config=/nonexistent/ctl.toml", "health"}},
		{3, []string{u, "--token=wrong", "host", "list"}},
		{3, []string{u, "host", "list"}}, // no token
		{3, []string{"--url=" + deadURL(t), "health"}},
		{3, []string{"--url=" + deadURL(t), "events"}},
		{3, []string{u, "--token=wrong", "events"}},
	} {
		if r := ctl(t, c.args...); r.code != c.code {
			t.Errorf("%v: exit %d, want %d\n%s%s", c.args, r.code, c.code, r.out, r.errOut)
		}
	}
	r := ctl(t, u, "host", "fly")
	contains(t, r.errOut, `unknown action "fly"`, "inedctl -h")
	r = ctl(t, "-h")
	if r.code != 0 || !strings.Contains(r.out, "Usage: inedctl") {
		t.Fatalf("-h: %+v", r)
	}
}

// TC-807: -version.
func TestVersion(t *testing.T) {
	r := ctl(t, "-version")
	if r.code != 0 || r.out != "inedctl "+version.Version+"\n" {
		t.Fatalf("%+v", r)
	}
}

// TC-804, TC-904: config file, environment and flags; ca_cert from the
// file or --ca-cert.
func TestConfigAndCACert(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{Token: "secret", TLS: true})
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.HTTP.Certificate().Raw}), 0o600)

	if r := ctl(t, "--url="+s.URL(), "--token=secret", "health"); r.code != 3 || !strings.Contains(r.errOut, "certificate") {
		t.Fatalf("untrusted: %+v", r)
	}
	ok(t, "--url="+s.URL(), "--token=secret", "--ca-cert="+ca, "host", "list")

	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "go-ined", "ctl.toml")
	os.MkdirAll(filepath.Dir(cfg), 0o700)
	os.WriteFile(cfg, []byte("[server]\nurl = \""+s.URL()+"\"\ntoken = \"wrong\"\nca_cert = \""+ca+"\"\n"), 0o600)
	if r := ctl(t, "host", "list"); r.code != 3 {
		t.Fatalf("token from file: %+v", r)
	}
	t.Setenv("INED_TOKEN", "secret") // environment beats the file
	ok(t, "host", "list")
	if r := ctl(t, "--token=wrong", "host", "list"); r.code != 3 { // flag beats the environment
		t.Fatalf("flag: %+v", r)
	}
}

// TC-906: discover start --wait shows progress; exit 0 on done, 1 on
// failed.
func TestDiscoverWait(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{
		Answer: func(a netip.Addr) bool { return a.As4()[3]%2 == 1 }})
	u := "--url=" + s.URL()
	out := ok(t, u, "discover", "start", "10.1.0.0/28", "--wait")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "running") ||
		!strings.Contains(lines[len(lines)-1], "done probed 14/14 found 7 added 7") {
		t.Fatalf("--wait output:\n%s", out)
	}
	contains(t, ok(t, u, "discover", "status", strings.Fields(lines[0])[1]), "done")
	contains(t, ok(t, u, "discover", "list"), strings.Fields(lines[0])[1]+" 10.1.0.0/28 done") // TC-217
	if out := ok(t, u, "host", "list"); strings.Count(out, "\n") != 8 {
		t.Fatalf("hosts:\n%s", out)
	}
	out = ok(t, u, "--json", "discover", "start", "--group", "1", "--wait")
	var last apitypes.Discovery
	ls := strings.Split(strings.TrimSpace(out), "\n")
	// The first line is the POST response, compact (C3, crosscheck 05).
	if !strings.HasPrefix(ls[0], `{"id":`) || !strings.Contains(ls[0], `"state":"running"`) {
		t.Fatalf("first --json line: %s", ls[0])
	}
	if err := json.Unmarshal([]byte(ls[len(ls)-1]), &last); err != nil || last.State != apitypes.DiscoveryDone || last.Added != 0 {
		t.Fatalf("--json --wait: %v\n%s", err, out)
	}
	// A job whose results can't be saved fails.
	ok(t, u, "group", "add", "--cidr", "10.4.0.0/29")
	s.BreakStore()
	r := ctl(t, u, "discover", "start", "--group", "2", "--wait")
	if r.code != 1 || !strings.Contains(r.out, "failed") {
		t.Fatalf("failed job: %+v", r)
	}
}

func TestDiscoverCancel(t *testing.T) {
	isolate(t)
	gate := make(chan struct{})
	s := testserver.Start(t, testserver.Options{Gate: gate})
	u := "--url=" + s.URL()
	out := ok(t, u, "discover", "start", "10.3.0.0/29")
	contains(t, out, "10.3.0.0/29 running")
	job := strings.Fields(out)[1]
	contains(t, ok(t, u, "discover", "cancel", job), job)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(ok(t, u, "discover", "status", job), "cancelled") {
		if time.Now().After(deadline) {
			t.Fatal("job not cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// syncBuf is a goroutine-safe buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TC-901: events prints one line per event until interrupted, then exits 0.
func TestEvents(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	for _, js := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var out, errOut syncBuf
		args := []string{"--url=" + s.URL(), "events"}
		if js {
			args = append(args, "--json")
		}
		done := make(chan int)
		go func() { done <- run(ctx, args, nil, &out, &errOut) }()
		ip := "192.0.2.10"
		if js {
			ip = "192.0.2.11"
		}
		// Retry until the stream is connected and the event arrives.
		deadline := time.Now().Add(5 * time.Second)
		created := false
		for !strings.Contains(out.String(), ip) {
			if time.Now().After(deadline) {
				t.Fatalf("no event:\n%s%s", out.String(), errOut.String())
			}
			if !created && strings.Contains(get(t, s, "/health"), "ok") {
				time.Sleep(50 * time.Millisecond) // let the stream connect
				ok(t, "--url="+s.URL(), "host", "add", ip)
				created = true
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		if code := <-done; code != 0 {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
		line := strings.TrimSpace(out.String())
		if js {
			var ev struct {
				ID, Type string
				Data     apitypes.Host
			}
			if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "host.created" || ev.Data.IP != ip {
				t.Fatalf("json event %q: %v", line, err)
			}
		} else if !strings.Contains(line, "host.created") || !strings.Contains(line, "run1-") {
			t.Fatalf("event line %q", line)
		}
	}
}

// C1 (crosscheck 05): Ctrl-C during discover --wait exits 130 and says the
// job keeps running.
func TestInterruptWait(t *testing.T) {
	isolate(t)
	gate := make(chan struct{}) // probes never finish
	s := testserver.Start(t, testserver.Options{Gate: gate})
	ctx, cancel := context.WithCancel(context.Background())
	var out, errOut syncBuf
	done := make(chan int)
	go func() {
		done <- run(ctx, []string{"--url=" + s.URL(), "discover", "start", "10.5.0.0/29", "--wait"}, nil, &out, &errOut)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "running") {
		if time.Now().After(deadline) {
			t.Fatal("job didn't start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 130 {
		t.Fatalf("exit %d", code)
	}
	contains(t, errOut.String(), "interrupted; job run1-1 keeps running on the server (inedctl discover cancel run1-1)")
	if strings.Contains(errOut.String(), "inedctl -h") {
		t.Fatal("usage hint on an interrupt")
	}
}

// C4 (crosscheck 05): an empty flag still overrides; I1: token warning.
func TestEmptyFlagAndWarning(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{Token: "secret"})
	t.Setenv("INED_URL", s.URL())
	t.Setenv("INED_TOKEN", "secret")
	ok(t, "host", "list")
	if r := ctl(t, "--token=", "host", "list"); r.code != 3 {
		t.Fatalf("--token= didn't clear the token: %+v", r)
	}
	if r := ctl(t, "host", "list"); strings.Contains(r.errOut, "warning") {
		t.Fatalf("warning on loopback: %q", r.errOut)
	}
	// Config errors don't get the usage hint (C2).
	if r := ctl(t, "--ca-cert=/nonexistent.pem", "host", "list"); r.code != 2 || strings.Contains(r.errOut, "inedctl -h") {
		t.Fatalf("config error: %+v", r)
	}
	// 0.0.0.0 isn't loopback: the token would cross the network.
	r := ctl(t, "--url=http://0.0.0.0:1", "health")
	contains(t, r.errOut, "warning: sending the token unencrypted to http://0.0.0.0:1")
}

func TestFormatRTT(t *testing.T) {
	for v, want := range map[float64]string{0.042: "0.04 ms", 0.5: "0.50 ms", 1: "1.0 ms", 12.345: "12.3 ms"} {
		if got := ms(&v); got != want {
			t.Errorf("%v: %q, want %q", v, got, want)
		}
	}
	if ms(nil) != "-" {
		t.Error("nil")
	}
}

// TC-129: inedctl view commands and --view.
func TestViewCommands(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	u := "--url=" + s.URL()
	contains(t, ok(t, u, "view", "add", "Payroll"), "ID:", "2", "Kind:", "custom", "Order:", "1")
	ok(t, u, "view", "add", "Lab", "--order", "-1")
	list := ok(t, u, "view", "list")
	lines := strings.Split(strings.TrimSpace(list), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[1], "Lab") || !strings.Contains(lines[2], "Networks") {
		t.Fatalf("view list (tab order):\n%s", list)
	}
	contains(t, ok(t, u, "view", "rename", "2", "Pay"), "Name:", "Pay")
	contains(t, ok(t, u, "view", "order", "2", "9"), "Order:", "9")
	contains(t, ok(t, u, "group", "add", "--name", "app", "--view", "2"), "ID:", "1")
	ok(t, u, "group", "add", "--cidr", "10.0.0.0/24")
	if out := ok(t, u, "group", "list", "--view", "2"); strings.Count(out, "\n") != 2 || !strings.Contains(out, "app") {
		t.Fatalf("group list --view:\n%s", out)
	}
	// Without --view, every group with its view (crosscheck 2026-11-11-01 C3).
	all := ok(t, u, "group", "list")
	contains(t, all, "COLLAPSED  VIEW")
	for _, line := range strings.Split(strings.TrimSpace(all), "\n")[1:] {
		f := strings.Fields(line)
		if want := map[string]string{"1": "2", "2": "1"}[f[0]]; f[len(f)-1] != want {
			t.Errorf("group list: view of group %s is %s, want %s", f[0], f[len(f)-1], want)
		}
	}
	contains(t, ok(t, u, "group", "show", "1"), "View:", "2")
	if r := ctl(t, u, "view", "show", "99"); r.code != 1 || r.out != "" {
		t.Fatalf("view show of a missing view: %+v", r)
	}
	ok(t, u, "host", "add", "10.0.0.5", "--name", "web")
	contains(t, ok(t, u, "view", "place", "2", "1", "--parent", "1"), "View:", "2", "Host:", "1", "Parent:", "1")
	contains(t, ok(t, u, "view", "place", "2", "1", "--x", "5", "--y", "6"), "Parent:   root", "Position: 5, 6")
	show := ok(t, u, "view", "show", "2")
	contains(t, show, "Name:", "Pay", "ID  IP        NAME  STATUS   PARENT", "1   10.0.0.5  web   unknown  root")
	var v apitypes.View
	if err := json.Unmarshal([]byte(ok(t, u, "--json", "view", "show", "2")), &v); err != nil || v.Name != "Pay" {
		t.Fatalf("view show --json: %+v %v", v, err)
	}
	ok(t, u, "view", "unplace", "2", "1")
	if r := ctl(t, u, "view", "unplace", "2", "1"); r.code != 1 {
		t.Fatalf("unplace twice: %+v", r)
	}
	if r := ctl(t, u, "view", "rm", "1"); r.code != 1 || !strings.Contains(r.errOut, "network view") {
		t.Fatalf("rm view 1: %+v", r)
	}
	ok(t, u, "view", "rm", "2")
	if out := ok(t, u, "group", "list"); strings.Contains(out, "app") {
		t.Fatalf("groups of a deleted view remain:\n%s", out)
	}
	if out := ok(t, u, "host", "list"); !strings.Contains(out, "web") {
		t.Fatal("hosts must stay")
	}
}

// TC-715, TC-716 (CLI): group shift, and move puts the item next to its
// new parent.
func TestGroupShift(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	u := "--url=" + s.URL()
	ok(t, u, "group", "add", "--name", "a")
	ok(t, u, "group", "add", "--name", "b")
	contains(t, ok(t, u, "group", "shift", "1", "--dx", "-40", "--dy", "60"), "Position:", "0, 100")
	if r := ctl(t, u, "group", "shift", "1"); r.code != 2 || !strings.Contains(r.errOut, "--dx") {
		t.Fatalf("shift without offset: %+v", r)
	}
	// b (at 140,40) into a (at 0,100): next to a, one row below.
	contains(t, ok(t, u, "group", "move", "2", "1"), "Parent:", "1", "Position:", "0, 220")
}

// TC-740 (CLI): group copy.
func TestGroupCopy(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	u := "--url=" + s.URL()
	ok(t, u, "group", "add", "--name", "lan")
	ok(t, u, "host", "add", "192.0.2.1", "--parent", "1")
	ok(t, u, "view", "add", "Payroll")
	contains(t, ok(t, u, "group", "copy", "1", "--view", "2"), "copied group 1 to view 2 as group 2: 1 group, 1 host (0 moved within the view)\n")
	contains(t, ok(t, u, "group", "show", "2"), "lan", "View:", "2")
	if r := ctl(t, u, "group", "copy", "1"); r.code != 2 {
		t.Errorf("no --view: %+v", r)
	}
	if r := ctl(t, u, "group", "copy", "1", "--view", "1"); r.code != 1 {
		t.Errorf("network view: %+v", r)
	}
}

// TC-724 (CLI): view arrange.
func TestViewArrange(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	u := "--url=" + s.URL()
	ok(t, u, "group", "add", "--name", "g")
	ok(t, u, "host", "add", "192.0.2.1", "--parent", "1")
	ok(t, u, "host", "add", "192.0.2.2", "--parent", "1")
	contains(t, ok(t, u, "view", "arrange", "1", "--group", "1", "--width", "300"), "arranged 1 member\n")
	contains(t, ok(t, u, "host", "show", "2"), "Position:", "340, 160")
	if r := ctl(t, u, "view", "arrange", "1", "--width", "3"); r.code != 1 {
		t.Errorf("bad width: %+v", r)
	}
}

// TC-510 (CLI): host services shows "not checked yet", --check checks the
// ports and shows what answered; host show has a Services line.
func TestHostServices(t *testing.T) {
	isolate(t)
	open := map[string]bool{"192.0.2.1:22": true, "192.0.2.1:5901": true}
	s := testserver.Start(t, testserver.Options{Services: services.Config{
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			if open[addr] {
				c, _ := net.Pipe()
				return c, nil
			}
			return nil, errors.New("connection refused")
		},
	}})
	u := "--url=" + s.URL()
	ok(t, u, "host", "add", "192.0.2.1")
	contains(t, ok(t, u, "host", "services", "1"), "not checked yet\n")
	contains(t, ok(t, u, "host", "services", "1", "--check"), "ssh, vnc :1 (checked ")
	contains(t, ok(t, u, "host", "show", "1"), "Services:", "ssh, vnc :1 (checked ")
	if r := ctl(t, u, "host", "services", "9", "--check"); r.code != 1 {
		t.Errorf("unknown host: %+v", r)
	}
	if r := ctl(t, u, "host", "services"); r.code != 2 {
		t.Errorf("no id: %+v", r)
	}
}

// TC-909: host snmp shows "not checked yet", --check finds the agent
// and prints its system values and latest values; host show has an SNMP
// line; without SNMP on the server --check is exit 1 with the reason; the
// community is never printed.
func TestHostSNMP(t *testing.T) {
	isolate(t)
	cpu := 13
	s := testserver.Start(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"s3cret"}, Client: snmp.Static{
		Community: "s3cret", Version: snmp.V2c,
		Sys: inventory.SNMPSystem{Name: "web", Descr: "Linux web 6.8.0", Location: "rack 4"},
		Vals: apitypes.SNMPValues{UptimeS: 1053224, CPUPct: &cpu, Storage: []apitypes.SNMPStorage{
			{Name: "Physical memory", Kind: apitypes.StorageMemory, SizeBytes: 1 << 33, UsedPct: 63},
			{Name: "/", Kind: apitypes.StorageDisk, SizeBytes: 1 << 35, UsedPct: 91}}},
	}}})
	u := "--url=" + s.URL()
	ok(t, u, "host", "add", "192.0.2.1")
	contains(t, ok(t, u, "host", "snmp", "1"), "SNMP: not checked yet\n")
	out := ok(t, u, "host", "snmp", "1", "--check")
	contains(t, out, "SNMP:", "v2c (checked ", "Name:", "web", "Location:", "rack 4", "Uptime:", "12 d 4 h",
		"Values:", "CPU 13 %, memory 63 %, / 91 % (read ")
	contains(t, ok(t, u, "host", "show", "1"), "SNMP:", "v2c · web · CPU 13 %, memory 63 %, / 91 %")
	if j := ok(t, u, "--json", "host", "show", "1"); strings.Contains(j, "s3cret") || !strings.Contains(j, `"version": "v2c"`) {
		t.Errorf("json: %s", j)
	}
	off := testserver.Start(t, testserver.Options{})
	u2 := "--url=" + off.URL()
	ok(t, u2, "host", "add", "192.0.2.1")
	if r := ctl(t, u2, "host", "snmp", "1", "--check"); r.code != 1 || !strings.Contains(r.errOut, "snmp.enabled = false") {
		t.Errorf("SNMP off: %+v", r)
	}
}

// TC-910: snmp-communities on a host and a group (a list, stdin, --clear),
// the Communities rows, never the strings; snmp check --group --wait and
// snmp list; mistakes are usage errors.
func TestSNMPCommunitiesAndChecks(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{SNMP: &snmp.Config{Communities: []string{"public"}, Client: snmp.Static{
		Community: "lab-ro", Version: snmp.V2c, Sys: inventory.SNMPSystem{Name: "web"}}}})
	u := "--url=" + s.URL()
	ok(t, u, "group", "add", "--name", "lab")
	ok(t, u, "host", "add", "192.0.2.1", "--parent", "1")
	ok(t, u, "host", "add", "192.0.2.2", "--parent", "1")
	contains(t, ok(t, u, "host", "show", "1"), "Communities:", "the server's list")
	contains(t, ok(t, u, "group", "snmp-communities", "1", "lab-ro,other"), "SNMP communities: 2 set here")
	contains(t, ok(t, u, "host", "show", "1"), "Communities:", "from group 1")
	if r := ctlIn(t, "mine\nyours\n\n", u, "host", "snmp-communities", "2", "-"); r.code != 0 || !strings.Contains(r.out, "2 set here") {
		t.Fatalf("stdin: %+v", r)
	}
	contains(t, ok(t, u, "host", "snmp-communities", "2", "--clear"), "SNMP communities: from group 1")
	for _, o := range []string{ok(t, u, "--json", "host", "show", "1"), ok(t, u, "--json", "group", "show", "1")} {
		if strings.Contains(o, "lab-ro") || strings.Contains(o, "mine") {
			t.Errorf("shown: %s", o)
		}
	}
	// --json --wait: one JSON line at the start, one at the end (crosscheck 2026-11-19-01 C2).
	if r := ctl(t, u, "--json", "snmp", "check", "--group", "1", "--wait"); r.code != 0 || strings.Count(r.out, "\n") != 2 ||
		!strings.Contains(r.out, `"state":"running"`) || !strings.Contains(r.out, `"state":"done"`) {
		t.Fatalf("--json --wait: %+v", r)
	}
	// "a, a" is a twice after trimming (L7); a custom-view group has no list (L8).
	if r := ctl(t, u, "host", "snmp-communities", "1", "a, a"); r.code != 2 {
		t.Errorf("a, a: %+v", r)
	}
	ok(t, u, "view", "add", "v")
	ok(t, u, "group", "add", "--name", "c", "--view", "2")
	contains(t, ok(t, u, "group", "show", "2"), "Communities:", "– (custom view)")
	out := ok(t, u, "snmp", "check", "--group", "1", "--wait")
	contains(t, out, " running 0/2", " done 2/2: 2 answered, 0 no answer, 0 skipped")
	contains(t, ok(t, u, "snmp", "list"), " done 2/2: 2 answered")
	for _, args := range [][]string{
		{"snmp", "check"}, {"snmp", "check", "--group", "1", "2"}, {"host", "snmp-communities", "1"},
		{"host", "snmp-communities", "1", "a", "--clear"}, {"host", "snmp-communities", "1", ","},
	} {
		if r := ctl(t, append([]string{u}, args...)...); r.code != 2 {
			t.Errorf("%v: %+v", args, r)
		}
	}
}

// TC-907: host watch sets, adds to, removes from and clears the watched
// services and shows them; host show has a Watched line, host list an SVC
// column; mistakes are reported.
func TestHostWatch(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	u := "--url=" + s.URL()
	ok(t, u, "host", "add", "192.0.2.1")
	contains(t, ok(t, u, "host", "watch", "1"), "none\n")
	contains(t, ok(t, u, "host", "watch", "1", "ssh, HTTPS,tcp:05432"), "ssh unknown, https unknown, tcp:5432 unknown\n")
	contains(t, ok(t, u, "host", "watch", "1", "--add", "vnc:1"), "ssh unknown, https unknown, tcp:5432 unknown, vnc :1 unknown\n")
	contains(t, ok(t, u, "host", "watch", "1", "--remove", "https,TCP:5432"), "ssh unknown, vnc :1 unknown\n")
	contains(t, ok(t, u, "host", "watch", "1", "--add", "SSH"), "ssh unknown, vnc :1 unknown\n") // watched already
	// The same port under another name: nothing to do either (L6 of
	// crosscheck 2026-11-18-01).
	contains(t, ok(t, u, "host", "watch", "1", "--add", "tcp:22,tcp:5901"), "ssh unknown, vnc :1 unknown\n")
	contains(t, ok(t, u, "host", "show", "1"), "Watched:", "ssh unknown, vnc :1 unknown")
	contains(t, ok(t, u, "host", "list"), "SVC", "unknown")
	contains(t, ok(t, u, "--json", "host", "watch", "1"), `"watch": [`, `"name": "ssh",`, `"port": 22,`)
	contains(t, ok(t, u, "host", "watch", "1", "--clear"), "none\n")
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"1", "ssh,ftp"}, 1, `unknown service "ftp"`},
		{[]string{"1", "ssh,tcp:22"}, 1, `"ssh" and "tcp:22" are the same port 22`},
		{[]string{"1", "tcp:70000"}, 1, "from 1 to 65535"},
		{[]string{"1", "--remove", "rdp"}, 2, "rdp is not watched on host 1"},
		{[]string{"1", "ssh", "--clear"}, 2, "give one of"},
		{[]string{"1", ""}, 2, "an empty list; use --clear"},
		{[]string{"1", " , "}, 2, "an empty list; use --clear"},
		{[]string{"1", "--add", "ftp"}, 2, `unknown service "ftp"`},
		{[]string{"9", "ssh"}, 1, "not found"},
		{[]string{}, 2, "missing argument"},
	} {
		r := ctl(t, append([]string{u, "host", "watch"}, c.args...)...)
		if r.code != c.code || !strings.Contains(r.errOut, c.msg) {
			t.Errorf("host watch %v: code %d, %q; want %d, %q", c.args, r.code, r.errOut, c.code, c.msg)
		}
	}
}

// TC-908: group export to a file (0600) and stdout; import into another
// server: a dry run, the import, the same file again refused with the
// conflicts (exit 1, also as a dry run), --update; stdin; usage errors;
// a file the server refuses.
func TestExportImport(t *testing.T) {
	isolate(t)
	a := testserver.Start(t, testserver.Options{})
	b := testserver.Start(t, testserver.Options{})
	ua, ub := "--url="+a.URL(), "--url="+b.URL()
	ok(t, ua, "group", "add", "--cidr", "10.0.1.0/24", "--name", "lan")
	ok(t, ua, "host", "add", "10.0.1.10", "--name", "web", "--note", "front")
	ok(t, ua, "host", "watch", "1", "ssh")
	file := filepath.Join(t.TempDir(), "lan.json")
	contains(t, ok(t, ua, "group", "export", "1", "-o", file), "exported Networks › lan: 1 group, 1 host to "+file)
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", st, err)
	}
	// An existing file gets 0600 too (L3 of crosscheck 2026-11-17-01).
	os.Chmod(file, 0o644)
	ok(t, ua, "group", "export", "1", "-o", file)
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("existing file: %v %v", st, err)
	}
	// A device keeps its mode, without an error (L7 of crosscheck
	// 2026-11-18-01).
	ok(t, ua, "group", "export", "1", "-o", os.DevNull)
	contains(t, ok(t, ua, "group", "export", "1"), `"format": "go-ined-network"`, `"watch": [`)

	contains(t, ok(t, ub, "import", file, "--dry-run"), "Would create: 1 group, 1 host.\nConflicts: none.\n")
	if out := ok(t, ub, "host", "list"); strings.Contains(out, "10.0.1.10") {
		t.Fatalf("the dry run made hosts: %q", out)
	}
	contains(t, ok(t, ub, "import", file, "--x", "300", "--y", "200"), "Created: 1 group, 1 host.\nConflicts: none.\nImported as group 1.\n")
	contains(t, ok(t, ub, "host", "show", "1"), "front", "ssh unknown")

	for _, extra := range [][]string{nil, {"--dry-run"}} {
		r := ctl(t, append([]string{ub, "import", file}, extra...)...)
		if r.code != 1 || !strings.Contains(r.out, "Conflicts (2):\n  network group 10.0.1.0/24 exists already") ||
			!strings.Contains(r.errOut, "refused: 2 conflicts; nothing was changed (--update updates them)") {
			t.Errorf("again %v: %d %q %q", extra, r.code, r.out, r.errOut)
		}
	}
	data, _ := os.ReadFile(file)
	edited := strings.Replace(string(data), `"name": "web"`, `"name": "web2"`, 1)
	r := ctlIn(t, edited, ub, "import", "-", "--update")
	if r.code != 0 || !strings.Contains(r.out, "Updated: 1 group, 1 host.") {
		t.Fatalf("update: %d %q %q", r.code, r.out, r.errOut)
	}
	contains(t, ok(t, ub, "host", "show", "1"), "web2")
	contains(t, ok(t, ub, "--json", "import", file, "--dry-run", "--update"), `"dry_run": true`)

	for _, c := range []struct {
		args  []string
		stdin string
		code  int
		msg   string
	}{
		{[]string{file, "--x", "1"}, "", 2, "give --x and --y together"},
		{[]string{file, "--parent", "0"}, "", 2, "--parent must be a positive group id"},
		{[]string{"-"}, "not json", 2, "- is not JSON"},
		{[]string{"-"}, `{"format": "go-ined-network", "version": 1, "oops": 1}`, 1, `unknown field "oops"`},
		{[]string{filepath.Join(t.TempDir(), "none.json")}, "", 2, "no such file"},
		{[]string{}, "", 2, "missing argument"},
	} {
		r := ctlIn(t, c.stdin, append([]string{ub, "import"}, c.args...)...)
		if r.code != c.code || !strings.Contains(r.errOut, c.msg) {
			t.Errorf("import %v: code %d, %q; want %d, %q", c.args, r.code, r.errOut, c.code, c.msg)
		}
	}
	if r := ctl(t, ua, "group", "export", "9"); r.code != 1 || !strings.Contains(r.errOut, "not found") {
		t.Errorf("export 9: %+v", r)
	}
}

// TC-910 (crosscheck 2026-11-19-02 L3): from stdin each line is kept as
// typed, spaces too, without a Windows CR; blank lines are left out. The
// comma list is trimmed.
func TestCommunitiesArgKeepsSpaces(t *testing.T) {
	a := &app{stdin: strings.NewReader(" a b \r\n\n   \nc\n")}
	got, err := a.communitiesArg("-", false)
	if err != nil || !slices.Equal(got, []string{" a b ", "c"}) {
		t.Errorf("stdin: %q %v", got, err)
	}
	if got, _ := a.communitiesArg(" x , y", false); !slices.Equal(got, []string{"x", "y"}) {
		t.Errorf("comma list: %q", got)
	}
}

// TC-911 (SPEC-901, 315 v0.69): `view snmp-communities` sets a custom
// view's list; a host placed there says "from view 2"; `view list` and
// `view show` have the count, the network view "-"; the network view's
// list is refused; --clear inherits; no string is printed.
func TestViewCommunitiesCtl(t *testing.T) {
	isolate(t)
	s := testserver.Start(t, testserver.Options{})
	u := "--url=" + s.URL()
	ok(t, u, "host", "add", "192.0.2.1")
	ok(t, u, "view", "add", "switches")
	ok(t, u, "view", "place", "2", "1")
	contains(t, ok(t, u, "view", "snmp-communities", "2", "sw-ro,sw-2"), "SNMP communities: 2 set here")
	contains(t, ok(t, u, "host", "show", "1"), "Communities:", "from view 2")
	out := ok(t, u, "view", "list")
	contains(t, out, "COMMUNITIES", "2 set here")
	contains(t, ok(t, u, "view", "show", "1"), "Communities:", "-")
	contains(t, ok(t, u, "view", "show", "2"), "Communities:", "2 set here")
	if r := ctl(t, u, "view", "snmp-communities", "1", "x"); r.code == 0 {
		t.Errorf("the network view took a list: %+v", r)
	}
	contains(t, ok(t, u, "view", "snmp-communities", "2", "--clear"), "SNMP communities: none set here")
	contains(t, ok(t, u, "host", "show", "1"), "Communities:", "the server's list")
	if strings.Contains(out+ok(t, u, "--json", "view", "show", "2"), "sw-ro") {
		t.Error("a string is printed")
	}
}
