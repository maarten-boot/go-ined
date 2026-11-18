package discovery

import (
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/monitor"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// heldProber answers every address; while held, every ping waits.
type heldProber struct {
	mu   sync.Mutex
	gate chan struct{}
}

func (p *heldProber) hold() { p.mu.Lock(); p.gate = make(chan struct{}); p.mu.Unlock() }
func (p *heldProber) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate != nil {
		close(p.gate)
		p.gate = nil
	}
}

func (p *heldProber) Ping(ctx context.Context, _ netip.Addr, count int, _ time.Duration) (monitor.Result, error) {
	p.mu.Lock()
	g := p.gate
	p.mu.Unlock()
	if g != nil {
		select {
		case <-g:
		case <-ctx.Done():
			return monitor.Result{}, ctx.Err()
		}
	}
	return monitor.Result{Sent: count, Received: count, RTTs: []time.Duration{time.Millisecond}, LastReply: time.Now()}, nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func scheduledRig(t *testing.T, p monitor.Prober) (*rig, *clock) {
	c := &clock{t: time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)}
	return newRig(t, p, Config{Scheduled: true, Tick: 5 * time.Millisecond, Retry: 10 * time.Minute, Now: c.now}), c
}

func (r *rig) rescan(t *testing.T, cidr, every string) apitypes.Group {
	t.Helper()
	g, err := r.svc.CreateGroup(context.Background(), apitypes.GroupCreate{CIDR: cidr})
	if err != nil {
		t.Fatal(err)
	}
	if g, err = r.svc.UpdateGroup(context.Background(), g.ID, apitypes.GroupPatch{Rescan: apitypes.Some(every)}); err != nil {
		t.Fatal(err)
	}
	return g
}

// quiet: no job finishes for a while.
func (r *rig) quiet(t *testing.T, why string) {
	t.Helper()
	select {
	case d := <-r.rec.fin:
		t.Fatalf("%s: a job ran: %+v", why, d)
	case <-time.After(60 * time.Millisecond):
	}
}

func (r *rig) scheduled() int {
	n := 0
	for _, d := range r.m.List() {
		if d.Scheduled {
			n++
		}
	}
	return n
}

// TC-215: a network group with a rescan interval is scanned at once, then
// again when the interval has passed, and not before; the group records
// when and how many hosts were new; a scan by hand of a range covering the
// group counts as its scan; the list shows scheduled jobs.
func TestSchedule(t *testing.T) {
	r, c := scheduledRig(t, &prober{answer: all})
	g := r.rescan(t, "10.5.0.0/30", "1h")
	d := r.finished(t)
	if !d.Scheduled || d.GroupID != g.ID || d.State != apitypes.DiscoveryDone || d.Added != 2 {
		t.Fatalf("first scan: %+v", d)
	}
	got, _ := r.svc.Group(g.ID)
	if got.RescanLast == nil || !got.RescanLast.Equal(c.now()) || got.RescanFound != 2 || got.Rescan != "1h" {
		t.Fatalf("group after the scan: %+v", got)
	}
	r.quiet(t, "not due")
	c.add(59 * time.Minute)
	r.quiet(t, "59 min")
	c.add(time.Minute)
	if d := r.finished(t); !d.Scheduled || d.Added != 0 {
		t.Fatalf("second scan: %+v", d)
	}
	if got, _ := r.svc.Group(g.ID); got.RescanFound != 0 || !got.RescanLast.Equal(c.now()) {
		t.Fatalf("after the second: %+v", got)
	}
	// By hand, over a range containing the group: counts.
	c.add(30 * time.Minute)
	r.start(t, apitypes.DiscoveryCreate{CIDR: "10.5.0.0/29"})
	if d := r.finished(t); d.Scheduled {
		t.Fatalf("manual: %+v", d)
	}
	// The /29 has 10.5.0.3, the /30's broadcast address: new in its range.
	if got, _ := r.svc.Group(g.ID); !got.RescanLast.Equal(c.now()) || got.RescanFound != 1 {
		t.Fatalf("after a manual scan: %+v", got)
	}
	c.add(59 * time.Minute)
	r.quiet(t, "a manual scan counts")
	// By hand, over part of the range: doesn't count.
	r.start(t, apitypes.DiscoveryCreate{CIDR: "10.5.0.1/32"})
	if d := r.finished(t); d.Scheduled {
		t.Fatalf("manual part: %+v", d)
	}
	c.add(time.Minute)
	if d := r.finished(t); !d.Scheduled {
		t.Fatalf("due after a partial manual scan: %+v", d)
	}
	if n := r.scheduled(); n != 3 || len(r.m.List()) != 5 {
		t.Errorf("list: %+v", r.m.List())
	}
	// Off: nothing more.
	if _, err := r.svc.UpdateGroup(context.Background(), g.ID, apitypes.GroupPatch{Rescan: apitypes.Some("off")}); err != nil {
		t.Fatal(err)
	}
	c.add(24 * time.Hour)
	r.quiet(t, "off")
}

// TC-216: one scheduled scan at a time, the longest due first; a scheduled
// scan waits for an overlapping manual one; a manual scan stops an
// overlapping scheduled one, which keeps what it found; a user's cancel
// skips the round; a failed scan is tried again after Retry.
func TestScheduleLoad(t *testing.T) {
	p := &heldProber{}
	r, c := scheduledRig(t, p)
	ctx := context.Background()

	// A manual scan runs: the group's scan waits for it, and then isn't
	// needed.
	p.hold()
	r.start(t, apitypes.DiscoveryCreate{CIDR: "10.6.0.0/24"})
	a := r.rescan(t, "10.6.0.0/30", "1h")
	r.quiet(t, "overlap with a manual scan")
	if n := r.scheduled(); n != 0 {
		t.Fatalf("a scheduled job started over a manual one: %+v", r.m.List())
	}
	p.release()
	if d := r.finished(t); d.Scheduled {
		t.Fatalf("manual: %+v", d)
	}
	r.quiet(t, "covered by the manual scan")

	// Two due: one at a time, the longest due first.
	p.hold()
	b := r.rescan(t, "10.7.0.0/30", "3h") // never scanned: due at once
	c.add(time.Hour)                      // a: due now, b: due since its creation
	time.Sleep(30 * time.Millisecond)
	if l := r.m.List(); r.scheduled() != 1 || l[len(l)-1].GroupID != b.ID {
		t.Fatalf("one at a time, b first: %+v", l)
	}
	p.release()
	if d := r.finished(t); d.GroupID != b.ID {
		t.Fatalf("first: %+v", d)
	}
	if d := r.finished(t); d.GroupID != a.ID {
		t.Fatalf("second: %+v", d)
	}

	// A manual scan stops an overlapping scheduled one.
	p.hold()
	c.add(time.Hour) // a due
	waitFor(t, "a scheduled job", func() bool { return r.running(true) == 1 })
	m, err := r.m.Start(ctx, apitypes.DiscoveryCreate{CIDR: "10.6.0.0/30"})
	if err != nil || m.Scheduled {
		t.Fatalf("manual over scheduled: %+v %v", m, err)
	}
	if d := r.finished(t); !d.Scheduled || d.State != apitypes.DiscoveryCancelled {
		t.Fatalf("the scheduled one: %+v", d)
	}
	p.release()
	if d := r.finished(t); d.ID != m.ID || d.State != apitypes.DiscoveryDone {
		t.Fatalf("the manual one: %+v", d)
	}
	r.quiet(t, "the manual scan counted")

	// A user's cancel skips the round.
	p.hold()
	c.add(time.Hour)
	waitFor(t, "a scheduled job", func() bool { return r.running(true) == 1 })
	for _, d := range r.m.List() {
		if d.State == apitypes.DiscoveryRunning {
			if _, err := r.m.Cancel(d.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if d := r.finished(t); d.State != apitypes.DiscoveryCancelled {
		t.Fatalf("cancelled: %+v", d)
	}
	p.release()
	r.quiet(t, "cancelled by the user")
	c.add(time.Hour)
	if d := r.finished(t); !d.Scheduled || d.GroupID != a.ID || d.State != apitypes.DiscoveryDone {
		t.Fatalf("next round: %+v", d)
	}
}

// TC-216: a scheduled scan that failed is tried again after Retry, not at
// every check.
func TestScheduleRetry(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)}
	r := newRig(t, &prober{answer: all}, Config{Tick: 5 * time.Millisecond, Retry: 10 * time.Minute, Now: c.now})
	r.m.Close()
	r.m = New(failingInv{r.svc}, &prober{answer: all}, "", r.rec, "run", Config{Scheduled: true, Tick: 5 * time.Millisecond, Retry: 10 * time.Minute, Now: c.now, Rate: 10000, Flush: 5 * time.Millisecond}, nil)
	t.Cleanup(r.m.Close)
	r.rescan(t, "10.8.0.0/30", "1h")
	if d := r.finished(t); d.State != apitypes.DiscoveryFailed {
		t.Fatalf("failed: %+v", d)
	}
	r.quiet(t, "retry too early")
	c.add(10 * time.Minute)
	if d := r.finished(t); d.State != apitypes.DiscoveryFailed {
		t.Fatalf("retry: %+v", d)
	}
}

// running counts running jobs, scheduled or by hand.
func (r *rig) running(scheduled bool) int {
	n := 0
	for _, d := range r.m.List() {
		if d.State == apitypes.DiscoveryRunning && d.Scheduled == scheduled {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("no %s", what)
}

// TC-216 (crosscheck 2026-11-18-02 C1): a group whose range became larger
// than max_prefix is passed over with one warning, and the others are
// scanned.
func TestScheduleSkipsTooLarge(t *testing.T) {
	r, _ := scheduledRig(t, &prober{answer: all})
	var buf safeBuf
	r.m.mu.Lock()
	r.m.log = slog.New(slog.NewTextHandler(&buf, nil))
	r.m.cfg.MaxPrefix = 28
	r.m.mu.Unlock()
	big := r.rescan(t, "10.20.0.0/24", "1h") // allowed by the inventory's limit (16)
	small := r.rescan(t, "10.20.1.0/30", "1h")
	if d := r.finished(t); d.GroupID != small.ID {
		t.Fatalf("scanned: %+v", d)
	}
	r.quiet(t, "the large group")
	if g, _ := r.svc.Group(big.ID); g.RescanLast != nil {
		t.Errorf("large group scanned: %+v", g)
	}
	if n := strings.Count(buf.String(), "larger than /28"); n != 1 {
		t.Errorf("%d warnings:\n%s", n, buf.String())
	}
}

// TC-216 (crosscheck 2026-11-18-02 L1, L3): a round skipped by a user's
// cancel ends when the setting changes; a scan whose every probe failed
// doesn't count, and is tried again after Retry.
func TestScheduleWaits(t *testing.T) {
	p := &heldProber{}
	r, c := scheduledRig(t, p)
	ctx := context.Background()
	p.hold()
	g := r.rescan(t, "10.21.0.0/30", "24h")
	waitFor(t, "a scheduled job", func() bool { return r.running(true) == 1 })
	for _, d := range r.m.List() {
		r.m.Cancel(d.ID)
	}
	if d := r.finished(t); d.State != apitypes.DiscoveryCancelled {
		t.Fatalf("cancelled: %+v", d)
	}
	p.release()
	r.quiet(t, "skipped round")
	if _, err := r.svc.UpdateGroup(ctx, g.ID, apitypes.GroupPatch{Rescan: apitypes.Some("6h")}); err != nil {
		t.Fatal(err)
	}
	if d := r.finished(t); d.State != apitypes.DiscoveryDone || d.GroupID != g.ID {
		t.Fatalf("after a new interval: %+v", d)
	}

	// Every probe fails: done, but not counted.
	r2, c2 := scheduledRig(t, errProber{})
	g2 := r2.rescan(t, "10.22.0.1/32", "1h")
	if d := r2.finished(t); d.State != apitypes.DiscoveryDone || d.Errors != 1 {
		t.Fatalf("blind scan: %+v", d)
	}
	if got, _ := r2.svc.Group(g2.ID); got.RescanLast != nil {
		t.Errorf("a blind scan counted: %+v", got)
	}
	r2.quiet(t, "retry too early")
	c2.add(10 * time.Minute)
	if d := r2.finished(t); d.GroupID != g2.ID {
		t.Fatalf("retry: %+v", d)
	}
	_ = c
}
