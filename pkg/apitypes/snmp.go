package apitypes

import (
	"fmt"
	"time"
)

// EventSNMPValues carries a host's latest SNMP values (SNMPValuesEvent),
// sent only when a value rounded to a whole percent changes (SPEC-313).
const EventSNMPValues = "snmp.values"

// SNMP is what the server found out about a host's SNMP agent (SPEC-312,
// 313). Version is "v2c" or "v1", or "" when no community answered. The
// community that worked is never part of it.
type SNMP struct {
	Version   string      `json:"version"`
	Name      string      `json:"name"`      // sysName
	Descr     string      `json:"descr"`     // sysDescr
	ObjectID  string      `json:"object_id"` // sysObjectID
	Contact   string      `json:"contact"`   // sysContact
	Location  string      `json:"location"`  // sysLocation
	CheckedAt time.Time   `json:"checked_at"`
	Values    *SNMPValues `json:"values"` // null until read, and after a restart until the next round
}

// SNMPValues are the values read from HOST-RESOURCES-MIB (SPEC-313).
// CPUPct is the average load over the processors (logical CPUs), CPUMaxPct
// the busiest one's and CPUs how many there are (v0.62); the CPU fields
// are null (0) when the agent has no processor table (most switches and
// printers); Storage is empty then too.
type SNMPValues struct {
	At        time.Time     `json:"at"`
	UptimeS   int64         `json:"uptime_s"`
	CPUPct    *int          `json:"cpu_pct"`
	CPUMaxPct *int          `json:"cpu_max_pct"`
	CPUs      int           `json:"cpus"`
	Storage   []SNMPStorage `json:"storage"`
}

// SNMPStorage is one fixed disk or the RAM.
type SNMPStorage struct {
	Name      string `json:"name"` // "/", "C:\\ Label:…", "Physical memory"
	Kind      string `json:"kind"` // "disk" or "memory"
	SizeBytes int64  `json:"size_bytes"`
	UsedPct   int    `json:"used_pct"`
}

// Kinds of SNMPStorage.
const (
	StorageDisk   = "disk"
	StorageMemory = "memory"
)

// SNMPValuesEvent is the payload of snmp.values.
type SNMPValuesEvent struct {
	HostID      int64      `json:"host_id"`
	DisplayName string     `json:"display_name"`
	IP          string     `json:"ip"`
	Values      SNMPValues `json:"values"`
}

// Same reports whether two value sets differ only in time and uptime:
// the same CPU and the same storage, in whole percents, and the busiest
// CPU in the same step of 5 %.
func (v SNMPValues) Same(o SNMPValues) bool {
	// The busiest CPU moves at nearly every read: compared in steps of 5 %
	// (crosscheck 2026-11-18-04 L2).
	if !sameInt(v.CPUPct, o.CPUPct) || !sameInt(step5(v.CPUMaxPct), step5(o.CPUMaxPct)) || v.CPUs != o.CPUs || len(v.Storage) != len(o.Storage) {
		return false
	}
	for i := range v.Storage {
		if v.Storage[i] != o.Storage[i] {
			return false
		}
	}
	return true
}

func sameInt(a, b *int) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func step5(p *int) *int {
	if p == nil {
		return nil
	}
	s := *p / 5
	return &s
}

// Limits of a community list set on a group or host (SPEC-315).
const (
	MaxCommunities  = 10
	MaxCommunityLen = 255
)

// CheckCommunities checks a community list for a group or host: at most
// MaxCommunities strings of 1 to MaxCommunityLen bytes, none twice.
func CheckCommunities(list []string) error {
	if len(list) > MaxCommunities {
		return fmt.Errorf("at most %d SNMP community strings, not %d", MaxCommunities, len(list))
	}
	seen := map[string]bool{}
	for i, c := range list {
		switch {
		case c == "":
			return fmt.Errorf("SNMP community %d is empty", i+1)
		case len(c) > MaxCommunityLen:
			return fmt.Errorf("SNMP community %d is longer than %d bytes", i+1, MaxCommunityLen)
		case seen[c]:
			return fmt.Errorf("SNMP community %d is given twice", i+1)
		}
		seen[c] = true
	}
	return nil
}

// Events of SNMP check jobs (SPEC-515); the payload is the SNMPCheck.
const (
	EventSNMPCheckProgress = "snmp.check.progress"
	EventSNMPCheckFinished = "snmp.check.finished"
)

// SNMPCheckCreate is the body of POST /snmp/checks: a group (with its
// subgroups, in its view) or hosts.
type SNMPCheckCreate struct {
	GroupID *int64  `json:"group_id,omitempty"`
	HostIDs []int64 `json:"host_ids,omitempty"`
}

// SNMPCheck is an SNMP check job (SPEC-515): State running, done or
// cancelled; Found hosts answered, NoAnswer didn't, Skipped weren't asked
// (outside snmp.networks without a list of their own, or gone).
type SNMPCheck struct {
	ID         string     `json:"id"`
	GroupID    *int64     `json:"group_id"`
	State      string     `json:"state"`
	Total      int        `json:"total"`
	Checked    int        `json:"checked"`
	Found      int        `json:"found"`
	NoAnswer   int        `json:"no_answer"`
	Skipped    int        `json:"skipped"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}
