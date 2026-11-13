package mapmodel

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// A focus shows one group of a view on its own (SPEC-726, user idea of
// 2026-11-13): the same map, narrowed to the group's subtree. Nothing is
// stored on the server.

// Trail returns the groups from the top of a loaded view down to group,
// group included (for the trail above the map). It is nil when the group
// isn't in the view.
func (m *Model) Trail(view, group int64) []apitypes.MapItem {
	vm := m.maps[view]
	if vm == nil {
		return nil
	}
	var out []apitypes.MapItem
	seen := map[int64]bool{}
	for id := &group; id != nil && !seen[*id]; {
		seen[*id] = true
		it := vm.Items[GroupKey(*id)]
		if it == nil {
			break
		}
		out = append(out, *it)
		id = it.ParentID
	}
	if len(out) == 0 {
		return nil
	}
	slices.Reverse(out)
	return out
}

// InFocus reports whether an item lies below the focus group (focus 0:
// the whole view, so always true for an item of the view).
func (m *Model) InFocus(view, focus int64, k Key) bool {
	vm := m.maps[view]
	if vm == nil || vm.Items[k] == nil {
		return false
	}
	if focus == 0 {
		return true
	}
	return k != GroupKey(focus) && m.Subtree(view, focus)[k]
}

// ShowEntry is one line of the "Show" list: a whole view (Group 0) or one
// of its groups.
type ShowEntry struct {
	Label string
	View  int64
	Group int64
}

// ShowEntries lists every view in tab order and, below each, its groups as
// an indented tree, siblings by name. A group name that occurs more than
// once gets its id, so every label is unique. A group whose parent is
// missing is listed at the top of its view.
func ShowEntries(views []apitypes.View, groups []apitypes.Group) []ShowEntry {
	count := map[string]int{}
	for _, v := range views {
		count[v.Name]++
	}
	for _, gr := range groups {
		count[gr.Name]++
	}
	byID := map[int64]bool{}
	for _, gr := range groups {
		byID[gr.ID] = true
	}
	type key struct{ view, parent int64 }
	children := map[key][]apitypes.Group{}
	for _, gr := range groups {
		var parent int64
		if gr.ParentID != nil && byID[*gr.ParentID] {
			parent = *gr.ParentID
		}
		children[key{gr.ViewID, parent}] = append(children[key{gr.ViewID, parent}], gr)
	}
	for _, c := range children {
		slices.SortFunc(c, func(a, b apitypes.Group) int {
			return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
		})
	}
	var out []ShowEntry
	seen := map[int64]bool{}
	var walk func(view, parent int64, depth int)
	walk = func(view, parent int64, depth int) {
		for _, gr := range children[key{view, parent}] {
			if seen[gr.ID] {
				continue
			}
			seen[gr.ID] = true
			label := gr.Name
			if count[gr.Name] > 1 {
				label = fmt.Sprintf("%s (%d)", gr.Name, gr.ID)
			}
			out = append(out, ShowEntry{Label: strings.Repeat("    ", depth) + label, View: view, Group: gr.ID})
			walk(view, gr.ID, depth+1)
		}
	}
	for _, v := range views {
		label := v.Name
		if count[v.Name] > 1 {
			label = fmt.Sprintf("%s (view %d)", v.Name, v.ID)
		}
		out = append(out, ShowEntry{Label: label, View: v.ID})
		walk(v.ID, 0, 1)
	}
	return out
}

// FilterEntries keeps the entries whose label contains the text, ignoring
// case and the indentation; an empty text keeps all.
func FilterEntries(entries []ShowEntry, text string) []ShowEntry {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return entries
	}
	var out []ShowEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(strings.TrimSpace(e.Label)), t) {
			out = append(out, e)
		}
	}
	return out
}
