// Package monitor pings the monitored hosts in the background (SPEC-301..307).
// The ICMP work is behind the Prober interface so that tests can use a fake.
package monitor

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Prober sends count echo requests to ip, waiting up to timeout for each
// reply. A cancelled ctx must make Ping return promptly with ctx.Err().
type Prober interface {
	Ping(ctx context.Context, ip netip.Addr, count int, timeout time.Duration) (Result, error)
}

// Result is the outcome of one ping round.
type Result struct {
	Sent      int
	Received  int
	RTTs      []time.Duration // one per reply
	LastReply time.Time       // arrival of the last reply; zero if none
}

// Inventory is the part of inventory.Service the monitor uses.
type Inventory interface {
	MonitorTargets() []inventory.Target
	ApplyObservations(ctx context.Context, obs []inventory.Observation) error
}

// Config holds the monitor settings. Zero values get defaults.
type Config struct {
	Interval    time.Duration // between rounds of one host (SPEC-301)
	Count       int           // echo requests per round (SPEC-302)
	Timeout     time.Duration // per echo request
	DownAfter   int           // failed rounds in a row before down
	Concurrency int           // rounds in flight at once; default 256
	Tick        time.Duration // scheduler resolution; default 100 ms
	Sync        time.Duration // how often the target list is re-read; default 1 s
	Flush       time.Duration // how often results are saved (SPEC-307); default 1 s
	Now         func() time.Time
}

func (c *Config) defaults() {
	if c.Interval <= 0 {
		c.Interval = time.Minute
	}
	if c.Count < 1 {
		c.Count = 3
	}
	if c.Timeout <= 0 {
		c.Timeout = time.Second
	}
	if c.DownAfter < 1 {
		c.DownAfter = 2
	}
	if c.Concurrency < 1 {
		c.Concurrency = 256
	}
	if c.Tick <= 0 {
		c.Tick = 100 * time.Millisecond
	}
	if c.Sync <= 0 {
		c.Sync = time.Second
	}
	if c.Flush <= 0 {
		c.Flush = time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// NextStatus applies the status rule of SPEC-302 to one finished round.
// streak is the number of failed rounds in a row before this one; the new
// streak is returned. status "" means: keep the current status.
func NextStatus(replied bool, streak, downAfter int) (status string, newStreak int) {
	if replied {
		return apitypes.StatusUp, 0
	}
	streak++
	if streak >= downAfter {
		return apitypes.StatusDown, streak
	}
	return "", streak
}

// Summarize turns a round's result into an observation (SPEC-303).
func Summarize(r Result) (rttMS *float64, lossPct float64) {
	if r.Sent > 0 {
		lossPct = float64(r.Sent-r.Received) * 100 / float64(r.Sent)
	}
	if len(r.RTTs) > 0 {
		var sum time.Duration
		for _, d := range r.RTTs {
			sum += d
		}
		ms := float64(sum) / float64(len(r.RTTs)) / float64(time.Millisecond)
		rttMS = &ms
	}
	return rttMS, lossPct
}

// Monitor schedules ping rounds and feeds the results to the inventory.
type Monitor struct {
	p   Prober
	inv Inventory
	cfg Config
	log *slog.Logger
}

// New returns a monitor. log may be nil.
func New(p Prober, inv Inventory, cfg Config, log *slog.Logger) *Monitor {
	cfg.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{p: p, inv: inv, cfg: cfg, log: log}
}

type hostState struct {
	ip     netip.Addr
	next   time.Time
	streak int
	busy   bool
}

type done struct {
	id  int64
	ip  netip.Addr
	at  time.Time
	res Result
	err error
}

// Run pings until ctx is cancelled. Before it returns it waits for the
// rounds in flight and saves every pending result (SPEC-804).
func (m *Monitor) Run(ctx context.Context) {
	var (
		hosts    = map[int64]*hostState{}
		results  = make(chan done)
		sem      = make(chan struct{}, m.cfg.Concurrency)
		wg       sync.WaitGroup
		pending  []inventory.Observation
		lastSync time.Time
		lastSave = m.cfg.Now()
	)
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()

	save := func(c context.Context) {
		if len(pending) == 0 {
			return
		}
		if err := m.inv.ApplyObservations(c, pending); err != nil {
			m.log.Error("saving ping results failed", "err", err, "results", len(pending))
		}
		pending = nil
	}
	finish := func(d done) {
		st := hosts[d.id]
		if st != nil {
			st.busy = false
		}
		if d.err != nil && ctx.Err() != nil {
			return // cancelled at shutdown: not a real result
		}
		if errors.Is(d.err, ErrFamilyUnavailable) {
			return // not pinged at all: the status stays as it is (SPEC-304)
		}
		if st == nil || st.ip != d.ip {
			return // removed, unmonitored or re-addressed meanwhile
		}
		if d.err != nil {
			m.log.Debug("ping failed", "host", d.id, "ip", d.ip, "err", d.err)
			d.res = Result{Sent: m.cfg.Count}
		}
		status, streak := NextStatus(d.res.Received > 0, st.streak, m.cfg.DownAfter)
		st.streak = streak
		rtt, loss := Summarize(d.res)
		seen := d.res.LastReply
		if seen.IsZero() {
			seen = d.at
		}
		pending = append(pending, inventory.Observation{
			HostID: d.id, IP: d.ip, Status: status, RTTms: rtt, LossPct: loss,
			Replied: d.res.Received > 0, SeenAt: seen, At: d.at,
		})
	}
	refresh := func(now time.Time) {
		lastSync = now
		targets := m.inv.MonitorTargets()
		seen := make(map[int64]bool, len(targets))
		var added []inventory.Target
		for _, t := range targets {
			seen[t.ID] = true
			st := hosts[t.ID]
			switch {
			case st == nil:
				added = append(added, t)
			case st.ip != t.IP:
				st.ip, st.streak = t.IP, 0
			}
		}
		for id, st := range hosts {
			if !seen[id] && !st.busy {
				delete(hosts, id) // SPEC-306; a busy one is dropped in finish
			} else if !seen[id] {
				st.ip = netip.Addr{}
			}
		}
		// Spread new hosts across the interval (SPEC-301): host i of n
		// starts at now + i*interval/n. One new host is pinged at once.
		for i, t := range added {
			off := time.Duration(int64(m.cfg.Interval) * int64(i) / int64(len(added)))
			hosts[t.ID] = &hostState{ip: t.IP, next: now.Add(off)}
		}
	}
	start := func(id int64, st *hostState, now time.Time) {
		st.busy = true
		st.next = st.next.Add(m.cfg.Interval)
		if st.next.Before(now) {
			st.next = now.Add(m.cfg.Interval) // fell behind: don't burst
		}
		ip := st.ip
		wg.Add(1)
		go func() {
			defer wg.Done()
			var d done
			select {
			case sem <- struct{}{}:
				res, err := m.p.Ping(pctx, ip, m.cfg.Count, m.cfg.Timeout)
				<-sem
				d = done{id: id, ip: ip, at: m.cfg.Now(), res: res, err: err}
			case <-pctx.Done():
				d = done{id: id, ip: ip, err: pctx.Err()}
			}
			results <- d
		}()
	}

	tick := time.NewTicker(m.cfg.Tick)
	defer tick.Stop()
	refresh(m.cfg.Now())
	for {
		now := m.cfg.Now()
		if now.Sub(lastSync) >= m.cfg.Sync {
			refresh(now)
		}
		for id, st := range hosts {
			if !st.busy && st.ip.IsValid() && !now.Before(st.next) {
				start(id, st, now)
			}
		}
		if now.Sub(lastSave) >= m.cfg.Flush {
			save(ctx)
			lastSave = now
		}
		select {
		case <-ctx.Done():
			cancel()
			go func() { wg.Wait(); close(results) }()
			for d := range results {
				finish(d)
			}
			sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			save(sctx)
			scancel()
			return
		case d := <-results:
			finish(d)
		case <-tick.C:
		}
	}
}
