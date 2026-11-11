package client_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/testserver"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

func newClient(t *testing.T, s *testserver.Server, token string) *client.Client {
	t.Helper()
	c, err := client.New(client.Options{URL: s.URL(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ptr[T any](v T) *T { return &v }

func apiErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *client.APIError
	if !errors.As(err, &ae) || ae.Status != status || ae.Code != code {
		t.Fatalf("got %v, want APIError %d %s", err, status, code)
	}
	if !client.IsCode(err, code) {
		t.Fatalf("IsCode(%v, %s) = false", err, code)
	}
}

// TC-503: every REST endpoint through the client.
func TestCRUD(t *testing.T) {
	s := testserver.Start(t, testserver.Options{Token: "secret"})
	c := newClient(t, s, "secret")
	ctx := context.Background()

	h, err := c.Health(ctx)
	if err != nil || h.Status != "ok" || h.RunID != "run1" || h.ICMPMode != apitypes.ICMPUnprivileged {
		t.Fatalf("health: %+v %v", h, err)
	}
	net1, err := c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "10.0.0.0/24"})
	if err != nil || net1.Name != "10.0.0.0/24" || !net1.Collapsed {
		t.Fatalf("create group: %+v %v", net1, err)
	}
	g2, err := c.CreateGroup(ctx, apitypes.GroupCreate{Name: "lab", Collapsed: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	host, err := c.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.0.5", Name: "web", Note: "n1"})
	if err != nil || host.ParentID == nil || *host.ParentID != net1.ID || host.DisplayName != "web" {
		t.Fatalf("create host: %+v %v", host, err)
	}
	_, err = c.CreateHost(ctx, apitypes.HostCreate{IP: "10.0.0.5"})
	apiErr(t, err, http.StatusConflict, apitypes.CodeConflict)
	_, err = c.CreateHost(ctx, apitypes.HostCreate{IP: "nonsense"})
	apiErr(t, err, http.StatusBadRequest, apitypes.CodeInvalidArgument)

	hs, err := c.Hosts(ctx)
	if err != nil || len(hs) != 1 || hs[0].ID != host.ID {
		t.Fatalf("hosts: %+v %v", hs, err)
	}
	got, err := c.Host(ctx, host.ID)
	if err != nil || got.Note != "n1" {
		t.Fatalf("host: %+v %v", got, err)
	}
	_, err = c.Host(ctx, 999)
	apiErr(t, err, http.StatusNotFound, apitypes.CodeNotFound)

	up, err := c.UpdateHost(ctx, host.ID, apitypes.HostPatch{
		ParentID: apitypes.Some(&g2.ID), Note: apitypes.Some(""), Monitored: apitypes.Some(false)})
	if err != nil || *up.ParentID != g2.ID || up.Note != "" || up.Monitored {
		t.Fatalf("update host: %+v %v", up, err)
	}
	up, err = c.UpdateHost(ctx, host.ID, apitypes.HostPatch{ParentID: apitypes.Some[*int64](nil)})
	if err != nil || up.ParentID != nil {
		t.Fatalf("move to root: %+v %v", up, err)
	}

	gs, err := c.Groups(ctx)
	if err != nil || len(gs) != 2 {
		t.Fatalf("groups: %+v %v", gs, err)
	}
	gg, err := c.Group(ctx, net1.ID)
	if err != nil || gg.CIDR != "10.0.0.0/24" {
		t.Fatalf("group: %+v %v", gg, err)
	}
	gg, err = c.UpdateGroup(ctx, net1.ID, apitypes.GroupPatch{Collapsed: apitypes.Some(false), Name: apitypes.Some("office")})
	if err != nil || gg.Collapsed || gg.Name != "office" {
		t.Fatalf("update group: %+v %v", gg, err)
	}
	m, err := c.Map(ctx)
	if err != nil || len(m.Items) != 3 {
		t.Fatalf("map: %+v %v", m, err)
	}

	// A non-empty group needs a children mode.
	if _, err := c.UpdateHost(ctx, host.ID, apitypes.HostPatch{ParentID: apitypes.Some(&g2.ID)}); err != nil {
		t.Fatal(err)
	}
	apiErr(t, c.DeleteGroup(ctx, g2.ID, ""), http.StatusConflict, apitypes.CodeConflict)
	if err := c.DeleteGroup(ctx, g2.ID, apitypes.ChildrenReparent); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteHost(ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteGroup(ctx, net1.ID, apitypes.ChildrenCascade); err != nil {
		t.Fatal(err)
	}
	if m, _ := c.Map(ctx); len(m.Items) != 0 {
		t.Fatalf("map not empty: %+v", m)
	}
}

// TC-503: Do with *json.RawMessage keeps the response unchanged.
func TestDoRaw(t *testing.T) {
	s := testserver.Start(t, testserver.Options{})
	c := newClient(t, s, "")
	ctx := context.Background()
	if _, err := c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := c.Do(ctx, http.MethodGet, "/hosts/1", nil, &raw); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(s.URL() + "/api/v1/hosts/1")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(raw) != strings.TrimRight(string(want), "\n") {
		t.Fatalf("raw differs:\n%s\n%s", raw, want)
	}
}

// TC-503: authentication and connection errors.
func TestErrors(t *testing.T) {
	s := testserver.Start(t, testserver.Options{Token: "secret"})
	ctx := context.Background()
	_, err := newClient(t, s, "wrong").Hosts(ctx)
	apiErr(t, err, http.StatusUnauthorized, apitypes.CodeUnauthenticated)
	h, err := newClient(t, s, "").Health(ctx)
	if err != nil || h.Status != "ok" || h.RunID != "" {
		t.Fatalf("reduced health: %+v %v", h, err)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c, _ := client.New(client.Options{URL: "http://" + addr})
	_, err = c.Health(ctx)
	var ae *client.APIError
	if err == nil || errors.As(err, &ae) {
		t.Fatalf("connection error expected, got %v", err)
	}

	for _, u := range []string{"", "127.0.0.1:8470", "ftp://x", "http://", "http://h?x=1"} {
		if _, err := client.New(client.Options{URL: u}); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
	// A non-API error body still gives an APIError, without a code.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer plain.Close()
	c, _ = client.New(client.Options{URL: plain.URL})
	_, err = c.Hosts(ctx)
	if !errors.As(err, &ae) || ae.Status != 502 || ae.Code != "" || ae.Message != "bad gateway" {
		t.Fatalf("plain error: %v", err)
	}
}

// writeCA saves the test server's certificate as a PEM file.
func writeCA(t *testing.T, s *testserver.Server) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "ca.pem")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.HTTP.Certificate().Raw})
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// TC-904: a self-signed server is trusted only with CACert.
func TestCACert(t *testing.T) {
	s := testserver.Start(t, testserver.Options{TLS: true})
	ctx := context.Background()
	c, _ := client.New(client.Options{URL: s.URL()})
	if _, err := c.Health(ctx); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	c, err := client.New(client.Options{URL: s.URL(), CACert: writeCA(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	if h, err := c.Health(ctx); err != nil || h.Status != "ok" {
		t.Fatalf("health over TLS: %+v %v", h, err)
	}
	if _, err := client.New(client.Options{URL: s.URL(), CACert: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Fatal("missing CA file accepted")
	}
	junk := filepath.Join(t.TempDir(), "junk.pem")
	os.WriteFile(junk, []byte("not a certificate"), 0o600)
	if _, err := client.New(client.Options{URL: s.URL(), CACert: junk}); err == nil {
		t.Fatal("CA file without a certificate accepted")
	}
}

// waitEvent reads events until match returns true.
func waitEvent(t *testing.T, st *client.Stream, match func(client.Event) bool) client.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-st.C():
			if !ok {
				t.Fatalf("stream ended: %v", st.Err())
			}
			if match(ev) {
				return ev
			}
		case <-timeout:
			t.Fatal("timeout waiting for an event")
		}
	}
}

func isType(typ string) func(client.Event) bool {
	return func(ev client.Event) bool { return ev.Type == typ }
}

// TC-503: discovery through the client, followed by events.
func TestDiscovery(t *testing.T) {
	gate := make(chan struct{})
	s := testserver.Start(t, testserver.Options{Gate: gate,
		Answer: func(a netip.Addr) bool { return a == netip.MustParseAddr("10.9.0.1") }})
	c := newClient(t, s, "")
	ctx := context.Background()
	st := c.Events(ctx, client.StreamOptions{})
	defer st.Close()
	<-st.Ready()

	d, err := c.StartDiscovery(ctx, apitypes.DiscoveryCreate{CIDR: "10.9.0.0/30"})
	if err != nil || d.State != apitypes.DiscoveryRunning || d.Total != 2 {
		t.Fatalf("start: %+v %v", d, err)
	}
	if got, err := c.Discovery(ctx, d.ID); err != nil || got.ID != d.ID {
		t.Fatalf("get: %+v %v", got, err)
	}
	close(gate)
	ev := waitEvent(t, st, isType(apitypes.EventDiscoveryFinished))
	var fin apitypes.Discovery
	if err := ev.Decode(&fin); err != nil || fin.State != apitypes.DiscoveryDone || fin.Found != 1 || fin.Added != 1 {
		t.Fatalf("finished: %+v %v", fin, err)
	}
	if !strings.Contains(ev.Short(), "done probed 2/2 found 1 added 1") {
		t.Errorf("short: %s", ev.Short())
	}
	_, err = c.Discovery(ctx, "nope")
	apiErr(t, err, http.StatusNotFound, apitypes.CodeNotFound)
	_, err = c.StartDiscovery(ctx, apitypes.DiscoveryCreate{CIDR: "2001:db8::/120"})
	apiErr(t, err, http.StatusBadRequest, apitypes.CodeInvalidArgument)
}

func TestCancelDiscovery(t *testing.T) {
	gate := make(chan struct{}) // never opened: probes wait until cancelled
	s := testserver.Start(t, testserver.Options{Gate: gate})
	c := newClient(t, s, "")
	ctx := context.Background()
	st := c.Events(ctx, client.StreamOptions{})
	defer st.Close()
	<-st.Ready()
	d, err := c.StartDiscovery(ctx, apitypes.DiscoveryCreate{CIDR: "10.9.0.0/29"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelDiscovery(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	var fin apitypes.Discovery
	waitEvent(t, st, isType(apitypes.EventDiscoveryFinished)).Decode(&fin)
	if fin.State != apitypes.DiscoveryCancelled {
		t.Fatalf("state %s", fin.State)
	}
}

// states records OnState calls.
type states struct {
	mu sync.Mutex
	l  []client.StreamState
}

func (s *states) add(st client.StreamState) { s.mu.Lock(); s.l = append(s.l, st); s.mu.Unlock() }
func (s *states) connects() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, st := range s.l {
		if st.Connected {
			n++
		}
	}
	return n
}

// TC-905: a dropped connection is resumed with Last-Event-ID, so an event
// published while disconnected is not lost.
func TestEventsReconnect(t *testing.T) {
	s := testserver.Start(t, testserver.Options{})
	c := newClient(t, s, "")
	ctx := context.Background()
	var sts states
	st := c.Events(ctx, client.StreamOptions{MinBackoff: 300 * time.Millisecond, OnState: sts.add})
	defer st.Close()
	<-st.Ready()

	a, _ := c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1"})
	ev := waitEvent(t, st, isType(apitypes.EventHostCreated))
	if ev.ID != "run1-1" || st.LastID() != ev.ID || !strings.HasPrefix(ev.Short(), "host 1 192.0.2.1") {
		t.Fatalf("event %+v short %q last %s", ev, ev.Short(), st.LastID())
	}
	_ = a
	s.HTTP.CloseClientConnections()
	// Within the 300 ms before the reconnect.
	if _, err := s.Svc.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2"}); err != nil {
		t.Fatal(err)
	}
	ev = waitEvent(t, st, isType(apitypes.EventHostCreated))
	var h apitypes.Host
	ev.Decode(&h)
	if h.IP != "192.0.2.2" || ev.ID != "run1-2" {
		t.Fatalf("missed event: %+v", ev)
	}
	if n := sts.connects(); n != 2 {
		t.Fatalf("%d connects, want 2", n)
	}
}

// TC-905: after a server restart the old id is unknown, so the client gets
// resync, and the stream continues.
func TestEventsRestart(t *testing.T) {
	s := testserver.Start(t, testserver.Options{})
	c := newClient(t, s, "")
	ctx := context.Background()
	st := c.Events(ctx, client.StreamOptions{MinBackoff: 20 * time.Millisecond})
	defer st.Close()
	<-st.Ready()
	c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1"})
	waitEvent(t, st, isType(apitypes.EventHostCreated))

	s.Down()
	time.Sleep(100 * time.Millisecond) // a few failed attempts (502)
	s.Up()
	ev := waitEvent(t, st, isType(apitypes.EventResync))
	if !strings.HasPrefix(ev.ID, "run2-") || !strings.HasPrefix(ev.Short(), "reload: ") {
		t.Fatalf("resync %+v", ev)
	}
	c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.2"})
	ev = waitEvent(t, st, isType(apitypes.EventHostCreated))
	if ev.ID != "run2-1" {
		t.Fatalf("after resync: %+v", ev)
	}
}

// A refused stream (401) ends with the error instead of retrying.
func TestEventsUnauthorized(t *testing.T) {
	s := testserver.Start(t, testserver.Options{Token: "secret"})
	st := newClient(t, s, "wrong").Events(context.Background(), client.StreamOptions{})
	for range st.C() {
	}
	apiErr(t, st.Err(), http.StatusUnauthorized, apitypes.CodeUnauthenticated)
}

// FailFirst reports an unreachable server; without it the stream retries
// until cancelled, and then Err is nil.
func TestEventsFailFirst(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c, _ := client.New(client.Options{URL: "http://" + addr})
	st := c.Events(context.Background(), client.StreamOptions{FailFirst: true})
	for range st.C() {
	}
	if st.Err() == nil {
		t.Fatal("no error")
	}

	var sts states
	ctx, cancel := context.WithCancel(context.Background())
	st = c.Events(ctx, client.StreamOptions{MinBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond, OnState: sts.add})
	time.Sleep(200 * time.Millisecond)
	cancel()
	for range st.C() {
	}
	sts.mu.Lock()
	n, last := len(sts.l), sts.l[len(sts.l)-1]
	sts.mu.Unlock()
	if st.Err() != nil || n < 3 || last.RetryIn != 40*time.Millisecond {
		t.Fatalf("err %v, %d attempts, last %+v", st.Err(), n, last)
	}
}

// A connection that goes silent (no keepalives) is dropped and retried.
func TestEventsIdleTimeout(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			w.(http.Flusher).Flush()
			<-r.Context().Done() // silent
			return
		}
		io.WriteString(w, ": hi\r\n\r\nid: x-1\r\nevent: resync\r\ndata: {\"reason\":\r\ndata: \"r\"}\r\n\r\n")
	}))
	defer srv.Close()
	c, _ := client.New(client.Options{URL: srv.URL})
	var sts states
	st := c.Events(context.Background(), client.StreamOptions{IdleTimeout: 100 * time.Millisecond, MinBackoff: 10 * time.Millisecond, OnState: sts.add})
	defer st.Close()
	ev := waitEvent(t, st, isType(apitypes.EventResync))
	var r apitypes.ResyncEvent
	if err := ev.Decode(&r); err != nil || r.Reason != "r" || ev.ID != "x-1" {
		t.Fatalf("multi-line data: %+v %q %v", r, ev.Data, err)
	}
	sts.mu.Lock()
	defer sts.mu.Unlock()
	if len(sts.l) < 2 || sts.l[1].Err == nil || !strings.Contains(sts.l[1].Err.Error(), "no data") {
		t.Fatalf("states %+v", sts.l)
	}
}

// I1 (crosscheck 05): a token over plain HTTP to a non-loopback host.
func TestTokenInClear(t *testing.T) {
	for _, c := range []struct {
		url, token string
		want       bool
	}{
		{"http://127.0.0.1:8470", "t", false},
		{"http://[::1]:8470", "t", false},
		{"http://localhost:8470", "t", false},
		{"http://ined.localhost", "t", false},
		{"https://192.0.2.1:8470", "t", false},
		{"http://192.0.2.1:8470", "", false},
		{"http://192.0.2.1:8470", "t", true},
		{"http://[2001:db8::1]:8470", "t", true},
		{"http://ined.example.org", "t", true},
	} {
		cl, err := client.New(client.Options{URL: c.url, Token: c.token})
		if err != nil {
			t.Fatal(err)
		}
		if got := cl.TokenInClear(); got != c.want {
			t.Errorf("%s token %q: %v, want %v", c.url, c.token, got, c.want)
		}
	}
}

// P3 (crosscheck 05): the one-line summary of every event type.
func TestEventShort(t *testing.T) {
	for _, c := range []struct {
		typ, data, want string
	}{
		{apitypes.EventHostCreated, `{"id":3,"ip":"192.0.2.1","display_name":"web","status":"up"}`, "host 3 192.0.2.1 web up"},
		{apitypes.EventHostUpdated, `{"id":3,"ip":"192.0.2.1","display_name":"web","status":"down"}`, "host 3 192.0.2.1 web down"},
		{apitypes.EventHostStatus, `{"id":3,"ip":"192.0.2.1","display_name":"web","status":"unknown"}`, "host 3 192.0.2.1 web unknown"},
		{apitypes.EventGroupCreated, `{"id":1,"name":"10.0.0.0/24","cidr":"10.0.0.0/24","status":"up"}`, "group 1 10.0.0.0/24 up"},
		{apitypes.EventGroupUpdated, `{"id":1,"name":"office","cidr":"10.0.0.0/24","status":"up"}`, "group 1 office 10.0.0.0/24 up"},
		{apitypes.EventHostDeleted, `{"id":4}`, "host 4"},
		{apitypes.EventGroupDeleted, `{"id":5}`, "group 5"},
		{apitypes.EventGroupStatus, `{"id":5,"status":"down"}`, "group 5 down"},
		{apitypes.EventGroupMoved, `{"id":5,"view_id":2,"dx":-10,"dy":2.5}`, "group 5 contents moved by -10,2.5 in view 2"},
		{apitypes.EventDiscoveryProgress, `{"id":"r-1","cidr":"10.0.0.0/30","state":"running","probed":1,"total":2}`, "job r-1 10.0.0.0/30 running probed 1/2 found 0 added 0"},
		{apitypes.EventDiscoveryFinished, `{"id":"r-1","cidr":"10.0.0.0/30","state":"failed","probed":2,"total":2,"errors":2,"error":"no route"}`, "job r-1 10.0.0.0/30 failed probed 2/2 found 0 added 0 errors 2: no route"},
		{apitypes.EventResync, `{"reason":"client fell behind"}`, "reload: client fell behind"},
		{apitypes.EventViewCreated, `{"id":2,"name":"Payroll","order":1}`, "view 2 Payroll order 1"},
		{apitypes.EventViewDeleted, `{"id":2}`, "view 2"},
		{apitypes.EventPlacementUpdated, `{"view_id":2,"host_id":5,"parent_id":3}`, "view 2 host 5 in group 3"},
		{apitypes.EventPlacementUpdated, `{"view_id":2,"host_id":5,"parent_id":null}`, "view 2 host 5 in root"},
		{apitypes.EventPlacementDeleted, `{"view_id":2,"host_id":5}`, "view 2 host 5 removed"},
		{"other.type", `{"x":1}`, `{"x":1}`},
		{apitypes.EventHostCreated, `not json`, `not json`},
	} {
		ev := client.Event{Type: c.typ, Data: json.RawMessage(c.data)}
		if got := ev.Short(); got != c.want {
			t.Errorf("%s: %q, want %q", c.typ, got, c.want)
		}
	}
}

// TC-129: the view calls of the client.
func TestViews(t *testing.T) {
	s := testserver.Start(t, testserver.Options{})
	c := newClient(t, s, "")
	ctx := context.Background()
	v, err := c.CreateView(ctx, apitypes.ViewCreate{Name: "Payroll"})
	if err != nil || v.ID != 2 || v.Kind != apitypes.ViewCustom {
		t.Fatalf("create: %+v %v", v, err)
	}
	if v, err = c.UpdateView(ctx, v.ID, apitypes.ViewPatch{Name: apitypes.Some("Pay")}); err != nil || v.Name != "Pay" {
		t.Fatalf("update: %+v %v", v, err)
	}
	if got, err := c.View(ctx, v.ID); err != nil || got != v {
		t.Fatalf("get: %+v %v", got, err)
	}
	vs, err := c.Views(ctx)
	if err != nil || len(vs) != 2 || vs[0].ID != 1 {
		t.Fatalf("list: %+v %v", vs, err)
	}
	g, err := c.CreateGroup(ctx, apitypes.GroupCreate{Name: "app", ViewID: &v.ID})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1"})
	p, err := c.PlaceHost(ctx, v.ID, h.ID, apitypes.PlacementSet{ParentID: &g.ID})
	if err != nil || *p.ParentID != g.ID {
		t.Fatalf("place: %+v %v", p, err)
	}
	m, err := c.ViewMap(ctx, v.ID)
	if err != nil || m.ViewID != v.ID || len(m.Items) != 2 {
		t.Fatalf("map: %+v %v", m, err)
	}
	gs, err := c.ViewGroups(ctx, v.ID)
	if err != nil || len(gs) != 1 || gs[0].ViewID != v.ID {
		t.Fatalf("groups: %+v %v", gs, err)
	}
	if err := c.UnplaceHost(ctx, v.ID, h.ID); err != nil {
		t.Fatal(err)
	}
	apiErr(t, c.UnplaceHost(ctx, v.ID, h.ID), http.StatusNotFound, apitypes.CodeNotFound)
	apiErr(t, c.DeleteView(ctx, 1), http.StatusConflict, apitypes.CodeConflict)
	if err := c.DeleteView(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	_, err = c.ViewMap(ctx, v.ID)
	apiErr(t, err, http.StatusNotFound, apitypes.CodeNotFound)
}

// TC-715, TC-717 (client): MoveGroup and the host's views.
func TestMoveGroupAndViews(t *testing.T) {
	s := testserver.Start(t, testserver.Options{})
	c := newClient(t, s, "")
	ctx := context.Background()
	g, _ := c.CreateGroup(ctx, apitypes.GroupCreate{Name: "g", X: ptrF(10), Y: ptrF(10)})
	h, _ := c.CreateHost(ctx, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &g.ID, X: ptrF(20), Y: ptrF(30)})
	g2, err := c.MoveGroup(ctx, g.ID, 5, -5)
	if err != nil || g2.X != 15 || g2.Y != 5 {
		t.Fatalf("move: %+v %v", g2, err)
	}
	h2, _ := c.Host(ctx, h.ID)
	if h2.X != 25 || h2.Y != 25 || len(h2.Views) != 1 || h2.Views[0] != 1 {
		t.Errorf("host after move: %+v", h2)
	}
	if _, err := c.MoveGroup(ctx, 99, 1, 1); !client.IsCode(err, apitypes.CodeNotFound) {
		t.Errorf("unknown group: %v", err)
	}
	var raw json.RawMessage
	if err := c.Do(ctx, "GET", "/groups/1/move", nil, &raw); !client.IsCode(err, apitypes.CodeMethodNotAllowed) {
		t.Errorf("GET move: %v", err)
	}
	if err := c.Do(ctx, "POST", "/groups/1/move", json.RawMessage(`{"dx":"x"}`), &raw); !client.IsCode(err, apitypes.CodeInvalidArgument) {
		t.Errorf("bad body: %v", err)
	}
}

func ptrF(f float64) *float64 { return &f }
