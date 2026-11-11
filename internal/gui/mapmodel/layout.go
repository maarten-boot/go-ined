package mapmodel

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Sizes of the map elements in map units (zoom 100 %). An item's (x, y)
// is the centre of its icon.
const (
	HostSize    = 28.0 // host icon: a square
	GroupW      = 40.0 // collapsed group icon
	GroupH      = 30.0
	LabelGap    = 4.0  // between an icon and its first label line
	FramePad    = 14.0 // inside an expanded frame, around its members
	FrameLabelH = 24.0 // the label strip at the top of a frame
	EmptyFrameW = 140.0
	EmptyFrameH = 60.0
)

// Kind is how an item is drawn.
type Kind int

const (
	KindHost  Kind = iota // a host icon
	KindGroup             // a collapsed group icon
	KindFrame             // an expanded group: a frame around its members
)

// Rect is a rectangle in map units.
type Rect struct{ X, Y, W, H float64 }

// Contains reports whether the point lies in r.
func (r Rect) Contains(x, y float64) bool {
	return x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H
}

// Union returns the smallest rectangle containing r and o. The zero Rect
// counts as empty.
func (r Rect) Union(o Rect) Rect {
	if r == (Rect{}) {
		return o
	}
	if o == (Rect{}) {
		return r
	}
	x0, y0 := math.Min(r.X, o.X), math.Min(r.Y, o.Y)
	x1, y1 := math.Max(r.X+r.W, o.X+o.W), math.Max(r.Y+r.H, o.Y+o.H)
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// Intersects reports whether r and o overlap.
func (r Rect) Intersects(o Rect) bool {
	return r.X <= o.X+o.W && o.X <= r.X+r.W && r.Y <= o.Y+o.H && o.Y <= r.Y+r.H
}

// Counts are the monitored-host counts of a group's subtree (SPEC-712).
type Counts struct{ Up, Down, Unknown, Off int }

// String formats the counts, for example "3 down · 1 ? · 40 up · 2 off".
// It is empty when the subtree has no hosts.
func (c Counts) String() string {
	var parts []string
	for _, p := range []struct {
		n    int
		word string
	}{{c.Down, "down"}, {c.Unknown, "?"}, {c.Up, "up"}, {c.Off, "off"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.word))
		}
	}
	return strings.Join(parts, " · ")
}

// Node is one thing to draw.
type Node struct {
	Item   *apitypes.MapItem
	Kind   Kind
	Icon   Rect     // host or collapsed-group icon; for a frame, the label strip
	Bounds Rect     // everything the node covers: icon and labels, or the whole frame
	Labels []string // text lines under an icon, or the frame's label
	Depth  int      // nesting depth of a frame (0 = top level)
	Counts Counts   // groups only
}

// Measure returns the width and height of a text line in map units.
type Measure func(text string) (w, h float64)

// Layout returns what to draw for a loaded view (SPEC-702, 703, 712):
// frames first, outer before inner, then the icons, each ordered by type
// and id. Items under a collapsed group are not shown (SPEC-112). An item
// whose parent isn't in the view is shown at the root.
func (m *Model) Layout(view int64, measure Measure) []Node {
	vm := m.maps[view]
	if vm == nil {
		return nil
	}
	l := &layout{vm: vm, measure: measure, children: map[int64][]*apitypes.MapItem{}, counts: vm.counts()}
	for _, k := range sortedKeys(vm.Items) {
		it := vm.Items[k]
		l.children[l.parent(it)] = append(l.children[l.parent(it)], it)
	}
	l.place(0, 0, map[int64]bool{})
	slices.SortStableFunc(l.frames, func(a, b Node) int { return a.Depth - b.Depth })
	return append(l.frames, l.icons...)
}

type layout struct {
	vm       *ViewMap
	measure  Measure
	children map[int64][]*apitypes.MapItem // by parent group id (0 = root)
	counts   map[int64]Counts
	frames   []Node
	icons    []Node
}

func (l *layout) parent(it *apitypes.MapItem) int64 {
	if it.ParentID == nil || l.vm.Items[GroupKey(*it.ParentID)] == nil {
		return 0
	}
	return *it.ParentID
}

// place lays out the children of group (0 = root) and returns their
// combined bounds.
func (l *layout) place(group int64, depth int, seen map[int64]bool) Rect {
	var all Rect
	for _, it := range l.children[group] {
		switch {
		case it.Type == apitypes.TypeHost:
			n := l.icon(it, KindHost, HostSize, HostSize, l.hostLabels(it))
			all = all.Union(n.Bounds)
		case it.Collapsed || seen[it.ID]:
			n := l.icon(it, KindGroup, GroupW, GroupH, l.groupLabels(it))
			all = all.Union(n.Bounds)
		default:
			seen[it.ID] = true
			inner := l.place(it.ID, depth+1, seen)
			r := Rect{it.X - EmptyFrameW/2, it.Y - EmptyFrameH/2, EmptyFrameW, EmptyFrameH}
			if inner != (Rect{}) {
				r = Rect{inner.X - FramePad, inner.Y - FramePad - FrameLabelH, inner.W + 2*FramePad, inner.H + 2*FramePad + FrameLabelH}
			}
			label := frameLabel(it)
			if w, _ := l.measure(label); r.W < w+2*FramePad {
				r.W = w + 2*FramePad
			}
			l.frames = append(l.frames, Node{
				Item: it, Kind: KindFrame, Bounds: r, Icon: Rect{r.X, r.Y, r.W, FrameLabelH},
				Labels: []string{label}, Depth: depth, Counts: l.counts[it.ID],
			})
			all = all.Union(r)
		}
	}
	return all
}

func (l *layout) icon(it *apitypes.MapItem, k Kind, w, h float64, labels []string) Node {
	icon := Rect{it.X - w/2, it.Y - h/2, w, h}
	b := icon
	y := icon.Y + icon.H + LabelGap
	for _, s := range labels {
		tw, th := l.measure(s)
		b = b.Union(Rect{it.X - tw/2, y, tw, th})
		y += th
	}
	n := Node{Item: it, Kind: k, Icon: icon, Bounds: b, Labels: labels}
	if it.Type == apitypes.TypeGroup {
		n.Counts = l.counts[it.ID]
	}
	l.icons = append(l.icons, n)
	return n
}

// StatusWord is the status in words, never by colour alone (SPEC-702).
func StatusWord(status string, stale bool) string {
	if status == "" {
		status = apitypes.StatusUnknown
	}
	if stale {
		return status + " (stale)"
	}
	return status
}

func (l *layout) hostLabels(it *apitypes.MapItem) []string {
	word := StatusWord(it.Status, it.Stale)
	if !it.Monitored {
		word = "off"
	}
	return []string{it.Name, word}
}

func (l *layout) groupLabels(it *apitypes.MapItem) []string {
	out := []string{it.Name}
	if it.CIDR != "" && it.CIDR != it.Name {
		out = append(out, it.CIDR)
	}
	out = append(out, StatusWord(it.Status, false))
	if c := l.counts[it.ID].String(); c != "" {
		out = append(out, c)
	}
	return out
}

func frameLabel(it *apitypes.MapItem) string {
	s := it.Name
	if it.CIDR != "" && it.CIDR != it.Name {
		s += " · " + it.CIDR
	}
	return s + " · " + StatusWord(it.Status, false)
}

// counts returns the host counts of every group's subtree.
func (vm *ViewMap) counts() map[int64]Counts {
	out := map[int64]Counts{}
	for _, it := range vm.Items {
		if it.Type != apitypes.TypeHost {
			continue
		}
		for p, n := it.ParentID, 0; p != nil && n <= len(vm.Items); n++ {
			g := vm.Items[GroupKey(*p)]
			if g == nil {
				break
			}
			c := out[g.ID]
			switch {
			case !it.Monitored:
				c.Off++
			case it.Status == apitypes.StatusUp:
				c.Up++
			case it.Status == apitypes.StatusDown:
				c.Down++
			default:
				c.Unknown++
			}
			out[g.ID] = c
			p = g.ParentID
		}
	}
	return out
}

// Hit returns the node at a map point: an icon (or its labels) first,
// then the innermost frame whose label strip is there, then the innermost
// frame around the point. It returns nil for empty space.
func Hit(nodes []Node, x, y float64) *Node {
	for i := len(nodes) - 1; i >= 0; i-- {
		if n := &nodes[i]; n.Kind != KindFrame && n.Bounds.Contains(x, y) {
			return n
		}
	}
	for i := len(nodes) - 1; i >= 0; i-- {
		if n := &nodes[i]; n.Kind == KindFrame && n.Icon.Contains(x, y) {
			return n
		}
	}
	for i := len(nodes) - 1; i >= 0; i-- {
		if n := &nodes[i]; n.Kind == KindFrame && n.Bounds.Contains(x, y) {
			return n
		}
	}
	return nil
}

// Extent returns the bounds of all nodes.
func Extent(nodes []Node) Rect {
	var r Rect
	for _, n := range nodes {
		r = r.Union(n.Bounds)
	}
	return r
}

func sortedKeys(m map[Key]*apitypes.MapItem) []Key {
	keys := make([]Key, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b Key) int {
		if a.Type != b.Type {
			return strings.Compare(b.Type, a.Type) // groups before hosts
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return keys
}
