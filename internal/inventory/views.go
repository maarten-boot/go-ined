package inventory

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Views returns all views, ordered by order, then id (SPEC-117).
func (s *Service) Views() []apitypes.View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]apitypes.View, 0, len(s.m.Views))
	for _, v := range s.m.Views {
		out = append(out, viewView(v))
	}
	slices.SortFunc(out, func(a, b apitypes.View) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// View returns one view.
func (s *Service) View(id int64) (apitypes.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.m.Views[id]
	if v == nil {
		return apitypes.View{}, notFoundf("view %d not found", id)
	}
	return viewView(v), nil
}

// CreateView adds a custom view (SPEC-117). Without an order it goes last.
func (s *Service) CreateView(ctx context.Context, in apitypes.ViewCreate) (apitypes.View, error) {
	name := strings.TrimSpace(in.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkViewName(name, 0); err != nil {
		return apitypes.View{}, err
	}
	now := s.stamp()
	v := View{ID: s.nextView, Name: name, Kind: apitypes.ViewCustom, CreatedAt: now, UpdatedAt: now}
	if in.Order != nil {
		v.Order = *in.Order
	} else {
		for _, o := range s.m.Views {
			v.Order = max(v.Order, o.Order+1)
		}
	}
	if err := s.repo.Apply(ctx, Changes{Views: []View{v}, NextViewID: v.ID + 1}); err != nil {
		return apitypes.View{}, err
	}
	s.nextView++
	s.m.Views[v.ID] = &v
	out := viewView(&v)
	s.notify.Publish(apitypes.EventViewCreated, out)
	return out, nil
}

// UpdateView renames or reorders a view. The network view can be renamed
// and reordered too.
func (s *Service) UpdateView(ctx context.Context, id int64, p apitypes.ViewPatch) (apitypes.View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m.Views[id]
	if cur == nil {
		return apitypes.View{}, notFoundf("view %d not found", id)
	}
	v := *cur
	if p.Name.Set {
		v.Name = strings.TrimSpace(p.Name.Value)
		if err := s.checkViewName(v.Name, id); err != nil {
			return apitypes.View{}, err
		}
	}
	if p.Order.Set {
		v.Order = p.Order.Value
	}
	v.UpdatedAt = s.stamp()
	if err := s.repo.Apply(ctx, Changes{Views: []View{v}}); err != nil {
		return apitypes.View{}, err
	}
	*cur = v
	out := viewView(cur)
	s.notify.Publish(apitypes.EventViewUpdated, out)
	return out, nil
}

// DeleteView removes a custom view with its groups and placements; hosts
// stay (SPEC-120). It emits one view.deleted.
func (s *Service) DeleteView(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m.Views[id] == nil {
		return notFoundf("view %d not found", id)
	}
	if id == NetworkView {
		return conflictf("the network view can't be deleted")
	}
	ch := Changes{DeleteViews: []int64{id}}
	for _, gid := range sortedKeys(s.m.Groups) {
		if s.m.Groups[gid].ViewID == id {
			ch.DeleteGroups = append(ch.DeleteGroups, gid)
		}
	}
	for _, hid := range sortedKeys(s.m.Placements[id]) {
		ch.DeletePlacements = append(ch.DeletePlacements, PlacementKey{id, hid})
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return err
	}
	for _, gid := range ch.DeleteGroups {
		delete(s.m.Groups, gid)
	}
	delete(s.m.Placements, id)
	delete(s.m.Views, id)
	s.notify.Publish(apitypes.EventViewDeleted, apitypes.DeletedEvent{ID: id})
	return nil
}

// PlaceHost adds a host to a custom view or moves it there (SPEC-119).
// Without x and y, a new placement or a change of parent gets a free
// position; otherwise the position is kept.
func (s *Service) PlaceHost(ctx context.Context, view, host int64, in apitypes.PlacementSet) (apitypes.Placement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCustomView(view); err != nil {
		return apitypes.Placement{}, err
	}
	if s.m.Hosts[host] == nil {
		return apitypes.Placement{}, notFoundf("host %d not found", host)
	}
	p := Placement{ViewID: view, HostID: host}
	cur := s.m.Placements[view][host]
	if cur != nil {
		p = *cur
	}
	parent := Root
	if in.ParentID != nil {
		parent = *in.ParentID
		if err := s.checkParent(parent, view); err != nil {
			return apitypes.Placement{}, err
		}
	}
	keep := cur != nil && cur.ParentID == parent && in.X == nil && in.Y == nil
	p.ParentID = parent
	if !keep {
		if err := s.placeNew(&p.X, &p.Y, in.X, in.Y, view, parent); err != nil {
			return apitypes.Placement{}, err
		}
	}
	if err := s.repo.Apply(ctx, Changes{Placements: []Placement{p}}); err != nil {
		return apitypes.Placement{}, err
	}
	before := s.m.AggregateAll()
	if s.m.Placements[view] == nil {
		s.m.Placements[view] = map[int64]*Placement{}
	}
	s.m.Placements[view][host] = &p
	out := placementView(&p)
	s.notify.Publish(apitypes.EventPlacementUpdated, out)
	s.emitGroupStatus(before)
	return out, nil
}

// UnplaceHost removes a host from a custom view; the host stays.
func (s *Service) UnplaceHost(ctx context.Context, view, host int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCustomView(view); err != nil {
		return err
	}
	if s.m.Placements[view][host] == nil {
		return notFoundf("host %d is not in view %d", host, view)
	}
	k := PlacementKey{view, host}
	if err := s.repo.Apply(ctx, Changes{DeletePlacements: []PlacementKey{k}}); err != nil {
		return err
	}
	before := s.m.AggregateAll()
	s.unplace(k)
	s.notify.Publish(apitypes.EventPlacementDeleted, apitypes.PlacementDeletedEvent{ViewID: view, HostID: host})
	s.emitGroupStatus(before)
	return nil
}

// ---- helpers (caller holds the lock) ----

func (s *Service) checkCustomView(view int64) error {
	switch {
	case s.m.Views[view] == nil:
		return notFoundf("view %d not found", view)
	case view == NetworkView:
		return invalidf("hosts are placed in the network view with PATCH /hosts/{id}")
	}
	return nil
}

func (s *Service) checkViewName(name string, self int64) error {
	if name == "" {
		return invalidf("view name must not be empty")
	}
	if err := checkText("name", name, MaxNameBytes); err != nil {
		return err
	}
	for _, v := range s.m.Views {
		if v.ID != self && v.Name == name {
			return conflictf("view %d already has the name %q", v.ID, name)
		}
	}
	return nil
}

func (s *Service) unplace(k PlacementKey) {
	ps := s.m.Placements[k.ViewID]
	delete(ps, k.HostID)
	if len(ps) == 0 {
		delete(s.m.Placements, k.ViewID)
	}
}

func viewView(v *View) apitypes.View {
	return apitypes.View{ID: v.ID, Name: v.Name, Kind: v.Kind, Order: v.Order, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}

func placementView(p *Placement) apitypes.Placement {
	return apitypes.Placement{ViewID: p.ViewID, HostID: p.HostID, ParentID: parentPtr(p.ParentID), X: p.X, Y: p.Y}
}
