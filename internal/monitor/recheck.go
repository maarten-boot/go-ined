package monitor

import (
	"context"
	"io"
	"net/netip"
	"sync"
	"time"
)

// Rechecker is a Prober that finds the ICMP modes again while one is
// missing (SPEC-304, v0.27). A fixed sysctl or a new capability is then
// used without a restart. Ping on a family without a mode returns
// ErrFamilyUnavailable, which the monitor skips (hosts keep their status).
type Rechecker struct {
	open     func() (Prober, Status, error)
	every    time.Duration
	now      func() time.Time
	onChange func(Status, error)

	mu   sync.Mutex
	cur  *slot
	st   Status
	err  error
	last time.Time
	// retired holds replaced probers that still have rounds running; each
	// is closed when its last round ends (crosscheck 2026-11-12-02 C2).
	retired map[*slot]bool
	closed  bool // after Close: no new attempts
}

// slot is a prober with the number of ping rounds running on it.
type slot struct {
	p      Prober
	rounds int
}

// closeProber closes p if it can be closed.
func closeProber(p Prober) {
	if c, ok := p.(io.Closer); ok && p != nil {
		c.Close()
	}
}

// NewRechecker detects the modes once with open, then again at most every
// interval (on Ping) while a family is missing, or at once on Recheck.
// onChange, if set, is called with the new state whenever it improves.
func NewRechecker(open func() (Prober, Status, error), every time.Duration, onChange func(Status, error)) *Rechecker {
	r := &Rechecker{open: open, every: every, now: time.Now, onChange: onChange, retired: map[*slot]bool{}}
	p, st, err := open()
	r.cur, r.st, r.err = &slot{p: p}, st, err
	r.last = r.now()
	return r
}

// score counts the address families with a working mode.
func score(p Prober, st Status, err error) int {
	if p == nil || err != nil {
		return 0
	}
	n := 0
	if st.IPv4Error == "" {
		n++
	}
	if st.IPv6Error == "" {
		n++
	}
	return n
}

// complete reports whether both families work; caller holds mu.
func (r *Rechecker) complete() bool { return score(r.cur.p, r.st, r.err) == 2 }

// current returns the slot to use, detecting the modes again first when
// one is missing and force is set or the interval has passed. With round
// set, the caller starts a ping round on it and must call done after.
func (r *Rechecker) current(force, round bool) *slot {
	r.mu.Lock()
	if r.closed || r.complete() || (!force && r.now().Sub(r.last) < r.every) {
		s := r.cur
		if round {
			s.rounds++
		}
		r.mu.Unlock()
		return s
	}
	r.last = r.now()
	p, st, err := r.open()
	improved := score(p, st, err) > score(r.cur.p, r.st, r.err)
	var closeNow Prober
	if improved {
		old := r.cur
		r.cur, r.st, r.err = &slot{p: p}, st, err
		if old.rounds == 0 {
			closeNow = old.p
		} else {
			r.retired[old] = true // closed when its last round ends
		}
	} else {
		closeNow = p // not better: drop the new one
	}
	s, cst, cerr := r.cur, r.st, r.err
	if round {
		s.rounds++
	}
	r.mu.Unlock()
	closeProber(closeNow)
	if improved && r.onChange != nil {
		r.onChange(cst, cerr)
	}
	return s
}

// done ends a round on s; a replaced prober is closed after its last one.
func (r *Rechecker) done(s *slot) {
	r.mu.Lock()
	s.rounds--
	last := s.rounds == 0 && r.retired[s]
	if last {
		delete(r.retired, s)
	}
	r.mu.Unlock()
	if last {
		closeProber(s.p)
	}
}

// Ping implements Prober.
func (r *Rechecker) Ping(ctx context.Context, ip netip.Addr, count int, timeout time.Duration) (Result, error) {
	s := r.current(false, true)
	defer r.done(s)
	if s.p == nil {
		return Result{}, ErrFamilyUnavailable
	}
	return s.p.Ping(ctx, ip, count, timeout)
}

// Recheck detects the modes again now if one is missing, and returns the
// state. Discovery calls it before a job, the server every interval.
func (r *Rechecker) Recheck() (Status, error) {
	r.current(true, false)
	return r.State()
}

// State returns the modes found and, if none works, why.
func (r *Rechecker) State() (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st, r.err
}

// Complete reports whether both address families have a working mode.
func (r *Rechecker) Complete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.complete()
}

// Close closes the current prober and any replaced one still in use; it
// is called after the monitor and discovery have stopped.
func (r *Rechecker) Close() error {
	r.mu.Lock()
	ps := []Prober{r.cur.p}
	for s := range r.retired {
		ps = append(ps, s.p)
	}
	r.retired = map[*slot]bool{}
	r.cur, r.closed = &slot{}, true
	r.mu.Unlock()
	for _, p := range ps {
		closeProber(p)
	}
	return nil
}
