package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TC-009: a contrast change is written back to gui.toml; comments, other
// keys and the file mode stay.
func TestSaveContrast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gui.toml")
	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "gui.toml"))
	if err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(string(example), `token = ""`, `token = "s3cret"`, 1)
	if err := os.WriteFile(path, []byte(src), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SaveContrast(path, "custom", 9); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	for _, want := range []string{
		`preset    = "custom"             # AA (4.5:1)`,
		`min_ratio = 9.0 `, "# Used only when preset",
		`token = "s3cret"`,
		"# go-ined GUI configuration",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if n := strings.Count(text, "\n"); n != strings.Count(src, "\n") {
		t.Errorf("line count %d, want %d", n, strings.Count(src, "\n"))
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	cfg, err := LoadGUI(path)
	if err != nil || cfg.Contrast.Preset != "custom" || cfg.Contrast.MinRatio != 9 || cfg.Server.Token != "s3cret" {
		t.Fatalf("reload: %+v %v", cfg, err)
	}
	if err := SaveContrast(path, "AA", 4.5); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := LoadGUI(path); cfg.Contrast.Preset != "AA" || cfg.Contrast.MinRatio != 4.5 {
		t.Fatalf("second save: %+v", cfg.Contrast)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".gui.toml-*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestSaveContrastCreates(t *testing.T) {
	dir := t.TempDir()
	// No file at all: created (with its folder), mode 0600.
	path := filepath.Join(dir, "sub", "gui.toml")
	if err := SaveContrast(path, "Max", 21); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("created file: %v %v", st, err)
	}
	if cfg, err := LoadGUI(path); err != nil || cfg.Contrast.Preset != "Max" || cfg.Contrast.MinRatio != 21 {
		t.Fatalf("created: %+v %v", cfg, err)
	}

	// A file without [contrast], and one with the section but no keys.
	for _, src := range []string{
		"[server]\nurl = \"http://h:1\"",
		"[contrast]\n# empty\n\n[server]\nurl = \"http://h:1\"\n",
	} {
		p := filepath.Join(dir, "gui.toml")
		os.WriteFile(p, []byte(src), 0o600)
		if err := SaveContrast(p, "AAA", 7); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadGUI(p)
		if err != nil || cfg.Contrast.Preset != "AAA" || cfg.Server.URL != "http://h:1" {
			out, _ := os.ReadFile(p)
			t.Fatalf("from %q: %+v %v\n%s", src, cfg, err, out)
		}
	}
	if p, _ := GUIPath("x.toml"); p != "x.toml" {
		t.Errorf("GUIPath: %s", p)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	if p, _ := GUIPath(""); p != filepath.Join(dir, AppDir, "gui.toml") {
		t.Errorf("GUIPath default: %s", p)
	}
}

// TC-723 (config): the wrap toggle is saved like the contrast; other
// sections stay.
func TestSaveWrapNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gui.toml")
	os.WriteFile(path, []byte("[contrast]\npreset = \"AAA\"  # mine\n"), 0o600)
	if err := SaveWrapNames(path, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGUI(path)
	if err != nil || cfg.Map.WrapNames || cfg.Contrast.Preset != "AAA" {
		t.Fatalf("after save: %+v %v", cfg, err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "# mine") {
		t.Errorf("comment lost:\n%s", b)
	}
	SaveWrapNames(path, true)
	if cfg, _ := LoadGUI(path); !cfg.Map.WrapNames {
		t.Error("not saved back to true")
	}
}
