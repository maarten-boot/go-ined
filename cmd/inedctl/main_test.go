package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	contains(t, list, "ID", "IP", "NAME", "STATUS", "RTT", "PARENT", "MON")
	lines := strings.Split(strings.TrimSpace(list), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "1 ") || !strings.Contains(lines[1], "web") ||
		!strings.Contains(lines[2], "unknown") || !strings.HasSuffix(lines[2], "no") {
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
	contains(t, ok(t, u, "group", "color", "2", "none"), "Bar colour:  none")
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
