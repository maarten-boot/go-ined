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
	SetPTRName(ctx context.Context, id int64, ip netip.Addr, name string) (old string, changed bool, err error)
}

// Config holds the DNS settings. Zero values get defaults.
type Config struct {
	Refresh     time.Duration // between lookups of one host (default 1 h)
	Recheck     time.Duration // after a "not found" for a host with a name (default 1 min)
	ClearAfter  int           // "not found" answers in a row that clear a name (default 3)
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
	if c.Recheck <= 0 {
		c.Recheck = time.Minute
	}
	if c.ClearAfter < 1 {
		c.ClearAfter = 3
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
// "" (not found) when the name doesn't exist; keep = true on any other
// error. The refresher clears a known name only after Config.ClearAfter
// "not found" answers in a row.
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
// IP changes, and then every Refresh. A host with a name that gets "not
// found" keeps it and is looked up again after Recheck; the name goes
// after ClearAfter such answers in a row (SPEC-403 v0.34).
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
	ip      netip.Addr
	name    string // the PTR name in the inventory
	next    time.Time
	busy    bool
	misses  int  // "not found" answers in a row while it had a name
	cleared bool // its name was cleared by a lookup since the start
}

// result is what a lookup reports back to the loop.
type result struct {
	id       int64
	ip       netip.Addr // the address that was looked up
	notFound bool       // the name doesn't exist
	keep     bool       // temporary error: nothing changed
	name     string     // otherwise the name now in the inventory
}

// Run looks up names until ctx is cancelled, then waits for the lookups in
// flight.
func (f *Refresher) Run(ctx context.Context) {
	var (
		hosts    = map[int64]*hostState{}
		sem      = make(chan struct{}, f.cfg.Concurrency)
		done     = make(chan result)
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
				hosts[t.ID] = &hostState{ip: t.IP, name: t.PTRName, next: now.Add(off)}
			case st == nil:
				hosts[t.ID] = &hostState{ip: t.IP, name: t.PTRName, next: now} // added: look up at once
			case st.ip != t.IP:
				// Re-addressed: look up at once; the old name is gone.
				st.ip, st.name, st.next, st.misses, st.cleared = t.IP, t.PTRName, now, 0, false
			case !st.busy:
				st.name = t.PTRName
			}
		}
		for id, st := range hosts {
			if !seen[id] && !st.busy {
				delete(hosts, id)
			}
		}
		started = true
	}
	// lookup runs one lookup of a host whose state was st (a copy); the
	// caller has taken a slot in sem.
	lookup := func(id int64, st hostState) {
		defer wg.Done()
		lctx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
		names, err := f.r.LookupAddr(lctx, st.ip.String())
		cancel()
		<-sem
		res := result{id: id, keep: true}
		if ctx.Err() == nil {
			res = f.apply(ctx, id, st, names, err)
		}
		res.ip = st.ip
		select {
		case done <- res:
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
		// Start due lookups while slots are free; the others stay due and
		// wait here, without a goroutine each.
	due:
		for id, st := range hosts {
			if !st.busy && !now.Before(st.next) {
				select {
				case sem <- struct{}{}:
				default:
					break due
				}
				st.busy = true
				st.next = now.Add(f.cfg.Refresh)
				wg.Add(1)
				go lookup(id, *st)
			}
		}
		select {
		case <-ctx.Done():
			return
		case res := <-done:
			if st := hosts[res.id]; st != nil {
				st.busy = false
				switch {
				case res.keep, res.ip != st.ip:
					// Nothing changed, or the host has another address by
					// now: that lookup is already due (crosscheck
					// 2026-11-12-05 C1).
				case res.notFound && st.name != "" && st.misses+1 < f.cfg.ClearAfter:
					st.misses++
					st.next = f.cfg.Now().Add(f.cfg.Recheck)
				default:
					st.cleared = st.cleared || (st.name != "" && res.name == "")
					st.name, st.misses = res.name, 0
				}
			}
		case <-tick.C:
		}
	}
}

// apply acts on one lookup result for a host whose state was st: it saves
// a found name, clears the name after ClearAfter "not found" answers in a
// row, and logs a cleared, changed or returning name at Info.
func (f *Refresher) apply(ctx context.Context, id int64, st hostState, names []string, err error) result {
	name, keep := Name(names, err)
	ip := st.ip
	switch {
	case keep:
		f.log.Debug("reverse lookup failed; keeping the name", "host", id, "ip", ip, "err", err)
		return result{id: id, keep: true}
	case name == "" && st.name != "" && st.misses+1 < f.cfg.ClearAfter:
		f.log.Debug("reverse lookup: name not found; keeping it for now", "host", id, "ip", ip,
			"name", st.name, "answers", st.misses+1, "clear_after", f.cfg.ClearAfter)
		return result{id: id, notFound: true}
	}
	old, changed, err := f.inv.SetPTRName(ctx, id, ip, name)
	if err != nil {
		f.log.Error("saving PTR name failed", "host", id, "err", err)
		return result{id: id, keep: true}
	}
	// Log only what was saved: the inventory ignores an answer for an
	// address the host no longer has (crosscheck 2026-11-12-05 C1).
	switch {
	case !changed:
	case name == "":
		f.log.Info("reverse DNS: name cleared", "host", id, "ip", ip, "name", old, "answers", f.cfg.ClearAfter)
	case old != "":
		f.log.Info("reverse DNS: name changed", "host", id, "ip", ip, "from", old, "to", name)
	case st.cleared:
		f.log.Info("reverse DNS: name back", "host", id, "ip", ip, "name", name)
	default:
		f.log.Debug("reverse DNS: name found", "host", id, "ip", ip, "name", name)
	}
	return result{id: id, notFound: name == "", name: name}
}
