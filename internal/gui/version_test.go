package gui

import (
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/internal/testserver"
)

// TC-738 (U, SPEC-728): only a server whose release is older than the
// GUI's gets the warning; builds after the same release, newer servers and
// versions that aren't releases don't.
func TestVersionWarning(t *testing.T) {
	for _, c := range []struct {
		server, gui string
		warn        bool
	}{
		{"v0.2.0-5-g812912d-dirty", "v0.5.0", true},
		{"v0.4.1", "v0.5.0-4-g893940e", true},
		{"v0.5.0", "v1.0.0", true},
		{"v0.5.0", "v0.5.0-4-g893940e", false}, // same release
		{"v0.5.0-2-gabcdef0", "v0.5.0", false},
		{"v0.6.0", "v0.5.0", false}, // newer server
		{"v0.10.0", "v0.9.3", false},
		{"dev", "v0.5.0", false},
		{"v0.2.0", "dev", false},
		{"test", "v0.5.0", false},
		{"", "v0.5.0", false},
	} {
		w := versionWarning(c.server, c.gui)
		if (w != "") != c.warn {
			t.Errorf("%s vs %s: %q", c.server, c.gui, w)
		}
		if c.warn && (!strings.Contains(w, c.server) || !strings.Contains(w, c.gui) || !strings.Contains(w, "update")) {
			t.Errorf("text: %q", w)
		}
	}
}

// TC-738 (GUI, SPEC-728): connected to an older server, the GUI shows the
// warning line above the status bar, and "Copy" includes it; with a server
// of the same release there is none.
func TestVersionWarningShown(t *testing.T) {
	e := startWith(t, testserver.Options{Version: "v0.2.0-5-g812912d-dirty"})
	e.version = "v0.5.0"
	e.open()
	g := e.g
	eventually(t, e.ui, "warning shown", func() bool {
		return g.status.verL.Visible() && strings.Contains(g.status.verL.Text, "ined-server v0.2.0-5-g812912d-dirty is older than this GUI (v0.5.0)")
	})
	e.ui.Wait(func() {
		g.status.copyLog()
		if got := g.status.clip().Content(); !strings.Contains(got, "is older than this GUI") {
			t.Errorf("copied: %q", got)
		}
	})

	e2 := startWith(t, testserver.Options{Version: "v0.5.0"})
	e2.version = "v0.5.0-4-g893940e"
	e2.open()
	// A warning set by hand must be cleared by the next health check.
	e2.ui.Wait(func() { e2.g.status.version("stale") })
	go e2.g.checkHealth()
	eventually(t, e2.ui, "no warning", func() bool { return !e2.g.status.verL.Visible() && e2.g.status.verL.Text == "" })
}
