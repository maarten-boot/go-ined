package inventory

import (
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Repository persists the inventory. Apply must be atomic. The Service is
// the only writer and keeps referential integrity itself.
type Repository interface {
	Load(ctx context.Context) (State, error)
	Apply(ctx context.Context, c Changes) error
}

// State is everything Load returns. NextHostID, NextGroupID and NextViewID
// are the persisted id sequences (SPEC-116); 0 means unknown.
type State struct {
	Views       []View
	Groups      []Group
	Hosts       []Host
	Placements  []Placement
	NextHostID  int64
	NextGroupID int64
	NextViewID  int64
}

// Changes is one atomic set of writes. Upserts insert or replace by ID (by
// view and host for placements). Deletes are applied first. The Next*
// fields, when not 0, update the persisted sequences.
type Changes struct {
	Views            []View
	Groups           []Group
	Hosts            []Host
	Placements       []Placement
	DeleteViews      []int64
	DeleteGroups     []int64
	DeleteHosts      []int64
	DeletePlacements []PlacementKey
	NextHostID       int64
	NextGroupID      int64
	NextViewID       int64
}

// Notifier receives the events of every change (SPEC-502, SPEC-305). It is
// called with the Service lock held, so events are published in the order
// of the changes; Publish must not block or call back into the Service.
type Notifier interface {
	Publish(typ string, data any)
}

type nopNotifier struct{}

func (nopNotifier) Publish(string, any) {}

// Service is the inventory with locking and write-through persistence. All
// returned values are copies.
type Service struct {
	mu        sync.RWMutex
	repo      Repository
	m         *Model
	nextHost  int64
	nextGroup int64
	nextView  int64
	now       func() time.Time
	warnings  []string
	notify    Notifier
}

// SetNotifier sets where change events go. nil turns events off. Call it
// before the Service is shared.
func (s *Service) SetNotifier(n Notifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n == nil {
		n = nopNotifier{}
	}
	s.notify = n
}

// emitGroupStatus publishes group.status for every group that existed in
// before and whose computed status differs now (SPEC-305). Groups that are
// new (their created event carries the status) or deleted are skipped.
func (s *Service) emitGroupStatus(before map[int64]Counts) {
	after := s.m.AggregateAll()
	for _, id := range sortedKeys(after) {
		if b, ok := before[id]; ok && b.Status() != after[id].Status() {
			s.notify.Publish(apitypes.EventGroupStatus, apitypes.GroupStatusEvent{ID: id, Status: after[id].Status()})
		}
	}
}

// NewService loads the inventory from repo. Every loaded monitored host is
// marked stale until it has been pinged again (SPEC-603). The tree is checked
// (SPEC-605): items whose parent is missing are moved to the root and
// reported by LoadWarnings; a cycle is an error.
func NewService(ctx context.Context, repo Repository) (*Service, error) {
	st, err := repo.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("inventory: load: %w", err)
	}
	s := &Service{repo: repo, m: NewModel(), nextHost: max(1, st.NextHostID), nextGroup: max(1, st.NextGroupID),
		nextView: max(NetworkView+1, st.NextViewID), now: time.Now, notify: nopNotifier{}}
	for i := range st.Views {
		v := st.Views[i]
		s.m.Views[v.ID] = &v
		s.nextView = max(s.nextView, v.ID+1)
	}
	for i := range st.Groups {
		g := st.Groups[i]
		if g.ViewID == 0 {
			g.ViewID = NetworkView
		}
		s.m.Groups[g.ID] = &g
		s.nextGroup = max(s.nextGroup, g.ID+1)
	}
	for i := range st.Placements {
		p := st.Placements[i]
		if s.m.Placements[p.ViewID] == nil {
			s.m.Placements[p.ViewID] = map[int64]*Placement{}
		}
		s.m.Placements[p.ViewID][p.HostID] = &p
	}
	for i := range st.Hosts {
		h := st.Hosts[i]
		h.Stale = h.Monitored
		s.m.Hosts[h.ID] = &h
		s.nextHost = max(s.nextHost, h.ID+1)
	}
	if err := s.repairOnLoad(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadWarnings returns the repairs made while loading (SPEC-605).
func (s *Service) LoadWarnings() []string { return slices.Clone(s.warnings) }

// repairOnLoad moves orphans to the root of their view (persisted) and
// rejects cycles (SPEC-605, SPEC-123).
func (s *Service) repairOnLoad(ctx context.Context) error {
	var ch Changes
	if s.m.Views[NetworkView] == nil {
		// A repository without views (only in tests: migration 3 seeds it).
		now := s.stamp()
		v := View{ID: NetworkView, Name: "Networks", Kind: apitypes.ViewNetwork, CreatedAt: now, UpdatedAt: now}
		s.m.Views[v.ID] = &v
		ch.Views = append(ch.Views, v)
	}
	warn := func(format string, a ...any) { s.warnings = append(s.warnings, fmt.Sprintf(format, a...)) }
	for _, id := range sortedKeys(s.m.Groups) {
		g := s.m.Groups[id]
		changed := false
		if s.m.Views[g.ViewID] == nil {
			warn("group %d: view %d does not exist; moved to the root of view %d", id, g.ViewID, NetworkView)
			g.ViewID, g.ParentID, changed = NetworkView, Root, true
		}
		if g.IsNetwork() && g.ViewID != NetworkView {
			warn("group %d: a network group in a custom view; moved to the root of view %d", id, NetworkView)
			g.ViewID, g.ParentID, changed = NetworkView, Root, true
		}
		if changed {
			ch.Groups = append(ch.Groups, *g)
		}
	}
	// Parents are checked after every group's view is final.
	for _, id := range sortedKeys(s.m.Groups) {
		g := s.m.Groups[id]
		if g.ParentID == Root {
			continue
		}
		switch p := s.m.Groups[g.ParentID]; {
		case p == nil:
			warn("group %d: parent group %d does not exist; moved to the root", id, g.ParentID)
		case p.ViewID != g.ViewID:
			warn("group %d: parent group %d is in another view; moved to the root", id, g.ParentID)
		default:
			continue
		}
		g.ParentID = Root
		ch.Groups = append(ch.Groups, *g)
	}
	for _, id := range sortedKeys(s.m.Hosts) {
		h := s.m.Hosts[id]
		if h.ParentID == Root {
			continue
		}
		switch p := s.m.Groups[h.ParentID]; {
		case p == nil:
			warn("host %d: parent group %d does not exist; moved to the root", id, h.ParentID)
		case p.ViewID != NetworkView:
			warn("host %d: parent group %d is not in the network view; moved to the root", id, h.ParentID)
		default:
			continue
		}
		h.ParentID = Root
		ch.Hosts = append(ch.Hosts, *h)
	}
	for _, vid := range sortedKeys(s.m.Placements) {
		for _, hid := range sortedKeys(s.m.Placements[vid]) {
			p := s.m.Placements[vid][hid]
			switch {
			case vid == NetworkView || s.m.Views[vid] == nil || s.m.Hosts[hid] == nil:
				warn("placement of host %d in view %d: no such host or custom view; removed", hid, vid)
				delete(s.m.Placements[vid], hid)
				ch.DeletePlacements = append(ch.DeletePlacements, PlacementKey{vid, hid})
			case p.ParentID != Root && (s.m.Groups[p.ParentID] == nil || s.m.Groups[p.ParentID].ViewID != vid):
				warn("placement of host %d in view %d: parent group %d is missing or in another view; moved to the root", hid, vid, p.ParentID)
				p.ParentID = Root
				ch.Placements = append(ch.Placements, *p)
			}
		}
		if len(s.m.Placements[vid]) == 0 {
			delete(s.m.Placements, vid)
		}
	}
	for _, id := range sortedKeys(s.m.Groups) {
		if s.m.inCycle(id) {
			return fmt.Errorf("inventory: the database is inconsistent: group %d is part of a parent cycle; fix it by hand", id)
		}
	}
	if len(ch.Views)+len(ch.Groups)+len(ch.Hosts)+len(ch.Placements)+len(ch.DeletePlacements) > 0 {
		if err := s.repo.Apply(ctx, ch); err != nil {
			return fmt.Errorf("inventory: save repairs: %w", err)
		}
	}
	return nil
}

func (s *Service) stamp() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

// ---- reads ----

// Hosts returns all hosts ordered by ID.
func (s *Service) Hosts() []apitypes.Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]apitypes.Host, 0, len(s.m.Hosts))
	for _, id := range sortedKeys(s.m.Hosts) {
		out = append(out, s.hostView(s.m.Hosts[id]))
	}
	return out
}

// Host returns one host.
func (s *Service) Host(id int64) (apitypes.Host, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.m.Hosts[id]
	if h == nil {
		return apitypes.Host{}, notFoundf("host %d not found", id)
	}
	return s.hostView(h), nil
}

// Groups returns all groups ordered by ID.
func (s *Service) Groups() []apitypes.Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	agg := s.m.AggregateAll()
	out := make([]apitypes.Group, 0, len(s.m.Groups))
	for _, id := range sortedKeys(s.m.Groups) {
		out = append(out, groupView(s.m.Groups[id], agg[id]))
	}
	return out
}

// GroupsIn returns the groups of one view ordered by ID (SPEC-122).
func (s *Service) GroupsIn(view int64) ([]apitypes.Group, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.m.Views[view] == nil {
		return nil, notFoundf("view %d not found", view)
	}
	vm := s.m.In(view)
	agg := vm.Aggregate()
	out := make([]apitypes.Group, 0, len(vm.Groups))
	for _, id := range sortedKeys(vm.Groups) {
		out = append(out, groupView(vm.Groups[id], agg[id]))
	}
	return out, nil
}

// Group returns one group.
func (s *Service) Group(id int64) (apitypes.Group, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.m.Groups[id]
	if g == nil {
		return apitypes.Group{}, notFoundf("group %d not found", id)
	}
	return groupView(g, s.m.In(g.ViewID).Aggregate()[id]), nil
}

// Map returns every item of the network view for GET /map (SPEC-501).
func (s *Service) Map() apitypes.Map {
	m, _ := s.MapOf(NetworkView)
	return m
}

// MapOf returns the items of one view (SPEC-122): groups first, then
// hosts, each ordered by ID. Hosts have their placement in that view.
func (s *Service) MapOf(view int64) (apitypes.Map, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.m.Views[view] == nil {
		return apitypes.Map{}, notFoundf("view %d not found", view)
	}
	vm := s.m.In(view)
	agg := vm.Aggregate()
	items := make([]apitypes.MapItem, 0, len(vm.Groups)+len(vm.Hosts))
	for _, id := range sortedKeys(vm.Groups) {
		g := vm.Groups[id]
		it := apitypes.MapItem{
			Type: apitypes.TypeGroup, ID: g.ID, ParentID: parentPtr(g.ParentID),
			X: g.X, Y: g.Y, Name: g.Name, Status: agg[id].Status(),
			HasNote: g.Note != "", Collapsed: g.Collapsed, Labels: g.Labels,
		}
		if g.IsNetwork() {
			it.CIDR = g.CIDR.String()
		}
		items = append(items, it)
	}
	for _, id := range sortedKeys(vm.Hosts) {
		h := vm.Hosts[id]
		items = append(items, apitypes.MapItem{
			Type: apitypes.TypeHost, ID: h.ID, ParentID: parentPtr(h.ParentID),
			X: h.X, Y: h.Y, Name: h.DisplayName(), Status: h.Status, Stale: h.Stale,
			HasNote: h.Note != "", Monitored: h.Monitored, IP: h.IP.String(), OutsideCIDR: view == NetworkView && s.m.OutsideCIDR(h),
		})
	}
	return apitypes.Map{ViewID: view, Items: items}, nil
}

// Snapshot returns a deep copy of the model, e.g. for computing visibility
// or counts on the client side of a test.
func (s *Service) Snapshot() *Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := NewModel()
	for id, h := range s.m.Hosts {
		hc := *h
		c.Hosts[id] = &hc
	}
	for id, g := range s.m.Groups {
		gc := *g
		c.Groups[id] = &gc
	}
	for id, v := range s.m.Views {
		vc := *v
		c.Views[id] = &vc
	}
	for vid, ps := range s.m.Placements {
		c.Placements[vid] = map[int64]*Placement{}
		for hid, p := range ps {
			pc := *p
			c.Placements[vid][hid] = &pc
		}
	}
	return c
}

// ---- host writes ----

// CreateHost adds a host (SPEC-101, SPEC-107).
func (s *Service) CreateHost(ctx context.Context, in apitypes.HostCreate) (apitypes.Host, error) {
	ip, err := ParseIP(in.IP)
	if err != nil {
		return apitypes.Host{}, err
	}
	h := Host{
		IP: ip, Name: strings.TrimSpace(in.Name), Description: in.Description, Note: in.Note,
		Monitored: true, Status: apitypes.StatusUnknown,
	}
	if in.Monitored != nil {
		h.Monitored = *in.Monitored
	}
	if err := checkHostText(&h); err != nil {
		return apitypes.Host{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkIPFree(ip, 0); err != nil {
		return apitypes.Host{}, err
	}
	if in.ParentID != nil {
		if err := s.checkParent(*in.ParentID, NetworkView); err != nil {
			return apitypes.Host{}, err
		}
		h.ParentID = *in.ParentID
	} else if g := s.m.BestNetworkFor(ip); g != nil {
		h.ParentID = g.ID
	}
	if err := s.placeNew(&h.X, &h.Y, in.X, in.Y, NetworkView, h.ParentID); err != nil {
		return apitypes.Host{}, err
	}
	h.ID = s.nextHost
	h.CreatedAt = s.stamp()
	h.UpdatedAt = h.CreatedAt
	if err := s.repo.Apply(ctx, Changes{Hosts: []Host{h}, NextHostID: h.ID + 1}); err != nil {
		return apitypes.Host{}, err
	}
	before := s.m.AggregateAll()
	s.nextHost++
	s.m.Hosts[h.ID] = &h
	v := s.hostView(&h)
	s.notify.Publish(apitypes.EventHostCreated, v)
	s.emitGroupStatus(before)
	return v, nil
}

// UpdateHost applies a PATCH to a host.
func (s *Service) UpdateHost(ctx context.Context, id int64, p apitypes.HostPatch) (apitypes.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m.Hosts[id]
	if cur == nil {
		return apitypes.Host{}, notFoundf("host %d not found", id)
	}
	h := *cur
	ipChanged := false
	if p.IP.Set {
		ip, err := ParseIP(p.IP.Value)
		if err != nil {
			return apitypes.Host{}, err
		}
		if err := s.checkIPFree(ip, id); err != nil {
			return apitypes.Host{}, err
		}
		if ip != h.IP {
			// The old results belong to another address (SPEC-308).
			ipChanged = true
			h.IP = ip
			h.RTTms, h.LossPct = nil, nil
			h.PTRName = "" // looked up again for the new address (SPEC-401)
			if h.Status != apitypes.StatusUnknown {
				h.Status = apitypes.StatusUnknown
				h.LastChange = s.stamp()
			}
		}
	}
	setIf(&h.Name, p.Name, strings.TrimSpace)
	setIf(&h.Description, p.Description, nil)
	setIf(&h.Note, p.Note, nil)
	if p.Monitored.Set {
		h.Monitored = p.Monitored.Value
		if h.Monitored != cur.Monitored {
			// SPEC-603: unmonitored hosts are never stale; a host whose
			// monitoring is turned back on is stale until its next round.
			h.Stale = h.Monitored
		}
	}
	if p.ParentID.Set {
		parent := Root
		if p.ParentID.Value != nil {
			parent = *p.ParentID.Value
			if err := s.checkParent(parent, NetworkView); err != nil {
				return apitypes.Host{}, err
			}
		}
		if parent != h.ParentID && !p.X.Set && !p.Y.Set {
			// A new parent without a position: next to it (SPEC-508).
			h.X, h.Y = s.m.In(NetworkView).FreePosition(parent)
		}
		h.ParentID = parent
	}
	if p.X.Set {
		h.X = p.X.Value
	}
	if p.Y.Set {
		h.Y = p.Y.Value
	}
	if err := checkHostText(&h); err != nil {
		return apitypes.Host{}, err
	}
	if err := checkPos(h.X, h.Y); err != nil {
		return apitypes.Host{}, err
	}
	h.UpdatedAt = s.stamp()
	if err := s.repo.Apply(ctx, Changes{Hosts: []Host{h}}); err != nil {
		return apitypes.Host{}, err
	}
	before := s.m.AggregateAll()
	*cur = h
	v := s.hostView(cur)
	s.notify.Publish(apitypes.EventHostUpdated, v)
	if ipChanged {
		s.notify.Publish(apitypes.EventHostStatus, v)
	}
	s.emitGroupStatus(before)
	return v, nil
}

// DeleteHost removes a host.
func (s *Service) DeleteHost(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m.Hosts[id] == nil {
		return notFoundf("host %d not found", id)
	}
	if err := s.repo.Apply(ctx, Changes{DeleteHosts: []int64{id}, DeletePlacements: s.placementsOf([]int64{id})}); err != nil {
		return err
	}
	before := s.m.AggregateAll()
	s.dropHost(id)
	s.notify.Publish(apitypes.EventHostDeleted, apitypes.DeletedEvent{ID: id})
	s.emitGroupStatus(before)
	return nil
}

// ---- group writes ----

// CreateGroup adds a group (SPEC-102, SPEC-112..114).
func (s *Service) CreateGroup(ctx context.Context, in apitypes.GroupCreate) (apitypes.Group, error) {
	cidr, err := ParseCIDR(in.CIDR)
	if err != nil {
		return apitypes.Group{}, err
	}
	g := Group{
		Name: strings.TrimSpace(in.Name), Description: in.Description, Note: in.Note,
		CIDR: cidr, Collapsed: true, Labels: in.Labels,
	}
	if g.Name == "" && cidr.IsValid() {
		g.Name = cidr.String()
	}
	if in.Collapsed != nil {
		g.Collapsed = *in.Collapsed
	}
	if err := checkGroupText(&g); err != nil {
		return apitypes.Group{}, err
	}

	g.ViewID = NetworkView
	if in.ViewID != nil {
		g.ViewID = *in.ViewID
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m.Views[g.ViewID] == nil {
		return apitypes.Group{}, invalidf("view %d does not exist", g.ViewID)
	}
	if cidr.IsValid() && g.ViewID != NetworkView {
		return apitypes.Group{}, invalidf("network groups (with a CIDR) exist only in the network view")
	}
	if err := s.checkCIDRFree(cidr, 0); err != nil {
		return apitypes.Group{}, err
	}
	if in.ParentID != nil {
		if err := s.checkParent(*in.ParentID, g.ViewID); err != nil {
			return apitypes.Group{}, err
		}
		g.ParentID = *in.ParentID
	}
	if err := s.placeNew(&g.X, &g.Y, in.X, in.Y, g.ViewID, g.ParentID); err != nil {
		return apitypes.Group{}, err
	}
	g.ID = s.nextGroup
	// Check nesting with the group tentatively in the model.
	s.m.Groups[g.ID] = &g
	if err := s.m.CheckNesting(g.ID); err != nil {
		delete(s.m.Groups, g.ID)
		return apitypes.Group{}, err
	}
	g.CreatedAt = s.stamp()
	g.UpdatedAt = g.CreatedAt
	if err := s.repo.Apply(ctx, Changes{Groups: []Group{g}, NextGroupID: g.ID + 1}); err != nil {
		delete(s.m.Groups, g.ID)
		return apitypes.Group{}, err
	}
	s.nextGroup++
	v := groupView(&g, Counts{})
	s.notify.Publish(apitypes.EventGroupCreated, v)
	return v, nil
}

// UpdateGroup applies a PATCH to a group (SPEC-103, SPEC-113, SPEC-114).
func (s *Service) UpdateGroup(ctx context.Context, id int64, p apitypes.GroupPatch) (apitypes.Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m.Groups[id]
	if cur == nil {
		return apitypes.Group{}, notFoundf("group %d not found", id)
	}
	g := *cur
	setIf(&g.Name, p.Name, strings.TrimSpace)
	setIf(&g.Description, p.Description, nil)
	setIf(&g.Note, p.Note, nil)
	if p.CIDR.Set {
		cidr, err := ParseCIDR(p.CIDR.Value)
		if err != nil {
			return apitypes.Group{}, err
		}
		if cidr.IsValid() && g.ViewID != NetworkView {
			return apitypes.Group{}, invalidf("network groups (with a CIDR) exist only in the network view")
		}
		if err := s.checkCIDRFree(cidr, id); err != nil {
			return apitypes.Group{}, err
		}
		g.CIDR = cidr
	}
	if p.Collapsed.Set {
		g.Collapsed = p.Collapsed.Value
	}
	if p.Labels.Set {
		g.Labels = p.Labels.Value
	}
	if p.ParentID.Set {
		parent := Root
		if p.ParentID.Value != nil {
			parent = *p.ParentID.Value
			if err := s.checkParent(parent, g.ViewID); err != nil {
				return apitypes.Group{}, err
			}
		}
		if err := s.m.CheckMove(id, parent); err != nil {
			return apitypes.Group{}, err
		}
		if parent != g.ParentID && !p.X.Set && !p.Y.Set {
			// A new parent without a position: next to it, with the
			// subtree moving along (SPEC-508).
			g.X, g.Y = s.m.In(g.ViewID).FreePosition(parent)
		}
		g.ParentID = parent
	}
	if p.X.Set {
		g.X = p.X.Value
	}
	if p.Y.Set {
		g.Y = p.Y.Value
	}
	if g.Name == "" {
		return apitypes.Group{}, invalidf("group name must not be empty")
	}
	if err := checkGroupText(&g); err != nil {
		return apitypes.Group{}, err
	}
	if err := checkPos(g.X, g.Y); err != nil {
		return apitypes.Group{}, err
	}
	before := s.m.AggregateAll()
	old := *cur
	*cur = g
	if err := s.m.CheckNesting(id); err != nil {
		*cur = old
		return apitypes.Group{}, err
	}
	cur.UpdatedAt = s.stamp()
	// Only a new parent takes the contents along (SPEC-508); a position
	// change alone moves just the group (crosscheck 2026-11-11-03 D1).
	var dx, dy float64
	if cur.ParentID != old.ParentID {
		dx, dy = cur.X-old.X, cur.Y-old.Y
	}
	ch := s.subtreeShift(cur, dx, dy, cur.UpdatedAt)
	ch.Groups = append([]Group{*cur}, ch.Groups...)
	if err := s.repo.Apply(ctx, ch); err != nil {
		*cur = old
		return apitypes.Group{}, err
	}
	s.applyShift(ch, cur.ViewID)
	v := groupView(cur, s.m.In(cur.ViewID).Aggregate()[id])
	s.notify.Publish(apitypes.EventGroupUpdated, v)
	if len(ch.Groups) > 1 || len(ch.Hosts) > 0 || len(ch.Placements) > 0 {
		s.notify.Publish(apitypes.EventGroupMoved, apitypes.GroupMovedEvent{ID: id, ViewID: cur.ViewID, DX: dx, DY: dy})
	}
	s.emitGroupStatus(before)
	return v, nil
}

// MoveGroup moves a group and everything in it by (dx, dy) in one
// transaction (SPEC-507). It emits group.updated for the group and one
// group.moved for its contents.
func (s *Service) MoveGroup(ctx context.Context, id int64, dx, dy float64) (apitypes.Group, error) {
	return s.moveGroup(ctx, id, dx, dy, false)
}

// MoveGroupContents moves everything inside a group by (dx, dy), but not
// the group itself (v0.29). It emits only group.moved, and nothing when
// the group is empty.
func (s *Service) MoveGroupContents(ctx context.Context, id int64, dx, dy float64) (apitypes.Group, error) {
	return s.moveGroup(ctx, id, dx, dy, true)
}

func (s *Service) moveGroup(ctx context.Context, id int64, dx, dy float64, contentsOnly bool) (apitypes.Group, error) {
	if err := checkPos(dx, dy); err != nil {
		return apitypes.Group{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m.Groups[id]
	if cur == nil {
		return apitypes.Group{}, notFoundf("group %d not found", id)
	}
	g := *cur
	now := s.stamp()
	if !contentsOnly {
		g.X, g.Y, g.UpdatedAt = g.X+dx, g.Y+dy, now
		if err := checkPos(g.X, g.Y); err != nil {
			return apitypes.Group{}, err
		}
	}
	ch := s.subtreeShift(cur, dx, dy, now)
	contents := len(ch.Groups) > 0 || len(ch.Hosts) > 0 || len(ch.Placements) > 0
	if !contentsOnly {
		ch.Groups = append([]Group{g}, ch.Groups...)
	}
	if !contents && contentsOnly {
		return groupView(cur, s.m.In(cur.ViewID).Aggregate()[id]), nil
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return apitypes.Group{}, err
	}
	s.applyShift(ch, g.ViewID)
	v := groupView(cur, s.m.In(cur.ViewID).Aggregate()[id])
	if !contentsOnly {
		s.notify.Publish(apitypes.EventGroupUpdated, v)
	}
	if contents { // only with contents (C6)
		s.notify.Publish(apitypes.EventGroupMoved, apitypes.GroupMovedEvent{ID: id, ViewID: cur.ViewID, DX: dx, DY: dy})
	}
	return v, nil
}

// subtreeShift returns the changes that move everything inside g (in its
// view, not g itself) by (dx, dy): subgroups, and hosts (network view) or
// placements (custom view). Caller holds the lock.
func (s *Service) subtreeShift(g *Group, dx, dy float64, now time.Time) Changes {
	var ch Changes
	if dx == 0 && dy == 0 {
		return ch
	}
	vm := s.m.In(g.ViewID)
	for _, id := range sortedKeys(vm.Groups) {
		if id != g.ID && vm.IsInSubtree(id, g.ID) {
			c := *s.m.Groups[id]
			c.X, c.Y, c.UpdatedAt = c.X+dx, c.Y+dy, now
			ch.Groups = append(ch.Groups, c)
		}
	}
	for _, hid := range sortedKeys(vm.Hosts) {
		if h := vm.Hosts[hid]; h.ParentID == Root || !vm.IsInSubtree(h.ParentID, g.ID) {
			continue
		}
		if g.ViewID == NetworkView {
			c := *s.m.Hosts[hid]
			c.X, c.Y, c.UpdatedAt = c.X+dx, c.Y+dy, now
			ch.Hosts = append(ch.Hosts, c)
		} else {
			p := *s.m.Placements[g.ViewID][hid]
			p.X, p.Y = p.X+dx, p.Y+dy
			ch.Placements = append(ch.Placements, p)
		}
	}
	return ch
}

// applyShift puts saved changes from subtreeShift (and the group) into
// the model.
func (s *Service) applyShift(ch Changes, view int64) {
	for _, g := range ch.Groups {
		*s.m.Groups[g.ID] = g
	}
	for _, h := range ch.Hosts {
		*s.m.Hosts[h.ID] = h
	}
	for _, p := range ch.Placements {
		*s.m.Placements[view][p.HostID] = p
	}
}

// DeleteGroup removes a group (SPEC-104, SPEC-120). mode is "",
// "reparent" or "cascade"; a non-empty group needs a mode. In a custom
// view, cascade removes placements, never hosts.
func (s *Service) DeleteGroup(ctx context.Context, id int64, mode string) error {
	switch mode {
	case "", apitypes.ChildrenReparent, apitypes.ChildrenCascade:
	default:
		return invalidf("children must be %q or %q", apitypes.ChildrenReparent, apitypes.ChildrenCascade)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.m.Groups[id]
	if g == nil {
		return notFoundf("group %d not found", id)
	}
	view := g.ViewID
	custom := view != NetworkView
	vm := s.m.In(view)
	ch := Changes{DeleteGroups: []int64{id}}
	if vm.HasChildren(id) {
		switch mode {
		case "":
			return conflictf("group %d is not empty; use children=%s or children=%s",
				id, apitypes.ChildrenReparent, apitypes.ChildrenCascade)
		case apitypes.ChildrenReparent:
			// Nesting stays valid: CIDR containment is transitive.
			now := s.stamp()
			for _, hid := range sortedKeys(vm.Hosts) {
				if vm.Hosts[hid].ParentID != id {
					continue
				}
				if custom {
					p := *s.m.Placements[view][hid]
					p.ParentID = g.ParentID
					ch.Placements = append(ch.Placements, p)
				} else {
					c := *s.m.Hosts[hid]
					c.ParentID, c.UpdatedAt = g.ParentID, now
					ch.Hosts = append(ch.Hosts, c)
				}
			}
			for _, cid := range sortedKeys(vm.Groups) {
				if c := vm.Groups[cid]; c.ParentID == id {
					cc := *c
					cc.ParentID, cc.UpdatedAt = g.ParentID, now
					ch.Groups = append(ch.Groups, cc)
				}
			}
		case apitypes.ChildrenCascade:
			ch.DeleteGroups = nil
			for _, cid := range sortedKeys(vm.Groups) {
				if vm.IsInSubtree(cid, id) {
					ch.DeleteGroups = append(ch.DeleteGroups, cid)
				}
			}
			for _, hid := range sortedKeys(vm.Hosts) {
				if h := vm.Hosts[hid]; h.ParentID == Root || !vm.IsInSubtree(h.ParentID, id) {
					continue
				}
				if custom {
					ch.DeletePlacements = append(ch.DeletePlacements, PlacementKey{view, hid})
				} else {
					ch.DeleteHosts = append(ch.DeleteHosts, hid)
				}
			}
			if !custom {
				ch.DeletePlacements = s.placementsOf(ch.DeleteHosts)
			}
		}
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return err
	}
	before := s.m.AggregateAll()
	for _, h := range ch.Hosts {
		*s.m.Hosts[h.ID] = h
	}
	for _, c := range ch.Groups {
		*s.m.Groups[c.ID] = c
	}
	for _, p := range ch.Placements {
		*s.m.Placements[p.ViewID][p.HostID] = p
	}
	for _, hid := range ch.DeleteHosts {
		s.dropHost(hid)
	}
	for _, k := range ch.DeletePlacements {
		s.unplace(k)
	}
	for _, gid := range ch.DeleteGroups {
		delete(s.m.Groups, gid)
	}
	// Events: moved children first, then one deleted event per item.
	// Placements of deleted hosts go with host.deleted (no own event).
	after := s.m.In(view).Aggregate()
	for _, h := range ch.Hosts {
		s.notify.Publish(apitypes.EventHostUpdated, s.hostView(s.m.Hosts[h.ID]))
	}
	for _, p := range ch.Placements {
		s.notify.Publish(apitypes.EventPlacementUpdated, placementView(&p))
	}
	for _, c := range ch.Groups {
		s.notify.Publish(apitypes.EventGroupUpdated, groupView(s.m.Groups[c.ID], after[c.ID]))
	}
	for _, hid := range ch.DeleteHosts {
		s.notify.Publish(apitypes.EventHostDeleted, apitypes.DeletedEvent{ID: hid})
	}
	if custom {
		for _, k := range ch.DeletePlacements {
			s.notify.Publish(apitypes.EventPlacementDeleted, apitypes.PlacementDeletedEvent{ViewID: k.ViewID, HostID: k.HostID})
		}
	}
	for _, gid := range ch.DeleteGroups {
		s.notify.Publish(apitypes.EventGroupDeleted, apitypes.DeletedEvent{ID: gid})
	}
	s.emitGroupStatus(before)
	return nil
}

// ---- helpers (caller holds the lock) ----

func (s *Service) checkIPFree(ip netip.Addr, self int64) error {
	for _, h := range s.m.Hosts {
		if h.ID != self && h.IP == ip {
			return conflictf("IP address %s already belongs to host %d", ip, h.ID)
		}
	}
	return nil
}

func (s *Service) checkCIDRFree(cidr netip.Prefix, self int64) error {
	if !cidr.IsValid() {
		return nil
	}
	for _, g := range s.m.Groups {
		if g.ID != self && g.CIDR == cidr {
			return conflictf("CIDR %s already belongs to group %d", cidr, g.ID)
		}
	}
	return nil
}

// checkParent verifies that group id exists in view (SPEC-118, SPEC-119).
func (s *Service) checkParent(id, view int64) error {
	g := s.m.Groups[id]
	switch {
	case g == nil:
		return invalidf("parent group %d does not exist", id)
	case g.ViewID != view:
		return invalidf("parent group %d is in view %d, not in view %d", id, g.ViewID, view)
	}
	return nil
}

// placementsOf returns the custom-view placements of the hosts.
func (s *Service) placementsOf(hosts []int64) []PlacementKey {
	var out []PlacementKey
	for _, vid := range sortedKeys(s.m.Placements) {
		for _, hid := range hosts {
			if s.m.Placements[vid][hid] != nil {
				out = append(out, PlacementKey{vid, hid})
			}
		}
	}
	return out
}

// dropHost removes a host and its placements from the model.
func (s *Service) dropHost(id int64) {
	delete(s.m.Hosts, id)
	for vid, ps := range s.m.Placements {
		delete(ps, id)
		if len(ps) == 0 {
			delete(s.m.Placements, vid)
		}
	}
}

func (s *Service) placeNew(x, y *float64, inX, inY *float64, view, parent int64) error {
	if inX != nil && inY != nil {
		*x, *y = *inX, *inY
		return checkPos(*x, *y)
	}
	if (inX == nil) != (inY == nil) {
		return invalidf("give both x and y, or neither")
	}
	*x, *y = s.m.In(view).FreePosition(parent)
	return nil
}

func (s *Service) hostView(h *Host) apitypes.Host {
	v := apitypes.Host{
		ID: h.ID, IP: h.IP.String(), PTRName: h.PTRName, Name: h.Name,
		DisplayName: h.DisplayName(), Description: h.Description, Note: h.Note,
		ParentID: parentPtr(h.ParentID), Monitored: h.Monitored, Status: h.Status,
		RTTms: h.RTTms, LossPct: h.LossPct, Stale: h.Stale, OutsideCIDR: s.m.OutsideCIDR(h),
		X: h.X, Y: h.Y, CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
		Views: s.hostViews(h.ID),
	}
	if !h.LastSeen.IsZero() {
		t := h.LastSeen
		v.LastSeen = &t
	}
	if !h.LastChange.IsZero() {
		t := h.LastChange
		v.LastChange = &t
	}
	return v
}

// hostViews lists the views a host is placed in: the network view, then
// the custom views in tab order (SPEC-509). Caller holds the lock.
func (s *Service) hostViews(id int64) []int64 {
	out := []int64{NetworkView}
	var custom []*View
	for vid, ps := range s.m.Placements {
		if ps[id] != nil && s.m.Views[vid] != nil {
			custom = append(custom, s.m.Views[vid])
		}
	}
	slices.SortFunc(custom, func(a, b *View) int { return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.ID, b.ID)) })
	for _, v := range custom {
		out = append(out, v.ID)
	}
	return out
}

func groupView(g *Group, c Counts) apitypes.Group {
	v := apitypes.Group{
		ID: g.ID, Name: g.Name, Description: g.Description, Note: g.Note,
		ViewID: g.ViewID, ParentID: parentPtr(g.ParentID), Collapsed: g.Collapsed, Labels: g.Labels, Status: c.Status(),
		X: g.X, Y: g.Y, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
	if g.IsNetwork() {
		v.CIDR = g.CIDR.String()
	}
	return v
}

func checkHostText(h *Host) error {
	if err := checkText("name", h.Name, MaxNameBytes); err != nil {
		return err
	}
	if err := checkText("description", h.Description, MaxDescriptionBytes); err != nil {
		return err
	}
	return checkText("note", h.Note, MaxNoteBytes)
}

func checkGroupText(g *Group) error {
	if g.Name == "" {
		return invalidf("group needs a name or a CIDR")
	}
	if err := checkText("name", g.Name, MaxNameBytes); err != nil {
		return err
	}
	if err := checkText("description", g.Description, MaxDescriptionBytes); err != nil {
		return err
	}
	if !apitypes.ValidLabels(g.Labels) {
		return invalidf("labels must be name, ip, both or empty (as the parent group), not %q", g.Labels)
	}
	return checkText("note", g.Note, MaxNoteBytes)
}

func setIf(dst *string, o apitypes.Opt[string], f func(string) string) {
	if !o.Set {
		return
	}
	if f != nil {
		*dst = f(o.Value)
	} else {
		*dst = o.Value
	}
}

func parentPtr(id int64) *int64 {
	if id == Root {
		return nil
	}
	return &id
}

func sortedKeys[V any](m map[int64]V) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
