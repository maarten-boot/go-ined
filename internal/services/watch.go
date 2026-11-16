package services

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// WatchInventory is the part of inventory.Service the watcher uses.
type WatchInventory interface {
	WatchTargets() []inventory.WatchTarget
	ApplyWatch(ctx context.Context, rs []inventory.WatchResult) error
}

// WatchConfig holds the settings of watched-service checks (SPEC-310).
// Zero values get defaults.
type WatchConfig struct {
	Interval    time.Duration // between checks of a service (default 5 min)
	DownAfter   int           // failed checks in a row before down (default 2)
	Concurrency int           // connects at once (default 50)
	Timeout     time.Duration // per connect (default 1 s)
	Tick        time.Duration // how often due hosts are looked for (default 5 s)
	Dial        func(ctx context.Context, network, addr string) (net.Conn, error)
	Now         func() time.Time
}

func (c *WatchConfig) defaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.DownAfter < 1 {
		c.DownAfter = 2
	}
	if c.Concurrency < 1 {
		c.Concurrency = 50
	}
	if c.Timeout <= 0 {
		c.Timeout = time.Second
	}
	if c.Tick <= 0 {
		c.Tick = 5 * time.Second
	}
	if c.Dial == nil {
		c.Dial = (&net.Dialer{}).DialContext
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// watchKey is one watched service of one host at one address. A new
// address or a service added again is a new key, so its count of failures
// starts again.
type watchKey struct {
	host int64
	ip   netip.Addr
	name string
	port int
}

// Watcher checks the watched services of monitored hosts every Interval,
// by one TCP connect each, and records their status (SPEC-310): down after
// DownAfter failures in a row, up after one success.
type Watcher struct {
	inv WatchInventory
	cfg WatchConfig
	log *slog.Logger
	sem chan struct{}

	mu    sync.Mutex
	next  map[watchKey]time.Time // when a service is due; absent = now
	fails map[watchKey]int
	busy  map[int64]bool // hosts being checked
}

// NewWatcher returns a watcher. log may be nil.
func NewWatcher(inv WatchInventory, cfg WatchConfig, log *slog.Logger) *Watcher {
	cfg.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{inv: inv, cfg: cfg, log: log, sem: make(chan struct{}, cfg.Concurrency),
		next: map[watchKey]time.Time{}, fails: map[watchKey]int{}, busy: map[int64]bool{}}
}

// Run checks due services until ctx is cancelled, then waits for the
// checks in flight.
func (w *Watcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(w.cfg.Tick)
	defer tick.Stop()
	for {
		w.round(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// round starts a check of every host with a due service that is not being
// checked already, and forgets services no longer watched.
func (w *Watcher) round(ctx context.Context, wg *sync.WaitGroup) {
	now := w.cfg.Now()
	targets := w.inv.WatchTargets()
	w.mu.Lock()
	defer w.mu.Unlock()
	live := map[watchKey]bool{}
	for _, t := range targets {
		var due []apitypes.ServicePort
		for _, s := range t.Services {
			k := watchKey{t.ID, t.IP, s.Name, s.Port}
			live[k] = true
			if n, ok := w.next[k]; !ok || !now.Before(n) {
				due = append(due, s)
			}
		}
		if len(due) == 0 || w.busy[t.ID] {
			continue
		}
		for _, s := range due {
			w.next[watchKey{t.ID, t.IP, s.Name, s.Port}] = now.Add(w.cfg.Interval)
		}
		w.busy[t.ID] = true
		wg.Go(func() {
			defer func() {
				w.mu.Lock()
				delete(w.busy, t.ID)
				w.mu.Unlock()
			}()
			w.check(ctx, t.ID, t.IP, due)
		})
	}
	for k := range w.next {
		if !live[k] {
			delete(w.next, k)
		}
	}
	for k := range w.fails {
		if !live[k] {
			delete(w.fails, k)
		}
	}
}

// check connects to every service of one host and saves the results.
func (w *Watcher) check(ctx context.Context, id int64, ip netip.Addr, svcs []apitypes.ServicePort) {
	ok := make([]bool, len(svcs))
	var wg sync.WaitGroup
	for i, s := range svcs {
		wg.Go(func() {
			select {
			case w.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-w.sem }()
			dctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
			defer cancel()
			conn, err := w.cfg.Dial(dctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(s.Port)))
			if err == nil {
				conn.Close()
				ok[i] = true
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return // cut short: not a failure of the service
	}
	at := w.cfg.Now()
	rs := make([]inventory.WatchResult, len(svcs))
	w.mu.Lock()
	for i, s := range svcs {
		k := watchKey{id, ip, s.Name, s.Port}
		r := inventory.WatchResult{HostID: id, IP: ip, Name: s.Name, Port: s.Port, OK: ok[i], At: at}
		if ok[i] {
			w.fails[k] = 0
			r.Status = apitypes.StatusUp
		} else {
			w.fails[k]++
			if w.fails[k] >= w.cfg.DownAfter {
				r.Status = apitypes.StatusDown
			}
		}
		rs[i] = r
	}
	w.mu.Unlock()
	if err := w.inv.ApplyWatch(ctx, rs); err != nil && ctx.Err() == nil {
		w.log.Error("saving watched services failed", "host", id, "err", err)
		return
	}
	w.log.Debug("watched services checked", "host", id, "ip", ip, "results", len(rs))
}
