package snmp

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Inventory is the part of inventory.Service the manager uses.
type Inventory interface {
	Host(id int64) (apitypes.Host, error)
	SNMPTargets() []inventory.SNMPTarget
	SNMPTarget(id int64) (inventory.SNMPTarget, error)
	SetSNMP(ctx context.Context, id int64, ip netip.Addr, a inventory.SNMPAgent) (apitypes.Host, bool, error)
	SetSNMPValues(id int64, ip netip.Addr, a inventory.SNMPAgent, v apitypes.SNMPValues, anyHost bool) bool
}

// Config holds the SNMP settings ([snmp] in server.toml). Zero values get
// defaults.
type Config struct {
	Communities []string       // tried in this order (default "public")
	Networks    []netip.Prefix // where communities may be tried; empty = everywhere
	Interval    time.Duration  // between value reads (default 5 min)
	Rescan      time.Duration  // between tries of a host without an agent (default 24 h)
	Concurrency int            // hosts at once (default 20)
	Tick        time.Duration  // how often due hosts are looked for (default 10 s)
	Client      Client         // default UDP{Timeout: 1 s, Retries: 1}
	Now         func() time.Time
}

func (c *Config) defaults() {
	if len(c.Communities) == 0 {
		c.Communities = []string{"public"}
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.Rescan <= 0 {
		c.Rescan = 24 * time.Hour
	}
	if c.Concurrency < 1 {
		c.Concurrency = 20
	}
	if c.Tick <= 0 {
		c.Tick = 10 * time.Second
	}
	if c.Client == nil {
		c.Client = UDP{Timeout: time.Second, Retries: 1}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Manager finds the agents of monitored hosts and reads their values
// (SPEC-312, 313), and checks any host on request (Check).
type Manager struct {
	inv Inventory
	cfg Config
	log *slog.Logger

	mu    sync.Mutex
	busy  map[int64]bool       // hosts being checked by Run
	state map[int64]*hostState // hosts whose reads fail; gone after a good read
}

// hostState counts the failures of a host with an agent (crosscheck
// 2026-11-18-03 C2, C3).
type hostState struct {
	ip    netip.Addr // the address counted for; another one starts again (L3)
	fails int        // value reads in a row without an answer
	tried time.Time  // the last of them
	lost  int        // searches since the agent stopped answering
	next  time.Time  // the next search while lost
}

// maxFails is how many reads in a row may fail before the agent is looked
// for again with every community.
const maxFails = 3

// lostWait is the wait before search n (1, 2, 3) for an agent that
// stopped answering; after that the host waits snmp.rescan and is stored
// as "no answer".
var lostWait = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// New returns a manager. log may be nil.
func New(inv Inventory, cfg Config, log *slog.Logger) *Manager {
	cfg.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Manager{inv: inv, cfg: cfg, log: log, busy: map[int64]bool{}, state: map[int64]*hostState{}}
}

// allowed: communities may be sent to ip (snmp.networks).
func (m *Manager) allowed(ip netip.Addr) bool {
	if len(m.cfg.Networks) == 0 {
		return true
	}
	ip = ip.Unmap()
	for _, p := range m.cfg.Networks {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Check finds the agent of one host now and reads its values (SPEC-312,
// "Check SNMP now"); with no agent it takes up to every community and
// version times the timeout and retries.
func (m *Manager) Check(ctx context.Context, id int64) (apitypes.Host, error) {
	t, err := m.inv.SNMPTarget(id)
	if err != nil {
		return apitypes.Host{}, err
	}
	if !m.allowed(t.IP) {
		return apitypes.Host{}, inventory.Errorf(inventory.Invalid,
			"host %d (%s) is outside snmp.networks: no community is sent to it", id, t.IP)
	}
	ok, err := m.find(ctx, t, true)
	if err == nil && !ok {
		err = inventory.Errorf(inventory.Conflict, "host %d was changed or deleted during the check; try again", id)
	}
	if err != nil {
		return apitypes.Host{}, err
	}
	return m.inv.Host(id)
}

// find tries every community with v2c, then v1, saves the result and, with
// an agent, reads the values. ok = false: the host changed meanwhile. On
// request (now) the result is saved as it is; in the rounds, an agent that
// stopped answering is kept, and looked for again after lostWait, before
// "no answer" is saved (C3).
func (m *Manager) find(ctx context.Context, t inventory.SNMPTarget, now bool) (ok bool, err error) {
	a := inventory.SNMPAgent{}
	n := len(m.cfg.Communities)
	var where string
search:
	for i, c := range m.cfg.Communities {
		for _, v := range []string{V2c, V1} {
			sys, err := m.cfg.Client.System(ctx, t.IP, c, v)
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if err == nil {
				a = inventory.SNMPAgent{Version: v, Community: c, System: sys}
				where = describe(t.IP, v, i, n)
				break search
			}
		}
	}
	if a.Version == "" && t.Agent.Version != "" && !now && m.away(t.ID) {
		m.log.Debug("SNMP agent not answering; tried again later", "host", t.ID, "ip", t.IP)
		return true, nil
	}
	a.CheckedAt = m.cfg.Now()
	_, ok, err = m.inv.SetSNMP(ctx, t.ID, t.IP, a)
	if err != nil || !ok {
		return ok, err
	}
	switch {
	case a.Version != "" && (a.Version != t.Agent.Version || a.Community != t.Agent.Community):
		m.log.Info("SNMP found on " + where)
	case a.Version == "" && t.Agent.Version != "":
		m.log.Info("SNMP agent no longer answers", "host", t.ID, "ip", t.IP)
	case a.Version == "":
		m.log.Debug("no SNMP agent answered", "host", t.ID, "ip", t.IP)
	}
	if a.Version == "" {
		m.forget(t.ID)
		return true, nil
	}
	t.Agent = a
	if !m.read(ctx, t, now) && !now && m.lost(t.ID) {
		// Found, but its values still can't be read: wait before the
		// next search; once the waits are used up, snmp.rescan
		// (crosscheck 2026-11-18-03 C2; 2026-11-18-04 C1, L1).
		if !m.away(t.ID) {
			m.holdOff(t.ID)
		}
	}
	return true, nil
}

// lost reports whether a host's agent counts as lost: maxFails reads in a
// row without an answer.
func (m *Manager) lost(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state[id]
	return st != nil && st.fails >= maxFails
}

// holdOff sets the next search of an agent that answers its system group
// but not its values, once the waits are used up, to snmp.rescan later
// (crosscheck 2026-11-18-04 C1).
func (m *Manager) holdOff(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.state[id]; st != nil {
		st.next = m.cfg.Now().Add(m.cfg.Rescan)
	}
}

// away counts a search for a lost agent and sets when the next one is due;
// false when the waits are used up, so "no answer" is to be saved.
func (m *Manager) away(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state[id]
	if st == nil {
		st = &hostState{fails: maxFails}
		m.state[id] = st
	}
	if st.lost >= len(lostWait) {
		st.next = m.cfg.Now().Add(m.cfg.Rescan) // never in the past (C1)
		return false
	}
	st.next = m.cfg.Now().Add(lostWait[st.lost])
	st.lost++
	return true
}

func (m *Manager) forget(id int64) {
	m.mu.Lock()
	delete(m.state, id)
	m.mu.Unlock()
}

// read reads the values with the host's agent and reports whether it
// answered. maxFails reads in a row without an answer make the agent lost,
// so it is looked for again; until then a failed read is tried again
// after snmp.interval (C2). anyHost keeps values of an unmonitored host
// too (a check on request, L8).
func (m *Manager) read(ctx context.Context, t inventory.SNMPTarget, anyHost bool) bool {
	v, err := m.cfg.Client.Values(ctx, t.IP, t.Agent.Community, t.Agent.Version)
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		m.mu.Lock()
		st := m.state[t.ID]
		if st == nil || st.ip != t.IP {
			st = &hostState{ip: t.IP}
			m.state[t.ID] = st
		}
		st.fails++
		st.tried = m.cfg.Now()
		m.mu.Unlock()
		m.log.Debug("SNMP values not read", "host", t.ID, "ip", t.IP, "err", err)
		return false
	}
	m.forget(t.ID)
	v.At = m.cfg.Now()
	m.inv.SetSNMPValues(t.ID, t.IP, t.Agent, v, anyHost)
	return true
}

// work is what Run should do for t now: find the agent, read values, or
// nothing.
func (m *Manager) work(t inventory.SNMPTarget, now time.Time) func(context.Context) {
	if !t.Monitored || !m.allowed(t.IP) {
		m.forget(t.ID) // counted again from nothing when it is back (L3)
		return nil
	}
	var st hostState
	m.mu.Lock()
	if p := m.state[t.ID]; p != nil {
		if p.ip.IsValid() && p.ip != t.IP {
			delete(m.state, t.ID) // a new address (L3)
		} else {
			st = *p
		}
	}
	m.mu.Unlock()
	a := t.Agent
	find := func(ctx context.Context) {
		if _, err := m.find(ctx, t, false); err != nil && ctx.Err() == nil {
			m.log.Error("SNMP check failed", "host", t.ID, "err", err)
		}
	}
	switch {
	case a.CheckedAt.IsZero(), a.Version == "" && now.Sub(a.CheckedAt) >= m.cfg.Rescan:
		return find
	case a.Version == "":
		return nil
	case st.fails >= maxFails:
		if now.Before(st.next) {
			return nil
		}
		return find
	}
	last := t.ReadAt
	if st.tried.After(last) {
		last = st.tried
	}
	if last.IsZero() || now.Sub(last) >= m.cfg.Interval {
		return func(ctx context.Context) { m.read(ctx, t, false) }
	}
	return nil
}

// Run checks due hosts until ctx is cancelled, at most Concurrency at once,
// then waits for the checks in flight.
func (m *Manager) Run(ctx context.Context) {
	sem := make(chan struct{}, m.cfg.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(m.cfg.Tick)
	defer tick.Stop()
	for {
		now := m.cfg.Now()
		all := m.inv.SNMPTargets()
		m.prune(all)
	targets:
		for _, t := range all {
			m.mu.Lock()
			busy := m.busy[t.ID]
			m.mu.Unlock()
			if busy {
				continue
			}
			job := m.work(t, now)
			if job == nil {
				continue
			}
			select {
			case sem <- struct{}{}:
			default:
				break targets
			}
			m.mu.Lock()
			m.busy[t.ID] = true
			m.mu.Unlock()
			wg.Go(func() {
				defer func() {
					m.mu.Lock()
					delete(m.busy, t.ID)
					m.mu.Unlock()
					<-sem
				}()
				job(ctx)
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// prune forgets the state of hosts that are gone (L3).
func (m *Manager) prune(all []inventory.SNMPTarget) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.state) == 0 {
		return
	}
	have := make(map[int64]bool, len(all))
	for _, t := range all {
		have[t.ID] = true
	}
	for id := range m.state {
		if !have[id] {
			delete(m.state, id)
		}
	}
}
