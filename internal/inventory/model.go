// Package inventory holds the domain model of go-ined: hosts and groups in a
// tree, network groups (groups with a CIDR), views, and the rules that keep
// the trees consistent (SPEC-101..124). The rules are pure functions on
// Model; Service adds locking and persistence.
//
// Every view is its own tree (SPEC-117..121). Model.In(view) projects the
// model onto one view, and the tree rules then work on that projection.
package inventory

import (
	"fmt"
	"math"
	"net/netip"
	"strings"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Root is the ParentID of items at the root of the tree.
const Root int64 = 0

// NetworkView is the id of the network view (SPEC-117).
const NetworkView = apitypes.NetworkViewID

// View is one map (SPEC-117).
type View struct {
	ID        int64
	Name      string
	Kind      string // apitypes.ViewNetwork or apitypes.ViewCustom
	Order     int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Placement puts a host in a custom view (SPEC-119). A host's placement in
// the network view is its own ParentID, X and Y.
type Placement struct {
	ViewID, HostID, ParentID int64
	X, Y                     float64
}

// PlacementKey identifies a placement.
type PlacementKey struct{ ViewID, HostID int64 }

// Limits.
const (
	MaxNoteBytes        = 64 * 1024 // SPEC-111
	MaxNameBytes        = 255
	MaxDescriptionBytes = 4096
)

// Host is a monitored IP address (SPEC-101).
type Host struct {
	ID          int64
	IP          netip.Addr
	PTRName     string
	Name        string // manual name, overrides PTRName
	Description string
	Note        string
	ParentID    int64
	Monitored   bool
	Status      string
	RTTms       *float64
	LossPct     *float64
	LastSeen    time.Time // zero = never
	LastChange  time.Time // zero = never
	Stale       bool      // not persisted (SPEC-603)
	X, Y        float64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Group is a node of the tree (SPEC-102). A group with a valid CIDR is a
// network group.
type Group struct {
	ID          int64
	Name        string
	Description string
	Note        string
	CIDR        netip.Prefix // invalid (zero) for ordinary groups
	ViewID      int64        // SPEC-118; fixed at creation
	ParentID    int64
	Collapsed   bool
	X, Y        float64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// label names a group in messages: "name (cidr)", or just the CIDR when
// the name is the CIDR.
func (g *Group) label() string {
	switch {
	case !g.IsNetwork():
		return fmt.Sprintf("%q", g.Name)
	case g.Name == g.CIDR.String():
		return g.Name
	default:
		return fmt.Sprintf("%q (%s)", g.Name, g.CIDR)
	}
}

// IsNetwork reports whether g is a network group.
func (g *Group) IsNetwork() bool { return g.CIDR.IsValid() }

// DisplayName returns the name to show for a host (SPEC-402).
func (h *Host) DisplayName() string {
	switch {
	case h.Name != "":
		return h.Name
	case h.PTRName != "":
		return strings.TrimSuffix(h.PTRName, ".")
	default:
		return h.IP.String()
	}
}

// Model is an in-memory snapshot of the inventory. Placements maps view →
// host → placement, for custom views only.
type Model struct {
	Hosts      map[int64]*Host
	Groups     map[int64]*Group
	Views      map[int64]*View
	Placements map[int64]map[int64]*Placement
}

// NewModel returns an empty model.
func NewModel() *Model {
	return &Model{Hosts: map[int64]*Host{}, Groups: map[int64]*Group{},
		Views: map[int64]*View{}, Placements: map[int64]map[int64]*Placement{}}
}

// In returns the tree of one view: its groups, and the hosts placed in it
// with that view's parent and position. For the network view the hosts and
// groups are the model's own; for a custom view the hosts are copies, so
// changes to them don't reach the model. The projection has no views or
// placements.
func (m *Model) In(view int64) *Model {
	p := &Model{Groups: map[int64]*Group{}}
	for id, g := range m.Groups {
		if g.ViewID == view {
			p.Groups[id] = g
		}
	}
	if view == NetworkView {
		p.Hosts = m.Hosts
		return p
	}
	p.Hosts = make(map[int64]*Host, len(m.Placements[view]))
	for hid, pl := range m.Placements[view] {
		if h := m.Hosts[hid]; h != nil {
			c := *h
			c.ParentID, c.X, c.Y = pl.ParentID, pl.X, pl.Y
			p.Hosts[hid] = &c
		}
	}
	return p
}

// AggregateAll returns the counts of every group in every view (SPEC-121).
func (m *Model) AggregateAll() map[int64]Counts {
	out := make(map[int64]Counts, len(m.Groups))
	views := map[int64]bool{NetworkView: true}
	for _, g := range m.Groups {
		views[g.ViewID] = true
	}
	for v := range views {
		for id, c := range m.In(v).Aggregate() {
			out[id] = c
		}
	}
	return out
}

// ParseIP parses and canonicalises a host IP: IPv4-mapped IPv6 addresses are
// unmapped; zones are rejected.
func ParseIP(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, invalidf("invalid IP address %q", s)
	}
	if a.Zone() != "" {
		return netip.Addr{}, invalidf("IP address %q must not have a zone", s)
	}
	return a.Unmap(), nil
}

// ParseCIDR parses a CIDR and requires canonical form: no host bits set
// (SPEC-113). "" returns the zero prefix.
func ParseCIDR(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Prefix{}, nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, invalidf("invalid CIDR %q", s)
	}
	if p.Addr().Is4In6() {
		return netip.Prefix{}, invalidf("CIDR %q: use plain IPv4 notation", s)
	}
	if m := p.Masked(); m != p {
		return netip.Prefix{}, invalidf("CIDR %q has host bits set; use %s", s, m)
	}
	return p, nil
}

// PrefixContains reports whether outer contains inner entirely.
func PrefixContains(outer, inner netip.Prefix) bool {
	return outer.Addr().Is4() == inner.Addr().Is4() &&
		outer.Bits() <= inner.Bits() &&
		outer.Contains(inner.Addr())
}

// Parent returns the parent group of an item, or nil at the root.
func (m *Model) Parent(parentID int64) *Group {
	if parentID == Root {
		return nil
	}
	return m.Groups[parentID]
}

// IsInSubtree reports whether group id is root or a descendant of root.
func (m *Model) IsInSubtree(id, root int64) bool {
	for seen := 0; id != Root && seen <= len(m.Groups); seen++ {
		if id == root {
			return true
		}
		g := m.Groups[id]
		if g == nil {
			return false
		}
		id = g.ParentID
	}
	return false
}

// inCycle reports whether walking up from group id never reaches the root.
func (m *Model) inCycle(id int64) bool {
	for steps := 0; id != Root; steps++ {
		if steps > len(m.Groups) {
			return true
		}
		g := m.Groups[id]
		if g == nil {
			return false
		}
		id = g.ParentID
	}
	return false
}

// NearestNetwork returns the nearest network group at or above group id,
// skipping ordinary groups, or nil.
func (m *Model) NearestNetwork(id int64) *Group {
	for seen := 0; id != Root && seen <= len(m.Groups); seen++ {
		g := m.Groups[id]
		if g == nil {
			return nil
		}
		if g.IsNetwork() {
			return g
		}
		id = g.ParentID
	}
	return nil
}

// CheckNesting verifies SPEC-114 for every network group in the subtree of
// group root (including root itself).
func (m *Model) CheckNesting(root int64) error {
	for _, g := range m.Groups {
		if !g.IsNetwork() || !m.IsInSubtree(g.ID, root) {
			continue
		}
		if anc := m.NearestNetwork(g.ParentID); anc != nil && !PrefixContains(anc.CIDR, g.CIDR) {
			return conflictf("network group %s is not inside its enclosing network group %s", g.label(), anc.label())
		}
	}
	return nil
}

// CheckMove verifies that group id can be moved under newParent (SPEC-103).
func (m *Model) CheckMove(id, newParent int64) error {
	if newParent != Root && m.IsInSubtree(newParent, id) {
		return conflictf("cannot move group %d into itself or one of its subgroups", id)
	}
	return nil
}

// BestNetworkFor returns the most specific network group containing ip
// (longest prefix), or nil (SPEC-107).
func (m *Model) BestNetworkFor(ip netip.Addr) *Group {
	var best *Group
	for _, g := range m.Groups {
		if !g.IsNetwork() || !g.CIDR.Contains(ip) {
			continue
		}
		if best == nil || g.CIDR.Bits() > best.CIDR.Bits() ||
			(g.CIDR.Bits() == best.CIDR.Bits() && g.ID < best.ID) {
			best = g
		}
	}
	return best
}

// OutsideCIDR reports whether a host's IP lies outside the nearest network
// group above it (SPEC-115).
func (m *Model) OutsideCIDR(h *Host) bool {
	anc := m.NearestNetwork(h.ParentID)
	return anc != nil && !anc.CIDR.Contains(h.IP)
}

// Visible reports whether an item with the given parent is visible: no
// group above it is collapsed (SPEC-112).
func (m *Model) Visible(parentID int64) bool {
	for seen := 0; parentID != Root && seen <= len(m.Groups); seen++ {
		g := m.Groups[parentID]
		if g == nil {
			return true
		}
		if g.Collapsed {
			return false
		}
		parentID = g.ParentID
	}
	return true
}

// Counts are the monitored-host counts of a group's subtree (SPEC-712).
type Counts struct {
	Up, Down, Unknown, Off int
}

// Status returns the aggregated status for the counts (SPEC-105).
func (c Counts) Status() string {
	switch {
	case c.Down > 0:
		return apitypes.StatusDown
	case c.Unknown > 0:
		return apitypes.StatusUnknown
	case c.Up > 0:
		return apitypes.StatusUp
	default:
		return apitypes.StatusUnknown
	}
}

// Aggregate computes the counts of every group (SPEC-105, SPEC-306,
// SPEC-712). Groups without hosts are present with zero counts.
func (m *Model) Aggregate() map[int64]Counts {
	out := make(map[int64]Counts, len(m.Groups))
	for id := range m.Groups {
		out[id] = Counts{}
	}
	for _, h := range m.Hosts {
		for id, seen := h.ParentID, 0; id != Root && seen <= len(m.Groups); seen++ {
			g := m.Groups[id]
			if g == nil {
				break
			}
			c := out[id]
			switch {
			case !h.Monitored:
				c.Off++
			case h.Status == apitypes.StatusUp:
				c.Up++
			case h.Status == apitypes.StatusDown:
				c.Down++
			default:
				c.Unknown++
			}
			out[id] = c
			id = g.ParentID
		}
	}
	return out
}

// HasChildren reports whether group id has any direct hosts or groups.
func (m *Model) HasChildren(id int64) bool {
	for _, h := range m.Hosts {
		if h.ParentID == id {
			return true
		}
	}
	for _, g := range m.Groups {
		if g.ParentID == id {
			return true
		}
	}
	return false
}

// Grid used to find a free position for a new item (SPEC-106).
const (
	gridCols    = 10
	gridDX      = 100.0
	gridDY      = 80.0
	minDistance = 50.0
	rootOriginX = 40.0
	rootOriginY = 40.0
	// childDY is the distance from a group to its first row of members:
	// room for the group's labels and, when it is expanded, the label strip
	// of its frame in the GUI (v0.18).
	childDY = 120.0
)

// FreePosition returns a position near the parent (or the root origin) that
// no sibling occupies.
func (m *Model) FreePosition(parentID int64) (float64, float64) {
	bx, by := rootOriginX, rootOriginY
	if p := m.Parent(parentID); p != nil {
		bx, by = p.X, p.Y+childDY
	}
	type pt struct{ x, y float64 }
	var sib []pt
	for _, h := range m.Hosts {
		if h.ParentID == parentID {
			sib = append(sib, pt{h.X, h.Y})
		}
	}
	for _, g := range m.Groups {
		if g.ParentID == parentID {
			sib = append(sib, pt{g.X, g.Y})
		}
	}
	for i := 0; ; i++ {
		x := bx + float64(i%gridCols)*gridDX
		y := by + float64(i/gridCols)*gridDY
		free := true
		for _, s := range sib {
			if math.Hypot(s.x-x, s.y-y) < minDistance {
				free = false
				break
			}
		}
		// A sibling blocks at most 2 slots (vertical neighbours are 80 apart,
		// all others >= 100), so a free slot exists within 2*len(sib)+1 tries.
		if free || i >= 2*len(sib) {
			return x, y
		}
	}
}

func checkText(field, s string, max int) error {
	if len(s) > max {
		if field == "note" {
			return &Error{Kind: TooLarge, Msg: fmt.Sprintf("note is %d bytes; the maximum is %d", len(s), max)}
		}
		return invalidf("%s is %d bytes; the maximum is %d", field, len(s), max)
	}
	return nil
}

func checkPos(x, y float64) error {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return invalidf("position must be finite")
	}
	return nil
}
