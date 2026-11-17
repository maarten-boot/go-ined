package apitypes

import (
	"strconv"
	"strings"
	"time"
)

// Network export and import (Design-network-export.md; SPEC-125, 513, 733, 905).

// ExportFormat and ExportVersion name the file format; an import refuses
// any other.
const (
	ExportFormat  = "go-ined-network"
	ExportVersion = 1
)

// Import limits (N5).
const (
	MaxImportItems = 10000 // groups and hosts together
	MaxImportDepth = 32    // levels of groups
)

// NetworkExport is the file: one group of the network view with
// everything below it (N1, N2, N5).
type NetworkExport struct {
	Format        string    `json:"format"`
	Version       int       `json:"version"`
	ExportedAt    time.Time `json:"exported_at"`
	ServerVersion string    `json:"server_version"`
	// From is where the group was, "Networks › dc › lan" (information only).
	From       string            `json:"from"`
	Group      ExportGroup       `json:"group"`
	ViewGroups []ExportViewGroup `json:"view_groups"`
}

// ExportGroup is a group with its subgroups and hosts. Positions are
// relative to the exported group, which is at 0, 0 (N2).
type ExportGroup struct {
	Name        string        `json:"name"`
	CIDR        string        `json:"cidr"`
	Description string        `json:"description"`
	Note        string        `json:"note"`
	Labels      string        `json:"labels"`
	Color       string        `json:"color"`
	Collapsed   bool          `json:"collapsed"`
	X           float64       `json:"x"`
	Y           float64       `json:"y"`
	Groups      []ExportGroup `json:"groups"`
	Hosts       []ExportHost  `json:"hosts"`
}

// ExportHost is a host without what the server measures (N3).
type ExportHost struct {
	IP          string            `json:"ip"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Note        string            `json:"note"`
	Monitored   bool              `json:"monitored"`
	Watch       []string          `json:"watch"`
	X           float64           `json:"x"`
	Y           float64           `json:"y"`
	Views       []ExportPlacement `json:"views"`
}

// ExportPlacement places a host in a custom view by the view's name and
// the names of the groups from the view's top level down to its parent
// (N4). Positions in custom views are the view's own.
type ExportPlacement struct {
	View string   `json:"view"`
	Path []string `json:"path"`
	X    float64  `json:"x"`
	Y    float64  `json:"y"`
}

// ExportViewGroup is a group on such a path, so an import can make it
// when it is missing (N4).
type ExportViewGroup struct {
	View        string   `json:"view"`
	Path        []string `json:"path"` // ending with this group's name
	Description string   `json:"description"`
	Note        string   `json:"note"`
	Labels      string   `json:"labels"`
	Color       string   `json:"color"`
	Collapsed   bool     `json:"collapsed"`
	X           float64  `json:"x"`
	Y           float64  `json:"y"`
}

// ImportCounts counts the items of an import.
type ImportCounts struct {
	Groups     int `json:"groups"`
	Hosts      int `json:"hosts"`
	ViewGroups int `json:"view_groups"` // groups in custom views
	Placements int `json:"placements"`  // in custom views
}

// ImportReport is what an import did, or would do (N7).
type ImportReport struct {
	DryRun    bool         `json:"dry_run"`
	Done      bool         `json:"done"`     // the changes were made
	GroupID   *int64       `json:"group_id"` // the imported (or updated) top group, when done
	Create    ImportCounts `json:"create"`
	Update    ImportCounts `json:"update"`
	Conflicts []string     `json:"conflicts"` // the first 50
	// MoreConflicts is how many conflicts are not listed.
	MoreConflicts int      `json:"more_conflicts"`
	Skipped       []string `json:"skipped"`  // views that don't exist
	Warnings      []string `json:"warnings"` // the import goes ahead
}

// ImportConflict is the body of a 409 from POST /import: the usual error
// (SPEC-504) with the report, which lists the conflicts.
type ImportConflict struct {
	Error  ErrorDetail  `json:"error"`
	Report ImportReport `json:"report"`
}

// MaxListedConflicts is how many conflicts a report lists.
const MaxListedConflicts = 50

// Lines describes a report for people, as inedctl and the GUI show it:
// "Created: 4 groups, 57 hosts.", "Skipped: …", "Warning: …",
// "Conflicts: none." or the conflicts, and "Imported as group 12.".
func (r ImportReport) Lines() []string {
	counts := func(c ImportCounts) string {
		var parts []string
		for _, x := range []struct {
			n    int
			noun string
		}{{c.Groups, "group"}, {c.Hosts, "host"}, {c.ViewGroups, "custom-view group"}, {c.Placements, "placement"}} {
			if x.n == 1 {
				parts = append(parts, "1 "+x.noun)
			} else if x.n > 1 {
				parts = append(parts, strconv.Itoa(x.n)+" "+x.noun+"s")
			}
		}
		if len(parts) == 0 {
			return "nothing"
		}
		return strings.Join(parts, ", ")
	}
	var out []string
	n := len(r.Conflicts) + r.MoreConflicts
	refused := n > 0 && !r.Done
	if !refused {
		verb, uverb := "Would create", "Would update"
		if r.Done {
			verb, uverb = "Created", "Updated"
		}
		out = append(out, verb+": "+counts(r.Create)+".")
		if r.Update != (ImportCounts{}) {
			out = append(out, uverb+": "+counts(r.Update)+".")
		}
	}
	for _, s := range r.Skipped {
		out = append(out, "Skipped: "+s+".")
	}
	for _, w := range r.Warnings {
		out = append(out, "Warning: "+w+".")
	}
	if n == 0 {
		out = append(out, "Conflicts: none.")
	} else {
		out = append(out, "Conflicts ("+strconv.Itoa(n)+"):")
		for _, c := range r.Conflicts {
			out = append(out, "  "+c)
		}
		if r.MoreConflicts > 0 {
			out = append(out, "  … and "+strconv.Itoa(r.MoreConflicts)+" more")
		}
	}
	if r.Done && r.GroupID != nil {
		out = append(out, "Imported as group "+strconv.FormatInt(*r.GroupID, 10)+".")
	}
	return out
}
