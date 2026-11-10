package dnsname

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

// resolver answers from a table; block, if set, holds every lookup.
type resolver struct {
	mu      sync.Mutex
	answers map[string]func() ([]string, error)
	calls   []string
	active  atomic.Int32
	maxSeen atomic.Int32
	block   chan struct{}
}

func (r *resolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		m := r.maxSeen.Load()
		if n <= m || r.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	r.mu.Lock()
	r.calls = append(r.calls, addr)
	f, block := r.answers[addr], r.block
	r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f == nil {
		return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
	}
	return f()
}

func (r *resolver) set(addr string, f func() ([]string, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[addr] = f
}

func (r *resolver) nCalls() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }

func names(n ...string) func() ([]string, error) { return func() ([]string, error) { return n, nil } }

type inv struct {
	mu      sync.Mutex
	targets []inventory.Target
	ptr     map[int64]string
	sets    int
}

func (i *inv) DNSTargets() []inventory.Target {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]inventory.Target(nil), i.targets...)
}

func (i *inv) SetPTRName(_ context.Context, id int64, _ netip.Addr, name string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ptr[id] = name
	i.sets++
	return nil
}

func (i *inv) name(id int64) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	n, ok := i.ptr[id]
	return n, ok
}

func (i *inv) setTargets(t ...inventory.Target) { i.mu.Lock(); defer i.mu.Unlock(); i.targets = t }

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

func settle() { time.Sleep(30 * time.Millisecond) }

type rig struct {
	clk *clock
	r   *resolver
	inv *inv
}

func newRig(t *testing.T, cfg Config, targets ...inventory.Target) *rig {
	t.Helper()
	g := &rig{clk: &clock{t: time.Unix(1000, 0)}, r: &resolver{answers: map[string]func() ([]string, error){}}, inv: &inv{ptr: map[int64]string{}}}
	g.inv.setTargets(targets...)
	cfg.Now, cfg.Tick = g.clk.Now, time.Millisecond
	f := New(g.r, g.inv, cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return g
}

// TC-402, TC-406: result rules.
func TestName(t *testing.T) {
	for _, tc := range []struct {
		names []string
		err   error
		want  string
		keep  bool
	}{
		{[]string{"a.example.", "b.example."}, nil, "a.example.", false},
		{nil, nil, "", false},
		{nil, &net.DNSError{Err: "no such host", IsNotFound: true}, "", false},
		{nil, &net.DNSError{Err: "server misbehaving", IsTemporary: true}, "", true},
		{nil, &net.DNSError{Err: "i/o timeout", IsTimeout: true}, "", true},
		{nil, errors.New("other"), "", true},
	} {
		name, keep := Name(tc.names, tc.err)
		if name != tc.want || keep != tc.keep {
			t.Errorf("Name(%v, %v) = %q, %v", tc.names, tc.err, name, keep)
		}
	}
}

// TC-403, TC-406: startup spreads lookups across the refresh period; each
// host is looked up again after dns.refresh; a new host at once.
func TestScheduling(t *testing.T) {
	g := newRig(t, Config{Refresh: 4 * time.Hour, Sync: time.Second},
		target(1, "10.0.0.1"), target(2, "10.0.0.2"), target(3, "10.0.0.3"), target(4, "10.0.0.4"))
	g.r.set("10.0.0.1", names("one.example."))
	for i := 1; i <= 4; i++ {
		waitFor(t, "lookup", func() bool { return g.r.nCalls() == i })
		settle()
		if n := g.r.nCalls(); n != i {
			t.Fatalf("after %dh: %d lookups (not spread)", i-1, n)
		}
		g.clk.Advance(time.Hour)
	}
	waitFor(t, "refresh of host 1", func() bool { return g.r.nCalls() == 5 })
	if n, _ := g.inv.name(1); n != "one.example." {
		t.Errorf("host 1 name %q", n)
	}
	// A new host is looked up at the next sync, without waiting.
	g.inv.setTargets(target(1, "10.0.0.1"), target(2, "10.0.0.2"), target(3, "10.0.0.3"), target(4, "10.0.0.4"), target(5, "10.0.0.5"))
	g.clk.Advance(time.Second)
	waitFor(t, "new host", func() bool { return g.r.nCalls() == 6 })
}

// TC-402: a temporary error keeps the name; NXDOMAIN clears it.
func TestKeepAndClear(t *testing.T) {
	g := newRig(t, Config{Refresh: time.Minute}, target(1, "10.0.0.1"))
	g.r.set("10.0.0.1", names("old.example."))
	waitFor(t, "first", func() bool { n, _ := g.inv.name(1); return n == "old.example." })
	g.r.set("10.0.0.1", func() ([]string, error) { return nil, &net.DNSError{Err: "timeout", IsTimeout: true} })
	sets := g.inv.sets
	g.clk.Advance(time.Minute)
	waitFor(t, "second lookup", func() bool { return g.r.nCalls() == 2 })
	settle()
	if n, _ := g.inv.name(1); n != "old.example." || g.inv.sets != sets {
		t.Fatalf("temporary error changed the name to %q", n)
	}
	g.r.set("10.0.0.1", nil) // NXDOMAIN
	g.clk.Advance(time.Minute)
	waitFor(t, "cleared", func() bool { n, _ := g.inv.name(1); return n == "" })
}

// TC-405: an IP change triggers a lookup of the new address at once.
func TestIPChange(t *testing.T) {
	g := newRig(t, Config{Refresh: time.Hour, Sync: time.Second}, target(1, "10.0.0.1"))
	waitFor(t, "first", func() bool { return g.r.nCalls() == 1 })
	g.inv.setTargets(target(1, "10.0.0.9"))
	g.clk.Advance(time.Second)
	waitFor(t, "lookup of the new IP", func() bool { return g.r.nCalls() == 2 })
	g.r.mu.Lock()
	defer g.r.mu.Unlock()
	if g.r.calls[1] != "10.0.0.9" {
		t.Fatalf("calls %v", g.r.calls)
	}
}

// TC-404: no more than dns.concurrency lookups at once.
func TestConcurrency(t *testing.T) {
	var ts []inventory.Target
	for i := 1; i <= 30; i++ {
		ts = append(ts, target(int64(i), netip.AddrFrom4([4]byte{10, 0, 1, byte(i)}).String()))
	}
	block := make(chan struct{})
	g := &rig{clk: &clock{t: time.Unix(1000, 0)}, r: &resolver{answers: map[string]func() ([]string, error){}, block: block}, inv: &inv{ptr: map[int64]string{}}}
	g.inv.setTargets(ts...)
	f := New(g.r, g.inv, Config{Refresh: time.Nanosecond * 30, Concurrency: 4, Now: g.clk.Now, Tick: time.Millisecond}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "first lookup", func() bool { return g.r.nCalls() == 1 }) // Run has started
	g.clk.Advance(time.Second)                                           // every host is due
	waitFor(t, "4 active", func() bool { return g.r.active.Load() == 4 })
	settle()
	if m := g.r.maxSeen.Load(); m != 4 {
		t.Fatalf("max concurrent lookups %d, want 4", m)
	}
	close(block)
	waitFor(t, "all looked up", func() bool { return g.r.nCalls() >= 30 })
	if m := g.r.maxSeen.Load(); m > 4 {
		t.Fatalf("max concurrent lookups %d", m)
	}
}

// TC-408: hosts waiting for a lookup slot don't each hold a goroutine.
func TestNoGoroutinePerWaitingHost(t *testing.T) {
	var ts []inventory.Target
	for i := 1; i <= 50; i++ {
		ts = append(ts, target(int64(i), netip.AddrFrom4([4]byte{10, 0, 2, byte(i)}).String()))
	}
	block := make(chan struct{})
	defer close(block)
	r := &resolver{answers: map[string]func() ([]string, error){}, block: block}
	in := &inv{ptr: map[int64]string{}}
	in.setTargets(ts...)
	clk := &clock{t: time.Unix(1000, 0)}
	base := runtime.NumGoroutine()
	f := New(r, in, Config{Refresh: time.Hour, Concurrency: 4, Now: clk.Now, Tick: time.Millisecond}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "first lookup", func() bool { return r.nCalls() == 1 })
	clk.Advance(2 * time.Hour) // all 50 hosts are due
	waitFor(t, "4 active", func() bool { return r.active.Load() == 4 })
	settle()
	// Run, its ticker and 4 workers; allow a few for the runtime.
	if n := runtime.NumGoroutine() - base; n > 10 {
		t.Fatalf("%d extra goroutines for 4 slots and 50 due hosts", n)
	}
}
