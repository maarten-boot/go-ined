// Package dnsname keeps the PTR names of the hosts up to date
// (SPEC-401..403).
package dnsname

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
)

// Resolver does reverse lookups; *net.Resolver satisfies it.
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// Inventory is the part of inventory.Service the refresher uses.
type Inventory interface {
	DNSTargets() []inventory.Target
	SetPTRName(ctx context.Context, id int64, ip netip.Addr, name string) error
}

// Config holds the DNS settings. Zero values get defaults.
type Config struct {
	Refresh     time.Duration // between lookups of one host (default 1 h)
	Concurrency int           // lookups at once (default 20)
	Timeout     time.Duration // per lookup (default 5 s)
	Tick        time.Duration // scheduler resolution (default 100 ms)
	Sync        time.Duration // how often the host list is re-read (default 1 s)
	Now         func() time.Time
}

func (c *Config) defaults() {
	if c.Refresh <= 0 {
		c.Refresh = time.Hour
	}
	if c.Concurrency < 1 {
		c.Concurrency = 20
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.Tick <= 0 {
		c.Tick = 100 * time.Millisecond
	}
	if c.Sync <= 0 {
		c.Sync = time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Name interprets a lookup result (SPEC-403): the first name on success;
// "" (clear) when the name doesn't exist; keep = true on any other error.
func Name(names []string, err error) (name string, keep bool) {
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return "", false
		}
		return "", true
	}
	if len(names) == 0 {
		return "", false
	}
	return names[0], false
}

// Refresher looks up the PTR name of every host when it is added, when its
// IP changes, and then every Refresh.
type Refresher struct {
	r   Resolver
	inv Inventory
	cfg Config
	log *slog.Logger
}

// New returns a refresher. log may be nil.
func New(r Resolver, inv Inventory, cfg Config, log *slog.Logger) *Refresher {
	cfg.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Refresher{r: r, inv: inv, cfg: cfg, log: log}
}

type hostState struct {
	ip   netip.Addr
	next time.Time
	busy bool
}

// Run looks up names until ctx is cancelled, then waits for the lookups in
// flight.
func (f *Refresher) Run(ctx context.Context) {
	var (
		hosts    = map[int64]*hostState{}
		sem      = make(chan struct{}, f.cfg.Concurrency)
		done     = make(chan int64)
		wg       sync.WaitGroup
		lastSync time.Time
		started  bool
	)
	defer wg.Wait()
	refresh := func(now time.Time) {
		lastSync = now
		targets := f.inv.DNSTargets()
		seen := make(map[int64]bool, len(targets))
		for i, t := range targets {
			seen[t.ID] = true
			st := hosts[t.ID]
			switch {
			case st == nil && !started:
				// Existing hosts at startup: spread across the period.
				off := time.Duration(int64(f.cfg.Refresh) * int64(i) / int64(len(targets)))
				hosts[t.ID] = &hostState{ip: t.IP, next: now.Add(off)}
			case st == nil:
				hosts[t.ID] = &hostState{ip: t.IP, next: now} // added: look up at once
			case st.ip != t.IP:
				st.ip, st.next = t.IP, now // re-addressed: look up at once
			}
		}
		for id, st := range hosts {
			if !seen[id] && !st.busy {
				delete(hosts, id)
			}
		}
		started = true
	}
	lookup := func(id int64, ip netip.Addr) {
		defer wg.Done()
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return // Run is stopping and no longer reads done
		}
		lctx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
		names, err := f.r.LookupAddr(lctx, ip.String())
		cancel()
		<-sem
		if ctx.Err() == nil {
			if name, keep := Name(names, err); !keep {
				if err := f.inv.SetPTRName(ctx, id, ip, name); err != nil {
					f.log.Error("saving PTR name failed", "host", id, "err", err)
				}
			} else {
				f.log.Debug("reverse lookup failed; keeping the name", "host", id, "ip", ip, "err", err)
			}
		}
		select {
		case done <- id:
		case <-ctx.Done():
		}
	}

	tick := time.NewTicker(f.cfg.Tick)
	defer tick.Stop()
	refresh(f.cfg.Now())
	for {
		now := f.cfg.Now()
		if now.Sub(lastSync) >= f.cfg.Sync {
			refresh(now)
		}
		for id, st := range hosts {
			if !st.busy && !now.Before(st.next) {
				st.busy = true
				st.next = now.Add(f.cfg.Refresh)
				wg.Add(1)
				go lookup(id, st.ip)
			}
		}
		select {
		case <-ctx.Done():
			return
		case id := <-done:
			if st := hosts[id]; st != nil {
				st.busy = false
			}
		case <-tick.C:
		}
	}
}
