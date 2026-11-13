package mapmodel

import (
	"slices"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-730 (SPEC-726): a focus lays out the group's subtree as the root,
// without the group itself, also when the group (or a group above it) is
// collapsed; collapsed subgroups stay collapsed; the frames' depth starts
// at 0; a group that isn't in the view gives nothing.
func TestLayoutIn(t *testing.T) {
	m := New()
	m.SetMap(apitypes.Map{ViewID: 1, Items: []apitypes.MapItem{
		{Type: "group", ID: 1, Name: "lan", X: 0, Y: 0, Collapsed: true},
		{Type: "group", ID: 2, ParentID: p(1), Name: "servers", X: 100, Y: 100},
		{Type: "host", ID: 1, ParentID: p(2), Name: "web", X: 100, Y: 200},
		{Type: "group", ID: 3, ParentID: p(2), Name: "db", X: 300, Y: 200, Collapsed: true},
		{Type: "host", ID: 2, ParentID: p(3), Name: "pg", X: 300, Y: 300},
		{Type: "host", ID: 3, ParentID: p(1), Name: "printer", X: 500, Y: 100},
		{Type: "host", ID: 4, Name: "laptop", X: 900, Y: 0},
	}})
	keys := func(ns []Node) []string {
		var out []string
		for _, n := range ns {
			out = append(out, n.Item.Name+":"+map[Kind]string{KindHost: "h", KindGroup: "g", KindFrame: "f"}[n.Kind])
		}
		slices.Sort(out)
		return out
	}
	if got := keys(m.Layout(1, measure)); !slices.Equal(got, []string{"lan:g", "laptop:h"}) {
		t.Errorf("whole view: %v", got)
	}
	// lan is collapsed on the shared map; its focus shows its contents.
	if got := keys(m.LayoutIn(1, 1, measure)); !slices.Equal(got, []string{"db:g", "printer:h", "servers:f", "web:h"}) {
		t.Errorf("focus lan: %v", got)
	}
	nodes := m.LayoutIn(1, 2, measure)
	if got := keys(nodes); !slices.Equal(got, []string{"db:g", "web:h"}) {
		t.Errorf("focus servers: %v", got)
	}
	for _, n := range m.LayoutIn(1, 1, measure) {
		if n.Kind == KindFrame && n.Depth != 0 {
			t.Errorf("frame %s at depth %d", n.Item.Name, n.Depth)
		}
	}
	if !m.maps[1].Items[GroupKey(1)].Collapsed {
		t.Error("the focus changed the collapsed flag")
	}
	if m.LayoutIn(1, 99, measure) != nil || m.LayoutIn(7, 1, measure) != nil {
		t.Error("unknown group or view")
	}
	// Wrapping looks only at neighbours inside the focus: laptop, outside
	// it, doesn't make printer wrap.
	m.WrapNames = true
	m.SetMap(apitypes.Map{ViewID: 2, Items: []apitypes.MapItem{
		{Type: "group", ID: 5, Name: "g", X: 0, Y: 0},
		{Type: "host", ID: 5, ParentID: p(5), Name: "printer.example.org", X: 100, Y: 100},
		{Type: "host", ID: 6, Name: "laptop", X: 140, Y: 100},
	}})
	for _, n := range m.Layout(2, measure) {
		if n.Item.Type == "host" && n.Item.ID == 5 && n.NameLines < 2 {
			t.Errorf("whole view: printer should wrap next to laptop: %q", n.Labels)
		}
	}
	for _, n := range m.LayoutIn(2, 5, measure) {
		if n.Item.Type == "host" && n.Item.ID == 5 && n.NameLines != 1 {
			t.Errorf("wrapped because of a host outside the focus: %q", n.Labels)
		}
	}
}

// TC-731 (SPEC-726): the trail runs from the top of the view down to the
// group; InFocus is true below the focus only.
func TestTrailAndInFocus(t *testing.T) {
	m := New()
	m.SetMap(apitypes.Map{ViewID: 1, Items: []apitypes.MapItem{
		{Type: "group", ID: 1, Name: "lan"},
		{Type: "group", ID: 2, ParentID: p(1), Name: "servers"},
		{Type: "host", ID: 1, ParentID: p(2), Name: "web"},
		{Type: "host", ID: 2, Name: "laptop"},
	}})
	var names []string
	for _, it := range m.Trail(1, 2) {
		names = append(names, it.Name)
	}
	if !slices.Equal(names, []string{"lan", "servers"}) {
		t.Errorf("trail %v", names)
	}
	if m.Trail(1, 9) != nil || m.Trail(5, 1) != nil {
		t.Error("trail of an unknown group or view")
	}
	for _, c := range []struct {
		focus int64
		k     Key
		want  bool
	}{
		{0, HostKey(2), true}, {1, HostKey(1), true}, {1, GroupKey(2), true},
		{1, GroupKey(1), false}, {1, HostKey(2), false}, {2, HostKey(9), false},
	} {
		if got := m.InFocus(1, c.focus, c.k); got != c.want {
			t.Errorf("InFocus(%d, %v) = %v", c.focus, c.k, got)
		}
	}
}

// TC-732 (SPEC-726): the "Show" list has every view in tab order with its
// groups below it as an indented tree, siblings by name; a duplicate name
// gets its id; the filter ignores case and indentation.
func TestShowEntries(t *testing.T) {
	views := []apitypes.View{{ID: 1, Name: "Networks"}, {ID: 2, Name: "Lab"}}
	groups := []apitypes.Group{
		{ID: 10, ViewID: 1, Name: "lan"},
		{ID: 11, ViewID: 1, Name: "Servers", ParentID: p(10)},
		{ID: 12, ViewID: 1, Name: "clients", ParentID: p(10)},
		{ID: 13, ViewID: 1, Name: "dmz"},
		{ID: 20, ViewID: 2, Name: "lan"},
		{ID: 21, ViewID: 2, Name: "rack", ParentID: p(99)}, // parent missing: at the top
	}
	var got []string
	for _, e := range ShowEntries(views, groups) {
		got = append(got, e.Label)
	}
	want := []string{
		"Networks", "    dmz", "    lan (10)", "        clients", "        Servers",
		"Lab", "    lan (20)", "    rack",
	}
	if !slices.Equal(got, want) {
		t.Errorf("entries\n got %q\nwant %q", got, want)
	}
	es := ShowEntries(views, groups)
	if f := FilterEntries(es, " SERV "); len(f) != 1 || f[0].Group != 11 || f[0].View != 1 {
		t.Errorf("filter: %+v", f)
	}
	if f := FilterEntries(es, "lan"); len(f) != 2 {
		t.Errorf("filter lan: %+v", f)
	}
	if len(FilterEntries(es, "")) != len(es) {
		t.Error("empty filter")
	}
}
