// Package mapmodel is the GUI's picture of the server: the views and the
// maps of the views it has loaded, kept up to date from the event stream
// (SPEC-715, SPEC-716). It also computes what the map shows: visibility,
// frames of expanded groups, group counts, coordinates and find. It has
// no GUI dependency and is not safe for concurrent use; the GUI calls it
// from its UI goroutine.
package mapmodel

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// NetworkView is the id of the network view.
const NetworkView = apitypes.NetworkViewID

// Key identifies an item within a view.
type Key struct {
	Type string // apitypes.TypeHost or apitypes.TypeGroup
	ID   int64
}

// HostKey and GroupKey build keys.
func HostKey(id int64) Key  { return Key{apitypes.TypeHost, id} }
func GroupKey(id int64) Key { return Key{apitypes.TypeGroup, id} }

// ViewMap holds the items of one loaded view.
type ViewMap struct {
	ID    int64
	Items map[Key]*apitypes.MapItem
	gen   uint64 // bumped on every change after the map was loaded
}

// Model holds the views and the loaded maps.
type Model struct {
	views    []apitypes.View
	viewsGen uint64 // bumped by every view event
	maps     map[int64]*ViewMap
}

// New returns an empty model.
func New() *Model { return &Model{maps: map[int64]*ViewMap{}} }

// SetViews replaces the list of views. Maps of views that no longer exist
// are dropped.
func (m *Model) SetViews(vs []apitypes.View) {
	m.views = slices.Clone(vs)
	m.sortViews()
	for id := range m.maps {
		if m.viewIndex(id) < 0 {
			delete(m.maps, id)
		}
	}
}

// ViewsGen returns a counter that changes with every view event. Like
// Gen for maps: a caller that fetches the views notes it, and fetches
// again if it changed meanwhile (crosscheck 2026-11-11-02 C2).
func (m *Model) ViewsGen() uint64 { return m.viewsGen }

// Views returns the views in tab order (SPEC-715).
func (m *Model) Views() []apitypes.View { return slices.Clone(m.views) }

// View returns one view.
func (m *Model) View(id int64) (apitypes.View, bool) {
	if i := m.viewIndex(id); i >= 0 {
		return m.views[i], true
	}
	return apitypes.View{}, false
}

// SetMap stores a loaded map, replacing an older one of the same view.
func (m *Model) SetMap(mp apitypes.Map) {
	vm := &ViewMap{ID: mp.ViewID, Items: make(map[Key]*apitypes.MapItem, len(mp.Items))}
	if old := m.maps[mp.ViewID]; old != nil {
		vm.gen = old.gen + 1
	}
	for i := range mp.Items {
		it := mp.Items[i]
		vm.Items[Key{it.Type, it.ID}] = &it
	}
	m.maps[mp.ViewID] = vm
}

// Map returns a loaded view's map, or nil.
func (m *Model) Map(view int64) *ViewMap { return m.maps[view] }

// Loaded reports whether a view's map is loaded.
func (m *Model) Loaded(view int64) bool { return m.maps[view] != nil }

// LoadedViews returns the ids of the loaded views.
func (m *Model) LoadedViews() []int64 {
	ids := make([]int64, 0, len(m.maps))
	for id := range m.maps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Gen returns a counter that changes whenever a view's map changes. A
// caller that starts a reload notes it; if it changed by the time the
// reload arrives, an event was applied meanwhile and the fresh map may
// miss it, so the caller reloads once more.
func (m *Model) Gen(view int64) uint64 {
	if vm := m.maps[view]; vm != nil {
		return vm.gen
	}
	return 0
}

// Effect tells the GUI what an event changed.
type Effect struct {
	Redraw       []int64 // loaded views whose items changed in place
	Reload       []int64 // loaded views whose map must be reloaded
	ViewsChanged bool    // the list of views (tabs) changed
	Deleted      []int64 // views that were deleted
	Resync       bool    // reload the views and every loaded map
}

// Apply updates the model from one event (SPEC-716): host.status,
// host.updated and group.status change items in place; other changes to
// a view's items ask for a reload of that view; view.* events change the
// list of views; resync asks for a full reload.
func (m *Model) Apply(typ string, data json.RawMessage) (Effect, error) {
	var e Effect
	switch typ {
	case apitypes.EventResync:
		e.Resync = true

	case apitypes.EventHostStatus, apitypes.EventHostUpdated:
		var h apitypes.Host
		if err := decode(typ, data, &h); err != nil {
			return e, err
		}
		for _, vid := range m.LoadedViews() {
			vm := m.maps[vid]
			it := vm.Items[HostKey(h.ID)]
			if it == nil {
				if vid == NetworkView {
					e.Reload = append(e.Reload, vid) // every host is in the network view
				}
				continue
			}
			it.Name, it.Status, it.Stale, it.HasNote, it.Monitored, it.IP = h.DisplayName, h.Status, h.Stale, h.Note != "", h.Monitored, h.IP
			if vid == NetworkView {
				it.ParentID, it.X, it.Y, it.OutsideCIDR = h.ParentID, h.X, h.Y, h.OutsideCIDR
			}
			vm.gen++
			e.Redraw = append(e.Redraw, vid)
		}

	case apitypes.EventGroupStatus:
		var s apitypes.GroupStatusEvent
		if err := decode(typ, data, &s); err != nil {
			return e, err
		}
		if vm, it := m.findGroup(s.ID); it != nil {
			it.Status = s.Status
			vm.gen++
			e.Redraw = append(e.Redraw, vm.ID)
		}

	case apitypes.EventHostCreated:
		e.Reload = m.ifLoaded(NetworkView) // new hosts are placed only in the network view (SPEC-119)

	case apitypes.EventHostDeleted:
		var d apitypes.DeletedEvent
		if err := decode(typ, data, &d); err != nil {
			return e, err
		}
		for _, vid := range m.LoadedViews() {
			if m.maps[vid].Items[HostKey(d.ID)] != nil {
				e.Reload = append(e.Reload, vid)
			}
		}

	case apitypes.EventGroupCreated, apitypes.EventGroupUpdated:
		var g apitypes.Group
		if err := decode(typ, data, &g); err != nil {
			return e, err
		}
		e.Reload = m.ifLoaded(cmp.Or(g.ViewID, NetworkView))

	case apitypes.EventGroupDeleted:
		var d apitypes.DeletedEvent
		if err := decode(typ, data, &d); err != nil {
			return e, err
		}
		if vm, _ := m.findGroup(d.ID); vm != nil {
			e.Reload = []int64{vm.ID}
		}

	case apitypes.EventPlacementUpdated, apitypes.EventPlacementDeleted:
		var p apitypes.PlacementDeletedEvent // both carry view_id and host_id
		if err := decode(typ, data, &p); err != nil {
			return e, err
		}
		e.Reload = m.ifLoaded(p.ViewID)

	case apitypes.EventViewCreated, apitypes.EventViewUpdated:
		var v apitypes.View
		if err := decode(typ, data, &v); err != nil {
			return e, err
		}
		if i := m.viewIndex(v.ID); i >= 0 {
			m.views[i] = v
		} else {
			m.views = append(m.views, v)
		}
		m.sortViews()
		m.viewsGen++
		e.ViewsChanged = true

	case apitypes.EventViewDeleted:
		var d apitypes.DeletedEvent
		if err := decode(typ, data, &d); err != nil {
			return e, err
		}
		if i := m.viewIndex(d.ID); i >= 0 {
			m.views = slices.Delete(m.views, i, i+1)
			e.ViewsChanged = true
		}
		delete(m.maps, d.ID)
		m.viewsGen++
		e.Deleted = []int64{d.ID}
	}
	return e, nil
}

func decode(typ string, data json.RawMessage, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("mapmodel: %s: %w", typ, err)
	}
	return nil
}

func (m *Model) ifLoaded(view int64) []int64 {
	if m.Loaded(view) {
		return []int64{view}
	}
	return nil
}

// findGroup returns the loaded view that holds group id (groups belong to
// exactly one view).
func (m *Model) findGroup(id int64) (*ViewMap, *apitypes.MapItem) {
	for _, vid := range m.LoadedViews() {
		if it := m.maps[vid].Items[GroupKey(id)]; it != nil {
			return m.maps[vid], it
		}
	}
	return nil, nil
}

func (m *Model) viewIndex(id int64) int {
	return slices.IndexFunc(m.views, func(v apitypes.View) bool { return v.ID == id })
}

func (m *Model) sortViews() {
	slices.SortFunc(m.views, func(a, b apitypes.View) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.ID, b.ID))
	})
}
