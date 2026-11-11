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
	// later runs f after d; a replaced prober is closed only after
	// retireAfter, so rounds still using it finish normally (crosscheck
	// 2026-11-12-01 C1).
	later func(d time.Duration, f func())

	mu   sync.Mutex
	p    Prober
	st   Status
	err  error
	last time.Time
}

// retireAfter is how long a replaced prober stays open: longer than any
// ping round (count × timeout plus the gaps), so no round fails because
// its prober was closed under it.
const retireAfter = 2 * time.Minute

// NewRechecker detects the modes once with open, then again at most every
// interval (on Ping) while a family is missing, or at once on Recheck.
// onChange, if set, is called with the new state whenever it improves.
func NewRechecker(open func() (Prober, Status, error), every time.Duration, onChange func(Status, error)) *Rechecker {
	r := &Rechecker{open: open, every: every, now: time.Now, onChange: onChange,
		later: func(d time.Duration, f func()) { time.AfterFunc(d, f) }}
	r.p, r.st, r.err = open()
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
func (r *Rechecker) complete() bool { return score(r.p, r.st, r.err) == 2 }

// current returns the prober to use, detecting the modes again first when
// one is missing and force is set or the interval has passed.
func (r *Rechecker) current(force bool) Prober {
	r.mu.Lock()
	if r.complete() || (!force && r.now().Sub(r.last) < r.every) {
		p := r.p
		r.mu.Unlock()
		return p
	}
	r.last = r.now()
	p, st, err := r.open()
	improved := score(p, st, err) > score(r.p, r.st, r.err)
	var old Prober
	if improved {
		old, r.p, r.st, r.err = r.p, p, st, err
	} else if c, ok := p.(io.Closer); ok && p != nil {
		c.Close() // not better: drop the new one
	}
	cur, cst, cerr := r.p, r.st, r.err
	r.mu.Unlock()
	if improved {
		if c, ok := old.(io.Closer); ok && old != nil {
			r.later(retireAfter, func() { c.Close() })
		}
		if r.onChange != nil {
			r.onChange(cst, cerr)
		}
	}
	return cur
}

// Ping implements Prober.
func (r *Rechecker) Ping(ctx context.Context, ip netip.Addr, count int, timeout time.Duration) (Result, error) {
	p := r.current(false)
	if p == nil {
		return Result{}, ErrFamilyUnavailable
	}
	return p.Ping(ctx, ip, count, timeout)
}

// Recheck detects the modes again now if one is missing, and returns the
// state. Discovery calls it before a job, the server every interval.
func (r *Rechecker) Recheck() (Status, error) {
	r.current(true)
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

// Close closes the current prober.
func (r *Rechecker) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.p.(io.Closer); ok && r.p != nil {
		return c.Close()
	}
	return nil
}
