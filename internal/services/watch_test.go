package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// statusLog records service.status events as "host name status".
type statusLog struct {
	mu   sync.Mutex
	list []string
}

func (l *statusLog) Publish(typ string, data any) {
	if typ != apitypes.EventServiceStatus {
		return
	}
	e := data.(apitypes.ServiceStatusEvent)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, fmt.Sprintf("%d %s %s", e.HostID, e.Name, e.Status))
}

func (l *statusLog) take() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := strings.Join(l.list, "; ")
	l.list = nil
	return s
}

// fakeNet answers connects by port: open ports accept, others refuse. It
// counts connects and the most at once.
type fakeNet struct {
	mu        sync.Mutex
	open      map[string]bool // "ip:port"
	dials     int
	now, most int
	hold      time.Duration
}

func (f *fakeNet) set(addr string, open bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open[addr] = open
}

func (f *fakeNet) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials
}

func (f *fakeNet) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.dials++
	f.now++
	f.most = max(f.most, f.now)
	ok := f.open[addr]
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.now--; f.mu.Unlock() }()
	if f.hold > 0 {
		select {
		case <-time.After(f.hold):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if !ok {
		return nil, errors.New("connection refused")
	}
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}

type watchRig struct {
	t   *testing.T
	svc *inventory.Service
	st  interface{ Close() error }
	log *statusLog
	net *fakeNet
	w   *Watcher
	at  time.Time
}

func newWatchRig(t *testing.T, cfg WatchConfig) (*watchRig, string) {
	t.Helper()
	svc, _, st, path := newInventory(t)
	r := &watchRig{t: t, svc: svc, st: st, log: &statusLog{}, net: &fakeNet{open: map[string]bool{}},
		at: time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)}
	svc.SetNotifier(r.log)
	cfg.Dial = r.net.dial
	cfg.Now = func() time.Time { return r.at }
	r.w = NewWatcher(svc, cfg, nil)
	return r, path
}

// round runs one round at r.at + d and waits for its checks.
func (r *watchRig) round(d time.Duration) {
	r.at = r.at.Add(d)
	var wg sync.WaitGroup
	r.w.round(ctx, &wg)
	wg.Wait()
}

func (r *watchRig) host(ip string, watch ...string) apitypes.Host {
	r.t.Helper()
	h, err := r.svc.CreateHost(ctx, apitypes.HostCreate{IP: ip})
	if err != nil {
		r.t.Fatal(err)
	}
	return r.patch(h.ID, apitypes.HostPatch{Watch: apitypes.Some(watch)})
}

func (r *watchRig) patch(id int64, p apitypes.HostPatch) apitypes.Host {
	r.t.Helper()
	h, err := r.svc.UpdateHost(ctx, id, p)
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

func (r *watchRig) watch(id int64) string {
	r.t.Helper()
	h, err := r.svc.Host(id)
	if err != nil {
		r.t.Fatal(err)
	}
	var parts []string
	for _, w := range h.Watch {
		parts = append(parts, w.Name+" "+w.Status)
	}
	return strings.Join(parts, ", ")
}

func (r *watchRig) want(got, want string) {
	r.t.Helper()
	if got != want {
		r.t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// TC-318: a watched service is checked every interval; it is up after one
// success and down after watch_down_after failures in a row, each change
// with service.status; it starts unknown and is kept across a restart.
func TestWatchStatus(t *testing.T) {
	r, path := newWatchRig(t, WatchConfig{Interval: 5 * time.Minute, DownAfter: 2})
	h := r.host("192.0.2.1", "ssh", "tcp:5432")
	r.want(r.watch(h.ID), "ssh unknown, tcp:5432 unknown")
	r.net.set("192.0.2.1:22", true)

	r.round(0) // checked at once
	r.want(r.watch(h.ID), "ssh up, tcp:5432 unknown")
	r.want(r.log.take(), "1 ssh up")
	if n := r.net.count(); n != 2 {
		t.Fatalf("%d connects, want 2", n)
	}
	r.round(time.Minute) // not due
	r.round(3*time.Minute + 59*time.Second)
	if n := r.net.count(); n != 2 {
		t.Fatalf("%d connects before the interval, want 2", n)
	}
	r.round(time.Second) // 5 min: second failure
	r.want(r.watch(h.ID), "ssh up, tcp:5432 down")
	r.want(r.log.take(), "1 tcp:5432 down")
	got, _ := r.svc.Host(h.ID)
	if w := got.Watch[1]; w.Port != 5432 || w.LastOK != nil || !w.LastChange.Equal(r.at) {
		t.Fatalf("tcp:5432: %+v", w)
	}
	if w := got.Watch[0]; !w.LastOK.Equal(r.at) || w.LastChange.Equal(r.at) {
		t.Fatalf("ssh: last_ok %v last_change %v", w.LastOK, w.LastChange)
	}
	m := r.svc.Map()
	if it := m.Items[len(m.Items)-1]; it.ServicesDown != 1 {
		t.Fatalf("map services_down %d, want 1", it.ServicesDown)
	}

	// One failure is not enough to go down; one success is enough for up.
	r.net.set("192.0.2.1:22", false)
	r.net.set("192.0.2.1:5432", true)
	r.round(5 * time.Minute)
	r.want(r.watch(h.ID), "ssh up, tcp:5432 up")
	r.want(r.log.take(), "1 tcp:5432 up")
	r.round(5 * time.Minute)
	r.want(r.log.take(), "1 ssh down")

	// Kept across a restart (schema 7).
	r.st.Close()
	svc2 := reopen(t, path)
	h2, err := svc2.Host(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(h2.Watch) != 2 || h2.Watch[0].Status != "down" || h2.Watch[1].Status != "up" || h2.Watch[1].LastOK == nil {
		t.Fatalf("after restart: %+v", h2.Watch)
	}
}

// TC-319: an unmonitored host is not checked and its services are
// unknown; a new IP address or a service added again starts from
// unknown; a result for an old address or a removed service is dropped.
func TestWatchResets(t *testing.T) {
	r, _ := newWatchRig(t, WatchConfig{DownAfter: 2})
	h := r.host("192.0.2.1", "ssh", "https")
	r.net.set("192.0.2.1:22", true)
	r.round(0)
	r.want(r.log.take(), "1 ssh up")

	// Monitoring off: unknown, not checked.
	r.patch(h.ID, apitypes.HostPatch{Monitored: apitypes.Some(false)})
	r.want(r.watch(h.ID), "ssh unknown, https unknown")
	r.want(r.log.take(), "1 ssh unknown")
	n := r.net.count()
	r.round(time.Hour)
	if r.net.count() != n {
		t.Fatal("an unmonitored host was checked")
	}
	r.patch(h.ID, apitypes.HostPatch{Monitored: apitypes.Some(true)})
	r.round(time.Hour)
	r.want(r.log.take(), "1 ssh up")

	// A list given again keeps the status of the services in it; new ones
	// start unknown.
	r.patch(h.ID, apitypes.HostPatch{Watch: apitypes.Some([]string{"tcp:8080", "SSH"})})
	r.want(r.watch(h.ID), "tcp:8080 unknown, ssh up")

	// A new address: unknown, last_ok cleared, and counted from zero.
	r.patch(h.ID, apitypes.HostPatch{IP: apitypes.Some("192.0.2.9")})
	r.want(r.log.take(), "1 ssh unknown")
	got, _ := r.svc.Host(h.ID)
	if got.Watch[1].LastOK != nil {
		t.Fatalf("last_ok after an IP change: %v", got.Watch[1].LastOK)
	}
	r.round(time.Hour)
	r.want(r.watch(h.ID), "tcp:8080 unknown, ssh unknown")

	// Results for an old address or a service no longer watched.
	err := r.svc.ApplyWatch(ctx, []inventory.WatchResult{
		{HostID: h.ID, IP: mustAddr("192.0.2.1"), Name: "ssh", Port: 22, OK: true, Status: "up", At: r.at},
		{HostID: got.ID, IP: mustAddr("192.0.2.9"), Name: "https", Port: 443, OK: true, Status: "up", At: r.at},
		{HostID: 99, IP: mustAddr("192.0.2.9"), Name: "ssh", Port: 22, Status: "down", At: r.at},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.want(r.log.take(), "")
	r.want(r.watch(h.ID), "tcp:8080 unknown, ssh unknown")
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

// TC-322: a host whose ping status becomes up again has its watched
// services checked at once, not at the end of the interval; while it stays
// up or down they keep the interval.
func TestWatchHostBackUp(t *testing.T) {
	r, _ := newWatchRig(t, WatchConfig{Interval: 5 * time.Minute, DownAfter: 1})
	h := r.host("192.0.2.1", "ssh")
	ping := func(status string) {
		t.Helper()
		err := r.svc.ApplyObservations(ctx, []inventory.Observation{{HostID: h.ID, IP: mustAddr("192.0.2.1"),
			Status: status, Replied: status == apitypes.StatusUp, At: r.at}})
		if err != nil {
			t.Fatal(err)
		}
	}
	ping(apitypes.StatusDown)
	r.round(0)
	r.want(r.log.take(), "1 ssh down")
	r.round(time.Minute)
	ping(apitypes.StatusDown) // no change: not due
	r.round(time.Minute)
	if n := r.net.count(); n != 1 {
		t.Fatalf("%d connects while the host stays down, want 1", n)
	}
	r.net.set("192.0.2.1:22", true)
	ping(apitypes.StatusUp)
	r.round(time.Second)
	r.want(r.log.take(), "1 ssh up")
	r.round(time.Minute) // up since the same time: not again
	if n := r.net.count(); n != 2 {
		t.Fatalf("%d connects, want 2", n)
	}
}

// TC-318: at most watch_concurrency connects at once; a check cut short by
// shutdown saves nothing.
func TestWatchConcurrency(t *testing.T) {
	r, _ := newWatchRig(t, WatchConfig{Concurrency: 3})
	r.net.hold = 20 * time.Millisecond
	for i := 1; i <= 4; i++ {
		r.host(fmt.Sprintf("192.0.2.%d", i), "ssh", "http", "https")
	}
	r.round(0)
	if r.net.most > 3 || r.net.dials != 12 {
		t.Fatalf("%d connects, %d at once; want 12, at most 3", r.net.dials, r.net.most)
	}

	cctx, cancel := context.WithCancel(ctx)
	var started atomic.Bool
	r.w.cfg.Dial = func(c context.Context, n, a string) (net.Conn, error) {
		started.Store(true)
		cancel()
		<-c.Done()
		return nil, c.Err()
	}
	r.w.cfg.DownAfter = 1
	r.at = r.at.Add(time.Hour)
	var wg sync.WaitGroup
	r.w.round(cctx, &wg)
	wg.Wait()
	if !started.Load() {
		t.Fatal("no check started")
	}
	r.want(r.log.take(), "")
}
