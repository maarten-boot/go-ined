package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/internal/config"
	"github.com/maarten-boot/go-ined/internal/theme"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("INED_URL", "")
	t.Setenv("INED_TOKEN", "")
	return dir
}

// TC-807: -version prints to stdout and exits 0, without a display.
func TestVersion(t *testing.T) {
	isolate(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"-version"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "ined-gui ") {
		t.Fatalf("code %d, out %q, err %q", code, out.String(), errOut.String())
	}
	if code := run([]string{"-h"}, &out, &errOut); code != 0 {
		t.Errorf("-h: %d", code)
	}
	for _, args := range [][]string{{"-bogus"}, {"extra"}, {"-url", "ftp://x"}} {
		if code := run(args, &out, &errOut); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// TC-707: flags, then INED_URL / INED_TOKEN, then gui.toml, then defaults.
func TestSettingsPrecedence(t *testing.T) {
	dir := isolate(t)
	s, err := resolve(nil, &bytes.Buffer{})
	if err != nil || s.client.URL != "http://127.0.0.1:8470" || s.client.Token != "" || s.contrast != theme.Default {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	if want := filepath.Join(dir, config.AppDir, "gui.toml"); s.configPath != want {
		t.Errorf("config path %q, want %q", s.configPath, want)
	}

	file := filepath.Join(dir, "gui.toml")
	os.WriteFile(file, []byte("[server]\nurl = \"http://file:1\"\ntoken = \"file\"\nca_cert = \"ca.pem\"\n[contrast]\npreset = \"AAA\"\n"), 0o600)
	s, _ = resolve([]string{"-config", file}, &bytes.Buffer{})
	if s.client.URL != "http://file:1" || s.client.Token != "file" || s.client.CACert != filepath.Join(dir, "ca.pem") ||
		s.contrast.Preset != theme.PresetAAA || s.configPath != file {
		t.Errorf("file: %+v", s)
	}
	t.Setenv("INED_URL", "http://env:2")
	t.Setenv("INED_TOKEN", "env")
	s, _ = resolve([]string{"-config", file}, &bytes.Buffer{})
	if s.client.URL != "http://env:2" || s.client.Token != "env" {
		t.Errorf("env: %+v", s.client)
	}
	s, _ = resolve([]string{"-config", file, "-url", "http://flag:3", "-token", "", "-ca-cert", "x.pem"}, &bytes.Buffer{})
	if s.client.URL != "http://flag:3" || s.client.Token != "" || s.client.CACert != "x.pem" {
		t.Errorf("flags: %+v", s.client)
	}
	if _, err := resolve([]string{"-config", filepath.Join(dir, "missing.toml")}, &bytes.Buffer{}); err == nil {
		t.Error("missing explicit config accepted")
	}
}

// TC-006: an invalid contrast setting falls back to AA with a warning.
func TestBadContrast(t *testing.T) {
	dir := isolate(t)
	file := filepath.Join(dir, "gui.toml")
	for _, src := range []string{"[contrast]\npreset = \"loud\"\n", "[contrast]\npreset = \"custom\"\nmin_ratio = 3.0\n"} {
		os.WriteFile(file, []byte(src), 0o600)
		s, err := resolve([]string{"-config", file}, &bytes.Buffer{})
		if err != nil || s.contrast != theme.Default || len(s.warnings) != 1 || !strings.Contains(s.warnings[0], "using AA (4.5:1)") {
			t.Errorf("%q: %+v %v", src, s, err)
		}
	}
	os.WriteFile(file, []byte("[contrast]\npreset = \"custom\"\nmin_ratio = 12.5\n"), 0o600)
	if s, _ := resolve([]string{"-config", file}, &bytes.Buffer{}); s.contrast != (theme.Contrast{Preset: theme.PresetCustom, Min: 12.5}) || s.warnings != nil {
		t.Errorf("custom: %+v", s)
	}
}
