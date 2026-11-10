package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/monitor"
	"github.com/maarten-boot/go-ined/internal/store"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// ---- fakes ----

// prober answers by rule: answer(ip, try) reports whether try 1 or 2 of
// that address gets a reply. gate, if set, holds every ping until closed.
type prober struct {
	answer func(ip netip.Addr, try int) bool
	gate   chan struct{}
	mu     sync.Mutex
	tries  map[netip.Addr]int
}

func (p *prober) Ping(ctx context.Context, ip netip.Addr, count int, _ time.Duration) (monitor.Result, error) {
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return monitor.Result{}, ctx.Err()
		}
	}
	p.mu.Lock()
	if p.tries == nil {
		p.tries = map[netip.Addr]int{}
	}
	p.tries[ip]++
	try := p.tries[ip]
	p.mu.Unlock()
	if !p.answer(ip, try) {
		return monitor.Result{Sent: count}, nil
	}
	return monitor.Result{Sent: count, Received: count, RTTs: []time.Duration{2 * time.Millisecond}, LastReply: time.Now()}, nil
}

func all(netip.Addr, int) bool { return true }

type recorder struct {
	mu  sync.Mutex
	evs []string
	fin chan apitypes.Discovery
}

func (r *recorder) Publish(typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, typ)
	if d, ok := data.(apitypes.Discovery); ok && typ == apitypes.EventDiscoveryFinished {
		r.fin <- d
	}
}

func (r *recorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.evs {
		if e == typ {
			n++
		}
	}
	return n
}

type rig struct {
	svc *inventory.Service
	m   *Manager
	rec *recorder
}

func newRig(t *testing.T, p monitor.Prober, cfg Config) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := inventory.NewService(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{fin: make(chan apitypes.Discovery, 10)}
	if cfg.Rate == 0 {
		cfg.Rate = 10000
	}
	if cfg.SecondTry == 0 {
		cfg.SecondTry = time.Millisecond
	}
	if cfg.Flush == 0 {
		cfg.Flush = 20 * time.Millisecond
	}
	m := New(svc, p, "no ICMP (test)", rec, "run", cfg, nil)
	t.Cleanup(m.Close)
	return &rig{svc: svc, m: m, rec: rec}
}

func (r *rig) start(t *testing.T, in apitypes.DiscoveryCreate) apitypes.Discovery {
	t.Helper()
	d, err := r.m.Start(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (r *rig) finished(t *testing.T) apitypes.Discovery {
	t.Helper()
	select {
	case d := <-r.rec.fin:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("no discovery.finished")
		return apitypes.Discovery{}
	}
}

func wantKind(t *testing.T, err error, k inventory.Kind) {
	t.Helper()
	if inventory.KindOf(err) != k {
		t.Fatalf("err = %v (kind %d), want kind %d", err, inventory.KindOf(err), k)
	}
}

func hostsByIP(svc *inventory.Service) map[string]apitypes.Host {
	out := map[string]apitypes.Host{}
	for _, h := range svc.Hosts() {
		out[h.IP] = h
	}
	return out
}

// ---- tests ----

// TC-201: CIDR expansion.
func TestAddresses(t *testing.T) {
	for _, tc := range []struct {
		cidr, first string
		n           int
	}{
		{"192.168.1.0/24", "192.168.1.1", 254},
		{"10.0.0.0/30", "10.0.0.1", 2},
		{"10.0.0.0/31", "10.0.0.0", 2},
		{"10.0.0.7/32", "10.0.0.7", 1},
		{"10.0.0.0/16", "10.0.0.1", 65534},
	} {
		first, n := Addresses(netip.MustParsePrefix(tc.cidr))
		if first.String() != tc.first || n != tc.n {
			t.Errorf("%s: %s, %d; want %s, %d", tc.cidr, first, n, tc.first, tc.n)
		}
	}
}

// TC-202: too large a range is rejected; the rate is respected.
func TestMaxPrefixAndRate(t *testing.T) {
	r := newRig(t, &prober{answer: all}, Config{MaxPrefix: 24, Rate: 100})
	_, err := r.m.Start(context.Background(), apitypes.DiscoveryCreate{CIDR: "10.0.0.0/23"})
	wantKind(t, err, inventory.Invalid)
	if !strings.Contains(err.Error(), "/24") {
		t.Errorf("message: %v", err)
	}
	if _, ok := r.svc.NetworkGroupByCIDR(netip.MustParsePrefix("10.0.0.0/23")); ok {
		t.Error("group created for a rejected job")
	}
	start := time.Now()
	r.start(t, apitypes.DiscoveryCreate{CIDR: "10.0.0.0/27"}) // 30 addresses at 100/s
	d := r.finished(t)
	if el := time.Since(start); el < 280*time.Millisecond {
		t.Errorf("30 addresses at 100/s took only %v", el)
	}
	if d.Probed != 30 || d.Found != 30 {
		t.Errorf("job: %+v", d)
	}
}

// TC-203, TC-210: responders are added as up hosts; an existing IP is
// neither duplicated nor moved; loss reflects a second try.
func TestAddsResponders(t *testing.T) {
	p := &prober{answer: func(ip netip.Addr, try int) bool {
		switch ip.As4()[3] {
		case 1, 2:
			return true
		case 3:
			return try == 2 // answers only the second request
		default:
			return false
		}
	}}
	r := newRig(t, p, Config{})
	other, _ := r.svc.CreateGroup(context.Background(), apitypes.GroupCreate{Name: "elsewhere"})
	if _, err := r.svc.CreateHost(context.Background(), apitypes.HostCreate{IP: "10.1.0.2", ParentID: &other.ID}); err != nil {
		t.Fatal(err)
	}
	d := r.start(t, apitypes.DiscoveryCreate{CIDR: "10.1.0.0/29"})
	if d.State != apitypes.DiscoveryRunning || d.Total != 6 || !strings.HasPrefix(d.ID, "run-") {
		t.Fatalf("start: %+v", d)
	}
	d = r.finished(t)
	if d.State != apitypes.DiscoveryDone || d.Probed != 6 || d.Found != 3 || d.Added != 2 || d.FinishedAt == nil {
		t.Fatalf("finished: %+v", d)
	}
	hs := hostsByIP(r.svc)
	if len(hs) != 3 {
		t.Fatalf("hosts: %v", hs)
	}
	if h := hs["10.1.0.2"]; *h.ParentID != other.ID {
		t.Errorf("existing host moved: %+v", h)
	}
	for ip, loss := range map[string]float64{"10.1.0.1": 0, "10.1.0.3": 50} {
		h := hs[ip]
		if h.ParentID == nil || *h.ParentID != d.GroupID || !h.Monitored || h.Status != apitypes.StatusUp ||
			h.RTTms == nil || *h.RTTms != 2 || *h.LossPct != loss || h.LastSeen == nil || h.Stale {
			t.Errorf("%s: %+v", ip, h)
		}
	}
	g, _ := r.svc.Group(d.GroupID)
	if g.CIDR != "10.1.0.0/29" || g.Name != "10.1.0.0/29" || g.ParentID != nil || g.Status != apitypes.StatusUp {
		t.Errorf("group: %+v", g)
	}
}

// TC-204: progress events, cancel, and the finished states.
func TestProgressCancelFail(t *testing.T) {
	p := &prober{answer: all, gate: make(chan struct{})}
	r := newRig(t, p, Config{Progress: time.Millisecond, Concurrency: 2})
	d := r.start(t, apitypes.DiscoveryCreate{CIDR: "10.2.0.0/24"})
	time.Sleep(50 * time.Millisecond)
	if r.rec.count(apitypes.EventDiscoveryProgress) == 0 {
		t.Error("no progress event")
	}
	got, err := r.m.Cancel(d.ID)
	if err != nil || got.ID != d.ID {
		t.Fatalf("cancel: %v %+v", err, got)
	}
	d = r.finished(t)
	if d.State != apitypes.DiscoveryCancelled || d.Probed != 0 {
		t.Fatalf("after cancel: %+v", d)
	}
	if g, _ := r.m.Get(d.ID); g.State != apitypes.DiscoveryCancelled {
		t.Errorf("Get: %+v", g)
	}
	_, err = r.m.Cancel("nope")
	wantKind(t, err, inventory.NotFound)

	// Failed: the inventory can't save.
	f := newRig(t, &prober{answer: all}, Config{})
	f.m.inv = failingInv{f.svc}
	f.start(t, apitypes.DiscoveryCreate{CIDR: "10.3.0.0/30"})
	d = f.finished(t)
	if d.State != apitypes.DiscoveryFailed || !strings.Contains(d.Error, "disk full") {
		t.Fatalf("failed job: %+v", d)
	}
}

type failingInv struct{ *inventory.Service }

func (failingInv) AddDiscovered(context.Context, int64, []inventory.Found) (int, error) {
	return 0, errors.New("disk full")
}

// TC-205, TC-209: by group id; by CIDR reusing a group or creating one
// under the target; hosts go into the most specific subgroup.
func TestTargets(t *testing.T) {
	r := newRig(t, &prober{answer: func(ip netip.Addr, _ int) bool { return ip.As4()[3] == 5 }}, Config{MaxPrefix: 16})
	ctx := context.Background()
	top, _ := r.svc.CreateGroup(ctx, apitypes.GroupCreate{Name: "sites"})
	wide, _ := r.svc.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.4.0.0/22", ParentID: &top.ID})
	sub, _ := r.svc.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.4.1.0/24", ParentID: &wide.ID})

	// By group id, with a subgroup inside (TC-209).
	d := r.start(t, apitypes.DiscoveryCreate{GroupID: &wide.ID})
	if d.CIDR != "10.4.0.0/22" || d.GroupID != wide.ID {
		t.Fatalf("by id: %+v", d)
	}
	r.finished(t)
	hs := hostsByIP(r.svc)
	if *hs["10.4.1.5"].ParentID != sub.ID || *hs["10.4.2.5"].ParentID != wide.ID {
		t.Fatalf("placement: 10.4.1.5 in %d, 10.4.2.5 in %d", *hs["10.4.1.5"].ParentID, *hs["10.4.2.5"].ParentID)
	}

	// By CIDR: an existing group is reused (parent_id ignored).
	d = r.start(t, apitypes.DiscoveryCreate{CIDR: "10.4.1.0/24", ParentID: &top.ID})
	if d.GroupID != sub.ID {
		t.Fatalf("reuse: %+v", d)
	}
	r.finished(t)
	// By CIDR: a new group under the target, or at the root.
	d = r.start(t, apitypes.DiscoveryCreate{CIDR: "10.5.0.0/24", ParentID: &top.ID})
	r.finished(t)
	if g, _ := r.svc.Group(d.GroupID); g.ParentID == nil || *g.ParentID != top.ID || g.Name != "10.5.0.0/24" {
		t.Fatalf("new under target: %+v", g)
	}
	d = r.start(t, apitypes.DiscoveryCreate{CIDR: "10.6.0.0/24"})
	r.finished(t)
	if g, _ := r.svc.Group(d.GroupID); g.ParentID != nil {
		t.Fatalf("new at root: %+v", g)
	}

	// Errors.
	_, err := r.m.Start(ctx, apitypes.DiscoveryCreate{GroupID: &top.ID})
	wantKind(t, err, inventory.Invalid) // not a network group
	missing := int64(999)
	_, err = r.m.Start(ctx, apitypes.DiscoveryCreate{GroupID: &missing})
	wantKind(t, err, inventory.NotFound)
	_, err = r.m.Start(ctx, apitypes.DiscoveryCreate{})
	wantKind(t, err, inventory.Invalid)
	_, err = r.m.Start(ctx, apitypes.DiscoveryCreate{CIDR: "10.7.0.0/24", GroupID: &wide.ID})
	wantKind(t, err, inventory.Invalid)
	_, err = r.m.Start(ctx, apitypes.DiscoveryCreate{CIDR: "10.7.0.5/24"})
	wantKind(t, err, inventory.Invalid) // host bits set
}

// TC-207: IPv6 is rejected, by CIDR and by group.
func TestIPv6Rejected(t *testing.T) {
	r := newRig(t, &prober{answer: all}, Config{})
	_, err := r.m.Start(context.Background(), apitypes.DiscoveryCreate{CIDR: "2001:db8::/120"})
	wantKind(t, err, inventory.Invalid)
	g, _ := r.svc.CreateGroup(context.Background(), apitypes.GroupCreate{CIDR: "2001:db8::/64"})
	_, err = r.m.Start(context.Background(), apitypes.DiscoveryCreate{GroupID: &g.ID})
	wantKind(t, err, inventory.Invalid)
}

// TC-208: a new group that breaks SPEC-114 → 409 before the job starts;
// nothing is created.
func TestNestingConflict(t *testing.T) {
	r := newRig(t, &prober{answer: all}, Config{})
	g, _ := r.svc.CreateGroup(context.Background(), apitypes.GroupCreate{CIDR: "10.0.0.0/8"})
	_, err := r.m.Start(context.Background(), apitypes.DiscoveryCreate{CIDR: "192.168.1.0/24", ParentID: &g.ID})
	wantKind(t, err, inventory.Conflict)
	if len(r.svc.Groups()) != 1 || len(r.svc.Hosts()) != 0 {
		t.Fatalf("created: %d groups, %d hosts", len(r.svc.Groups()), len(r.svc.Hosts()))
	}
	if r.rec.count(apitypes.EventDiscoveryFinished) != 0 {
		t.Fatal("a job ran")
	}
}

// TC-211: overlap with a running job → 409; finished jobs are kept up to
// the limit.
func TestOverlapAndKeep(t *testing.T) {
	gate := make(chan struct{})
	r := newRig(t, &prober{answer: all, gate: gate}, Config{Keep: 2})
	d := r.start(t, apitypes.DiscoveryCreate{CIDR: "10.8.0.0/24"})
	_, err := r.m.Start(context.Background(), apitypes.DiscoveryCreate{CIDR: "10.8.0.128/25"})
	wantKind(t, err, inventory.Conflict)
	if _, ok := r.svc.NetworkGroupByCIDR(netip.MustParsePrefix("10.8.0.128/25")); ok {
		t.Error("group created for the overlapping job")
	}
	close(gate)
	r.finished(t)
	var ids []string
	for i := range 3 {
		ids = append(ids, r.start(t, apitypes.DiscoveryCreate{CIDR: fmt.Sprintf("10.9.%d.0/30", i)}).ID)
		r.finished(t)
	}
	for _, id := range []string{d.ID, ids[0]} {
		if _, err := r.m.Get(id); inventory.KindOf(err) != inventory.NotFound {
			t.Errorf("%s still kept", id)
		}
	}
	for _, id := range ids[1:] {
		if _, err := r.m.Get(id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	if ids[2] != "run-4" {
		t.Errorf("id = %s", ids[2])
	}
}

// TC-212: without a prober, discovery is unavailable.
func TestUnavailable(t *testing.T) {
	r := newRig(t, &prober{answer: all}, Config{})
	r.m.prober = nil
	_, err := r.m.Start(context.Background(), apitypes.DiscoveryCreate{CIDR: "10.0.0.0/30"})
	wantKind(t, err, inventory.Unavailable)
	if !strings.Contains(err.Error(), "no ICMP (test)") {
		t.Errorf("message: %v", err)
	}
}

// Close cancels running jobs and keeps what they found.
func TestCloseSavesFound(t *testing.T) {
	r := newRig(t, &prober{answer: all}, Config{Rate: 50, Flush: time.Hour})
	r.start(t, apitypes.DiscoveryCreate{CIDR: "10.10.0.0/24"})
	time.Sleep(150 * time.Millisecond)
	r.m.Close()
	d := r.finished(t)
	if d.State != apitypes.DiscoveryCancelled || d.Found == 0 || d.Added != d.Found || len(r.svc.Hosts()) != d.Found {
		t.Fatalf("job %+v, %d hosts", d, len(r.svc.Hosts()))
	}
	_, err := r.m.Start(context.Background(), apitypes.DiscoveryCreate{CIDR: "10.11.0.0/30"})
	wantKind(t, err, inventory.Unavailable)
}

// errProber fails every probe of addresses ending in .1 with an error.
type errProber struct{}

func (errProber) Ping(_ context.Context, ip netip.Addr, count int, _ time.Duration) (monitor.Result, error) {
	if ip.As4()[3] == 1 {
		return monitor.Result{}, errors.New("sendto: network is unreachable")
	}
	return monitor.Result{Sent: count, Received: count, RTTs: []time.Duration{time.Millisecond}}, nil
}

// TC-213: probe errors are counted and reported; the job still ends done.
func TestProbeErrors(t *testing.T) {
	r := newRig(t, errProber{}, Config{})
	r.start(t, apitypes.DiscoveryCreate{CIDR: "10.12.0.0/30"})
	d := r.finished(t)
	if d.State != apitypes.DiscoveryDone || d.Probed != 2 || d.Found != 1 || d.Errors != 1 ||
		!strings.Contains(d.Error, "1 probes failed") || !strings.Contains(d.Error, "network is unreachable") {
		t.Fatalf("job: %+v", d)
	}
	// Without errors, error stays empty.
	r.start(t, apitypes.DiscoveryCreate{CIDR: "10.12.1.4/30"})
	if d := r.finished(t); d.Errors != 0 || d.Error != "" {
		t.Fatalf("clean job: %+v", d)
	}
}

// TC-214: a range larger than A1 logs a warning.
func TestLargeRangeWarning(t *testing.T) {
	var buf safeBuf
	r := newRig(t, &prober{answer: func(netip.Addr, int) bool { return false }}, Config{})
	r.m.log = slog.New(slog.NewTextHandler(&buf, nil))
	d := r.start(t, apitypes.DiscoveryCreate{CIDR: "10.13.0.0/19"}) // 8,190 addresses
	r.m.Cancel(d.ID)
	r.finished(t)
	if !strings.Contains(buf.String(), "large discovery range") || !strings.Contains(buf.String(), "addresses=8190") {
		t.Fatalf("log: %s", buf.String())
	}
	buf.Reset()
	d = r.start(t, apitypes.DiscoveryCreate{CIDR: "10.14.0.0/24"})
	r.finished(t)
	if strings.Contains(buf.String(), "large discovery range") {
		t.Fatalf("warning for a /24: %s", buf.String())
	}
}

type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func (s *safeBuf) Reset()                      { s.mu.Lock(); defer s.mu.Unlock(); s.b.Reset() }
