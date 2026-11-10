package monitor

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// ---- fakes ----

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

type ping struct {
	ip netip.Addr
	at time.Time
}

type fakeProber struct {
	clk   *clock
	mu    sync.Mutex
	pings []ping
	reply map[netip.Addr]bool
	block chan struct{} // if not nil, Ping waits for it or ctx
}

func (p *fakeProber) Ping(ctx context.Context, ip netip.Addr, count int, _ time.Duration) (Result, error) {
	p.mu.Lock()
	p.pings = append(p.pings, ping{ip, p.clk.Now()})
	ok, block := p.reply[ip], p.block
	p.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	if !ok {
		return Result{Sent: count}, nil
	}
	return Result{Sent: count, Received: count, RTTs: []time.Duration{time.Millisecond, 3 * time.Millisecond}}, nil
}

func (p *fakeProber) got() []ping {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ping(nil), p.pings...)
}

type fakeInv struct {
	mu      sync.Mutex
	targets []inventory.Target
	batches [][]inventory.Observation
}

func (f *fakeInv) MonitorTargets() []inventory.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]inventory.Target(nil), f.targets...)
}

func (f *fakeInv) ApplyObservations(_ context.Context, obs []inventory.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, obs)
	return nil
}

func (f *fakeInv) set(t ...inventory.Target) { f.mu.Lock(); defer f.mu.Unlock(); f.targets = t }

func (f *fakeInv) all() []inventory.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []inventory.Observation
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

func target(id int64, ip string) inventory.Target {
	return inventory.Target{ID: id, IP: netip.MustParseAddr(ip)}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// settle lets the scheduler run a few real ticks without changing time.
func settle() { time.Sleep(30 * time.Millisecond) }

type rig struct {
	clk  *clock
	p    *fakeProber
	inv  *fakeInv
	stop func()
}

func newRig(t *testing.T, cfg Config, targets ...inventory.Target) *rig {
	t.Helper()
	r := &rig{clk: &clock{t: time.Unix(1000, 0)}, inv: &fakeInv{}}
	r.p = &fakeProber{clk: r.clk, reply: map[netip.Addr]bool{}}
	r.inv.set(targets...)
	cfg.Now = r.clk.Now
	cfg.Tick = time.Millisecond
	m := New(r.p, r.inv, cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	var once sync.Once
	r.stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(r.stop)
	return r
}

// ---- tests ----

// TC-301: up on any reply; down only after down_after failed rounds in a
// row; before that the status is kept (unknown before the first reply).
func TestNextStatus(t *testing.T) {
	type step struct {
		replied bool
		want    string
	}
	for name, tc := range map[string]struct {
		downAfter int
		steps     []step
	}{
		"default 2":    {2, []step{{false, ""}, {false, "down"}, {false, "down"}, {true, "up"}, {false, ""}, {true, "up"}, {false, ""}, {false, "down"}}},
		"down at once": {1, []step{{false, "down"}, {true, "up"}, {false, "down"}}},
		"3 rounds":     {3, []step{{false, ""}, {false, ""}, {false, "down"}}},
	} {
		streak := 0
		for i, s := range tc.steps {
			var got string
			got, streak = NextStatus(s.replied, streak, tc.downAfter)
			if got != s.want {
				t.Errorf("%s: step %d: got %q, want %q", name, i, got, s.want)
			}
		}
	}
}

// TC-303: average round-trip time and loss.
func TestSummarize(t *testing.T) {
	rtt, loss := Summarize(Result{Sent: 3, Received: 2, RTTs: []time.Duration{time.Millisecond, 2 * time.Millisecond}})
	if rtt == nil || *rtt != 1.5 || loss != 100.0/3 {
		t.Fatalf("got %v, %v", rtt, loss)
	}
	rtt, loss = Summarize(Result{Sent: 3})
	if rtt != nil || loss != 100 {
		t.Fatalf("no replies: got %v, %v", rtt, loss)
	}
}

// TC-302: start times are spread across the interval; each host is then
// pinged once per interval.
func TestSpread(t *testing.T) {
	r := newRig(t, Config{Interval: 4 * time.Second},
		target(1, "10.0.0.1"), target(2, "10.0.0.2"), target(3, "10.0.0.3"), target(4, "10.0.0.4"))
	for i := 1; i <= 4; i++ {
		waitFor(t, "ping", func() bool { return len(r.p.got()) == i })
		settle()
		if n := len(r.p.got()); n != i {
			t.Fatalf("at +%ds: %d pings, want %d (not spread)", i-1, n, i)
		}
		r.clk.Advance(time.Second)
	}
	// +4s: host 1's second round.
	waitFor(t, "second round", func() bool { return len(r.p.got()) == 5 })
	got := r.p.got()
	for i, want := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.1"} {
		if got[i].ip.String() != want || !got[i].at.Equal(time.Unix(1000+int64(i), 0)) {
			t.Errorf("ping %d = %v at %v", i, got[i].ip, got[i].at.Unix())
		}
	}
}

// TC-307: an unmonitored host is never pinged; turning it back on
// schedules it.
func TestUnmonitoredNotPinged(t *testing.T) {
	r := newRig(t, Config{Interval: time.Second, Sync: time.Second}, target(1, "10.0.0.1"))
	waitFor(t, "first ping", func() bool { return len(r.p.got()) == 1 })
	r.inv.set() // host 1 turned off
	r.clk.Advance(time.Second)
	settle()
	r.clk.Advance(time.Second)
	settle()
	if n := len(r.p.got()); n != 1 {
		t.Fatalf("unmonitored host pinged: %d pings", n)
	}
	r.inv.set(target(1, "10.0.0.1"))
	r.clk.Advance(time.Second)
	waitFor(t, "re-enabled ping", func() bool { return len(r.p.got()) == 2 })
}

// TC-301, TC-303, TC-308: rounds turn into observations, saved in batches.
func TestObservations(t *testing.T) {
	r := newRig(t, Config{Interval: time.Second, Flush: time.Second, DownAfter: 2},
		target(1, "10.0.0.1"), target(2, "10.0.0.2"))
	r.p.mu.Lock()
	r.p.reply[netip.MustParseAddr("10.0.0.1")] = true
	r.p.mu.Unlock()
	// Two rounds each (host 2 starts half an interval later): host 1 up,
	// host 2 unknown then down.
	for n := 1; n <= 4; n++ {
		waitFor(t, "round", func() bool { return len(r.p.got()) == n })
		settle()
		r.clk.Advance(500 * time.Millisecond)
	}
	waitFor(t, "saved", func() bool { return len(r.inv.all()) >= 4 })
	r.stop()
	var h1, h2 []string
	for _, o := range r.inv.all() {
		switch o.HostID {
		case 1:
			if !o.Replied || o.RTTms == nil || *o.RTTms != 2 || o.LossPct != 0 {
				t.Errorf("host 1: %+v", o)
			}
			h1 = append(h1, o.Status)
		case 2:
			if o.Replied || o.RTTms != nil || o.LossPct != 100 {
				t.Errorf("host 2: %+v", o)
			}
			h2 = append(h2, o.Status)
		}
	}
	if len(h1) < 2 || h1[0] != apitypes.StatusUp || h1[1] != apitypes.StatusUp {
		t.Errorf("host 1 statuses %q", h1)
	}
	if len(h2) < 2 || h2[0] != "" || h2[1] != apitypes.StatusDown {
		t.Errorf("host 2 statuses %q", h2)
	}
	// Results of one flush period form one batch.
	r.inv.mu.Lock()
	defer r.inv.mu.Unlock()
	if len(r.inv.batches) == 0 || len(r.inv.batches[0]) != 2 {
		t.Errorf("first batch has %d results, want 2", len(r.inv.batches[0]))
	}
}

// TC-802 (monitor part): stopping waits for rounds in flight, drops the
// cancelled ones and saves pending results.
func TestStopSavesPending(t *testing.T) {
	r := newRig(t, Config{Interval: time.Second, Flush: time.Hour},
		target(1, "10.0.0.1"), target(2, "10.0.0.2"))
	waitFor(t, "host 1", func() bool { return len(r.p.got()) == 1 })
	settle() // host 1's result is now pending (flush is an hour away)
	block := make(chan struct{})
	r.p.mu.Lock()
	r.p.block = block
	r.p.mu.Unlock()
	r.clk.Advance(500 * time.Millisecond) // host 2 starts and blocks
	waitFor(t, "host 2", func() bool { return len(r.p.got()) == 2 })
	if len(r.inv.all()) != 0 {
		t.Fatal("saved before the flush period")
	}
	r.stop()
	obs := r.inv.all()
	if len(obs) != 1 || obs[0].HostID != 1 {
		t.Fatalf("saved at stop: %+v", obs)
	}
}

// A prober error counts as a failed round.
func TestProberError(t *testing.T) {
	inv := &fakeInv{}
	inv.set(target(1, "10.0.0.1"))
	m := New(errProber{}, inv, Config{Interval: time.Hour, Flush: time.Millisecond, Tick: time.Millisecond, DownAfter: 1}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "result", func() bool { return len(inv.all()) == 1 })
	if o := inv.all()[0]; o.Status != apitypes.StatusDown || o.LossPct != 100 || o.Replied {
		t.Fatalf("got %+v", o)
	}
}

type errProber struct{}

func (errProber) Ping(context.Context, netip.Addr, int, time.Duration) (Result, error) {
	return Result{}, errors.New("sendto: network is unreachable")
}
