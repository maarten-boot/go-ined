package dnsname

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"strings"
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
	out := append([]inventory.Target(nil), i.targets...)
	for k := range out {
		out[k].PTRName = i.ptr[out[k].ID]
	}
	return out
}

// SetPTRName stores the name unless the host has another address by now,
// like the inventory.
func (i *inv) SetPTRName(_ context.Context, id int64, ip netip.Addr, name string) (string, bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, t := range i.targets {
		if t.ID == id && t.IP != ip {
			return "", false, nil
		}
	}
	old := i.ptr[id]
	i.sets++
	if old == name {
		return "", false, nil
	}
	i.ptr[id] = name
	return old, true, nil
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
	log *logBuf
}

// logBuf collects the refresher's log lines.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func newRig(t *testing.T, cfg Config, targets ...inventory.Target) *rig {
	t.Helper()
	g := &rig{clk: &clock{t: time.Unix(1000, 0)}, r: &resolver{answers: map[string]func() ([]string, error){}}, inv: &inv{ptr: map[int64]string{}}, log: &logBuf{}}
	g.inv.setTargets(targets...)
	cfg.Now, cfg.Tick = g.clk.Now, time.Millisecond
	f := New(g.r, g.inv, cfg, slog.New(slog.NewTextHandler(g.log, &slog.HandlerOptions{Level: slog.LevelInfo})))
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
	g.r.set("10.0.0.1", nil) // NXDOMAIN: cleared after 3 in a row, a minute apart
	for i := 3; i <= 5; i++ {
		g.clk.Advance(time.Minute)
		waitFor(t, "lookup", func() bool { return g.r.nCalls() == i })
		settle()
	}
	waitFor(t, "cleared", func() bool { n, _ := g.inv.name(1); return n == "" })
}

// TC-406 (v0.34, crosscheck 2026-11-12-04 D2): a known name survives
// "not found" answers until ClearAfter of them in a row, with a recheck
// after Recheck instead of Refresh; a name found again resets the count;
// clearing and the name's return are logged at Info. A host without a
// name isn't rechecked.
func TestClearAfterMisses(t *testing.T) {
	g := newRig(t, Config{Refresh: time.Hour, Recheck: time.Minute, ClearAfter: 3, Sync: time.Millisecond},
		target(1, "10.0.0.1"), target(2, "10.0.0.2"))
	calls := func(addr string) int {
		g.r.mu.Lock()
		defer g.r.mu.Unlock()
		n := 0
		for _, c := range g.r.calls {
			if c == addr {
				n++
			}
		}
		return n
	}
	g.r.set("10.0.0.1", names("nas.example."))
	waitFor(t, "first", func() bool { n, _ := g.inv.name(1); return n == "nas.example." })
	g.clk.Advance(time.Hour) // host 2's first lookup (spread at start), host 1's second
	waitFor(t, "second round", func() bool { return calls("10.0.0.1") == 2 && calls("10.0.0.2") == 1 })
	settle()
	miss := func(want int) {
		t.Helper()
		g.clk.Advance(time.Minute)
		waitFor(t, "recheck", func() bool { return calls("10.0.0.1") == want })
		settle()
	}
	g.r.set("10.0.0.1", nil) // NXDOMAIN from now on
	g.clk.Advance(time.Hour)
	waitFor(t, "miss 1", func() bool { return calls("10.0.0.1") == 3 })
	settle()
	miss(4) // miss 2, one minute later
	if n, _ := g.inv.name(1); n != "nas.example." {
		t.Fatalf("name cleared after 2 misses: %q", n)
	}
	if calls("10.0.0.2") != 2 {
		t.Errorf("host without a name rechecked: %d lookups", calls("10.0.0.2"))
	}
	// Found again: the count starts over.
	g.r.set("10.0.0.1", names("nas.example."))
	miss(5)
	g.r.set("10.0.0.1", nil)
	g.clk.Advance(time.Hour)
	waitFor(t, "miss 1 again", func() bool { return calls("10.0.0.1") == 6 })
	settle()
	miss(7)
	if n, _ := g.inv.name(1); n != "nas.example." {
		t.Fatalf("count not reset: cleared after %q", n)
	}
	miss(8) // the third in a row
	waitFor(t, "cleared", func() bool { n, _ := g.inv.name(1); return n == "" })
	// Cleared: back to the hourly lookups.
	g.clk.Advance(time.Minute)
	settle()
	if c := calls("10.0.0.1"); c != 8 {
		t.Errorf("rechecked after clearing: %d lookups", c)
	}
	g.r.set("10.0.0.1", names("nas.example."))
	g.clk.Advance(time.Hour)
	waitFor(t, "back", func() bool { n, _ := g.inv.name(1); return n == "nas.example." })
	settle()
	log := g.log.String()
	for _, want := range []string{"name cleared", "name=nas.example.", "name back"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Count(log, "level=INFO") != 2 {
		t.Errorf("want 2 Info lines:\n%s", log)
	}
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

// TC-405 (crosscheck 2026-11-12-05 C1): an answer for an address the host
// no longer has is dropped: nothing is logged for it, and the new address
// gets its name with a log line that names the stored one.
func TestIPChangeDuringLookup(t *testing.T) {
	g := newRig(t, Config{Refresh: time.Hour, Sync: time.Millisecond}, target(1, "10.0.0.1"))
	g.r.set("10.0.0.1", names("old.example."))
	waitFor(t, "first", func() bool { n, _ := g.inv.name(1); return n == "old.example." })
	g.r.set("10.0.0.1", names("other.example."))
	g.r.mu.Lock()
	g.r.block = make(chan struct{})
	g.r.mu.Unlock()
	g.clk.Advance(time.Hour)
	waitFor(t, "held", func() bool { return g.r.active.Load() == 1 })
	// The IP changes meanwhile; the inventory clears the name.
	g.inv.mu.Lock()
	g.inv.ptr[1] = ""
	g.inv.mu.Unlock()
	g.inv.setTargets(target(1, "10.0.0.9"))
	g.r.set("10.0.0.9", names("new.example."))
	g.clk.Advance(time.Millisecond)
	settle()
	g.r.mu.Lock()
	close(g.r.block)
	g.r.block = nil
	g.r.mu.Unlock()
	waitFor(t, "new name", func() bool { n, _ := g.inv.name(1); return n == "new.example." })
	settle()
	if log := g.log.String(); strings.Contains(log, "other.example.") || strings.Contains(log, "level=INFO") {
		t.Errorf("stale answer logged, or a first name at Info:\n%s", log)
	}
}
