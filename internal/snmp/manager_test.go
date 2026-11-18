package snmp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/store"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

type events struct {
	mu  sync.Mutex
	all []string
}

func (e *events) Publish(typ string, data any) {
	b, _ := json.Marshal(data)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.all = append(e.all, typ+" "+string(b))
}

func (e *events) take(typ string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, s := range e.all {
		if typ == "" || strings.HasPrefix(s, typ+" ") {
			out = append(out, s)
		}
	}
	e.all = nil
	return out
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type rig struct {
	svc *inventory.Service
	ev  *events
	log *logBuf
	clk *clock
	m   *Manager
}

func newRig(t *testing.T, a *agent, cfg Config) *rig {
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
	ev := &events{}
	svc.SetNotifier(ev)
	r := &rig{svc: svc, ev: ev, log: &logBuf{}, clk: &clock{t: time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)}}
	cfg.Now = r.clk.now
	if cfg.Client == nil {
		cfg.Client = udp(a)
	}
	if cfg.Tick == 0 {
		cfg.Tick = 5 * time.Millisecond
	}
	r.m = New(svc, cfg, slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return r
}

func (r *rig) run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (r *rig) host(t *testing.T, ip string, monitored bool) apitypes.Host {
	t.Helper()
	h, err := r.svc.CreateHost(context.Background(), apitypes.HostCreate{IP: ip, Monitored: &monitored})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("no %s", what)
}

// TC-326: a monitored host's agent is found with the second community over
// v2c, its system values and then its values are saved, with host.updated
// and snmp.values; the community is in no API answer, event or log line;
// values are read again after the interval, with snmp.values only when a
// whole percent changes; an unmonitored host and one outside
// snmp.networks get no request.
func TestFindAndRead(t *testing.T) {
	a := newAgent(t, "s3cret", gosnmp.Version2c)
	a.system("web", "Linux web 6.8.0")
	a.hostResources()
	r := newRig(t, a, Config{Communities: []string{"public", "s3cret"}, Networks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/30")}})
	web := r.host(t, "127.0.0.1", true)
	off := r.host(t, "127.0.0.2", false)
	far := r.host(t, "127.0.0.9", true)
	r.ev.take("")
	r.run(t)
	eventually(t, "values", func() bool {
		h, _ := r.svc.Host(web.ID)
		return h.SNMP != nil && h.SNMP.Values != nil
	})
	h, _ := r.svc.Host(web.ID)
	s := h.SNMP
	if s.Version != V2c || s.Name != "web" || s.Location != "rack 4" || !s.CheckedAt.Equal(r.clk.now()) ||
		*s.Values.CPUPct != 13 || len(s.Values.Storage) != 2 || s.Values.UptimeS != 1234 {
		t.Fatalf("snmp: %+v %+v", s, s.Values)
	}
	for _, id := range []int64{off.ID, far.ID} {
		if h, _ := r.svc.Host(id); h.SNMP != nil {
			t.Errorf("host %d checked: %+v", id, h.SNMP)
		}
	}
	// public with v2c and v1, then s3cret: found, and the values read.
	if got := a.communities(); len(got) < 3 || got[0] != "public" || got[1] != "public" || got[2] != "s3cret" {
		t.Errorf("communities tried: %v", got)
	}
	all := strings.Join(r.ev.take(""), "\n")
	b, _ := json.Marshal(h)
	if strings.Contains(all, "s3cret") || strings.Contains(string(b), "s3cret") || strings.Contains(r.log.String(), "s3cret") {
		t.Errorf("community shown:\n%s\n%s\n%s", all, b, r.log.String())
	}
	if !strings.Contains(all, "host.updated") || strings.Count(all, "snmp.values") != 1 || !strings.Contains(all, `"cpu_pct":13`) {
		t.Errorf("events:\n%s", all)
	}
	if !strings.Contains(r.log.String(), "SNMP found on 127.0.0.1 (v2c, community 2 of 2)") {
		t.Errorf("log:\n%s", r.log.String())
	}

	// The same values again: no event; a change in whole percents: one.
	r.clk.add(5 * time.Minute)
	eventually(t, "a second read", func() bool { h, _ := r.svc.Host(web.ID); return h.SNMP.Values.At.Equal(r.clk.now()) })
	if ev := r.ev.take(apitypes.EventSNMPValues); len(ev) != 0 {
		t.Errorf("unchanged values sent: %v", ev)
	}
	a.set(oidStorageEntry+".6.31", gosnmp.Integer, 950)
	r.clk.add(5 * time.Minute)
	eventually(t, "a changed disk", func() bool {
		h, _ := r.svc.Host(web.ID)
		return h.SNMP.Values.Storage[1].UsedPct == 95
	})
	if ev := r.ev.take(apitypes.EventSNMPValues); len(ev) != 1 || !strings.Contains(ev[0], `"used_pct":95`) {
		t.Errorf("changed values: %v", ev)
	}
}

// TC-327: no agent: "no answer" with the time, tried again after Rescan;
// an agent whose community changes is lost at the next read and found
// again with the list; a new IP address drops it; Check on request, and
// refused outside snmp.networks.
func TestNoAgentLostAndCheck(t *testing.T) {
	a := newAgent(t, "public", gosnmp.Version1)
	r := newRig(t, a, Config{Communities: []string{"public", "other"}, Rescan: time.Hour, Networks: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	ctx := context.Background()
	web := r.host(t, "127.0.0.1", true)
	a.mu.Lock()
	a.community = "nobody"
	a.mu.Unlock()
	r.run(t)
	eventually(t, "a check", func() bool { h, _ := r.svc.Host(web.ID); return h.SNMP != nil })
	if h, _ := r.svc.Host(web.ID); h.SNMP.Version != "" || h.SNMP.Values != nil {
		t.Fatalf("no agent: %+v", h.SNMP)
	}
	n := len(a.communities()) // 2 communities × 2 versions × (1 + retries)
	time.Sleep(50 * time.Millisecond)
	if len(a.communities()) != n {
		t.Fatal("tried again before Rescan")
	}
	a.mu.Lock()
	a.community = "other"
	a.mu.Unlock()
	a.system("printer", "HP")
	r.clk.add(time.Hour)
	eventually(t, "found after Rescan", func() bool { h, _ := r.svc.Host(web.ID); return h.SNMP.Version == V1 })
	eventually(t, "values", func() bool { h, _ := r.svc.Host(web.ID); return h.SNMP.Values != nil && h.SNMP.Values.CPUPct == nil })

	// Lost: the agent's community changes; after three failed reads, one
	// per interval, the list is tried again (crosscheck 2026-11-18-03 C2).
	a.mu.Lock()
	a.community = "public"
	a.mu.Unlock()
	for range 3 {
		r.clk.add(5 * time.Minute)
		time.Sleep(300 * time.Millisecond) // a read without an answer takes the timeout
	}
	eventually(t, "found again", func() bool {
		tg, _ := r.svc.SNMPTarget(web.ID)
		return tg.Agent.Community == "public"
	})

	// On request; outside the networks refused.
	h, err := r.m.Check(ctx, web.ID)
	if err != nil || h.SNMP.Version != V1 || h.SNMP.Name != "printer" {
		t.Errorf("Check: %+v %v", h.SNMP, err)
	}
	far := r.host(t, "127.0.0.2", false)
	if _, err := r.m.Check(ctx, far.ID); inventory.KindOf(err) != inventory.Invalid {
		t.Errorf("outside networks: %v", err)
	}
	// A new IP address drops it.
	h, err = r.svc.UpdateHost(ctx, web.ID, apitypes.HostPatch{IP: apitypes.Some("127.0.0.3")})
	if err != nil || h.SNMP != nil {
		t.Errorf("after a new IP: %+v %v", h.SNMP, err)
	}
}

// scripted is a Client whose agent can stop answering, or answer the
// system group but not the values.
type scripted struct {
	mu           sync.Mutex
	down, noVals bool
	sysN, valN   int
}

func (s *scripted) System(context.Context, netip.Addr, string, string) (inventory.SNMPSystem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sysN++
	if s.down {
		return inventory.SNMPSystem{}, errNoAnswer
	}
	return inventory.SNMPSystem{Name: "web"}, nil
}

func (s *scripted) Values(context.Context, netip.Addr, string, string) (apitypes.SNMPValues, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.valN++
	if s.down || s.noVals {
		return apitypes.SNMPValues{}, errNoAnswer
	}
	return apitypes.SNMPValues{}, nil
}

func (s *scripted) set(f func(*scripted)) { s.mu.Lock(); f(s); s.mu.Unlock() }
func (s *scripted) counts() (int, int)    { s.mu.Lock(); defer s.mu.Unlock(); return s.sysN, s.valN }

// settle lets Run do what is due now.
func settle() { time.Sleep(60 * time.Millisecond) }

// TC-327 (crosscheck 2026-11-18-03 C2, C3): a value read that fails is
// tried again after the interval, not at every tick; after 3 failed reads
// the agent is looked for again, then after 1, 5 and 30 min while its
// values can't be read; an agent that stops answering is kept through a
// short outage and stored as "no answer" only after those waits.
func TestFailingAgent(t *testing.T) {
	c := &scripted{}
	r := newRig(t, nil, Config{Client: c, Interval: 5 * time.Minute})
	h := r.host(t, "127.0.0.1", true)
	r.run(t)
	eventually(t, "values", func() bool { x, _ := r.svc.Host(h.ID); return x.SNMP != nil && x.SNMP.Values != nil })

	// Values fail: one try per interval.
	c.set(func(s *scripted) { s.noVals = true })
	sys0, _ := c.counts()
	for i := 1; i <= 2; i++ {
		r.clk.add(5 * time.Minute)
		settle()
		if _, v := c.counts(); v != 1+i {
			t.Fatalf("read %d: %d value reads", i, v)
		}
	}
	// The third failure makes it lost: looked for again at the next tick,
	// found, its read fails, and the next search waits 1 min.
	r.clk.add(5 * time.Minute)
	settle()
	if s, v := c.counts(); s != sys0+1 || v != 5 {
		t.Fatalf("after 3 failures: %d searches, %d reads", s-sys0, v)
	}
	settle()
	if s, v := c.counts(); s != sys0+1 || v != 5 {
		t.Fatalf("again before 1 min: %d searches, %d reads", s-sys0, v)
	}
	r.clk.add(time.Minute)
	settle()
	if s, _ := c.counts(); s != sys0+2 {
		t.Fatalf("not searched after 1 min: %d", s-sys0)
	}
	// Values come back: all is well again.
	c.set(func(s *scripted) { s.noVals = false })
	r.clk.add(5 * time.Minute)
	settle()
	if x, _ := r.svc.Host(h.ID); x.SNMP.Version != V2c || !x.SNMP.Values.At.Equal(r.clk.now()) {
		t.Fatalf("after recovery: %+v", x.SNMP)
	}

	// A short outage: the agent is kept.
	c.set(func(s *scripted) { s.down = true })
	for range 3 {
		r.clk.add(5 * time.Minute)
		settle()
	}
	settle() // lost: searched, nothing answers
	c.set(func(s *scripted) { s.down = false })
	if x, _ := r.svc.Host(h.ID); x.SNMP.Version != V2c {
		t.Fatalf("agent dropped after a short outage: %+v", x.SNMP)
	}
	r.clk.add(5 * time.Minute) // the next search (after 5 min) finds it
	eventually(t, "back", func() bool {
		x, _ := r.svc.Host(h.ID)
		return x.SNMP.Values != nil && x.SNMP.Values.At.Equal(r.clk.now())
	})

	// A long outage: "no answer" after 1, 5 and 30 min of searching.
	c.set(func(s *scripted) { s.down = true })
	for range 3 {
		r.clk.add(5 * time.Minute)
		settle()
	}
	settle()
	for _, w := range []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute} {
		if x, _ := r.svc.Host(h.ID); x.SNMP.Version != V2c {
			t.Fatalf("dropped before the wait of %v", w)
		}
		r.clk.add(w)
		settle()
	}
	if x, _ := r.svc.Host(h.ID); x.SNMP.Version != "" {
		t.Fatalf("still kept: %+v", x.SNMP)
	}
}

// TC-327 (crosscheck 2026-11-18-03 L3, L8): a check on request of an
// unmonitored host keeps its values; the state of a deleted host is
// forgotten.
func TestCheckUnmonitoredAndPrune(t *testing.T) {
	c := &scripted{noVals: true}
	r := newRig(t, nil, Config{Client: c})
	off := r.host(t, "127.0.0.1", false)
	if h, err := r.m.Check(context.Background(), off.ID); err != nil || h.SNMP.Values != nil {
		t.Fatalf("failing read: %+v %v", h.SNMP, err)
	}
	c.set(func(s *scripted) { s.noVals = false })
	if h, err := r.m.Check(context.Background(), off.ID); err != nil || h.SNMP.Values == nil {
		t.Fatalf("unmonitored host on request: %+v %v", h.SNMP, err)
	}
	r.m.mu.Lock()
	r.m.state[999] = &hostState{fails: 1}
	r.m.mu.Unlock()
	r.m.prune(r.svc.SNMPTargets())
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if len(r.m.state) != 0 {
		t.Errorf("state kept: %v", r.m.state)
	}
}
