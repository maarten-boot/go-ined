package inventory

import (
	"context"
	"net/netip"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// NetworkGroupByCIDR returns the network group with exactly this CIDR.
func (s *Service) NetworkGroupByCIDR(p netip.Prefix) (apitypes.Group, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range sortedKeys(s.m.Groups) {
		if g := s.m.Groups[id]; g.CIDR == p {
			return groupView(g, s.m.In(NetworkView).Aggregate()[id]), true
		}
	}
	return apitypes.Group{}, false
}

// Found is an address that answered a discovery probe (SPEC-204).
type Found struct {
	IP      netip.Addr
	RTTms   float64
	LossPct float64
	At      time.Time // arrival of the reply
}

// AddDiscovered adds the found addresses that aren't in the inventory yet,
// in one transaction (SPEC-204). Each goes into the most specific network
// group inside group's subtree that contains it; if group no longer exists,
// into the most specific network group anywhere, or the root. Existing IPs
// are neither duplicated nor moved. It returns the number of hosts added.
func (s *Service) AddDiscovered(ctx context.Context, group int64, found []Found) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		ch    Changes
		added []int64
	)
	undo := func() {
		for _, id := range added {
			delete(s.m.Hosts, id)
		}
	}
	before := s.m.AggregateAll()
	next := s.nextHost
	now := s.stamp()
	for _, f := range found {
		ip := f.IP.Unmap()
		if s.checkIPFree(ip, 0) != nil {
			continue
		}
		at := f.At.UTC().Truncate(time.Millisecond)
		rtt, loss := f.RTTms, f.LossPct
		h := Host{
			ID: next, IP: ip, ParentID: s.discoveryParent(ip, group), Monitored: true,
			Status: apitypes.StatusUp, RTTms: &rtt, LossPct: &loss, LastSeen: at, LastChange: at,
			CreatedAt: now, UpdatedAt: now,
		}
		h.X, h.Y = s.m.In(NetworkView).FreePosition(h.ParentID)
		next++
		// Insert at once, so the next free position and the IP check see it.
		s.m.Hosts[h.ID] = &h
		added = append(added, h.ID)
		ch.Hosts = append(ch.Hosts, h)
	}
	if len(added) == 0 {
		return 0, nil
	}
	ch.NextHostID = next
	if err := s.repo.Apply(ctx, ch); err != nil {
		undo()
		return 0, err
	}
	s.nextHost = next
	for _, id := range added {
		s.notify.Publish(apitypes.EventHostCreated, s.hostView(s.m.Hosts[id]))
	}
	s.emitGroupStatus(before)
	return len(added), nil
}

// discoveryParent picks the group for a discovered host (caller holds the
// lock).
func (s *Service) discoveryParent(ip netip.Addr, group int64) int64 {
	if s.m.Groups[group] == nil {
		if g := s.m.BestNetworkFor(ip); g != nil {
			return g.ID
		}
		return Root
	}
	best := s.m.Groups[group]
	for _, g := range s.m.Groups {
		if !g.IsNetwork() || !g.CIDR.Contains(ip) || !s.m.IsInSubtree(g.ID, group) {
			continue
		}
		if !best.IsNetwork() || g.CIDR.Bits() > best.CIDR.Bits() || (g.CIDR.Bits() == best.CIDR.Bits() && g.ID < best.ID) {
			best = g
		}
	}
	return best.ID
}

// DNSTargets returns every host (monitored or not), ordered by ID
// (SPEC-401).
func (s *Service) DNSTargets() []Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Target, 0, len(s.m.Hosts))
	for _, id := range sortedKeys(s.m.Hosts) {
		h := s.m.Hosts[id]
		out = append(out, Target{ID: h.ID, IP: h.IP})
	}
	return out
}

// SetPTRName records the result of a reverse lookup of ip (SPEC-401,
// SPEC-403). It does nothing if the host is gone, has another IP by now, or
// already has this name; otherwise it saves the name and emits
// host.updated.
func (s *Service) SetPTRName(ctx context.Context, id int64, ip netip.Addr, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m.Hosts[id]
	if cur == nil || cur.IP != ip || cur.PTRName == name {
		return nil
	}
	h := *cur
	h.PTRName = name
	if err := s.repo.Apply(ctx, Changes{Hosts: []Host{h}}); err != nil {
		return err
	}
	*cur = h
	s.notify.Publish(apitypes.EventHostUpdated, s.hostView(cur))
	return nil
}
