package inventory

import (
	"context"
	"net/netip"
	"slices"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// SNMPAgent is what was found out about a host's SNMP agent (SPEC-312).
// Version "" with a CheckedAt means no community answered; a zero
// CheckedAt means never checked. Community is saved but never shown.
type SNMPAgent struct {
	Version   string // "v2c", "v1" or ""
	Community string
	System    SNMPSystem
	CheckedAt time.Time
}

// SNMPSystem is the agent's system group.
type SNMPSystem struct {
	Name, Descr, ObjectID, Contact, Location string
}

func snmpView(h *Host) *apitypes.SNMP {
	a := h.SNMP
	if a.CheckedAt.IsZero() {
		return nil
	}
	v := &apitypes.SNMP{
		Version: a.Version, Name: a.System.Name, Descr: a.System.Descr, ObjectID: a.System.ObjectID,
		Contact: a.System.Contact, Location: a.System.Location, CheckedAt: a.CheckedAt,
	}
	if h.SNMPValues != nil && a.Version != "" {
		c := *h.SNMPValues
		c.Storage = slices.Clone(c.Storage)
		v.Values = &c
	}
	return v
}

// SNMPTarget is a host for the SNMP checks.
type SNMPTarget struct {
	ID        int64
	IP        netip.Addr
	Monitored bool
	Agent     SNMPAgent
	ReadAt    time.Time // when values were last read; zero = not since the start
}

func snmpTarget(h *Host) SNMPTarget {
	t := SNMPTarget{ID: h.ID, IP: h.IP, Monitored: h.Monitored, Agent: h.SNMP}
	if h.SNMPValues != nil {
		t.ReadAt = h.SNMPValues.At
	}
	return t
}

// SNMPTargets returns every host, ordered by ID.
func (s *Service) SNMPTargets() []SNMPTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SNMPTarget, 0, len(s.m.Hosts))
	for _, id := range sortedKeys(s.m.Hosts) {
		out = append(out, snmpTarget(s.m.Hosts[id]))
	}
	return out
}

// SNMPTarget returns one host for a check on request, or a not-found
// error.
func (s *Service) SNMPTarget(id int64) (SNMPTarget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.m.Hosts[id]
	if h == nil {
		return SNMPTarget{}, notFoundf("host %d not found", id)
	}
	return snmpTarget(h), nil
}

// SetSNMP records what a check of ip found (SPEC-312): an agent, or none
// (Version ""). A result for a host that is gone or has another IP by now
// is dropped (ok = false). It is saved; host.updated is emitted when the
// version or the system values changed, or on the first check. A new
// version or community drops the values read with the old one.
func (s *Service) SetSNMP(ctx context.Context, id int64, ip netip.Addr, a SNMPAgent) (h apitypes.Host, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m.Hosts[id]
	if cur == nil || cur.IP != ip {
		return apitypes.Host{}, false, nil
	}
	n := *cur
	a.CheckedAt = a.CheckedAt.UTC().Truncate(time.Millisecond)
	if a.Version == "" {
		a.Community, a.System = "", SNMPSystem{}
	}
	n.SNMP = a
	if a.Version != cur.SNMP.Version || a.Community != cur.SNMP.Community {
		n.SNMPValues = nil
	}
	if err := s.repo.Apply(ctx, Changes{Hosts: []Host{n}}); err != nil {
		return apitypes.Host{}, false, err
	}
	changed := cur.SNMP.CheckedAt.IsZero() || cur.SNMP.Version != a.Version || cur.SNMP.System != a.System
	*cur = n
	v := s.hostView(cur)
	if changed {
		s.notify.Publish(apitypes.EventHostUpdated, v)
	}
	return v, true, nil
}

// SetSNMPValues records values read from ip with the host's agent
// (SPEC-313), in memory. Values for a host that is gone, has another IP
// or agent by now, or isn't monitored any more are dropped. snmp.values is
// emitted when they differ from the last in whole percents (or are the
// first); it reports whether it was.
func (s *Service) SetSNMPValues(id int64, ip netip.Addr, a SNMPAgent, v apitypes.SNMPValues) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m.Hosts[id]
	if cur == nil || cur.IP != ip || !cur.Monitored || cur.SNMP.Version == "" ||
		cur.SNMP.Version != a.Version || cur.SNMP.Community != a.Community {
		return false
	}
	v.At = v.At.UTC().Truncate(time.Millisecond)
	v.Storage = slices.Clone(v.Storage)
	if v.Storage == nil {
		v.Storage = []apitypes.SNMPStorage{}
	}
	old := cur.SNMPValues
	cur.SNMPValues = &v
	if old != nil && old.Same(v) {
		return false
	}
	s.notify.Publish(apitypes.EventSNMPValues, apitypes.SNMPValuesEvent{
		HostID: cur.ID, DisplayName: cur.DisplayName(), IP: cur.IP.String(), Values: *snmpView(cur).Values,
	})
	return true
}
