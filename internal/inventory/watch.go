package inventory

import (
	"context"
	"net/netip"
	"slices"
	"sync/atomic"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// parseWatch turns a PATCH watch list into Watched entries (SPEC-310).
// Services already watched keep their status; new ones start unknown.
// watchGen numbers the watchings of services (Watched.Gen).
var watchGen atomic.Uint64

func parseWatch(names []string, cur []Watched) ([]Watched, error) {
	ps, err := apitypes.ParseWatchList(names)
	if err != nil {
		return nil, invalidf("watch: %v", err)
	}
	out := make([]Watched, 0, len(ps))
	for _, p := range ps {
		w := Watched{Name: p.Name, Port: uint16(p.Port), Status: apitypes.StatusUnknown, Gen: watchGen.Add(1)}
		// A service given again keeps its status, also under another name
		// for the same port ("ssh" and "tcp:22"); a list has one entry per port.
		if i := slices.IndexFunc(cur, func(c Watched) bool { return c.Port == w.Port }); i >= 0 {
			name := w.Name
			w = cur[i]
			w.Name = name
		}
		out = append(out, w)
	}
	return out, nil
}

// resetWatch sets every watched service to unknown, as after an IP change
// or when monitoring is turned off (SPEC-310). It returns the new list and
// the indexes of the entries whose status changed.
func resetWatch(ws []Watched, at time.Time, clearOK bool) ([]Watched, []int) {
	out := slices.Clone(ws)
	var changed []int
	for i := range out {
		if clearOK {
			out[i].LastOK = time.Time{}
		}
		if out[i].Status != apitypes.StatusUnknown {
			out[i].Status, out[i].LastChange = apitypes.StatusUnknown, at
			changed = append(changed, i)
		}
	}
	return out, changed
}

// publishWatch emits service.status for entries idx of h, with the
// host's count of services down after all of them. Caller holds the lock.
func (s *Service) publishWatch(h *Host, idx []int) {
	down := h.ServicesDown()
	for _, i := range idx {
		w := h.Watch[i]
		s.notify.Publish(apitypes.EventServiceStatus, apitypes.ServiceStatusEvent{
			HostID: h.ID, DisplayName: h.DisplayName(), IP: h.IP.String(),
			Name: w.Name, Port: int(w.Port), Status: w.Status, LastChange: timePtr(w.LastChange),
			ServicesDown: down,
		})
	}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func watchView(ws []Watched) []apitypes.WatchedService {
	out := make([]apitypes.WatchedService, len(ws))
	for i, w := range ws {
		out[i] = apitypes.WatchedService{Name: w.Name, Port: int(w.Port), Status: w.Status,
			LastChange: timePtr(w.LastChange), LastOK: timePtr(w.LastOK)}
	}
	return out
}

// ResetAllWatch sets every watched service that isn't unknown to unknown,
// for a server started with services.enabled = false: nothing checks them,
// so an old "down" must not stay (SPEC-310). It returns how many changed.
func (s *Service) ResetAllWatch(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type upd struct {
		h   Host
		idx []int
	}
	var ups []upd
	n := 0
	for _, id := range sortedKeys(s.m.Hosts) {
		cur := s.m.Hosts[id]
		ws, idx := resetWatch(cur.Watch, s.stamp(), false)
		if len(idx) == 0 {
			continue
		}
		h := *cur
		h.Watch = ws
		ups = append(ups, upd{h, idx})
		n += len(idx)
	}
	if len(ups) == 0 {
		return 0, nil
	}
	ch := Changes{Hosts: make([]Host, len(ups))}
	for i, u := range ups {
		ch.Hosts[i] = u.h
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return 0, err
	}
	for _, u := range ups {
		cur := s.m.Hosts[u.h.ID]
		*cur = u.h
		s.publishWatch(cur, u.idx)
	}
	return n, nil
}

// WatchTarget is a monitored host with watched services, for the watch
// checker (SPEC-310).
type WatchTarget struct {
	ID       int64
	IP       netip.Addr
	Services []WatchService
	UpSince  time.Time // when the host's ping status became up; zero when it isn't up
}

// WatchService is a watched service of a WatchTarget, with its Watched.Gen.
type WatchService struct {
	apitypes.ServicePort
	Gen uint64
}

// WatchTargets returns every monitored host that watches services, ordered
// by ID. Unmonitored hosts are left out: their services stay unknown.
func (s *Service) WatchTargets() []WatchTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []WatchTarget
	for _, id := range sortedKeys(s.m.Hosts) {
		h := s.m.Hosts[id]
		if !h.Monitored || len(h.Watch) == 0 {
			continue
		}
		t := WatchTarget{ID: h.ID, IP: h.IP, Services: make([]WatchService, len(h.Watch))}
		if h.Status == apitypes.StatusUp {
			t.UpSince = h.LastChange
		}
		for i, w := range h.Watch {
			t.Services[i] = WatchService{apitypes.ServicePort{Name: w.Name, Port: int(w.Port)}, w.Gen}
		}
		out = append(out, t)
	}
	return out
}

// WatchResult is one check of a watched service (SPEC-310).
type WatchResult struct {
	HostID int64
	IP     netip.Addr // the address that was checked
	Name   string
	Port   int
	Gen    uint64 // the WatchService's; a result for another watching is dropped (0: any, for tests)
	OK     bool   // the port accepted a connection
	Status string // the new status; "" keeps the current one
	At     time.Time
}

// ApplyWatch records check results in one transaction. Results for a host
// that is gone, unmonitored or has another IP by now, or for a service it
// no longer watches, are dropped. service.status is emitted for every
// status change; the host's other fields are not touched, so no
// host.updated.
func (s *Service) ApplyWatch(ctx context.Context, rs []WatchResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	type upd struct {
		h       Host
		changed []int
	}
	var order []int64
	ups := map[int64]*upd{}
	for _, r := range rs {
		cur := s.m.Hosts[r.HostID]
		if cur == nil || !cur.Monitored || cur.IP != r.IP {
			continue
		}
		u := ups[r.HostID]
		if u == nil {
			u = &upd{h: *cur}
			u.h.Watch = slices.Clone(cur.Watch)
			ups[r.HostID] = u
			order = append(order, r.HostID)
		}
		i := slices.IndexFunc(u.h.Watch, func(w Watched) bool {
			return w.Name == r.Name && int(w.Port) == r.Port && (r.Gen == 0 || w.Gen == r.Gen)
		})
		if i < 0 {
			continue
		}
		at := r.At.UTC().Truncate(time.Millisecond)
		w := &u.h.Watch[i]
		if r.OK {
			w.LastOK = at
		}
		if r.Status != "" && r.Status != w.Status {
			w.Status, w.LastChange = r.Status, at
			if !slices.Contains(u.changed, i) {
				u.changed = append(u.changed, i)
			}
		}
	}
	if len(order) == 0 {
		return nil
	}
	ch := Changes{Hosts: make([]Host, 0, len(order))}
	for _, id := range order {
		ch.Hosts = append(ch.Hosts, ups[id].h)
	}
	if err := s.repo.Apply(ctx, ch); err != nil {
		return err
	}
	for _, id := range order {
		cur := s.m.Hosts[id]
		*cur = ups[id].h
		s.publishWatch(cur, ups[id].changed)
	}
	return nil
}
