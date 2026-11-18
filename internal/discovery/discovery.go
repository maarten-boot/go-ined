// Package discovery runs discovery jobs: it probes every address of an
// IPv4 range with ICMP echo and adds the responders to the inventory
// (SPEC-201..205).
package discovery

import (
	"cmp"
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
	AddDiscovered(ctx context.Context, group int64, found []inventory.Found) ([]netip.Addr, error)
	RescanDue(now time.Time) []inventory.RescanTarget
	RescanIsDue(id int64, now time.Time) bool
	RescanDone(ctx context.Context, p netip.Prefix, at time.Time, added []netip.Addr) error
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
	// Scheduled runs the scans on a schedule of network groups
	// (discovery.scheduled, SPEC-206, SPEC-207).
	Scheduled bool
	Tick      time.Duration    // how often the schedule is checked (default 10 s)
	Retry     time.Duration    // wait after a scheduled scan failed (default 5 min)
	Now       func() time.Time // default time.Now
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
	if c.Tick <= 0 {
		c.Tick = 10 * time.Second
	}
	if c.Retry <= 0 {
		c.Retry = 5 * time.Minute
	}
	if c.Now == nil {
		c.Now = time.Now
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
	// The schedule (SPEC-206, 207): a group isn't started again before
	// later[id] (a failed scan, one cancelled by a user, or one that
	// couldn't start), unless its setting changed since; waiting counts
	// manual starts waiting for a scheduled job to stop; schedErr is the
	// last reason no scheduled scan could start (discovery unavailable),
	// warned the last warning per group, both logged once.
	later    map[int64]wait
	waiting  int
	schedErr string
	warned   map[int64]string
}

// wait holds a group back until a time, for the rescan setting it had
// (inventory.RescanTarget.Gen); a changed setting ends it (crosscheck
// 2026-11-18-02 L1).
type wait struct {
	until time.Time
	gen   uint64
}

type job struct {
	view   apitypes.Discovery
	prefix netip.Prefix
	cancel context.CancelFunc
	done   chan struct{} // closed when the job has ended
	added  []netip.Addr
	sched  *inventory.RescanTarget // the group of a scheduled job
	byUser bool                    // cancelled through Cancel
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
	m := &Manager{
		inv: inv, prober: prober, whyNot: whyNot, notify: notify, cfg: cfg, log: log, run: run,
		baseCtx: ctx, stopAll: cancel, jobs: map[string]*job{}, later: map[int64]wait{}, warned: map[int64]string{},
	}
	if cfg.Scheduled {
		m.wg.Add(1)
		go m.schedule()
	}
	return m
}

// Start validates the request, finds or creates the network group and
// starts the job (SPEC-201, SPEC-203). A scheduled job over an overlapping
// range is cancelled first, keeping what it found (SPEC-207).
func (m *Manager) Start(ctx context.Context, in apitypes.DiscoveryCreate) (apitypes.Discovery, error) {
	return m.start(ctx, in, nil)
}

// start starts a job; sched != nil marks it scheduled for that group. A
// scheduled job never cancels another.
func (m *Manager) start(ctx context.Context, in apitypes.DiscoveryCreate, sched *inventory.RescanTarget) (apitypes.Discovery, error) {
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
	for {
		if m.closed {
			m.mu.Unlock()
			return apitypes.Discovery{}, inventory.Errorf(inventory.Unavailable, "the server is shutting down")
		}
		var stop []*job
		for _, j := range m.jobs {
			if j.view.State != apitypes.DiscoveryRunning || !j.prefix.Overlaps(prefix) {
				continue
			}
			if sched != nil || !j.view.Scheduled {
				m.mu.Unlock()
				return apitypes.Discovery{}, inventory.Errorf(inventory.Conflict,
					"range %s overlaps running discovery %s (%s)", prefix, j.view.ID, j.prefix)
			}
			stop = append(stop, j)
		}
		if len(stop) == 0 {
			break
		}
		// A manual scan wins: stop the scheduled ones, which save what
		// they found, and look again (SPEC-207).
		m.waiting++
		m.mu.Unlock()
		for _, j := range stop {
			m.log.Info("scheduled discovery cancelled for a manual one", "id", j.view.ID, "cidr", j.view.CIDR, "for", prefix.String())
			j.cancel()
		}
		var err error
		for _, j := range stop {
			select {
			case <-j.done:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		m.mu.Lock()
		m.waiting--
		if err != nil {
			m.mu.Unlock()
			return apitypes.Discovery{}, err
		}
	}
	// A scheduled scan checks again, now that no overlapping job runs: a
	// job that just ended has recorded its scan by now (SPEC-206).
	if sched != nil && !m.inv.RescanIsDue(sched.ID, m.cfg.Now()) {
		m.mu.Unlock()
		return apitypes.Discovery{}, inventory.Errorf(inventory.Conflict, "not due any more")
	}
	m.seq++
	id := m.run + "-" + strconv.Itoa(m.seq)
	jctx, cancel := context.WithCancel(m.baseCtx)
	first, n := Addresses(prefix)
	j := &job{prefix: prefix, cancel: cancel, done: make(chan struct{}), sched: sched, view: apitypes.Discovery{
		ID: id, CIDR: prefix.String(), State: apitypes.DiscoveryRunning, Total: n, StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		Scheduled: sched != nil,
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
		close(j.done)
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

// List returns the jobs kept (running and the last finished), oldest first.
func (m *Manager) List() []apitypes.Discovery {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]apitypes.Discovery, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j.view)
	}
	slices.SortFunc(out, func(a, b apitypes.Discovery) int {
		return cmp.Or(a.StartedAt.Compare(b.StartedAt), cmp.Compare(len(a.ID), len(b.ID)), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// Cancel asks a running job to stop and returns its current state; the
// job ends with state cancelled. Cancelling a finished job does nothing.
func (m *Manager) Cancel(id string) (apitypes.Discovery, error) {
	m.mu.Lock()
	j := m.jobs[id]
	if j == nil {
		m.mu.Unlock()
		return apitypes.Discovery{}, inventory.Errorf(inventory.NotFound, "discovery %s not found", id)
	}
	if j.view.State == apitypes.DiscoveryRunning {
		j.byUser = true
	}
	m.mu.Unlock()
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
		j.view.Added += len(added)
		j.added = append(j.added, added...)
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

	state := apitypes.DiscoveryDone
	switch {
	case failure != nil:
		state = apitypes.DiscoveryFailed
	case ctx.Err() != nil:
		state = apitypes.DiscoveryCancelled
	}
	m.mu.Lock()
	added := slices.Clone(j.added)
	// Every probe failed (no route, say): done, but nothing was scanned
	// (crosscheck 2026-11-18-02 L3).
	blind := j.view.Probed > 0 && j.view.Errors == j.view.Probed
	m.mu.Unlock()
	if state == apitypes.DiscoveryDone && !blind {
		// Every group with a schedule in the range was scanned now, by
		// hand or not (SPEC-206). Recorded while the job still runs, so
		// the schedule can't start that group's scan in between.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := m.inv.RescanDone(rctx, j.prefix, m.cfg.Now(), added); err != nil {
			m.log.Error("discovery: recording the scan failed", "id", j.view.ID, "cidr", j.view.CIDR, "err", err)
		}
		cancel()
	}

	m.mu.Lock()
	j.view.State = state
	if failure != nil {
		j.view.Error = failure.Error()
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
	if s := j.sched; s != nil {
		switch {
		case state == apitypes.DiscoveryFailed, state == apitypes.DiscoveryDone && blind:
			m.later[s.ID] = wait{m.cfg.Now().Add(m.cfg.Retry), s.Gen}
		case state == apitypes.DiscoveryCancelled && j.byUser:
			m.later[s.ID] = wait{m.cfg.Now().Add(s.Interval), s.Gen} // skip this round
		}
	}
	m.mu.Unlock()
	if failure != nil {
		m.log.Error("discovery failed", "id", j.view.ID, "cidr", j.view.CIDR, "err", failure)
	}
	if probeErr != nil {
		m.log.Warn("discovery probes failed", "id", j.view.ID, "cidr", j.view.CIDR, "errors", nErr, "first", probeErr)
	}
	m.publish(apitypes.EventDiscoveryFinished, j)
	close(j.done)
}

// schedule starts the scans that are due, one at a time (SPEC-206, 207),
// until Close.
func (m *Manager) schedule() {
	defer m.wg.Done()
	t := time.NewTicker(m.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-m.baseCtx.Done():
			return
		case <-t.C:
			m.scheduleOnce()
		}
	}
}

// scheduleOnce starts the scan due longest, unless a scheduled scan runs
// or a manual one waits for one to stop. A group whose range overlaps a
// running job is tried at the next check. A group that can't be scanned
// (its range now larger than max_prefix, or another error) is passed over
// with a warning, so it doesn't hold up the others (crosscheck
// 2026-11-18-02 C1); when discovery is unavailable, no group can start.
func (m *Manager) scheduleOnce() {
	m.mu.Lock()
	busy := m.closed || m.waiting > 0
	for _, j := range m.jobs {
		busy = busy || (j.view.Scheduled && j.view.State == apitypes.DiscoveryRunning)
	}
	m.mu.Unlock()
	if busy {
		return
	}
	now := m.cfg.Now()
	for _, t := range m.inv.RescanDue(now) {
		m.mu.Lock()
		w, ok := m.later[t.ID]
		hold := ok && w.gen == t.Gen && now.Before(w.until)
		if ok && !hold {
			delete(m.later, t.ID)
		}
		m.mu.Unlock()
		if hold {
			continue
		}
		if t.CIDR.Bits() < m.cfg.MaxPrefix {
			m.warn(t, fmt.Sprintf("range %s is larger than /%d (discovery.max_prefix); not scanned until rescan is turned off or the limit raised", t.CIDR, m.cfg.MaxPrefix))
			continue
		}
		id := t.ID
		v, err := m.start(m.baseCtx, apitypes.DiscoveryCreate{GroupID: &id}, &t)
		switch inventory.KindOf(err) {
		case inventory.Conflict:
			continue
		case inventory.Unavailable: // no group can start: tried at every check, logged once
			m.mu.Lock()
			last := m.schedErr
			m.schedErr = err.Error()
			m.mu.Unlock()
			if err.Error() != last {
				m.log.Warn("scheduled discovery can't start", "err", err)
			}
			return
		}
		m.mu.Lock()
		m.schedErr = ""
		if err != nil {
			m.later[t.ID] = wait{now.Add(m.cfg.Retry), t.Gen}
		}
		m.mu.Unlock()
		if err != nil {
			m.warn(t, err.Error())
			continue
		}
		m.mu.Lock()
		delete(m.warned, t.ID)
		m.mu.Unlock()
		m.log.Info("scheduled discovery started", "id", v.ID, "cidr", v.CIDR, "group", t.ID)
		return
	}
}

// warn logs why a group's scheduled scan can't run, once per reason.
func (m *Manager) warn(t inventory.RescanTarget, why string) {
	m.mu.Lock()
	same := m.warned[t.ID] == why
	m.warned[t.ID] = why
	m.mu.Unlock()
	if !same {
		m.log.Warn("scheduled discovery can't start", "cidr", t.CIDR.String(), "group", t.ID, "err", why)
	}
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
