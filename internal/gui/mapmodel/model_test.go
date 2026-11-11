package mapmodel

import (
	"encoding/json"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

func p(id int64) *int64 { return &id }

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A network view: net group 1 (10.0.0.0/24, expanded) holds group 2
// (collapsed) with hosts 1 (up) and 2 (down), and host 3 (unmonitored)
// directly. Host 4 sits at the root.
func networkMap() apitypes.Map {
	return apitypes.Map{ViewID: 1, Items: []apitypes.MapItem{
		{Type: "group", ID: 1, Name: "10.0.0.0/24", CIDR: "10.0.0.0/24", X: 100, Y: 100, Status: "down"},
		{Type: "group", ID: 2, ParentID: p(1), Name: "servers", X: 100, Y: 200, Collapsed: true, Status: "down"},
		{Type: "host", ID: 1, ParentID: p(2), Name: "web", X: 50, Y: 300, Status: "up", Monitored: true},
		{Type: "host", ID: 2, ParentID: p(2), Name: "db", X: 150, Y: 300, Status: "down", Monitored: true},
		{Type: "host", ID: 3, ParentID: p(1), Name: "printer", X: 200, Y: 200, Status: "unknown"},
		{Type: "host", ID: 4, Name: "laptop", X: 400, Y: 40, Status: "unknown", Stale: true, Monitored: true},
	}}
}

// A custom view 2 with group 5 holding host 1.
func customMap() apitypes.Map {
	return apitypes.Map{ViewID: 2, Items: []apitypes.MapItem{
		{Type: "group", ID: 5, Name: "Payroll", X: 100, Y: 100, Collapsed: true, Status: "up"},
		{Type: "host", ID: 1, ParentID: p(5), Name: "web", X: 100, Y: 200, Status: "up", Monitored: true},
	}}
}

func loaded() *Model {
	m := New()
	m.SetViews([]apitypes.View{{ID: 2, Name: "Payroll", Order: 1}, {ID: 1, Name: "Networks"}, {ID: 3, Name: "Lab", Order: 1}})
	m.SetMap(networkMap())
	m.SetMap(customMap())
	return m
}

// TC-710: tab order and view events.
func TestViews(t *testing.T) {
	m := loaded()
	names := func() []string {
		var out []string
		for _, v := range m.Views() {
			out = append(out, v.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"Networks", "Payroll", "Lab"}) {
		t.Fatalf("order: %v", got)
	}
	g0 := m.ViewsGen()
	e, _ := m.Apply(apitypes.EventViewCreated, raw(t, apitypes.View{ID: 4, Name: "First", Order: -1}))
	if m.ViewsGen() == g0 {
		t.Error("ViewsGen not bumped by view.created")
	}
	if !e.ViewsChanged || names()[0] != "First" {
		t.Fatalf("created: %+v %v", e, names())
	}
	e, _ = m.Apply(apitypes.EventViewUpdated, raw(t, apitypes.View{ID: 2, Name: "Pay", Order: 9}))
	if !e.ViewsChanged || !slices.Equal(names(), []string{"First", "Networks", "Lab", "Pay"}) {
		t.Fatalf("updated: %v", names())
	}
	if v, ok := m.View(2); !ok || v.Name != "Pay" {
		t.Fatalf("View(2): %+v", v)
	}
	e, _ = m.Apply(apitypes.EventViewDeleted, raw(t, apitypes.DeletedEvent{ID: 2}))
	g1 := m.ViewsGen()
	if !e.ViewsChanged || !slices.Equal(e.Deleted, []int64{2}) || m.Loaded(2) || len(m.Views()) != 3 || g1 <= g0+1 {
		t.Fatalf("deleted: %+v loaded=%v", e, m.Loaded(2))
	}
	// SetViews drops the maps of views that are gone.
	m.SetViews([]apitypes.View{{ID: 3, Name: "Lab"}})
	if m.Loaded(1) {
		t.Error("map of a vanished view kept")
	}
	if _, ok := m.View(1); ok {
		t.Error("View(1) after SetViews")
	}
}

// TC-711: in-place changes, reloads, resync.
func TestApply(t *testing.T) {
	m := loaded()
	g1, g2 := m.Gen(1), m.Gen(2)

	// host.status changes the host in every loaded view.
	h := apitypes.Host{ID: 1, IP: "10.0.0.1", DisplayName: "web", Status: "down", Monitored: true, Note: "n",
		ParentID: p(2), X: 55, Y: 305}
	e, err := m.Apply(apitypes.EventHostStatus, raw(t, h))
	if err != nil || !slices.Equal(e.Redraw, []int64{1, 2}) || e.Reload != nil {
		t.Fatalf("host.status: %+v %v", e, err)
	}
	for _, v := range []int64{1, 2} {
		it := m.Map(v).Items[HostKey(1)]
		if it.Status != "down" || !it.HasNote {
			t.Errorf("view %d: %+v", v, it)
		}
	}
	// The network placement comes from the host; a custom one doesn't.
	if it := m.Map(1).Items[HostKey(1)]; it.X != 55 {
		t.Errorf("network position: %+v", it)
	}
	if it := m.Map(2).Items[HostKey(1)]; it.X != 100 || *it.ParentID != 5 {
		t.Errorf("custom position changed: %+v", it)
	}
	if m.Gen(1) == g1 || m.Gen(2) == g2 {
		t.Error("gen not bumped")
	}

	// group.status in place, only in the group's view.
	e, _ = m.Apply(apitypes.EventGroupStatus, raw(t, apitypes.GroupStatusEvent{ID: 5, Status: "down"}))
	if !slices.Equal(e.Redraw, []int64{2}) || m.Map(2).Items[GroupKey(5)].Status != "down" {
		t.Fatalf("group.status: %+v", e)
	}
	// Unknown group: nothing.
	if e, _ = m.Apply(apitypes.EventGroupStatus, raw(t, apitypes.GroupStatusEvent{ID: 99, Status: "up"})); e.Redraw != nil {
		t.Errorf("unknown group: %+v", e)
	}

	// Structural changes reload only the affected, loaded view.
	for _, tc := range []struct {
		typ  string
		data any
		want []int64
	}{
		{apitypes.EventHostCreated, apitypes.Host{ID: 9}, []int64{1}},
		{apitypes.EventHostDeleted, apitypes.DeletedEvent{ID: 1}, []int64{1, 2}},
		{apitypes.EventHostDeleted, apitypes.DeletedEvent{ID: 4}, []int64{1}},
		{apitypes.EventGroupCreated, apitypes.Group{ID: 7, ViewID: 2}, []int64{2}},
		{apitypes.EventGroupUpdated, apitypes.Group{ID: 1, ViewID: 1}, []int64{1}},
		{apitypes.EventGroupCreated, apitypes.Group{ID: 8, ViewID: 3}, nil}, // view 3 isn't loaded
		{apitypes.EventGroupDeleted, apitypes.DeletedEvent{ID: 5}, []int64{2}},
		{apitypes.EventGroupDeleted, apitypes.DeletedEvent{ID: 99}, nil},
		{apitypes.EventPlacementUpdated, apitypes.Placement{ViewID: 2, HostID: 4}, []int64{2}},
		{apitypes.EventPlacementDeleted, apitypes.PlacementDeletedEvent{ViewID: 3, HostID: 4}, nil},
		{apitypes.EventDiscoveryProgress, apitypes.Discovery{}, nil},
	} {
		e, err := m.Apply(tc.typ, raw(t, tc.data))
		if err != nil || !slices.Equal(e.Reload, tc.want) || e.Redraw != nil {
			t.Errorf("%s %+v: %+v %v", tc.typ, tc.data, e, err)
		}
	}
	// A host.updated for a host missing from the network view reloads it.
	if e, _ := m.Apply(apitypes.EventHostUpdated, raw(t, apitypes.Host{ID: 42})); !slices.Equal(e.Reload, []int64{1}) {
		t.Errorf("missing host: %+v", e)
	}
	if e, _ := m.Apply(apitypes.EventResync, raw(t, map[string]string{"reason": "x"})); !e.Resync {
		t.Error("resync")
	}
	if _, err := m.Apply(apitypes.EventHostStatus, json.RawMessage(`{`)); err == nil {
		t.Error("bad JSON accepted")
	}
	if !slices.Equal(m.LoadedViews(), []int64{1, 2}) {
		t.Errorf("loaded: %v", m.LoadedViews())
	}
}

func measure(s string) (float64, float64) { return float64(len(s)) * 7, 14 }

// TC-704, TC-706: layout, frames and counts.
func TestLayout(t *testing.T) {
	m := loaded()
	nodes := m.Layout(1, measure)
	byID := map[Key]Node{}
	for _, n := range nodes {
		byID[Key{n.Item.Type, n.Item.ID}] = n
	}
	// Hosts 1 and 2 are under the collapsed group 2, so not drawn.
	if len(nodes) != 4 {
		t.Fatalf("%d nodes: %+v", len(nodes), nodes)
	}
	if _, ok := byID[HostKey(1)]; ok {
		t.Error("host under a collapsed group is drawn")
	}
	f := byID[GroupKey(1)]
	if f.Kind != KindFrame || nodes[0].Item.ID != 1 {
		t.Fatalf("frame first: %+v", nodes[0])
	}
	// The frame contains its visible members with padding and a label strip.
	for _, k := range []Key{GroupKey(2), HostKey(3)} {
		b := byID[k].Bounds
		if !f.Bounds.Contains(b.X-FramePad+0.1, b.Y-0.1) || !f.Bounds.Contains(b.X+b.W, b.Y+b.H) {
			t.Errorf("frame %+v doesn't contain %v %+v", f.Bounds, k, b)
		}
	}
	if f.Icon.H != FrameLabelH || f.Icon.Y != f.Bounds.Y || f.Labels[0] != "10.0.0.0/24 · down" {
		t.Errorf("frame label: %+v %q", f.Icon, f.Labels)
	}
	g := byID[GroupKey(2)]
	if g.Kind != KindGroup || g.Counts != (Counts{Up: 1, Down: 1}) || !slices.Equal(g.Labels, []string{"servers", "down", "1 down · 1 up"}) {
		t.Errorf("collapsed group: %+v", g)
	}
	if f.Counts != (Counts{Up: 1, Down: 1, Off: 1}) || f.Counts.String() != "1 down · 1 up · 1 off" {
		t.Errorf("frame counts: %+v %q", f.Counts, f.Counts.String())
	}
	if h := byID[HostKey(3)]; !slices.Equal(h.Labels, []string{"printer", "off"}) || h.Icon != (Rect{186, 186, 28, 28}) {
		t.Errorf("unmonitored host: %+v", h)
	}
	if h := byID[HostKey(4)]; !slices.Equal(h.Labels, []string{"laptop", "unknown (stale)"}) {
		t.Errorf("stale host: %+v", h)
	}
	if (Counts{}).String() != "" || (Counts{Unknown: 2}).String() != "2 ?" {
		t.Error("Counts.String")
	}

	// Expanding group 2 nests a frame inside frame 1.
	m.Map(1).Items[GroupKey(2)].Collapsed = false
	nodes = m.Layout(1, measure)
	if len(nodes) != 6 || nodes[0].Item.ID != 1 || nodes[1].Item.ID != 2 || nodes[1].Depth != 1 {
		t.Fatalf("nested: %+v", nodes[:2])
	}
	if !nodes[0].Bounds.Contains(nodes[1].Bounds.X, nodes[1].Bounds.Y) {
		t.Error("inner frame outside the outer one")
	}
	// An empty expanded group gets a fixed-size frame at its position;
	// a parent that isn't in the view counts as the root.
	m.SetMap(apitypes.Map{ViewID: 3, Items: []apitypes.MapItem{
		{Type: "group", ID: 9, Name: "empty", X: 0, Y: 0},
		{Type: "host", ID: 1, ParentID: p(77), Name: "web", X: 500, Y: 500},
	}})
	nodes = m.Layout(3, measure)
	if len(nodes) != 2 || nodes[0].Bounds != (Rect{-70, -30, 140, 60}) || nodes[1].Icon.X != 486 {
		t.Errorf("empty frame / orphan: %+v", nodes)
	}
	if m.Layout(99, measure) != nil {
		t.Error("layout of an unloaded view")
	}

	// Hit testing: icons first, then a frame's label strip, then a frame.
	m.Map(1).Items[GroupKey(2)].Collapsed = true
	nodes = m.Layout(1, measure)
	if n := Hit(nodes, 200, 200); n == nil || n.Item.ID != 3 || n.Item.Type != "host" {
		t.Errorf("hit host: %+v", n)
	}
	f = nodes[0]
	if n := Hit(nodes, f.Icon.X+2, f.Icon.Y+2); n == nil || n.Kind != KindFrame {
		t.Errorf("hit frame label: %+v", n)
	}
	if n := Hit(nodes, f.Bounds.X+2, f.Bounds.Y+f.Bounds.H-2); n == nil || n.Kind != KindFrame {
		t.Errorf("hit frame: %+v", n)
	}
	if n := Hit(nodes, -500, -500); n != nil {
		t.Errorf("hit empty: %+v", n)
	}
	if e := Extent(nodes); !e.Contains(400, 40) || !e.Contains(f.Bounds.X, f.Bounds.Y) {
		t.Errorf("extent: %+v", e)
	}
}

// TC-708: screen and map coordinates convert back at every zoom.
func TestViewport(t *testing.T) {
	for _, z := range []float64{0.1, 1, 4} {
		v := Viewport{X: -120, Y: 35.5, Zoom: z}
		sx, sy := v.ToScreen(250, -80)
		mx, my := v.ToMap(sx, sy)
		if math.Abs(mx-250) > 1e-9 || math.Abs(my+80) > 1e-9 {
			t.Errorf("zoom %g: round trip gives %g,%g", z, mx, my)
		}
	}
	v := Viewport{X: 0, Y: 0, Zoom: 1}
	w := v.ZoomAt(2, 100, 50)
	if mx, my := w.ToMap(100, 50); w.Zoom != 2 || mx != 100 || my != 50 {
		t.Errorf("ZoomAt keeps the point: %+v", w)
	}
	if v.ZoomAt(100, 0, 0).Zoom != MaxZoom || v.ZoomAt(0.001, 0, 0).Zoom != MinZoom {
		t.Error("zoom not clamped")
	}
	r := Rect{0, 0, 2000, 1000}
	f := Fit(r, 1040, 540)
	vis := f.Visible(1040, 540)
	if f.Zoom != 0.5 || !vis.Contains(0, 0) || !vis.Contains(2000, 1000) {
		t.Errorf("fit: %+v visible %+v", f, vis)
	}
	if f := Fit(Rect{0, 0, 10, 10}, 1000, 1000); f.Zoom != 1 {
		t.Errorf("fit of a small map zooms in: %+v", f)
	}
	if f := Fit(Rect{0, 0, 1e6, 1e6}, 500, 500); f.Zoom != MinZoom {
		t.Errorf("fit of a huge map: %+v", f)
	}
	if f := Fit(Rect{}, 500, 500); f.Zoom != 1 {
		t.Errorf("fit of an empty map: %+v", f)
	}
	c := v.CentreOn(500, 500, 200, 100)
	if sx, sy := c.ToScreen(500, 500); sx != 100 || sy != 50 {
		t.Errorf("centre: %g %g", sx, sy)
	}
}

// TC-709: find and locate.
func TestFindAndLocate(t *testing.T) {
	hosts := []apitypes.Host{
		{ID: 1, DisplayName: "web", PTRName: "web.example.org.", IP: "10.0.0.1"},
		{ID: 2, DisplayName: "DB", PTRName: "", IP: "10.0.0.2"},
		{ID: 3, DisplayName: "printer", PTRName: "prn.EXAMPLE.org.", IP: "10.0.1.3"},
	}
	ids := func(hs []apitypes.Host) []int64 {
		var out []int64
		for _, h := range hs {
			out = append(out, h.ID)
		}
		return out
	}
	for q, want := range map[string][]int64{
		"example": {3, 1}, // by PTR name, ordered by display name
		"db":      {2},    // case-insensitive
		"10.0.0":  {2, 1}, // by IP
		"  ":      nil,
		"nothing": nil,
	} {
		if got := ids(Find(hosts, q)); !slices.Equal(got, want) {
			t.Errorf("Find(%q) = %v, want %v", q, got, want)
		}
	}

	m := loaded()
	// Host 1 is in the current custom view 2, under collapsed group 5.
	if v, exp, ok := m.Locate(1, 2); !ok || v != 2 || !slices.Equal(exp, []int64{5}) {
		t.Errorf("locate in custom view: %d %v %v", v, exp, ok)
	}
	// Host 3 isn't in view 2: the network view; group 1 is expanded.
	if v, exp, ok := m.Locate(3, 2); !ok || v != 1 || exp != nil {
		t.Errorf("locate in network view: %d %v %v", v, exp, ok)
	}
	// Host 1 from the network view: group 2 is collapsed, group 1 isn't.
	if v, exp, ok := m.Locate(1, 1); !ok || v != 1 || !slices.Equal(exp, []int64{2}) {
		t.Errorf("locate: %d %v %v", v, exp, ok)
	}
	if _, _, ok := m.Locate(99, 1); ok {
		t.Error("unknown host located")
	}
	empty := New()
	if v, _, ok := empty.Locate(1, 2); ok || v != 1 {
		t.Errorf("network view not loaded: %d %v", v, ok)
	}
}

// TC-711: reloads are coalesced, at most once per delay per view.
func TestReloader(t *testing.T) {
	var mu sync.Mutex
	got := map[int64]int{}
	done := make(chan int64, 10)
	r := NewReloader(50*time.Millisecond, func(v int64) {
		mu.Lock()
		got[v]++
		mu.Unlock()
		done <- v
	})
	for range 5 {
		r.Request(1)
	}
	r.Request(2)
	for range 2 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("no reload")
		}
	}
	mu.Lock()
	if got[1] != 1 || got[2] != 1 {
		t.Errorf("reloads: %v", got)
	}
	mu.Unlock()
	// After the reload, a new request schedules another one.
	r.Request(1)
	<-done
	r.Request(1)
	r.Stop()
	r.Request(2)
	select {
	case v := <-done:
		t.Errorf("reload %d after Stop", v)
	case <-time.After(150 * time.Millisecond):
	}
}
