package inventory

import (
	"context"
	"net/netip"
	"slices"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Target is a host the monitor should ping (SPEC-301, SPEC-306), or whose
// PTR name the DNS refresher looks up (SPEC-401).
type Target struct {
	ID      int64
	IP      netip.Addr
	PTRName string // set by DNSTargets only
}

// MonitorTargets returns every monitored host, ordered by ID. Hosts with
// monitored = false are left out (SPEC-306).
func (s *Service) MonitorTargets() []Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Target, 0, len(s.m.Hosts))
	for _, id := range sortedKeys(s.m.Hosts) {
		if h := s.m.Hosts[id]; h.Monitored {
			out = append(out, Target{ID: h.ID, IP: h.IP})
		}
	}
	return out
}

// Observation is the result of one finished ping round of a host
// (SPEC-302, SPEC-303).
type Observation struct {
	HostID  int64
	IP      netip.Addr // the address that was pinged
	Status  string     // the new status; "" keeps the current one
	RTTms   *float64   // average round-trip time; nil when no reply
	LossPct float64
	Replied bool      // at least one reply
	SeenAt  time.Time // arrival of the last reply: last_seen (SPEC-303); zero = At
	At      time.Time // end of the round: last_change
}

// ApplyObservations records a batch of ping results in one database
// transaction (SPEC-303, SPEC-307, SPEC-603). Results for hosts that were
// deleted, unmonitored or given another IP meanwhile are dropped. It emits
// host.status when a host's status changes or its stale flag clears, and
// group.status for every group whose computed status changes (SPEC-305).
func (s *Service) ApplyObservations(ctx context.Context, obs []Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		ch     Changes
		notify []bool // per entry of ch.Hosts: emit host.status
	)
	idx := map[int64]int{} // host id -> index in ch.Hosts (later results win)
	for _, o := range obs {
		cur := s.m.Hosts[o.HostID]
		if cur == nil || !cur.Monitored || cur.IP != o.IP {
			continue
		}
		h := *cur
		if i, ok := idx[o.HostID]; ok {
			h = ch.Hosts[i]
		}
		at := o.At.UTC().Truncate(time.Millisecond)
		emit := h.Stale
		h.Stale = false
		h.RTTms = o.RTTms
		loss := o.LossPct
		h.LossPct = &loss
		if o.Replied {
			seen := o.SeenAt
			if seen.IsZero() {
				seen = o.At
			}
			h.LastSeen = seen.UTC().Truncate(time.Millisecond)
		}
		if o.Status != "" && o.Status != h.Status {
			h.Status = o.Status
			h.LastChange = at
			emit = true
		}
		if i, ok := idx[o.HostID]; ok {
			ch.Hosts[i] = h
			notify[i] = notify[i] || emit
		} else {
			idx[o.HostID] = len(ch.Hosts)
			ch.Hosts = append(ch.Hosts, h)
			notify = append(notify, emit)
		}
	}
	if len(ch.Hosts) == 0 {
		return nil
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return err
	}
	before := s.m.AggregateAll()
	for i, h := range ch.Hosts {
		cur := s.m.Hosts[h.ID]
		*cur = h
		if notify[i] {
			s.notify.Publish(apitypes.EventHostStatus, s.hostView(cur))
		}
	}
	s.emitGroupStatus(before)
	return nil
}

// ServiceTarget is a host for the service check (SPEC-309).
type ServiceTarget struct {
	ID        int64
	IP        netip.Addr
	Monitored bool
	CheckedAt time.Time // zero = never checked
}

// ServiceTargets returns every host with the time of its last service
// check, ordered by ID.
func (s *Service) ServiceTargets() []ServiceTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServiceTarget, 0, len(s.m.Hosts))
	for _, id := range sortedKeys(s.m.Hosts) {
		h := s.m.Hosts[id]
		out = append(out, ServiceTarget{ID: h.ID, IP: h.IP, Monitored: h.Monitored, CheckedAt: h.ServicesAt})
	}
	return out
}

// ServiceTarget returns one host for a check on request, or a not-found
// error.
func (s *Service) ServiceTarget(id int64) (ServiceTarget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.m.Hosts[id]
	if h == nil {
		return ServiceTarget{}, notFoundf("host %d not found", id)
	}
	return ServiceTarget{ID: h.ID, IP: h.IP, Monitored: h.Monitored, CheckedAt: h.ServicesAt}, nil
}

// SetServices records the result of a service check of ip at time at
// (SPEC-309). A result for a host that is gone or has another IP by now is
// dropped (ok = false). The time is always saved; host.updated is emitted
// only when the list changes or the host had never been checked, so the
// periodic check of an unchanged host sends nothing.
func (s *Service) SetServices(ctx context.Context, id int64, ip netip.Addr, services []string, at time.Time) (h apitypes.Host, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m.Hosts[id]
	if cur == nil || cur.IP != ip {
		return apitypes.Host{}, false, nil
	}
	n := *cur
	n.Services = slices.Clone(services)
	n.ServicesAt = at.UTC().Truncate(time.Millisecond)
	if err := s.repo.Apply(ctx, Changes{Hosts: []Host{n}}); err != nil {
		return apitypes.Host{}, false, err
	}
	changed := cur.ServicesAt.IsZero() || !slices.Equal(cur.Services, n.Services)
	*cur = n
	v := s.hostView(cur)
	if changed {
		s.notify.Publish(apitypes.EventHostUpdated, v)
	}
	return v, true, nil
}
