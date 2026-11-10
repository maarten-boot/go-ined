package inventory

import (
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

// State is everything Load returns. NextHostID and NextGroupID are the
// persisted id sequences (SPEC-116); 0 means unknown.
type State struct {
	Groups      []Group
	Hosts       []Host
	NextHostID  int64
	NextGroupID int64
}

// Changes is one atomic set of writes. Upserts insert or replace by ID.
// NextHostID / NextGroupID, when not 0, update the persisted sequences.
type Changes struct {
	Groups       []Group
	Hosts        []Host
	DeleteGroups []int64
	DeleteHosts  []int64
	NextHostID   int64
	NextGroupID  int64
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
	after := s.m.Aggregate()
	for _, id := range sortedKeys(after) {
		if b, ok := before[id]; ok && b.Status() != after[id].Status() {
			s.notify.Publish(apitypes.EventGroupStatus, apitypes.GroupStatusEvent{ID: id, Status: after[id].Status()})
		}
	}
}

// NewService loads the inventory from repo. Every loaded host is marked
// stale until it has been pinged again (SPEC-603). The tree is checked
// (SPEC-605): items whose parent is missing are moved to the root and
// reported by LoadWarnings; a cycle is an error.
func NewService(ctx context.Context, repo Repository) (*Service, error) {
	st, err := repo.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("inventory: load: %w", err)
	}
	s := &Service{repo: repo, m: NewModel(), nextHost: max(1, st.NextHostID), nextGroup: max(1, st.NextGroupID), now: time.Now, notify: nopNotifier{}}
	for i := range st.Groups {
		g := st.Groups[i]
		s.m.Groups[g.ID] = &g
		s.nextGroup = max(s.nextGroup, g.ID+1)
	}
	for i := range st.Hosts {
		h := st.Hosts[i]
		h.Stale = true
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

// repairOnLoad moves orphans to the root (persisted) and rejects cycles.
func (s *Service) repairOnLoad(ctx context.Context) error {
	var ch Changes
	for _, id := range sortedKeys(s.m.Groups) {
		g := s.m.Groups[id]
		if g.ParentID != Root && s.m.Groups[g.ParentID] == nil {
			s.warnings = append(s.warnings, fmt.Sprintf("group %d: parent group %d does not exist; moved to the root", id, g.ParentID))
			g.ParentID = Root
			ch.Groups = append(ch.Groups, *g)
		}
	}
	for _, id := range sortedKeys(s.m.Hosts) {
		h := s.m.Hosts[id]
		if h.ParentID != Root && s.m.Groups[h.ParentID] == nil {
			s.warnings = append(s.warnings, fmt.Sprintf("host %d: parent group %d does not exist; moved to the root", id, h.ParentID))
			h.ParentID = Root
			ch.Hosts = append(ch.Hosts, *h)
		}
	}
	for _, id := range sortedKeys(s.m.Groups) {
		if s.m.inCycle(id) {
			return fmt.Errorf("inventory: the database is inconsistent: group %d is part of a parent cycle; fix it by hand", id)
		}
	}
	if len(ch.Groups)+len(ch.Hosts) > 0 {
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
	agg := s.m.Aggregate()
	out := make([]apitypes.Group, 0, len(s.m.Groups))
	for _, id := range sortedKeys(s.m.Groups) {
		out = append(out, groupView(s.m.Groups[id], agg[id]))
	}
	return out
}

// Group returns one group.
func (s *Service) Group(id int64) (apitypes.Group, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.m.Groups[id]
	if g == nil {
		return apitypes.Group{}, notFoundf("group %d not found", id)
	}
	return groupView(g, s.m.Aggregate()[id]), nil
}

// Map returns every item for GET /map (SPEC-501): groups first, then hosts,
// each ordered by ID.
func (s *Service) Map() apitypes.Map {
	s.mu.RLock()
	defer s.mu.RUnlock()
	agg := s.m.Aggregate()
	items := make([]apitypes.MapItem, 0, len(s.m.Groups)+len(s.m.Hosts))
	for _, id := range sortedKeys(s.m.Groups) {
		g := s.m.Groups[id]
		it := apitypes.MapItem{
			Type: apitypes.TypeGroup, ID: g.ID, ParentID: parentPtr(g.ParentID),
			X: g.X, Y: g.Y, Name: g.Name, Status: agg[id].Status(),
			HasNote: g.Note != "", Collapsed: g.Collapsed,
		}
		if g.IsNetwork() {
			it.CIDR = g.CIDR.String()
		}
		items = append(items, it)
	}
	for _, id := range sortedKeys(s.m.Hosts) {
		h := s.m.Hosts[id]
		items = append(items, apitypes.MapItem{
			Type: apitypes.TypeHost, ID: h.ID, ParentID: parentPtr(h.ParentID),
			X: h.X, Y: h.Y, Name: h.DisplayName(), Status: h.Status, Stale: h.Stale,
			HasNote: h.Note != "", IP: h.IP.String(), OutsideCIDR: s.m.OutsideCIDR(h),
		})
	}
	return apitypes.Map{Items: items}
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
		if err := s.checkParent(*in.ParentID); err != nil {
			return apitypes.Host{}, err
		}
		h.ParentID = *in.ParentID
	} else if g := s.m.BestNetworkFor(ip); g != nil {
		h.ParentID = g.ID
	}
	if err := s.placeNew(&h.X, &h.Y, in.X, in.Y, h.ParentID); err != nil {
		return apitypes.Host{}, err
	}
	h.ID = s.nextHost
	h.CreatedAt = s.stamp()
	h.UpdatedAt = h.CreatedAt
	if err := s.repo.Apply(ctx, Changes{Hosts: []Host{h}, NextHostID: h.ID + 1}); err != nil {
		return apitypes.Host{}, err
	}
	before := s.m.Aggregate()
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
	if p.IP.Set {
		ip, err := ParseIP(p.IP.Value)
		if err != nil {
			return apitypes.Host{}, err
		}
		if err := s.checkIPFree(ip, id); err != nil {
			return apitypes.Host{}, err
		}
		h.IP = ip
	}
	setIf(&h.Name, p.Name, strings.TrimSpace)
	setIf(&h.Description, p.Description, nil)
	setIf(&h.Note, p.Note, nil)
	if p.Monitored.Set {
		h.Monitored = p.Monitored.Value
	}
	if p.ParentID.Set {
		parent := Root
		if p.ParentID.Value != nil {
			parent = *p.ParentID.Value
			if err := s.checkParent(parent); err != nil {
				return apitypes.Host{}, err
			}
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
	before := s.m.Aggregate()
	*cur = h
	v := s.hostView(cur)
	s.notify.Publish(apitypes.EventHostUpdated, v)
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
	if err := s.repo.Apply(ctx, Changes{DeleteHosts: []int64{id}}); err != nil {
		return err
	}
	before := s.m.Aggregate()
	delete(s.m.Hosts, id)
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
		CIDR: cidr, Collapsed: true,
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

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCIDRFree(cidr, 0); err != nil {
		return apitypes.Group{}, err
	}
	if in.ParentID != nil {
		if err := s.checkParent(*in.ParentID); err != nil {
			return apitypes.Group{}, err
		}
		g.ParentID = *in.ParentID
	}
	if err := s.placeNew(&g.X, &g.Y, in.X, in.Y, g.ParentID); err != nil {
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
	v := groupView(&g, s.m.Aggregate()[g.ID])
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
		if err := s.checkCIDRFree(cidr, id); err != nil {
			return apitypes.Group{}, err
		}
		g.CIDR = cidr
	}
	if p.Collapsed.Set {
		g.Collapsed = p.Collapsed.Value
	}
	if p.ParentID.Set {
		parent := Root
		if p.ParentID.Value != nil {
			parent = *p.ParentID.Value
			if err := s.checkParent(parent); err != nil {
				return apitypes.Group{}, err
			}
		}
		if err := s.m.CheckMove(id, parent); err != nil {
			return apitypes.Group{}, err
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
	before := s.m.Aggregate()
	old := *cur
	*cur = g
	if err := s.m.CheckNesting(id); err != nil {
		*cur = old
		return apitypes.Group{}, err
	}
	cur.UpdatedAt = s.stamp()
	if err := s.repo.Apply(ctx, Changes{Groups: []Group{*cur}}); err != nil {
		*cur = old
		return apitypes.Group{}, err
	}
	v := groupView(cur, s.m.Aggregate()[id])
	s.notify.Publish(apitypes.EventGroupUpdated, v)
	s.emitGroupStatus(before)
	return v, nil
}

// DeleteGroup removes a group (SPEC-104). mode is "", "reparent" or
// "cascade"; a non-empty group needs a mode.
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
	ch := Changes{DeleteGroups: []int64{id}}
	if s.m.HasChildren(id) {
		switch mode {
		case "":
			return conflictf("group %d is not empty; use children=%s or children=%s",
				id, apitypes.ChildrenReparent, apitypes.ChildrenCascade)
		case apitypes.ChildrenReparent:
			// Nesting stays valid: CIDR containment is transitive.
			now := s.stamp()
			for _, hid := range sortedKeys(s.m.Hosts) {
				if h := s.m.Hosts[hid]; h.ParentID == id {
					c := *h
					c.ParentID, c.UpdatedAt = g.ParentID, now
					ch.Hosts = append(ch.Hosts, c)
				}
			}
			for _, cid := range sortedKeys(s.m.Groups) {
				if c := s.m.Groups[cid]; c.ParentID == id {
					cc := *c
					cc.ParentID, cc.UpdatedAt = g.ParentID, now
					ch.Groups = append(ch.Groups, cc)
				}
			}
		case apitypes.ChildrenCascade:
			ch.DeleteGroups = nil
			for _, cid := range sortedKeys(s.m.Groups) {
				if s.m.IsInSubtree(cid, id) {
					ch.DeleteGroups = append(ch.DeleteGroups, cid)
				}
			}
			for _, hid := range sortedKeys(s.m.Hosts) {
				if h := s.m.Hosts[hid]; h.ParentID != Root && s.m.IsInSubtree(h.ParentID, id) {
					ch.DeleteHosts = append(ch.DeleteHosts, hid)
				}
			}
		}
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return err
	}
	before := s.m.Aggregate()
	for _, h := range ch.Hosts {
		*s.m.Hosts[h.ID] = h
	}
	for _, c := range ch.Groups {
		*s.m.Groups[c.ID] = c
	}
	for _, hid := range ch.DeleteHosts {
		delete(s.m.Hosts, hid)
	}
	for _, gid := range ch.DeleteGroups {
		delete(s.m.Groups, gid)
	}
	// Events: moved children first, then one deleted event per item.
	after := s.m.Aggregate()
	for _, h := range ch.Hosts {
		s.notify.Publish(apitypes.EventHostUpdated, s.hostView(s.m.Hosts[h.ID]))
	}
	for _, c := range ch.Groups {
		s.notify.Publish(apitypes.EventGroupUpdated, groupView(s.m.Groups[c.ID], after[c.ID]))
	}
	for _, hid := range ch.DeleteHosts {
		s.notify.Publish(apitypes.EventHostDeleted, apitypes.DeletedEvent{ID: hid})
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

func (s *Service) checkParent(id int64) error {
	if s.m.Groups[id] == nil {
		return invalidf("parent group %d does not exist", id)
	}
	return nil
}

func (s *Service) placeNew(x, y *float64, inX, inY *float64, parent int64) error {
	if inX != nil && inY != nil {
		*x, *y = *inX, *inY
		return checkPos(*x, *y)
	}
	if (inX == nil) != (inY == nil) {
		return invalidf("give both x and y, or neither")
	}
	*x, *y = s.m.FreePosition(parent)
	return nil
}

func (s *Service) hostView(h *Host) apitypes.Host {
	v := apitypes.Host{
		ID: h.ID, IP: h.IP.String(), PTRName: h.PTRName, Name: h.Name,
		DisplayName: h.DisplayName(), Description: h.Description, Note: h.Note,
		ParentID: parentPtr(h.ParentID), Monitored: h.Monitored, Status: h.Status,
		RTTms: h.RTTms, LossPct: h.LossPct, Stale: h.Stale, OutsideCIDR: s.m.OutsideCIDR(h),
		X: h.X, Y: h.Y, CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
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

func groupView(g *Group, c Counts) apitypes.Group {
	v := apitypes.Group{
		ID: g.ID, Name: g.Name, Description: g.Description, Note: g.Note,
		ParentID: parentPtr(g.ParentID), Collapsed: g.Collapsed, Status: c.Status(),
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
