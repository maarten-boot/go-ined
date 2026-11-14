package gui

import (
	"fmt"
	"regexp"
	"strconv"
)

// ---- server version check (SPEC-728) ----

// releaseRe matches a release version as `git describe` gives it:
// v0.5.0, v0.5.0-4-g893940e, v0.2.0-5-g812912d-dirty.
var releaseRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(-\d+-g[0-9a-f]+)?(-dirty)?$`)

// release returns the major, minor and patch number of a version, or
// ok false for anything else ("dev", "test", "").
func release(v string) (r [3]int, ok bool) {
	m := releaseRe.FindStringSubmatch(v)
	if m == nil {
		return r, false
	}
	for i := range r {
		if r[i], ok = atoi(m[i+1]); !ok {
			return r, false
		}
	}
	return r, true
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// versionWarning is the warning line for a server whose release is older
// than the GUI's, or "". Builds after the same release don't count, nor
// versions that aren't releases (a development build, "dev").
func versionWarning(server, gui string) string {
	s, ok1 := release(server)
	g, ok2 := release(gui)
	if !ok1 || !ok2 || !older(s, g) {
		return ""
	}
	return fmt.Sprintf("ined-server %s is older than this GUI (%s): newer changes, such as a bar colour, are refused; update and restart the server", server, gui)
}

func older(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
