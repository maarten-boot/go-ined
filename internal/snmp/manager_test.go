package snmp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
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

// TC-327 (crosscheck 2026-11-18-04 C1, L1, L3): an agent that answers its
// system group but never its values: the first failed reads use no wait;
// after the 1, 5 and 30 min waits the next search is a rescan later, not
// at every tick; the counts go with a new IP address and with monitoring
// turned off.
func TestValuesNeverRead(t *testing.T) {
	c := &scripted{noVals: true}
	r := newRig(t, nil, Config{Client: c, Interval: 5 * time.Minute, Rescan: 24 * time.Hour})
	h := r.host(t, "127.0.0.1", true)
	r.run(t)
	eventually(t, "found", func() bool { x, _ := r.svc.Host(h.ID); return x.SNMP != nil })
	settle()
	r.m.mu.Lock()
	if st := r.m.state[h.ID]; st == nil || st.fails != 1 || st.lost != 0 {
		t.Errorf("after the first failed read: %+v", st)
	}
	r.m.mu.Unlock()
	for range 2 {
		r.clk.add(5 * time.Minute)
		settle()
	}
	r.clk.add(5 * time.Minute) // the third failure, then a search
	settle()
	for _, w := range []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute} {
		r.clk.add(w)
		settle()
	}
	s0, _ := c.counts()
	time.Sleep(200 * time.Millisecond)
	if s1, _ := c.counts(); s1 != s0 {
		t.Fatalf("%d searches with the waits used up", s1-s0)
	}
	r.clk.add(24 * time.Hour)
	settle()
	if s1, _ := c.counts(); s1 != s0+1 {
		t.Fatalf("%d searches after rescan", s1-s0)
	}
	if x, _ := r.svc.Host(h.ID); x.SNMP.Version != V2c {
		t.Errorf("an agent that answers was dropped: %+v", x.SNMP)
	}

	// L3: a new address or monitoring off forgets the counts.
	r.m.mu.Lock()
	r.m.state[h.ID] = &hostState{ip: netip.MustParseAddr("127.0.0.9"), fails: 7}
	r.m.mu.Unlock()
	r.m.work(inventory.SNMPTarget{ID: h.ID, IP: netip.MustParseAddr("127.0.0.1"), Monitored: true, Agent: inventory.SNMPAgent{Version: V2c, CheckedAt: r.clk.now()}}, r.clk.now())
	r.m.mu.Lock()
	if st := r.m.state[h.ID]; st != nil {
		t.Errorf("kept for another address: %+v", st)
	}
	r.m.state[h.ID] = &hostState{fails: 7}
	r.m.mu.Unlock()
	r.m.work(inventory.SNMPTarget{ID: h.ID, IP: netip.MustParseAddr("127.0.0.1")}, r.clk.now())
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if st := r.m.state[h.ID]; st != nil {
		t.Errorf("kept with monitoring off: %+v", st)
	}
}

// byCommunity answers only for one community and records what it was asked
// with, per address.
type byCommunity struct {
	mu     sync.Mutex
	answer string
	asked  map[string][]string // ip -> communities, in order
}

func (b *byCommunity) System(_ context.Context, ip netip.Addr, c, _ string) (inventory.SNMPSystem, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.asked == nil {
		b.asked = map[string][]string{}
	}
	b.asked[ip.String()] = append(b.asked[ip.String()], c)
	if c != b.answer {
		return inventory.SNMPSystem{}, errNoAnswer
	}
	return inventory.SNMPSystem{Name: "x"}, nil
}

func (b *byCommunity) Values(_ context.Context, _ netip.Addr, c, _ string) (apitypes.SNMPValues, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c != b.answer {
		return apitypes.SNMPValues{}, errNoAnswer
	}
	return apitypes.SNMPValues{}, nil
}

func (b *byCommunity) seen(ip string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.asked[ip])
}

func (b *byCommunity) set(c string) { b.mu.Lock(); b.answer = c; b.mu.Unlock() }

// TC-328: a group's list replaces the server's for the hosts below it,
// also outside snmp.networks; a host's own list beats the group's; a
// changed list, or a move below another group, makes the host looked for
// again at once with the new list; the server's list still goes only to
// snmp.networks.
func TestCommunityLists(t *testing.T) {
	ctx := context.Background()
	b := &byCommunity{answer: "lab-ro"}
	r := newRig(t, nil, Config{Client: b, Communities: []string{"public"}, Networks: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	lab, _ := r.svc.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab"})
	other, _ := r.svc.CreateGroup(ctx, apitypes.GroupCreate{Name: "other"})
	if _, err := r.svc.UpdateGroup(ctx, lab.ID, apitypes.GroupPatch{SNMPCommunities: apitypes.Some([]string{"lab-ro"})}); err != nil {
		t.Fatal(err)
	}
	in := func(ip string, parent int64) apitypes.Host {
		h, err := r.svc.CreateHost(ctx, apitypes.HostCreate{IP: ip, ParentID: &parent})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	far := in("127.0.0.9", lab.ID)    // outside networks, but the group has a list
	near := in("127.0.0.1", other.ID) // inside networks: the server's list
	out := in("127.0.0.8", other.ID)  // outside, no list: never asked
	r.run(t)
	eventually(t, "far found", func() bool { h, _ := r.svc.Host(far.ID); return h.SNMP != nil && h.SNMP.Version == V2c })
	eventually(t, "near checked", func() bool { h, _ := r.svc.Host(near.ID); return h.SNMP != nil })
	if got := b.seen("127.0.0.9"); strings.Join(got, ",") != "lab-ro" {
		t.Errorf("far asked with %v", got)
	}
	if got := b.seen("127.0.0.1"); len(got) == 0 || got[0] != "public" || slices.Contains(got, "lab-ro") {
		t.Errorf("near asked with %v", got)
	}
	if got := b.seen("127.0.0.8"); len(got) != 0 {
		t.Errorf("out asked with %v", got)
	}
	if h, _ := r.svc.Host(far.ID); h.SNMPCommunitiesSet != 0 || h.SNMPCommunitiesFrom != fmt.Sprintf("group %d", lab.ID) {
		t.Errorf("far: set %d from %q", h.SNMPCommunitiesSet, h.SNMPCommunitiesFrom)
	}
	// The host's own list beats the group's, and is looked for again at once.
	b.set("mine")
	if _, err := r.svc.UpdateHost(ctx, far.ID, apitypes.HostPatch{SNMPCommunities: apitypes.Some([]string{"mine"})}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "far with its own list", func() bool { return slices.Contains(b.seen("127.0.0.9"), "mine") })
	if h, _ := r.svc.Host(far.ID); h.SNMPCommunitiesSet != 1 || h.SNMPCommunitiesFrom != "host" {
		t.Errorf("far: %d %q", h.SNMPCommunitiesSet, h.SNMPCommunitiesFrom)
	}
	// out moves below lab: asked with lab's list at once.
	b.set("lab-ro")
	if _, err := r.svc.UpdateHost(ctx, out.ID, apitypes.HostPatch{ParentID: apitypes.Some(&lab.ID)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "out after the move", func() bool { return strings.Join(b.seen("127.0.0.8"), ",") == "lab-ro" })
	// Never in a host's or group's JSON.
	h, _ := r.svc.Host(far.ID)
	g, _ := r.svc.Group(lab.ID)
	hb, _ := json.Marshal(h)
	gb, _ := json.Marshal(g)
	if strings.Contains(string(hb)+string(gb), "mine") || strings.Contains(string(gb), "lab-ro") || g.SNMPCommunitiesSet != 1 {
		t.Errorf("shown: %s %s", hb, gb)
	}
}

// TC-329: a check job of a group checks its hosts with subgroups (found,
// no answer, skipped), with progress and finished events; one of hosts;
// neither or both is 400, an unknown group 404; a job can be cancelled.
func TestCheckJobs(t *testing.T) {
	ctx := context.Background()
	b := &byCommunity{answer: "public"}
	r := newRig(t, nil, Config{Client: b, Communities: []string{"public"}, Networks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/30")}})
	r.m.cfg.Notify = r.ev
	top, _ := r.svc.CreateGroup(ctx, apitypes.GroupCreate{Name: "top"})
	sub, _ := r.svc.CreateGroup(ctx, apitypes.GroupCreate{Name: "sub", ParentID: &top.ID})
	for _, c := range []struct {
		ip     string
		parent int64
	}{{"127.0.0.1", top.ID}, {"127.0.0.2", sub.ID}, {"127.0.0.9", sub.ID}} {
		p := c.parent
		if _, err := r.svc.CreateHost(ctx, apitypes.HostCreate{IP: c.ip, ParentID: &p}); err != nil {
			t.Fatal(err)
		}
	}
	b.set("") // nobody answers at first
	j, err := r.m.StartCheck(apitypes.SNMPCheckCreate{GroupID: &top.ID})
	if err != nil || j.Total != 3 || j.State != "running" {
		t.Fatalf("start: %+v %v", j, err)
	}
	var done apitypes.SNMPCheck
	eventually(t, "finished", func() bool { done, _ = r.m.GetCheck(j.ID); return done.State != "running" })
	if done.State != "done" || done.Checked != 3 || done.Found != 0 || done.NoAnswer != 2 || done.Skipped != 1 || done.FinishedAt == nil {
		t.Errorf("job: %+v", done)
	}
	var ev string
	eventually(t, "the finished event", func() bool {
		ev += strings.Join(r.ev.take(""), "\n")
		return strings.Contains(ev, `snmp.check.finished {"id":"`+j.ID+`"`)
	})
	b.set("public")
	j2, _ := r.m.StartCheck(apitypes.SNMPCheckCreate{HostIDs: []int64{1, 1}})
	eventually(t, "second", func() bool { d, _ := r.m.GetCheck(j2.ID); return d.State == "done" && d.Found == 1 && d.Total == 1 })
	if l := r.m.Checks(); len(l) != 2 || l[0].ID != j.ID {
		t.Errorf("list: %+v", l)
	}
	for _, in := range []apitypes.SNMPCheckCreate{{}, {GroupID: &top.ID, HostIDs: []int64{1}}} {
		if _, err := r.m.StartCheck(in); inventory.KindOf(err) != inventory.Invalid {
			t.Errorf("%+v: %v", in, err)
		}
	}
	nine := int64(999)
	if _, err := r.m.StartCheck(apitypes.SNMPCheckCreate{GroupID: &nine}); inventory.KindOf(err) != inventory.NotFound {
		t.Errorf("unknown group: %v", err)
	}
	if _, err := r.m.CancelCheck(j2.ID); err != nil {
		t.Errorf("cancel a finished job: %v", err)
	}
	if _, err := r.m.GetCheck("nope"); inventory.KindOf(err) != inventory.NotFound {
		t.Errorf("unknown job: %v", err)
	}
}

// TC-328 (crosscheck 2026-11-19-01 C3): with one slot, every host whose
// list changed is still looked for with the new list, one per tick.
func TestListChangePastConcurrency(t *testing.T) {
	ctx := context.Background()
	b := &byCommunity{answer: "old"}
	r := newRig(t, nil, Config{Client: b, Communities: []string{"old"}, Concurrency: 1})
	lab, _ := r.svc.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab"})
	var ids []int64
	for _, ip := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"} {
		h, err := r.svc.CreateHost(ctx, apitypes.HostCreate{IP: ip, ParentID: &lab.ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, h.ID)
	}
	r.run(t)
	eventually(t, "all found", func() bool {
		for _, id := range ids {
			if h, _ := r.svc.Host(id); h.SNMP == nil || h.SNMP.Version == "" {
				return false
			}
		}
		return true
	})
	b.set("new")
	if _, err := r.svc.UpdateGroup(ctx, lab.ID, apitypes.GroupPatch{SNMPCommunities: apitypes.Some([]string{"new"})}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "every host asked with the new list", func() bool {
		for _, ip := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"} {
			if !slices.Contains(b.seen(ip), "new") {
				return false
			}
		}
		return true
	})
}

// TC-329 (crosscheck 2026-11-19-01 L1, L2): a cancelled job counts the
// hosts not started as skipped, and Checked reaches Total; after Close no
// job starts.
func TestCheckJobCancelAndClose(t *testing.T) {
	ctx := context.Background()
	gate := make(chan struct{})
	r := newRig(t, nil, Config{Client: blocking{gate}, Communities: []string{"x"}, Concurrency: 1})
	var ids []int64
	for _, ip := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"} {
		h, _ := r.svc.CreateHost(ctx, apitypes.HostCreate{IP: ip})
		ids = append(ids, h.ID)
	}
	j, err := r.m.StartCheck(apitypes.SNMPCheckCreate{HostIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	r.m.CancelCheck(j.ID)
	var d apitypes.SNMPCheck
	eventually(t, "cancelled", func() bool { d, _ = r.m.GetCheck(j.ID); return d.State == "cancelled" })
	if d.Checked != 3 || d.Skipped != 3 || d.Found+d.NoAnswer != 0 {
		t.Errorf("cancelled job: %+v", d)
	}
	close(gate)
	r.m.Close()
	if _, err := r.m.StartCheck(apitypes.SNMPCheckCreate{HostIDs: ids}); inventory.KindOf(err) != inventory.Unavailable {
		t.Errorf("after Close: %v", err)
	}
}

// blocking holds every System call until its gate closes or ctx ends.
type blocking struct{ gate chan struct{} }

func (b blocking) System(ctx context.Context, _ netip.Addr, _, _ string) (inventory.SNMPSystem, error) {
	select {
	case <-b.gate:
		return inventory.SNMPSystem{}, errNoAnswer
	case <-ctx.Done():
		return inventory.SNMPSystem{}, ctx.Err()
	}
}

func (blocking) Values(context.Context, netip.Addr, string, string) (apitypes.SNMPValues, error) {
	return apitypes.SNMPValues{}, errNoAnswer
}
