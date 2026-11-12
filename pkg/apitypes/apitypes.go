// Package apitypes holds the JSON types of the go-ined REST API (/api/v1).
// It is shared by the server, pkg/client, the GUI and inedctl.
package apitypes

import (
	"bytes"
	"encoding/json"
	"reflect"
	"time"
)

// Status values of hosts and groups (SPEC-105).
const (
	StatusUp      = "up"
	StatusDown    = "down"
	StatusUnknown = "unknown"
)

// Item types in the map.
const (
	TypeHost  = "host"
	TypeGroup = "group"
)

// Error codes (SPEC-504).
const (
	CodeInvalidArgument  = "invalid_argument"
	CodeUnauthenticated  = "unauthenticated"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeConflict         = "conflict"
	CodeTooLarge         = "too_large"
	CodeInternal         = "internal"
	CodeUnavailable      = "unavailable" // 503, e.g. while shutting down
)

// Delete modes for a non-empty group (SPEC-104).
const (
	ChildrenReparent = "reparent"
	ChildrenCascade  = "cascade"
)

// Event types of GET /api/v1/events (SPEC-502).
const (
	EventHostCreated  = "host.created"
	EventHostUpdated  = "host.updated"
	EventHostDeleted  = "host.deleted"
	EventHostStatus   = "host.status"
	EventGroupCreated = "group.created"
	EventGroupUpdated = "group.updated"
	EventGroupDeleted = "group.deleted"
	EventGroupStatus  = "group.status"
	EventGroupMoved   = "group.moved"  // v0.21, SPEC-507
	EventMapArranged  = "map.arranged" // v0.28, SPEC-510

	EventViewCreated      = "view.created"
	EventViewUpdated      = "view.updated"
	EventViewDeleted      = "view.deleted"
	EventPlacementUpdated = "placement.updated"
	EventPlacementDeleted = "placement.deleted"

	EventDiscoveryProgress = "discovery.progress"
	EventDiscoveryFinished = "discovery.finished"
	EventResync            = "resync"
)

// Event payloads (SPEC-502). host.created/updated/status carry a Host and
// group.created/updated a Group.
type (
	// DeletedEvent is the payload of host.deleted and group.deleted.
	DeletedEvent struct {
		ID int64 `json:"id"`
	}
	// GroupStatusEvent is the payload of group.status.
	GroupStatusEvent struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	// GroupMovedEvent is the payload of group.moved: everything inside the
	// group (its subtree, not the group itself, whose new position comes
	// with group.updated) moved by DX, DY in view ViewID (SPEC-507, 508).
	GroupMovedEvent struct {
		ID     int64   `json:"id"`
		ViewID int64   `json:"view_id"`
		DX     float64 `json:"dx"`
		DY     float64 `json:"dy"`
	}
	// MapArrangedEvent is the payload of map.arranged: the members of a
	// group (ParentID nil: the root) in a view were laid out again; clients
	// reload the view (SPEC-510).
	MapArrangedEvent struct {
		ViewID   int64  `json:"view_id"`
		ParentID *int64 `json:"parent_id"`
	}
	// PlacementDeletedEvent is the payload of placement.deleted.
	PlacementDeletedEvent struct {
		ViewID int64 `json:"view_id"`
		HostID int64 `json:"host_id"`
	}
	// ResyncEvent is the payload of resync: reload the full state.
	ResyncEvent struct {
		Reason string `json:"reason"`
	}
)

// ICMP modes reported by /health (SPEC-304).
const (
	ICMPUnprivileged = "unprivileged"
	ICMPPrivileged   = "privileged"
	ICMPUnavailable  = "unavailable"
	ICMPNotStarted   = "not started"
	ICMPDisabled     = "disabled" // monitor.enabled = false
)

// View kinds (SPEC-117). The network view always has id NetworkViewID.
const (
	ViewNetwork   = "network"
	ViewCustom    = "custom"
	NetworkViewID = int64(1)
)

// View is one map: one tab in the GUI (SPEC-117).
type View struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Order     int64     `json:"order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ViewCreate is the body of POST /views. Without order the view goes last.
type ViewCreate struct {
	Name  string `json:"name"`
	Order *int64 `json:"order,omitempty"`
}

// ViewPatch is the body of PATCH /views/{id}.
type ViewPatch struct {
	Name  Opt[string] `json:"name,omitzero"`
	Order Opt[int64]  `json:"order,omitzero"`
}

// Placement puts a host in a custom view (SPEC-119). It is the response of
// PUT /views/{id}/hosts/{host_id} and the payload of placement.updated.
type Placement struct {
	ViewID   int64   `json:"view_id"`
	HostID   int64   `json:"host_id"`
	ParentID *int64  `json:"parent_id"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

// PlacementSet is the body of PUT /views/{id}/hosts/{host_id}. Without x
// and y, a new placement or one that changes parent gets a free position;
// otherwise the position is kept.
type PlacementSet struct {
	ParentID *int64   `json:"parent_id,omitempty"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
}

// Host is a host as returned by the API (SPEC-101). ParentID, X and Y are
// its placement in the network view.
type Host struct {
	ID          int64      `json:"id"`
	IP          string     `json:"ip"`
	PTRName     string     `json:"ptr_name"`
	Name        string     `json:"name"`
	DisplayName string     `json:"display_name"`
	Description string     `json:"description"`
	Note        string     `json:"note"`
	ParentID    *int64     `json:"parent_id"`
	Monitored   bool       `json:"monitored"`
	Status      string     `json:"status"`
	RTTms       *float64   `json:"rtt_ms"`
	LossPct     *float64   `json:"loss_pct"`
	LastSeen    *time.Time `json:"last_seen"`
	LastChange  *time.Time `json:"last_change"`
	Stale       bool       `json:"stale"`
	OutsideCIDR bool       `json:"outside_cidr"`
	Views       []int64    `json:"views"` // views the host is placed in, network view first (v0.21, SPEC-509)
	X           float64    `json:"x"`
	Y           float64    `json:"y"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Group is a group as returned by the API (SPEC-102). CIDR is empty for an
// ordinary group and set for a network group.
type Group struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Note        string    `json:"note"`
	CIDR        string    `json:"cidr"`
	ViewID      int64     `json:"view_id"`
	ParentID    *int64    `json:"parent_id"`
	Collapsed   bool      `json:"collapsed"`
	Labels      string    `json:"labels"` // SPEC-725: "" (as the parent), "name", "ip" or "both"
	Status      string    `json:"status"`
	X           float64   `json:"x"`
	Y           float64   `json:"y"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MapItem is one entry of GET /map (SPEC-501).
type MapItem struct {
	Type        string  `json:"type"`
	ID          int64   `json:"id"`
	ParentID    *int64  `json:"parent_id"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Name        string  `json:"name"` // display name
	Status      string  `json:"status"`
	Stale       bool    `json:"stale"`
	HasNote     bool    `json:"has_note"`
	Monitored   bool    `json:"monitored"`        // hosts only, false for groups (v0.18, SPEC-712)
	IP          string  `json:"ip,omitempty"`     // hosts only
	OutsideCIDR bool    `json:"outside_cidr"`     // hosts only, false for groups
	Collapsed   bool    `json:"collapsed"`        // groups only, false for hosts
	CIDR        string  `json:"cidr,omitempty"`   // network groups only
	Labels      string  `json:"labels,omitempty"` // groups only (SPEC-725)
}

// Map is the response of GET /map: the items of one view (SPEC-122).
type Map struct {
	ViewID int64     `json:"view_id"`
	Items  []MapItem `json:"items"`
}

// HostCreate is the body of POST /hosts. Without parent_id the host is
// placed in the most specific matching network group (SPEC-107).
type HostCreate struct {
	IP          string   `json:"ip"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Note        string   `json:"note,omitempty"`
	ParentID    *int64   `json:"parent_id,omitempty"`
	Monitored   *bool    `json:"monitored,omitempty"`
	X           *float64 `json:"x,omitempty"`
	Y           *float64 `json:"y,omitempty"`
}

// HostPatch is the body of PATCH /hosts/{id}. Only fields that are present
// are changed. "parent_id": null moves the host to the root.
type HostPatch struct {
	IP          Opt[string]  `json:"ip,omitzero"`
	Name        Opt[string]  `json:"name,omitzero"`
	Description Opt[string]  `json:"description,omitzero"`
	Note        Opt[string]  `json:"note,omitzero"`
	ParentID    Opt[*int64]  `json:"parent_id,omitzero"`
	Monitored   Opt[bool]    `json:"monitored,omitzero"`
	X           Opt[float64] `json:"x,omitzero"`
	Y           Opt[float64] `json:"y,omitzero"`
}

// GroupCreate is the body of POST /groups. Name defaults to the CIDR when
// empty; at least one of them is required. Collapsed defaults to true
// (SPEC-112).
type GroupCreate struct {
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Note        string   `json:"note,omitempty"`
	CIDR        string   `json:"cidr,omitempty"`
	ViewID      *int64   `json:"view_id,omitempty"` // default: the network view
	ParentID    *int64   `json:"parent_id,omitempty"`
	Collapsed   *bool    `json:"collapsed,omitempty"`
	Labels      string   `json:"labels,omitempty"` // SPEC-725
	X           *float64 `json:"x,omitempty"`
	Y           *float64 `json:"y,omitempty"`
}

// GroupMove is the body of POST /groups/{id}/move (SPEC-507).
type GroupMove struct {
	DX float64 `json:"dx"`
	DY float64 `json:"dy"`
	// ContentsOnly moves everything inside the group but not the group
	// itself (v0.29): the GUI uses it to expand a group with its frame
	// starting where its icon was.
	ContentsOnly bool `json:"contents_only,omitempty"`
}

// Arrange is the body of POST /views/{id}/arrange (SPEC-510): lay out the
// direct members of a group (ParentID nil: the view's root) on a grid of
// CellWidth × CellHeight map units (0: the normal grid, 100 × 80).
type Arrange struct {
	ParentID   *int64  `json:"parent_id"`
	CellWidth  float64 `json:"cell_width"`
	CellHeight float64 `json:"cell_height"`
}

// ArrangeResult is the answer of POST /views/{id}/arrange.
type ArrangeResult struct {
	ViewID   int64  `json:"view_id"`
	ParentID *int64 `json:"parent_id"`
	Moved    int    `json:"moved"`
}

// Host label modes of a group (SPEC-725): what the GUI shows under the
// hosts inside it. LabelsInherit takes the parent group's mode; at the
// root that is LabelsName.
const (
	LabelsInherit = ""
	LabelsName    = "name" // the display name (SPEC-402)
	LabelsIP      = "ip"   // the IP address
	LabelsBoth    = "both" // the display name, and the IP address below it
)

// ValidLabels reports whether s is a label mode.
func ValidLabels(s string) bool {
	switch s {
	case LabelsInherit, LabelsName, LabelsIP, LabelsBoth:
		return true
	}
	return false
}

// GroupPatch is the body of PATCH /groups/{id}. "cidr": "" turns a network
// group into an ordinary group; "parent_id": null moves it to the root.
type GroupPatch struct {
	Name        Opt[string]  `json:"name,omitzero"`
	Description Opt[string]  `json:"description,omitzero"`
	Note        Opt[string]  `json:"note,omitzero"`
	CIDR        Opt[string]  `json:"cidr,omitzero"`
	ParentID    Opt[*int64]  `json:"parent_id,omitzero"`
	Collapsed   Opt[bool]    `json:"collapsed,omitzero"`
	Labels      Opt[string]  `json:"labels,omitzero"` // SPEC-725; "" = as the parent
	X           Opt[float64] `json:"x,omitzero"`
	Y           Opt[float64] `json:"y,omitzero"`
}

// Discovery job states (SPEC-205).
const (
	DiscoveryRunning   = "running"
	DiscoveryDone      = "done"
	DiscoveryCancelled = "cancelled"
	DiscoveryFailed    = "failed"
)

// DiscoveryCreate is the body of POST /discovery (SPEC-201). Give either
// CIDR or GroupID (an IPv4 network group). ParentID is where a new network
// group for CIDR is created; it is ignored when a network group with that
// CIDR exists.
type DiscoveryCreate struct {
	CIDR     string `json:"cidr,omitempty"`
	GroupID  *int64 `json:"group_id,omitempty"`
	ParentID *int64 `json:"parent_id,omitempty"`
}

// Discovery is a discovery job (SPEC-205). It is also the payload of
// discovery.progress and discovery.finished.
type Discovery struct {
	ID         string     `json:"id"`
	CIDR       string     `json:"cidr"`
	GroupID    int64      `json:"group_id"`
	State      string     `json:"state"`
	Probed     int        `json:"probed"`
	Total      int        `json:"total"`
	Found      int        `json:"found"`  // addresses that answered
	Added      int        `json:"added"`  // of those, new hosts
	Errors     int        `json:"errors"` // probes that failed with an error
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Health is the response of GET /health. Without a valid token only Status
// and Version are filled in (SPEC-501, SPEC-802).
type Health struct {
	Status     string `json:"status"`
	Version    string `json:"version"`
	ICMPMode   string `json:"icmp_mode,omitempty"`
	ICMPError  string `json:"icmp_error,omitempty"`
	ICMP6Error string `json:"icmp6_error,omitempty"`
	DB         string `json:"db,omitempty"`
	RunID      string `json:"run_id,omitempty"`
}

// ErrorBody is the body of every error response (SPEC-504).
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the content of ErrorBody.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Opt is an optional PATCH field. Set is false when the field is absent
// from the JSON. A JSON null is accepted only when T is a pointer (for
// example "parent_id": null, the root); for any other T it is an error, so
// a null can't silently set a zero value (SPEC-504).
type Opt[T any] struct {
	Set   bool
	Value T
}

// Some returns a set Opt.
func Some[T any](v T) Opt[T] { return Opt[T]{Set: true, Value: v} }

// IsZero reports whether the field is absent (used by `omitzero`).
func (o Opt[T]) IsZero() bool { return !o.Set }

// UnmarshalJSON implements json.Unmarshaler.
func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		t := reflect.TypeFor[T]()
		if t.Kind() != reflect.Pointer {
			// The decoder adds the field name to this error.
			return &json.UnmarshalTypeError{Value: "null", Type: t}
		}
		var zero T
		o.Value = zero
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// MarshalJSON implements json.Marshaler.
func (o Opt[T]) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }
