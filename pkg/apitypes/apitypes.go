// Package apitypes holds the JSON types of the go-ined REST API (/api/v1).
// It is shared by the server, pkg/client, the GUI and inedctl.
package apitypes

import (
	"bytes"
	"encoding/json"
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
	EventResync       = "resync"
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

// Host is a host as returned by the API (SPEC-101).
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
	ParentID    *int64    `json:"parent_id"`
	Collapsed   bool      `json:"collapsed"`
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
	IP          string  `json:"ip,omitempty"`   // hosts only
	OutsideCIDR bool    `json:"outside_cidr"`   // hosts only, false for groups
	Collapsed   bool    `json:"collapsed"`      // groups only, false for hosts
	CIDR        string  `json:"cidr,omitempty"` // network groups only
}

// Map is the response of GET /map.
type Map struct {
	Items []MapItem `json:"items"`
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
	ParentID    *int64   `json:"parent_id,omitempty"`
	Collapsed   *bool    `json:"collapsed,omitempty"`
	X           *float64 `json:"x,omitempty"`
	Y           *float64 `json:"y,omitempty"`
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
	X           Opt[float64] `json:"x,omitzero"`
	Y           Opt[float64] `json:"y,omitzero"`
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
// from the JSON; a JSON null sets it to the zero value of T.
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
		var zero T
		o.Value = zero
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// MarshalJSON implements json.Marshaler.
func (o Opt[T]) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }
