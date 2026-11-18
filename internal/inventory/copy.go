package inventory

import (
	"context"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// CopyGroup copies a group of another view into a custom view (SPEC-729,
// Design-group-show-in-view.md G1-G6): a new group with the same name,
// description, host labels, bar colour and collapsed state at the view's
// root where new items go, the same subgroups below it, and every host of
// the subtree placed in the matching new group at the same position
// relative to the group. Hosts already in the view move into the copy. A
// network group's CIDR isn't copied. It is one transaction; the events are
// group.created for each new group (the top first) and placement.updated
// for each host.
func (s *Service) CopyGroup(ctx context.Context, view, group int64) (apitypes.GroupCopyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.m.Views[view] == nil:
		return apitypes.GroupCopyResult{}, notFoundf("view %d not found", view)
	case view == NetworkView:
		return apitypes.GroupCopyResult{}, invalidf("groups can be copied only into a custom view")
	}
	src := s.m.Groups[group]
	if src == nil {
		return apitypes.GroupCopyResult{}, invalidf("group %d not found", group)
	}
	if src.ViewID == view {
		return apitypes.GroupCopyResult{}, invalidf("group %d is in view %d already", group, view)
	}

	// The subtree in the source view, parents before children.
	in := s.m.In(src.ViewID)
	order := []int64{group}
	inTree := map[int64]bool{group: true}
	for i := 0; i < len(order); i++ {
		for _, id := range sortedKeys(in.Groups) {
			if g := in.Groups[id]; g.ParentID == order[i] && !inTree[id] {
				inTree[id] = true
				order = append(order, id)
			}
		}
	}

	tx, ty := s.m.In(view).FreePosition(Root)
	dx, dy := tx-src.X, ty-src.Y
	now := s.stamp()
	newID := map[int64]int64{}
	next := s.nextGroup
	var ch Changes
	for _, id := range order {
		g := in.Groups[id]
		c := Group{
			ID: next, ViewID: view, Name: g.Name, Description: g.Description,
			Collapsed: g.Collapsed, Labels: g.Labels, Color: g.Color,
			X: roundPos(g.X + dx), Y: roundPos(g.Y + dy), CreatedAt: now, UpdatedAt: now,
		}
		if c.Name == "" && g.CIDR.IsValid() {
			c.Name = g.CIDR.String()
		}
		if id != group {
			c.ParentID = newID[g.ParentID]
		}
		newID[id] = next
		next++
		ch.Groups = append(ch.Groups, c)
	}
	moved := 0
	for _, hid := range sortedKeys(in.Hosts) {
		h := in.Hosts[hid]
		if !inTree[h.ParentID] {
			continue
		}
		if s.m.Placements[view][hid] != nil {
			moved++
		}
		ch.Placements = append(ch.Placements, Placement{ViewID: view, HostID: hid, ParentID: newID[h.ParentID], X: roundPos(h.X + dx), Y: roundPos(h.Y + dy)})
	}
	ch.NextGroupID = next
	if err := checkRange(ch); err != nil {
		return apitypes.GroupCopyResult{}, err
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return apitypes.GroupCopyResult{}, err
	}
	before := s.m.AggregateAll()
	s.nextGroup = next
	for i := range ch.Groups {
		g := ch.Groups[i]
		s.m.Groups[g.ID] = &g
	}
	if s.m.Placements[view] == nil && len(ch.Placements) > 0 {
		s.m.Placements[view] = map[int64]*Placement{}
	}
	for i := range ch.Placements {
		p := ch.Placements[i]
		s.m.Placements[view][p.HostID] = &p
	}
	counts := s.m.In(view).Aggregate()
	for _, g := range ch.Groups {
		s.notify.Publish(apitypes.EventGroupCreated, groupView(s.m.Groups[g.ID], counts[g.ID]))
	}
	for _, p := range ch.Placements {
		s.notify.Publish(apitypes.EventPlacementUpdated, placementView(s.m.Placements[view][p.HostID]))
	}
	s.emitGroupStatus(before)
	return apitypes.GroupCopyResult{
		ViewID: view, GroupID: newID[group], Groups: len(ch.Groups), Hosts: len(ch.Placements), Moved: moved,
	}, nil
}
