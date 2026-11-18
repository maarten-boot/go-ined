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
	SetSNMPValues(id int64, ip netip.Addr, a inventory.SNMPAgent, v apitypes.SNMPValues) bool
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

	mu   sync.Mutex
	busy map[int64]bool // hosts being checked by Run
	lost map[int64]bool // hosts whose agent stopped answering: found again
}

// New returns a manager. log may be nil.
func New(inv Inventory, cfg Config, log *slog.Logger) *Manager {
	cfg.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Manager{inv: inv, cfg: cfg, log: log, busy: map[int64]bool{}, lost: map[int64]bool{}}
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
	ok, err := m.find(ctx, t)
	if err == nil && !ok {
		err = inventory.Errorf(inventory.Conflict, "host %d was changed or deleted during the check; try again", id)
	}
	if err != nil {
		return apitypes.Host{}, err
	}
	return m.inv.Host(id)
}

// find tries every community with v2c, then v1, saves the result and, with
// an agent, reads the values. ok = false: the host changed meanwhile.
func (m *Manager) find(ctx context.Context, t inventory.SNMPTarget) (ok bool, err error) {
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
	a.CheckedAt = m.cfg.Now()
	_, ok, err = m.inv.SetSNMP(ctx, t.ID, t.IP, a)
	if err != nil || !ok {
		return ok, err
	}
	m.mu.Lock()
	delete(m.lost, t.ID)
	m.mu.Unlock()
	switch {
	case a.Version != "" && (a.Version != t.Agent.Version || a.Community != t.Agent.Community):
		m.log.Info("SNMP found on " + where)
	case a.Version == "" && t.Agent.Version != "":
		m.log.Info("SNMP agent no longer answers", "host", t.ID, "ip", t.IP)
	case a.Version == "":
		m.log.Debug("no SNMP agent answered", "host", t.ID, "ip", t.IP)
	}
	if a.Version != "" {
		t.Agent = a
		m.read(ctx, t)
	}
	return true, nil
}

// read reads the values with the host's agent; no answer marks the agent
// lost, so the next round tries every community again.
func (m *Manager) read(ctx context.Context, t inventory.SNMPTarget) {
	v, err := m.cfg.Client.Values(ctx, t.IP, t.Agent.Community, t.Agent.Version)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		m.mu.Lock()
		m.lost[t.ID] = true
		m.mu.Unlock()
		m.log.Debug("SNMP values not read", "host", t.ID, "ip", t.IP, "err", err)
		return
	}
	v.At = m.cfg.Now()
	m.inv.SetSNMPValues(t.ID, t.IP, t.Agent, v)
}

// work is what Run should do for t now: find the agent, read values, or
// nothing.
func (m *Manager) work(t inventory.SNMPTarget, now time.Time) func(context.Context) {
	if !t.Monitored || !m.allowed(t.IP) {
		return nil
	}
	m.mu.Lock()
	lost := m.lost[t.ID]
	m.mu.Unlock()
	a := t.Agent
	switch {
	case a.CheckedAt.IsZero(), lost, a.Version == "" && now.Sub(a.CheckedAt) >= m.cfg.Rescan:
		return func(ctx context.Context) {
			if _, err := m.find(ctx, t); err != nil && ctx.Err() == nil {
				m.log.Error("SNMP check failed", "host", t.ID, "err", err)
			}
		}
	case a.Version != "" && (t.ReadAt.IsZero() || now.Sub(t.ReadAt) >= m.cfg.Interval):
		return func(ctx context.Context) { m.read(ctx, t) }
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
	targets:
		for _, t := range m.inv.SNMPTargets() {
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
