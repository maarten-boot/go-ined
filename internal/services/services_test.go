package services

import (
	"context"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/store"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

var ctx = context.Background()

// ports returns a port with a listener and a port without one.
func ports(t *testing.T) (open, closed uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone.Close()
	return port(ln.Addr()), port(gone.Addr())
}

func port(a net.Addr) uint16 {
	_, p, _ := net.SplitHostPort(a.String())
	n, _ := strconv.Atoi(p)
	return uint16(n)
}

// events records published events.
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) Publish(typ string, _ any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, typ)
}
func (e *events) count(typ string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, t := range e.list {
		if t == typ {
			n++
		}
	}
	return n
}

func newInventory(t *testing.T) (*inventory.Service, *events, *store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ined.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := inventory.NewService(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	ev := &events{}
	svc.SetNotifier(ev)
	return svc, ev, st, path
}

func TestDefaultPorts(t *testing.T) {
	var got []string
	for _, p := range DefaultPorts() {
		got = append(got, p.Name+"/"+strconv.Itoa(int(p.Port)))
	}
	want := []string{"ssh/22", "rdp/3389", "vnc/5900", "vnc:1/5901", "vnc:2/5902", "vnc:3/5903", "vnc:4/5904",
		"vnc:5/5905", "vnc:6/5906", "vnc:7/5907", "vnc:8/5908", "vnc:9/5909", "http/80", "https/443"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// TC-316: a check finds the ports that accept a connection, saves them
// with the time, keeps them across a restart, and emits host.updated only
// when they change. An unknown host is not found.
func TestCheck(t *testing.T) {
	open, closed := ports(t)
	svc, ev, st, path := newInventory(t)
	h, err := svc.CreateHost(ctx, apitypes.HostCreate{IP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if h.Services != nil || h.ServicesCheckedAt != nil {
		t.Fatalf("new host has services %v at %v", h.Services, h.ServicesCheckedAt)
	}
	at := time.Date(2026, 11, 14, 14, 2, 0, 0, time.UTC)
	c := New(svc, Config{Ports: []Port{{"ssh", open}, {"rdp", closed}, {"http", open}}, Now: func() time.Time { return at }}, nil)
	before := ev.count(apitypes.EventHostUpdated)
	got, err := c.Check(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Services, []string{"ssh", "http"}) || got.ServicesCheckedAt == nil || !got.ServicesCheckedAt.Equal(at) {
		t.Fatalf("got %v at %v", got.Services, got.ServicesCheckedAt)
	}
	if n := ev.count(apitypes.EventHostUpdated) - before; n != 1 {
		t.Fatalf("%d host.updated, want 1", n)
	}
	if _, err := c.Check(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if n := ev.count(apitypes.EventHostUpdated) - before; n != 1 {
		t.Fatalf("unchanged result: %d host.updated, want still 1", n)
	}

	// Nothing open: an empty list, not "never checked".
	c = New(svc, Config{Ports: []Port{{"ssh", closed}}, Now: func() time.Time { return at.Add(time.Hour) }}, nil)
	got, err = c.Check(ctx, h.ID)
	if err != nil || got.Services == nil || len(got.Services) != 0 || got.ServicesCheckedAt == nil {
		t.Fatalf("got %#v at %v (%v)", got.Services, got.ServicesCheckedAt, err)
	}
	c = New(svc, Config{Ports: []Port{{"ssh", open}, {"vnc:1", open}}, Now: func() time.Time { return at }}, nil)
	if _, err := c.Check(ctx, h.ID); err != nil {
		t.Fatal(err)
	}

	// Kept across a restart (schema 6).
	if _, err := c.Check(ctx, 999); inventory.KindOf(err) != inventory.NotFound {
		t.Fatalf("unknown host: %v", err)
	}
	st.Close()
	svc2 := reopen(t, path)
	h2, err := svc2.Host(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h2.Services, []string{"ssh", "vnc:1"}) || h2.ServicesCheckedAt == nil || !h2.ServicesCheckedAt.Equal(at) {
		t.Fatalf("after restart: %v at %v", h2.Services, h2.ServicesCheckedAt)
	}
}

func reopen(t *testing.T, path string) *inventory.Service {
	t.Helper()
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := inventory.NewService(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TC-317: a new IP address clears the services, and a result for the old
// address is dropped.
func TestIPChangeClears(t *testing.T) {
	open, _ := ports(t)
	svc, _, _, _ := newInventory(t)
	h, err := svc.CreateHost(ctx, apitypes.HostCreate{IP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	c := New(svc, Config{Ports: []Port{{"ssh", open}}}, nil)
	if _, err := c.Check(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	h, err = svc.UpdateHost(ctx, h.ID, apitypes.HostPatch{IP: apitypes.Opt[string]{Set: true, Value: "127.0.0.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if h.Services != nil || h.ServicesCheckedAt != nil {
		t.Fatalf("after IP change: %v at %v", h.Services, h.ServicesCheckedAt)
	}
	if _, ok, err := svc.SetServices(ctx, h.ID, netip.MustParseAddr("127.0.0.1"), []string{"ssh"}, time.Now()); ok || err != nil {
		t.Fatalf("result for the old address saved: ok %v, %v", ok, err)
	}
}

// TC-316: Run checks monitored hosts that were never checked, then again
// after Refresh; unmonitored hosts only on request.
func TestRun(t *testing.T) {
	open, _ := ports(t)
	svc, _, _, _ := newInventory(t)
	off := false
	mon, err := svc.CreateHost(ctx, apitypes.HostCreate{IP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	unmon, err := svc.CreateHost(ctx, apitypes.HostCreate{IP: "127.0.0.2", Monitored: &off})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	var dials atomic.Int32
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	c := New(svc, Config{Ports: []Port{{"ssh", open}}, Refresh: time.Hour, Tick: 5 * time.Millisecond, Now: clock, Dial: dial}, nil)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { c.Run(rctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "first check", func() bool { h, _ := svc.Host(mon.ID); return h.ServicesCheckedAt != nil })
	time.Sleep(50 * time.Millisecond)
	if n := dials.Load(); n != 1 {
		t.Fatalf("%d connects before Refresh, want 1", n)
	}
	if h, _ := svc.Host(unmon.ID); h.ServicesCheckedAt != nil {
		t.Fatal("unmonitored host was checked")
	}
	mu.Lock()
	now = now.Add(time.Hour)
	mu.Unlock()
	waitFor(t, "check after Refresh", func() bool { h, _ := svc.Host(mon.ID); return h.ServicesCheckedAt.Equal(clock()) })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
