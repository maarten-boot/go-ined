package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// GUIPath returns the GUI config file that LoadGUI reads for path: path
// itself, or the default location when path is empty.
func GUIPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	return DefaultPath("gui.toml")
}

var (
	sectionRE = regexp.MustCompile(`^\s*\[\s*([^\]\s]+)\s*\]`)
	keyRE     = func(key string) *regexp.Regexp {
		return regexp.MustCompile(`^(\s*` + key + `\s*=\s*)("[^"]*"|'[^']*'|[^\s#]+)(.*)$`)
	}
)

// SaveContrast writes the contrast preset and ratio into the [contrast]
// section of the GUI config file (SPEC-005, decision M5-7). Only those two
// values change: comments, other keys and their order stay. A missing
// file or section is created. The file is replaced atomically.
func SaveContrast(path, preset string, ratio float64) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("config: %w", err)
	}
	r := strconv.FormatFloat(ratio, 'f', -1, 64)
	if !strings.ContainsAny(r, ".eE") {
		r += ".0" // a TOML float, not an integer
	}
	values := map[string]string{"preset": strconv.Quote(preset), "min_ratio": r}
	out := setKeys(string(old), "contrast", []string{"preset", "min_ratio"}, values)

	mode := fs.FileMode(0o600) // the file may hold a token
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".gui.toml-*")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// setKeys sets keys (in this order when added) to values inside [section]
// of a TOML text, keeping everything else as it is.
func setKeys(text, section string, keys []string, values map[string]string) string {
	lines := strings.Split(text, "\n")
	if text == "" {
		lines = nil
	}
	start, end := -1, len(lines) // body of the section: lines[start+1 : end]
	for i, l := range lines {
		m := sectionRE.FindStringSubmatch(l)
		switch {
		case m == nil:
		case m[1] == section && start < 0:
			start = i
		case start >= 0:
			end = i
		}
		if start >= 0 && end != len(lines) {
			break
		}
	}
	if start < 0 {
		var b strings.Builder
		b.WriteString(text)
		if text != "" && !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
		if text != "" {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[%s]\n", section)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s = %s\n", k, values[k])
		}
		return b.String()
	}
	var missing []string
	for _, k := range keys {
		re, found := keyRE(k), false
		for i := start + 1; i < end; i++ {
			if m := re.FindStringSubmatch(lines[i]); m != nil {
				lines[i] = m[1] + values[k] + m[3]
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, k+" = "+values[k])
		}
	}
	if len(missing) > 0 {
		lines = append(lines[:start+1], append(missing, lines[start+1:]...)...)
	}
	return strings.Join(lines, "\n")
}
