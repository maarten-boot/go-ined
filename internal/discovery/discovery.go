// Package discovery runs discovery jobs: it probes every address of an
// IPv4 range with ICMP echo and adds the responders to the inventory
// (SPEC-201..205).
package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/monitor"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// SecondTry is the delay before the second request to an address that
// didn't answer the first (SPEC-201).
const SecondTry = 200 * time.Millisecond

// DesignHosts is the inventory size the server is designed for (A1). A job
// with a larger range logs a warning (SPEC-203).
const DesignHosts = 5000

// Inventory is the part of inventory.Service that discovery uses.
type Inventory interface {
	Group(id int64) (apitypes.Group, error)
	NetworkGroupByCIDR(p netip.Prefix) (apitypes.Group, bool)
	CreateGroup(ctx context.Context, in apitypes.GroupCreate) (apitypes.Group, error)
	AddDiscovered(ctx context.Context, group int64, found []inventory.Found) (int, error)
}

// Notifier receives discovery.progress and discovery.finished.
type Notifier interface {
	Publish(typ string, data any)
}

// Config holds the discovery settings. Zero values get defaults.
type Config struct {
	MaxPrefix   int           // largest range: smallest prefix length (default 16)
	Rate        int           // addresses per second (default 200)
	Concurrency int           // probes in flight (default 64)
	Timeout     time.Duration // per echo request (monitor.timeout, default 1 s)
	Keep        int           // finished jobs kept (default 100)
	Flush       time.Duration // found hosts are saved this often (default 1 s)
	Progress    time.Duration // at most one progress event per this (default 1 s)
	SecondTry   time.Duration // default SecondTry
	// Ready, if set, is asked before every job: "" when IPv4 ICMP works,
	// else why not. The server uses it to find ICMP again after a fixed
	// sysctl without a restart (SPEC-304, v0.27). Without it, a nil prober
	// means discovery is unavailable.
	Ready func() string
}

func (c *Config) defaults() {
	if c.MaxPrefix == 0 {
		c.MaxPrefix = 16
	}
	if c.Rate < 1 {
		c.Rate = 200
	}
	if c.Concurrency < 1 {
		c.Concurrency = 64
	}
	if c.Timeout <= 0 {
		c.Timeout = time.Second
	}
	if c.Keep < 1 {
		c.Keep = 100
	}
	if c.Flush <= 0 {
		c.Flush = time.Second
	}
	if c.Progress <= 0 {
		c.Progress = time.Second
	}
	if c.SecondTry <= 0 {
		c.SecondTry = SecondTry
	}
}

// Manager starts, tracks and cancels discovery jobs.
type Manager struct {
	inv      Inventory
	prober   monitor.Prober // nil: discovery is unavailable
	whyNot   string         // reason when prober is nil
	notify   Notifier
	cfg      Config
	log      *slog.Logger
	run      string
	baseCtx  context.Context
	stopAll  context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	seq      int
	jobs     map[string]*job
	finished []string // ids of finished jobs, oldest first
	closed   bool
}

type job struct {
	view   apitypes.Discovery
	prefix netip.Prefix
	cancel context.CancelFunc
}

// New returns a manager. prober nil means ICMP is unavailable; whyNot is
// then the reason given to clients (SPEC-201). run is the server run id,
// used in job ids.
func New(inv Inventory, prober monitor.Prober, whyNot string, notify Notifier, run string, cfg Config, log *slog.Logger) *Manager {
	cfg.defaults()
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		inv: inv, prober: prober, whyNot: whyNot, notify: notify, cfg: cfg, log: log, run: run,
		baseCtx: ctx, stopAll: cancel, jobs: map[string]*job{},
	}
}

// Start validates the request, finds or creates the network group and
// starts the job (SPEC-201, SPEC-203).
func (m *Manager) Start(ctx context.Context, in apitypes.DiscoveryCreate) (apitypes.Discovery, error) {
	if m.cfg.Ready != nil {
		if why := m.cfg.Ready(); why != "" {
			return apitypes.Discovery{}, inventory.Errorf(inventory.Unavailable, "discovery is unavailable: %s", why)
		}
	}
	if m.prober == nil {
		return apitypes.Discovery{}, inventory.Errorf(inventory.Unavailable, "discovery is unavailable: %s", m.whyNot)
	}
	prefix, err := m.target(in)
	if err != nil {
		return apitypes.Discovery{}, err
	}
	if prefix.Bits() < m.cfg.MaxPrefix {
		return apitypes.Discovery{}, inventory.Errorf(inventory.Invalid,
			"range %s is too large: the largest allowed is /%d (discovery.max_prefix)", prefix, m.cfg.MaxPrefix)
	}
	// Reserve the range before creating anything, so that a conflict
	// creates no group (SPEC-201, SPEC-205).
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return apitypes.Discovery{}, inventory.Errorf(inventory.Unavailable, "the server is shutting down")
	}
	for _, j := range m.jobs {
		if j.view.State == apitypes.DiscoveryRunning && j.prefix.Overlaps(prefix) {
			m.mu.Unlock()
			return apitypes.Discovery{}, inventory.Errorf(inventory.Conflict,
				"range %s overlaps running discovery %s (%s)", prefix, j.view.ID, j.prefix)
		}
	}
	m.seq++
	id := m.run + "-" + strconv.Itoa(m.seq)
	jctx, cancel := context.WithCancel(m.baseCtx)
	first, n := Addresses(prefix)
	j := &job{prefix: prefix, cancel: cancel, view: apitypes.Discovery{
		ID: id, CIDR: prefix.String(), State: apitypes.DiscoveryRunning, Total: n, StartedAt: time.Now().UTC().Truncate(time.Millisecond),
	}}
	m.jobs[id] = j
	m.wg.Add(1) // under the lock, so Close can't miss this job
	m.mu.Unlock()
	if n > DesignHosts {
		m.log.Warn("large discovery range: the server is designed for about 5,000 hosts",
			"cidr", prefix.String(), "addresses", n)
	}

	group, err := m.group(ctx, in, prefix)
	if err != nil {
		cancel()
		m.mu.Lock()
		delete(m.jobs, id)
		m.mu.Unlock()
		m.wg.Done()
		return apitypes.Discovery{}, err
	}
	m.mu.Lock()
	j.view.GroupID = group
	v := j.view
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		m.runJob(jctx, j, first, n)
	}()
	return v, nil
}

// target returns the range to probe.
func (m *Manager) target(in apitypes.DiscoveryCreate) (netip.Prefix, error) {
	switch {
	case (in.CIDR == "") == (in.GroupID == nil):
		return netip.Prefix{}, inventory.Errorf(inventory.Invalid, "give either cidr or group_id")
	case in.GroupID != nil:
		g, err := m.inv.Group(*in.GroupID)
		if err != nil {
			return netip.Prefix{}, err
		}
		if g.CIDR == "" {
			return netip.Prefix{}, inventory.Errorf(inventory.Invalid, "group %d is not a network group", g.ID)
		}
		p := netip.MustParsePrefix(g.CIDR)
		if !p.Addr().Is4() {
			return netip.Prefix{}, inventory.Errorf(inventory.Invalid, "group %d has an IPv6 range; discovery is IPv4 only", g.ID)
		}
		return p, nil
	default:
		p, err := inventory.ParseCIDR(in.CIDR)
		if err != nil {
			return netip.Prefix{}, err
		}
		if !p.Addr().Is4() {
			return netip.Prefix{}, inventory.Errorf(inventory.Invalid, "discovery is IPv4 only; %s is IPv6", p)
		}
		return p, nil
	}
}

// group returns the network group to fill, creating it if needed.
func (m *Manager) group(ctx context.Context, in apitypes.DiscoveryCreate, p netip.Prefix) (int64, error) {
	if in.GroupID != nil {
		return *in.GroupID, nil
	}
	if g, ok := m.inv.NetworkGroupByCIDR(p); ok {
		return g.ID, nil
	}
	g, err := m.inv.CreateGroup(ctx, apitypes.GroupCreate{CIDR: p.String(), ParentID: in.ParentID})
	if err != nil {
		return 0, err
	}
	return g.ID, nil
}

// Get returns a job.
func (m *Manager) Get(id string) (apitypes.Discovery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	if j == nil {
		return apitypes.Discovery{}, inventory.Errorf(inventory.NotFound, "discovery %s not found", id)
	}
	return j.view, nil
}

// Cancel asks a running job to stop and returns its current state; the
// job ends with state cancelled. Cancelling a finished job does nothing.
func (m *Manager) Cancel(id string) (apitypes.Discovery, error) {
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j == nil {
		return apitypes.Discovery{}, inventory.Errorf(inventory.NotFound, "discovery %s not found", id)
	}
	j.cancel()
	return m.Get(id)
}

// Close cancels every job and waits until each has saved what it found
// (SPEC-804).
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.stopAll()
	m.wg.Wait()
}

type probeResult struct {
	ip      netip.Addr
	replied bool
	rtt     time.Duration
	loss    float64
	at      time.Time
	err     error
}

// runJob probes every address at the configured rate and saves the
// responders in batches.
func (m *Manager) runJob(ctx context.Context, j *job, first netip.Addr, n int) {
	results := make(chan probeResult, m.cfg.Concurrency)
	sem := make(chan struct{}, m.cfg.Concurrency)
	var inflight sync.WaitGroup

	// Producer: one address per tick.
	go func() {
		defer func() { inflight.Wait(); close(results) }()
		tick := time.NewTicker(time.Second / time.Duration(m.cfg.Rate))
		defer tick.Stop()
		ip := first
		for i := 0; i < n; i, ip = i+1, ip.Next() {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			inflight.Add(1)
			go func(ip netip.Addr) {
				defer inflight.Done()
				defer func() { <-sem }()
				results <- m.probe(ctx, ip)
			}(ip)
		}
	}()

	var (
		pending   []inventory.Found
		lastSave  = time.Now()
		lastEvent time.Time
		failure   error
		probeErr  error // first probe error (SPEC-205)
	)
	save := func(c context.Context) {
		if len(pending) == 0 || failure != nil {
			return
		}
		added, err := m.inv.AddDiscovered(c, j.view.GroupID, pending)
		pending = nil
		m.mu.Lock()
		j.view.Added += added
		m.mu.Unlock()
		if err != nil {
			failure = err
			j.cancel() // stop probing; the job fails
		}
	}
	flushTick := time.NewTicker(m.cfg.Flush)
	defer flushTick.Stop()
	for done := false; !done; {
		select {
		case r, ok := <-results:
			if !ok {
				done = true
				break
			}
			if r.err != nil && ctx.Err() != nil {
				continue // cancelled: the address doesn't count as probed
			}
			m.mu.Lock()
			j.view.Probed++
			if r.err != nil {
				j.view.Errors++
				if probeErr == nil {
					probeErr = r.err
				}
			}
			if r.replied {
				j.view.Found++
			}
			m.mu.Unlock()
			if r.replied {
				pending = append(pending, inventory.Found{
					IP: r.ip, RTTms: float64(r.rtt) / float64(time.Millisecond), LossPct: r.loss, At: r.at,
				})
			}
		case <-flushTick.C:
		}
		if time.Since(lastSave) >= m.cfg.Flush {
			save(ctx)
			lastSave = time.Now()
		}
		if time.Since(lastEvent) >= m.cfg.Progress {
			m.publish(apitypes.EventDiscoveryProgress, j)
			lastEvent = time.Now()
		}
	}
	// Save the rest even if the job was cancelled (SPEC-205).
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	save(sctx)
	cancel()

	m.mu.Lock()
	switch {
	case failure != nil:
		j.view.State = apitypes.DiscoveryFailed
		j.view.Error = failure.Error()
	case ctx.Err() != nil:
		j.view.State = apitypes.DiscoveryCancelled
	default:
		j.view.State = apitypes.DiscoveryDone
	}
	if failure == nil && probeErr != nil {
		j.view.Error = fmt.Sprintf("%d probes failed; first error: %v", j.view.Errors, probeErr)
	}
	nErr := j.view.Errors
	now := time.Now().UTC().Truncate(time.Millisecond)
	j.view.FinishedAt = &now
	m.finished = append(m.finished, j.view.ID)
	for len(m.finished) > m.cfg.Keep {
		delete(m.jobs, m.finished[0])
		m.finished = slices.Delete(m.finished, 0, 1)
	}
	m.mu.Unlock()
	if failure != nil {
		m.log.Error("discovery failed", "id", j.view.ID, "cidr", j.view.CIDR, "err", failure)
	}
	if probeErr != nil {
		m.log.Warn("discovery probes failed", "id", j.view.ID, "cidr", j.view.CIDR, "errors", nErr, "first", probeErr)
	}
	m.publish(apitypes.EventDiscoveryFinished, j)
}

func (m *Manager) publish(typ string, j *job) {
	m.mu.Lock()
	v := j.view
	m.mu.Unlock()
	if m.notify != nil {
		m.notify.Publish(typ, v)
	}
}

// probe sends one echo request, and a second one SecondTry later if the
// first got no answer (SPEC-201).
func (m *Manager) probe(ctx context.Context, ip netip.Addr) probeResult {
	for try := 1; try <= 2; try++ {
		if try == 2 {
			t := time.NewTimer(m.cfg.SecondTry)
			select {
			case <-ctx.Done():
				t.Stop()
				return probeResult{ip: ip, err: ctx.Err()}
			case <-t.C:
			}
		}
		res, err := m.prober.Ping(ctx, ip, 1, m.cfg.Timeout)
		if err != nil {
			return probeResult{ip: ip, err: err}
		}
		if res.Received > 0 {
			at := res.LastReply
			if at.IsZero() {
				at = time.Now()
			}
			return probeResult{ip: ip, replied: true, rtt: res.RTTs[0], loss: float64(try-1) * 50, at: at}
		}
	}
	return probeResult{ip: ip}
}

// Addresses returns the first address to probe and how many there are
// (SPEC-202): prefixes /30 and shorter skip the network and broadcast
// addresses; /31 and /32 probe every address.
func Addresses(p netip.Prefix) (first netip.Addr, n int) {
	p = p.Masked()
	size := 1 << (32 - p.Bits())
	if p.Bits() <= 30 {
		return p.Addr().Next(), size - 2
	}
	return p.Addr(), size
}
