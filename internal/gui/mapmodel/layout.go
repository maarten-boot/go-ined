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
	// NameLines is how many of Labels hold the name (more than 1 when
	// wrapped); they are drawn bold.
	NameLines int
	Depth     int    // nesting depth of a frame (0 = top level)
	Counts    Counts // groups only
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
	if m.WrapNames {
		l.room = roomPerItem(vm, l)
	}
	l.place(0, 0, map[int64]bool{})
	slices.SortStableFunc(l.frames, func(a, b Node) int { return a.Depth - b.Depth })
	return append(l.frames, l.icons...)
}

type layout struct {
	room     map[Key]float64 // width a name may use before it wraps; absent: no limit
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
	nameLines := 1
	if room, ok := l.room[Key{it.Type, it.ID}]; ok && len(labels) > 0 {
		if lines := WrapName(labels[0], room, l.measure); len(lines) > 1 {
			labels = append(lines, labels[1:]...)
			nameLines = len(lines)
		}
	}
	icon := Rect{it.X - w/2, it.Y - h/2, w, h}
	b := icon
	y := icon.Y + icon.H + LabelGap
	for _, s := range labels {
		tw, th := l.measure(s)
		b = b.Union(Rect{it.X - tw/2, y, tw, th})
		y += th
	}
	n := Node{Item: it, Kind: k, Icon: icon, Bounds: b, Labels: labels, NameLines: nameLines}
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

// Name wrapping (SPEC-722).
const (
	rowBand  = 40.0 // items whose y differs less than this share a row
	labelGap = 8.0  // free space kept between neighbouring labels
	minRoom  = 40.0 // a name never wraps narrower than this
)

// roomPerItem gives every visible host and collapsed group the width its
// name may use: the distance to the nearest visible icon item on the same
// row, minus a gap. An item alone on its row gets no limit.
func roomPerItem(vm *ViewMap, l *layout) map[Key]float64 {
	type icon struct {
		k    Key
		x, y float64
	}
	var icons []icon
	for k, it := range vm.Items {
		if !l.visible(it) || (it.Type == apitypes.TypeGroup && !it.Collapsed) {
			continue
		}
		icons = append(icons, icon{k, it.X, it.Y})
	}
	slices.SortFunc(icons, func(a, b icon) int { return cmp.Or(cmp.Compare(a.y, b.y), cmp.Compare(a.x, b.x)) })
	room := map[Key]float64{}
	for i, a := range icons {
		best := math.Inf(1)
		for j := i - 1; j >= 0 && a.y-icons[j].y < rowBand; j-- {
			best = math.Min(best, math.Abs(a.x-icons[j].x))
		}
		for j := i + 1; j < len(icons) && icons[j].y-a.y < rowBand; j++ {
			best = math.Min(best, math.Abs(a.x-icons[j].x))
		}
		if !math.IsInf(best, 1) {
			room[a.k] = math.Max(minRoom, best-labelGap)
		}
	}
	return room
}

// visible reports whether no group above the item is collapsed.
func (l *layout) visible(it *apitypes.MapItem) bool {
	for p, n := it.ParentID, 0; p != nil && n <= len(l.vm.Items); n++ {
		g := l.vm.Items[GroupKey(*p)]
		if g == nil {
			return true
		}
		if g.Collapsed {
			return false
		}
		p = g.ParentID
	}
	return true
}

// WrapName breaks a name into lines no wider than room: after its dots
// (DNS names) or colons (IPv6), and inside a part only if that part alone
// is too wide. A name that fits stays one line.
func WrapName(name string, room float64, measure Measure) []string {
	if w, _ := measure(name); w <= room {
		return []string{name}
	}
	var parts []string
	start := 0
	for i, r := range name {
		if r == '.' || r == ':' {
			parts = append(parts, name[start:i+1])
			start = i + 1
		}
	}
	if start < len(name) {
		parts = append(parts, name[start:])
	}
	var lines []string
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, p := range parts {
		if w, _ := measure(cur + p); w <= room {
			cur += p
			continue
		}
		flush()
		for p != "" { // a part too wide by itself: split it by characters
			if w, _ := measure(p); w <= room {
				cur = p
				break
			}
			n := len(p) - 1
			for n > 1 {
				if w, _ := measure(p[:n]); w <= room {
					break
				}
				n--
			}
			lines = append(lines, p[:n])
			p = p[n:]
		}
	}
	flush()
	return lines
}

// Reflow cell (SPEC-723).
const (
	reflowGapX = 24.0 // free space between cells
	reflowGapY = 16.0
	minCellW   = 60.0
	minCellH   = 50.0
	maxCell    = 5000.0
)

// ReflowCell returns the grid cell for "Reflow" of a group's direct
// members (parent nil: the view's root): wide and high enough for the
// biggest member with its full, unwrapped name (or its whole frame), plus
// a gap. n is the number of members.
func (m *Model) ReflowCell(view int64, parent *int64, measure Measure) (w, h float64, n int) {
	vm := m.maps[view]
	if vm == nil {
		return 0, 0, 0
	}
	wrap := m.WrapNames
	m.WrapNames = false
	nodes := m.Layout(view, measure)
	m.WrapNames = wrap
	bounds := map[Key]Rect{}
	for _, nd := range nodes {
		bounds[Key{nd.Item.Type, nd.Item.ID}] = nd.Bounds
	}
	for k, it := range vm.Items {
		if !sameParent(it.ParentID, parent) {
			continue
		}
		n++
		b, ok := bounds[k]
		if !ok { // hidden in a collapsed group: estimate from its labels
			iw, ih := HostSize, HostSize
			labels := []string{it.Name, StatusWord(it.Status, it.Stale)}
			if it.Type == apitypes.TypeGroup {
				iw, ih = GroupW, GroupH
				labels = append(labels, it.CIDR)
			}
			b = Rect{W: iw, H: ih + LabelGap}
			for _, s := range labels {
				tw, th := measure(s)
				b.W, b.H = math.Max(b.W, tw), b.H+th
			}
		}
		w, h = math.Max(w, b.W), math.Max(h, b.H)
	}
	w = math.Min(maxCell, math.Max(minCellW, w+reflowGapX))
	h = math.Min(maxCell, math.Max(minCellH, h+reflowGapY))
	return w, h, n
}

func sameParent(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
