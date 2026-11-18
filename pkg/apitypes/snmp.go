package apitypes

import "time"

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
// the same CPU and the same storage, in whole percents.
func (v SNMPValues) Same(o SNMPValues) bool {
	if !sameInt(v.CPUPct, o.CPUPct) || !sameInt(v.CPUMaxPct, o.CPUMaxPct) || v.CPUs != o.CPUs || len(v.Storage) != len(o.Storage) {
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
