package monitor

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// recheckProber is a fake prober that records Close. With hold set, a
// Ping says it started and then waits until hold is closed.
type recheckProber struct {
	name    string
	mu      sync.Mutex
	closes  int
	started chan struct{}
	hold    chan struct{}
}

func (p *recheckProber) Ping(context.Context, netip.Addr, int, time.Duration) (Result, error) {
	if p.hold != nil {
		p.started <- struct{}{}
		<-p.hold
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closes > 0 {
		return Result{}, errors.New("prober is closed")
	}
	return Result{Sent: 1, Received: 1}, nil
}
func (p *recheckProber) Close() error { p.mu.Lock(); p.closes++; p.mu.Unlock(); return nil }
func (p *recheckProber) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closes > 0
}

// TC-315: while ICMP is missing, the Rechecker tries again at most every
// interval (or at once on Recheck) and switches when it gets better.
func TestRechecker(t *testing.T) {
	var mu sync.Mutex
	opens := 0
	answer := func() (Prober, Status, error) { return nil, Status{}, errors.New("socket: permission denied") }
	open := func() (Prober, Status, error) {
		mu.Lock()
		defer mu.Unlock()
		opens++
		return answer()
	}
	var changes []Status
	now := time.Unix(1000, 0)
	r := NewRechecker(open, time.Minute, func(st Status, _ error) { changes = append(changes, st) })
	r.now, r.last = func() time.Time { return now }, now
	ip := netip.MustParseAddr("192.0.2.1")
	ctx := context.Background()

	if _, err := r.Ping(ctx, ip, 1, time.Second); !errors.Is(err, ErrFamilyUnavailable) {
		t.Fatalf("no mode: %v", err)
	}
	if _, err := r.State(); err == nil || opens != 1 {
		t.Fatalf("state: %v, %d opens", err, opens)
	}

	// The sysctl is fixed, but the interval hasn't passed: no new attempt.
	v4only := &recheckProber{name: "v4"}
	mu.Lock()
	answer = func() (Prober, Status, error) {
		return v4only, Status{Mode: "unprivileged", IPv6Error: "v6 denied"}, nil
	}
	mu.Unlock()
	if _, err := r.Ping(ctx, ip, 1, time.Second); !errors.Is(err, ErrFamilyUnavailable) || opens != 1 {
		t.Fatalf("before the interval: %v, %d opens", err, opens)
	}
	// After the interval, a Ping detects again and uses the new prober.
	now = now.Add(time.Minute)
	if res, err := r.Ping(ctx, ip, 1, time.Second); err != nil || res.Received != 1 || opens != 2 {
		t.Fatalf("after the interval: %+v %v, %d opens", res, err, opens)
	}
	if len(changes) != 1 || changes[0].Mode != "unprivileged" || r.Complete() {
		t.Fatalf("changes: %+v, complete %v", changes, r.Complete())
	}

	// IPv6 still missing: Recheck tries at once; a result that isn't
	// better is dropped (and closed).
	same := &recheckProber{name: "same"}
	mu.Lock()
	answer = func() (Prober, Status, error) { return same, Status{Mode: "unprivileged", IPv6Error: "v6 denied"}, nil }
	mu.Unlock()
	if st, err := r.Recheck(); err != nil || st.IPv6Error == "" || opens != 3 || !same.isClosed() || len(changes) != 1 {
		t.Fatalf("recheck, not better: %+v %v, %d opens, closed %v", st, err, opens, same.isClosed())
	}
	// Both families now, while a round runs on the old prober: adopted;
	// the old prober is closed only when that round has ended, however
	// long it takes (crosscheck 2026-11-12-02 C2).
	v4only.mu.Lock()
	v4only.started, v4only.hold = make(chan struct{}), make(chan struct{})
	v4only.mu.Unlock()
	type out struct {
		res Result
		err error
	}
	running := make(chan out, 1)
	go func() {
		res, err := r.Ping(ctx, ip, 1, time.Second)
		running <- out{res, err}
	}()
	<-v4only.started
	both := &recheckProber{name: "both"}
	mu.Lock()
	answer = func() (Prober, Status, error) { return both, Status{Mode: "unprivileged"}, nil }
	mu.Unlock()
	if st, _ := r.Recheck(); st.IPv6Error != "" || v4only.isClosed() || !r.Complete() || len(changes) != 2 {
		t.Fatalf("recheck, better: %+v, old closed during its round %v", st, v4only.isClosed())
	}
	// New rounds use the new prober while the old round still runs.
	if res, err := r.Ping(ctx, ip, 1, time.Second); err != nil || res.Received != 1 {
		t.Fatalf("new round: %+v %v", res, err)
	}
	if v4only.isClosed() {
		t.Fatal("old prober closed while its round runs")
	}
	close(v4only.hold)
	if o := <-running; o.err != nil || o.res.Received != 1 {
		t.Fatalf("round on the replaced prober: %+v %v", o.res, o.err)
	}
	if !v4only.isClosed() {
		t.Error("replaced prober not closed after its last round")
	}
	now = now.Add(time.Hour)
	r.Ping(ctx, ip, 1, time.Second)
	r.Recheck()
	if opens != 4 {
		t.Errorf("attempts after complete: %d opens", opens)
	}
	r.Close()
	if !both.isClosed() {
		t.Error("Close didn't close the prober")
	}
	// After Close nothing is opened again.
	now = now.Add(time.Hour)
	if _, err := r.Ping(ctx, ip, 1, time.Second); !errors.Is(err, ErrFamilyUnavailable) || opens != 4 {
		t.Errorf("after Close: %v, %d opens", err, opens)
	}
}

// TC-315: a replaced prober without running rounds is closed at once, and
// Close also closes a replaced prober whose round hasn't ended.
func TestRecheckerRetire(t *testing.T) {
	first := &recheckProber{name: "v4", started: make(chan struct{}), hold: make(chan struct{})}
	next := first
	var nextSt Status
	open := func() (Prober, Status, error) { return next, nextSt, nil }
	nextSt = Status{Mode: "unprivileged", IPv6Error: "v6 denied"}
	r := NewRechecker(open, time.Minute, nil)
	ip := netip.MustParseAddr("192.0.2.1")
	done := make(chan struct{})
	go func() { r.Ping(context.Background(), ip, 1, time.Second); close(done) }()
	<-first.started

	second := &recheckProber{name: "both"}
	next, nextSt = second, Status{Mode: "unprivileged"}
	r.Recheck()
	if first.isClosed() {
		t.Fatal("closed during its round")
	}
	r.Close()
	if !first.isClosed() || !second.isClosed() {
		t.Fatalf("Close: replaced %v, current %v", first.isClosed(), second.isClosed())
	}
	close(first.hold)
	<-done
	first.mu.Lock()
	n := first.closes
	first.mu.Unlock()
	if n != 1 {
		t.Errorf("replaced prober closed %d times", n)
	}

	// Nothing running on it: closed at once.
	a := &recheckProber{name: "a"}
	b := &recheckProber{name: "b"}
	next, nextSt = a, Status{Mode: "unprivileged", IPv6Error: "v6 denied"}
	r = NewRechecker(open, time.Minute, nil)
	next, nextSt = b, Status{Mode: "unprivileged"}
	r.Recheck()
	if !a.isClosed() || b.isClosed() {
		t.Errorf("idle replace: old closed %v, new closed %v", a.isClosed(), b.isClosed())
	}
}
