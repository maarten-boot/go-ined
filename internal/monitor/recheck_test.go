package monitor

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// recheckProber is a fake prober that records Close.
type recheckProber struct {
	name   string
	closed bool
}

func (p *recheckProber) Ping(context.Context, netip.Addr, int, time.Duration) (Result, error) {
	return Result{Sent: 1, Received: 1}, nil
}
func (p *recheckProber) Close() error { p.closed = true; return nil }

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
	var retire []func() // closes scheduled for later
	var retireAfterSeen time.Duration
	r.later = func(d time.Duration, f func()) { retireAfterSeen = d; retire = append(retire, f) }
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
	if st, err := r.Recheck(); err != nil || st.IPv6Error == "" || opens != 3 || !same.closed || len(changes) != 1 {
		t.Fatalf("recheck, not better: %+v %v, %d opens, closed %v", st, err, opens, same.closed)
	}
	// Both families now: adopted, the old prober closed; afterwards no
	// more attempts.
	both := &recheckProber{name: "both"}
	mu.Lock()
	answer = func() (Prober, Status, error) { return both, Status{Mode: "unprivileged"}, nil }
	mu.Unlock()
	if st, _ := r.Recheck(); st.IPv6Error != "" || v4only.closed || !r.Complete() || len(changes) != 2 {
		t.Fatalf("recheck, better: %+v, old closed at once %v", st, v4only.closed)
	}
	// The replaced prober is closed later, so rounds on it can finish.
	if len(retire) != 1 || retireAfterSeen != retireAfter {
		t.Fatalf("retire: %d scheduled, after %v", len(retire), retireAfterSeen)
	}
	retire[0]()
	if !v4only.closed {
		t.Error("replaced prober never closed")
	}
	now = now.Add(time.Hour)
	r.Ping(ctx, ip, 1, time.Second)
	r.Recheck()
	if opens != 4 {
		t.Errorf("attempts after complete: %d opens", opens)
	}
	r.Close()
	if !both.closed {
		t.Error("Close didn't close the prober")
	}
}
