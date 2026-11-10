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
