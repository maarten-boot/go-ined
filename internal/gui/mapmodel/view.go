package mapmodel

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Zoom limits (SPEC-713).
const (
	MinZoom = 0.1
	MaxZoom = 4.0
)

// Viewport maps between map units and screen pixels: the map point (X, Y)
// is at the top left of the screen, and one map unit is Zoom pixels.
type Viewport struct{ X, Y, Zoom float64 }

// ToScreen converts a map point to screen coordinates.
func (v Viewport) ToScreen(mx, my float64) (float64, float64) {
	return (mx - v.X) * v.Zoom, (my - v.Y) * v.Zoom
}

// ToMap converts a screen point to map coordinates.
func (v Viewport) ToMap(sx, sy float64) (float64, float64) {
	return v.X + sx/v.Zoom, v.Y + sy/v.Zoom
}

// Visible returns the map area shown on a screen of w × h pixels.
func (v Viewport) Visible(w, h float64) Rect { return Rect{v.X, v.Y, w / v.Zoom, h / v.Zoom} }

// ZoomAt sets the zoom, clamped to 10 %–400 %, keeping the map point under
// the screen point (sx, sy) in place.
func (v Viewport) ZoomAt(zoom, sx, sy float64) Viewport {
	mx, my := v.ToMap(sx, sy)
	z := math.Min(MaxZoom, math.Max(MinZoom, zoom))
	return Viewport{X: mx - sx/z, Y: my - sy/z, Zoom: z}
}

// Fit returns the viewport that shows r on a w × h screen with a margin,
// centred ("fit all"). The zoom is clamped (SPEC-713) and at most 100 %.
func Fit(r Rect, w, h float64) Viewport {
	const margin = 20.0
	if r.W <= 0 || r.H <= 0 || w <= 2*margin || h <= 2*margin {
		return Viewport{X: r.X - margin, Y: r.Y - margin, Zoom: 1}
	}
	z := math.Min((w-2*margin)/r.W, (h-2*margin)/r.H)
	z = math.Min(1, math.Max(MinZoom, z))
	return Viewport{X: r.X + r.W/2 - w/2/z, Y: r.Y + r.H/2 - h/2/z, Zoom: z}
}

// CentreOn returns the viewport with the same zoom, centred on a map point.
func (v Viewport) CentreOn(mx, my, w, h float64) Viewport {
	return Viewport{X: mx - w/2/v.Zoom, Y: my - h/2/v.Zoom, Zoom: v.Zoom}
}

// Find returns the hosts whose display name, PTR name or IP address
// contains q (case-insensitive), ordered by display name, then id
// (SPEC-714). An empty query finds nothing.
func Find(hosts []apitypes.Host, q string) []apitypes.Host {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	var out []apitypes.Host
	for _, h := range hosts {
		for _, s := range []string{h.DisplayName, h.PTRName, h.IP} {
			if strings.Contains(strings.ToLower(s), q) {
				out = append(out, h)
				break
			}
		}
	}
	slices.SortFunc(out, func(a, b apitypes.Host) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// Locate says where to show a found host (SPEC-714): in the current view
// if the host is placed there, otherwise in the network view. It returns
// the collapsed groups above the host in that view, innermost first, which
// must be expanded to show it. ok is false when the chosen view isn't
// loaded yet (the caller loads it and asks again) or the host isn't in it.
func (m *Model) Locate(host, current int64) (view int64, expand []int64, ok bool) {
	view = NetworkView
	if vm := m.maps[current]; vm != nil && vm.Items[HostKey(host)] != nil {
		view = current
	}
	vm := m.maps[view]
	if vm == nil || vm.Items[HostKey(host)] == nil {
		return view, nil, false
	}
	p := vm.Items[HostKey(host)].ParentID
	for n := 0; p != nil && n <= len(vm.Items); n++ {
		g := vm.Items[GroupKey(*p)]
		if g == nil {
			break
		}
		if g.Collapsed {
			expand = append(expand, g.ID)
		}
		p = g.ParentID
	}
	return view, expand, true
}

// Reloader coalesces reload requests: a view is reloaded at most once per
// delay (SPEC-716). load runs on the timer's goroutine.
type Reloader struct {
	delay   time.Duration
	load    func(view int64)
	mu      sync.Mutex
	pending map[int64]*time.Timer
	stopped bool
}

// NewReloader returns a Reloader that calls load.
func NewReloader(delay time.Duration, load func(view int64)) *Reloader {
	return &Reloader{delay: delay, load: load, pending: map[int64]*time.Timer{}}
}

// Request asks for a reload of view. Requests that arrive while one is
// pending are merged into it.
func (r *Reloader) Request(view int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.pending[view] != nil {
		return
	}
	r.pending[view] = time.AfterFunc(r.delay, func() {
		r.mu.Lock()
		delete(r.pending, view)
		stopped := r.stopped
		r.mu.Unlock()
		if !stopped {
			r.load(view)
		}
	})
}

// Stop cancels pending reloads; later requests are ignored.
func (r *Reloader) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for v, t := range r.pending {
		t.Stop()
		delete(r.pending, v)
	}
}

// Subtree returns the keys of a group's items in a loaded view: the group
// and everything below it (for dragging, SPEC-704).
func (m *Model) Subtree(view, group int64) map[Key]bool {
	vm := m.maps[view]
	out := map[Key]bool{}
	if vm == nil || vm.Items[GroupKey(group)] == nil {
		return out
	}
	out[GroupKey(group)] = true
	for k, it := range vm.Items {
		for p, n := it.ParentID, 0; p != nil && n <= len(vm.Items); n++ {
			if *p == group {
				out[k] = true
				break
			}
			g := vm.Items[GroupKey(*p)]
			if g == nil {
				break
			}
			p = g.ParentID
		}
	}
	return out
}

// Contents counts the hosts and groups below a group in a loaded view (for
// the delete question, SPEC-719).
func (m *Model) Contents(view, group int64) (hosts, groups int) {
	for k := range m.Subtree(view, group) {
		switch {
		case k == GroupKey(group):
		case k.Type == apitypes.TypeHost:
			hosts++
		default:
			groups++
		}
	}
	return hosts, groups
}

// Shift moves an item, and for a group everything below it, by (dx, dy)
// in the model only: the GUI shows a drag's result at once, and the
// server's events confirm or correct it (SPEC-704).
func (m *Model) Shift(view int64, k Key, dx, dy float64) {
	vm := m.maps[view]
	if vm == nil {
		return
	}
	keys := map[Key]bool{k: true}
	if k.Type == apitypes.TypeGroup {
		keys = m.Subtree(view, k.ID)
	}
	for key := range keys {
		if it := vm.Items[key]; it != nil {
			it.X += dx
			it.Y += dy
		}
	}
	vm.gen++
}

// Groups returns the groups of a loaded view ordered by name, then id (for
// "Move to group…" and the discovery target).
func (m *Model) Groups(view int64) []apitypes.MapItem {
	vm := m.maps[view]
	if vm == nil {
		return nil
	}
	var out []apitypes.MapItem
	for _, it := range vm.Items {
		if it.Type == apitypes.TypeGroup {
			out = append(out, *it)
		}
	}
	slices.SortFunc(out, func(a, b apitypes.MapItem) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return out
}
