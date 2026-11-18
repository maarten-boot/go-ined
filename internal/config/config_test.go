package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TC-805: every examples/*.toml parses, has no unknown keys, and equals the
// built-in defaults. This keeps the reference files and the code in sync.
func TestExamplesMatchDefaults(t *testing.T) {
	ex := filepath.Join("..", "..", "examples")

	srv, err := LoadServer(filepath.Join(ex, "server.toml"))
	if err != nil {
		t.Fatalf("server.toml: %v", err)
	}
	if want := DefaultServer(); !reflect.DeepEqual(srv, want) {
		t.Errorf("server.toml differs from defaults:\n got %+v\nwant %+v", srv, want)
	}

	t.Setenv("INED_URL", "")
	t.Setenv("INED_TOKEN", "")
	gui, err := LoadGUI(filepath.Join(ex, "gui.toml"))
	if err != nil {
		t.Fatalf("gui.toml: %v", err)
	}
	if want := DefaultGUI(); !reflect.DeepEqual(gui, want) {
		t.Errorf("gui.toml differs from defaults:\n got %+v\nwant %+v", gui, want)
	}

	ctl, err := LoadClient(filepath.Join(ex, "ctl.toml"))
	if err != nil {
		t.Fatalf("ctl.toml: %v", err)
	}
	if want := DefaultClient(); !reflect.DeepEqual(ctl, want) {
		t.Errorf("ctl.toml differs from defaults:\n got %+v\nwant %+v", ctl, want)
	}
}

// TC-805: unknown keys are rejected.
func TestUnknownKeysRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.toml")
	if err := os.WriteFile(p, []byte("[server]\nlisten = \"127.0.0.1:1\"\nlisten_typo = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadServer(p)
	if err == nil || !strings.Contains(err.Error(), "server.listen_typo") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
}

// TC-804: file values override defaults; untouched keys keep defaults.
func TestServerFileOverridesDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.toml")
	if err := os.WriteFile(p, []byte("[monitor]\ninterval = \"30s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Monitor.Interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", cfg.Monitor.Interval)
	}
	if cfg.Monitor.Count != 3 || cfg.Server.Listen != "127.0.0.1:8470" {
		t.Errorf("defaults lost: %+v", cfg)
	}
}

// TC-804: a missing explicit file is an error; a missing default file is not.
func TestMissingFiles(t *testing.T) {
	if _, err := LoadServer(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("missing explicit file: want error")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := LoadServer(""); err != nil {
		t.Errorf("missing default file: %v", err)
	}
}

// TC-804: XDG variables, fallbacks to ~/.config and ~/.local/share, and
// relative XDG values are ignored.
func TestXDGPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if p, _ := DefaultPath("server.toml"); p != filepath.Join(home, ".config", "go-ined", "server.toml") {
		t.Errorf("config fallback = %s", p)
	}
	if p, _ := DefaultDBPath(); p != filepath.Join(home, ".local", "share", "go-ined", "ined.db") {
		t.Errorf("data fallback = %s", p)
	}

	t.Setenv("XDG_CONFIG_HOME", "/xdg/conf")
	t.Setenv("XDG_DATA_HOME", "relative/ignored")
	if p, _ := DefaultPath("gui.toml"); p != "/xdg/conf/go-ined/gui.toml" {
		t.Errorf("config xdg = %s", p)
	}
	if p, _ := DefaultDBPath(); p != filepath.Join(home, ".local", "share", "go-ined", "ined.db") {
		t.Errorf("relative XDG_DATA_HOME not ignored: %s", p)
	}
}

// TC-804, TC-707: environment beats file for clients.
func TestClientEnvOverridesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ctl.toml")
	if err := os.WriteFile(p, []byte("[server]\nurl = \"http://file:1\"\ntoken = \"filetok\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INED_URL", "http://env:2")
	t.Setenv("INED_TOKEN", "")
	cfg, err := LoadClient(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.URL != "http://env:2" || cfg.Server.Token != "filetok" {
		t.Errorf("got %+v", cfg.Server)
	}
}

func TestValidate(t *testing.T) {
	c := DefaultServer()
	c.Server.TLSCert = "x"
	c.Monitor.Count = 0
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "tls_key") || !strings.Contains(err.Error(), "monitor.count") {
		t.Errorf("want both errors, got %v", err)
	}
}

// TC-214 (config part): discovery.rate must be within 1..100000.
func TestDiscoveryRateBounds(t *testing.T) {
	for rate, ok := range map[int]bool{0: false, 1: true, 200: true, 100000: true, 100001: false, 2_000_000_000: false} {
		c := DefaultServer()
		c.Discovery.Rate = rate
		if err := c.Validate(); (err == nil) != ok {
			t.Errorf("rate %d: err = %v", rate, err)
		}
	}
}

// TC-318 (config part): watched services are checked every 5 minutes by
// default; the interval is at least 10 s, down_after and concurrency at
// least 1.
func TestWatchSettings(t *testing.T) {
	c := DefaultServer()
	if c.Services.WatchInterval != 5*time.Minute || c.Services.WatchDownAfter != 2 || c.Services.WatchConcurrency != 50 {
		t.Fatalf("defaults: %+v", c.Services)
	}
	for _, f := range []func(*Server){
		func(c *Server) { c.Services.WatchInterval = 9 * time.Second },
		func(c *Server) { c.Services.WatchDownAfter = 0 },
		func(c *Server) { c.Services.WatchConcurrency = 0 },
	} {
		c := DefaultServer()
		f(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "services.watch_interval must be at least 10s") {
			t.Errorf("%+v: err = %v", c.Services, err)
		}
	}
	c.Services.WatchInterval = 10 * time.Second
	if err := c.Validate(); err != nil {
		t.Errorf("10s: %v", err)
	}
}

// A relative ca_cert is relative to the config file's folder; an absolute
// one is kept.
func TestRelativeCACert(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INED_URL", "")
	t.Setenv("INED_TOKEN", "")
	for _, c := range []struct{ in, want string }{
		{"ca.pem", filepath.Join(dir, "ca.pem")},
		{"certs/ca.pem", filepath.Join(dir, "certs", "ca.pem")},
		{"/etc/ssl/ca.pem", "/etc/ssl/ca.pem"},
		{"", ""},
	} {
		p := filepath.Join(dir, "ctl.toml")
		if err := os.WriteFile(p, []byte("[server]\nca_cert = \""+c.in+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadClient(p)
		if err != nil || cfg.Server.CACert != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, cfg.Server.CACert, err, c.want)
		}
		g, err := LoadGUI(p)
		if err != nil || g.Server.CACert != c.want {
			t.Errorf("gui %q: got %q, %v", c.in, g.Server.CACert, err)
		}
	}
}

// TC-216: discovery.scheduled is on by default and can be turned off.
func TestDiscoveryScheduled(t *testing.T) {
	if !DefaultServer().Discovery.Scheduled {
		t.Error("off by default")
	}
	p := filepath.Join(t.TempDir(), "s.toml")
	if err := os.WriteFile(p, []byte("[discovery]\nscheduled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(p)
	if err != nil || c.Discovery.Scheduled || c.Discovery.Rate != 200 {
		t.Fatalf("%+v %v", c.Discovery, err)
	}
}

// TC-326 (config): [snmp] is off by default with "public"; bad values are
// rejected; networks are read as prefixes.
func TestSNMPConfig(t *testing.T) {
	d := DefaultServer().SNMP
	if d.Enabled || len(d.Communities) != 1 || d.Communities[0] != "public" || d.Port != 161 {
		t.Errorf("defaults: %+v", d)
	}
	p := filepath.Join(t.TempDir(), "s.toml")
	write := func(s string) error {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadServer(p)
		return err
	}
	if err := write("[snmp]\nenabled = true\ncommunities = [\"a\", \"b\"]\nnetworks = [\"10.0.0.0/8\", \" 192.168.1.7/24 \"]\n"); err != nil {
		t.Fatal(err)
	}
	c, _ := LoadServer(p)
	if n := c.SNMP.SNMPNetworks(); len(n) != 2 || n[1].String() != "192.168.1.0/24" {
		t.Errorf("networks: %v", n)
	}
	for _, bad := range []string{
		"[snmp]\nenabled = true\ncommunities = []\n", "[snmp]\ncommunities = [\"\"]\n", "[snmp]\nnetworks = [\"10.0.0.0\"]\n",
		"[snmp]\nport = 0\n", "[snmp]\nretries = 6\n", "[snmp]\ninterval = \"5s\"\n", "[snmp]\nrescan = \"30s\"\n", "[snmp]\nconcurrency = 0\n",
	} {
		if err := write(bad); err == nil || !strings.Contains(err.Error(), "snmp.") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
