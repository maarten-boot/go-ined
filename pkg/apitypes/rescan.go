package apitypes

import (
	"fmt"
	"strings"
	"time"
)

// Limits of a group's rescan interval (SPEC-206).
const (
	MinRescan = 15 * time.Minute
	MaxRescan = 365 * 24 * time.Hour
)

// ParseRescan reads a rescan interval: "" or "off" is off (0), else a Go
// duration ("6h", "90m", "168h") between MinRescan and MaxRescan.
func ParseRescan(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "off") {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("rescan must be off or an interval such as 6h or 24h, not %q", s)
	}
	if d < MinRescan || d > MaxRescan {
		return 0, fmt.Errorf("rescan must be off or between %s and %s, not %s", FormatRescan(MinRescan), FormatRescan(MaxRescan), FormatRescan(d))
	}
	return d.Truncate(time.Second), nil
}

// FormatRescan writes an interval without zero parts: "24h", "1h30m",
// "15m"; 0 is "".
func FormatRescan(d time.Duration) string {
	if d == 0 {
		return ""
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
