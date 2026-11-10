package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/events"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

type frame struct {
	id, typ, data string
	comment       bool
}

// stream is a minimal SSE client.
type stream struct {
	t      *testing.T
	resp   *http.Response
	frames chan frame
	cancel context.CancelFunc
}

func (e *env) stream(lastID string) *stream {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+Prefix+"/events", nil)
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		e.t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		cancel()
		e.t.Fatalf("events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	s := &stream{t: e.t, resp: resp, frames: make(chan frame, 100), cancel: cancel}
	go func() {
		defer close(s.frames)
		sc := bufio.NewScanner(resp.Body)
		var f frame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				s.frames <- f
				f = frame{}
			case strings.HasPrefix(line, ":"):
				f.comment = true
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.typ = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.data = line[6:]
			}
		}
	}()
	e.t.Cleanup(s.close)
	return s
}

func (s *stream) close() { s.cancel(); s.resp.Body.Close() }

// next returns the next event, skipping comments; ok is false at the end
// of the stream.
func (s *stream) next() (f frame, ok bool) {
	s.t.Helper()
	for {
		select {
		case f, ok = <-s.frames:
			if !ok || !f.comment {
				return f, ok
			}
		case <-time.After(5 * time.Second):
			s.t.Fatal("timeout waiting for an event")
		}
	}
}

func (s *stream) want(typ string) frame {
	s.t.Helper()
	f, ok := s.next()
	if !ok || f.typ != typ {
		s.t.Fatalf("got %+v (open=%v), want %s", f, ok, typ)
	}
	return f
}

// TC-502, TC-507, TC-112: events arrive in order with the documented
// payloads; a second client sees a collapse.
func TestEventsStream(t *testing.T) {
	e := newEnv(t, "tok")
	a, b := e.stream(""), e.stream("")

	var g apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{CIDR: "10.0.0.0/24"}, &g)
	var h apitypes.Host
	e.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "10.0.0.5"}, &h)
	e.want(200, "PATCH", "/groups/"+itoa(g.ID), map[string]any{"collapsed": false}, nil)
	if err := e.svc.ApplyObservations(context.Background(), []inventory.Observation{{
		HostID: h.ID, IP: netip.MustParseAddr("10.0.0.5"), Status: apitypes.StatusUp, Replied: true, At: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	e.want(204, "DELETE", "/groups/"+itoa(g.ID)+"?children=cascade", nil, nil)

	for _, s := range []*stream{a, b} {
		f := s.want(apitypes.EventGroupCreated)
		var gv apitypes.Group
		if json.Unmarshal([]byte(f.data), &gv); gv.CIDR != "10.0.0.0/24" || f.id != "run1-1" {
			t.Errorf("group.created: %+v", f)
		}
		f = s.want(apitypes.EventHostCreated)
		var hv apitypes.Host
		if json.Unmarshal([]byte(f.data), &hv); hv.IP != "10.0.0.5" || hv.ParentID == nil || *hv.ParentID != g.ID {
			t.Errorf("host.created: %+v", f)
		}
		f = s.want(apitypes.EventGroupUpdated)
		if json.Unmarshal([]byte(f.data), &gv); gv.Collapsed {
			t.Errorf("group.updated: %+v", f)
		}
		f = s.want(apitypes.EventHostStatus)
		if json.Unmarshal([]byte(f.data), &hv); hv.Status != apitypes.StatusUp {
			t.Errorf("host.status: %+v", f)
		}
		if f = s.want(apitypes.EventGroupStatus); f.data != `{"id":1,"status":"up"}` {
			t.Errorf("group.status: %+v", f)
		}
		if f = s.want(apitypes.EventHostDeleted); f.data != `{"id":1}` {
			t.Errorf("host.deleted: %+v", f)
		}
		if f = s.want(apitypes.EventGroupDeleted); f.data != `{"id":1}` || f.id != "run1-7" {
			t.Errorf("group.deleted: %+v", f)
		}
	}
}

// TC-502, TC-506: Last-Event-ID replays missed events; an id from an
// earlier run gets resync carrying the newest id.
func TestEventsReplayAndResync(t *testing.T) {
	e := newEnv(t, "")
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "a"}, nil)
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "b"}, nil)
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "c"}, nil)

	s := e.stream("run1-1")
	if f := s.want(apitypes.EventGroupCreated); f.id != "run1-2" {
		t.Fatalf("replay starts at %s", f.id)
	}
	if f := s.want(apitypes.EventGroupCreated); f.id != "run1-3" {
		t.Fatalf("replay continues at %s", f.id)
	}
	s.close()

	e.restart()
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "d"}, nil)
	s = e.stream("run1-3")
	f := s.want(apitypes.EventResync)
	if f.id != "run2-1" || !strings.Contains(f.data, "another server run") {
		t.Fatalf("resync: %+v", f)
	}
	// The stream stays open and continues after the resync id.
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "e"}, nil)
	if f := s.want(apitypes.EventGroupCreated); f.id != "run2-2" {
		t.Fatalf("after resync: %+v", f)
	}
}

// TC-802 (API part): closing the bus ends open streams; a new stream is
// refused with 503.
func TestEventsClosedAtShutdown(t *testing.T) {
	e := newEnv(t, "")
	s := e.stream("")
	e.bus.Close()
	if f, ok := s.next(); ok {
		t.Fatalf("stream still open, got %+v", f)
	}
	e.wantErr(503, apitypes.CodeUnavailable, "GET", "/events", nil)
}

// TC-507: a client that falls behind gets resync and the stream ends.
func TestEventsLagged(t *testing.T) {
	bus := events.New("r", 10)
	srv := &server{opts: Options{Events: bus, Keepalive: time.Minute, WriteTimeout: time.Minute}}
	rec := &blockingWriter{header: http.Header{}, unblock: make(chan struct{}), wrote: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		srv.events(rec, (&http.Request{Header: http.Header{}}).WithContext(context.Background()))
		close(done)
	}()
	<-rec.wrote // the handler is blocked writing ": connected"
	for range events.SubBuffer + 5 {
		bus.Publish("x", 1)
	}
	close(rec.unblock)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not end")
	}
	if !strings.Contains(rec.body.String(), "event: resync\n") || !strings.Contains(rec.body.String(), "fell behind") {
		t.Fatalf("no resync in %q", rec.body.String())
	}
}

// TC-509: a client that stops reading is cut off after the write timeout,
// so it can't hold up the shutdown.
func TestEventsStalledClient(t *testing.T) {
	bus := events.New("r", 10)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var returned atomic.Bool
	h := New(nil, Options{Events: bus, WriteTimeout: 300 * time.Millisecond})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		returned.Store(true)
	})}
	go srv.Serve(ln)
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.(*net.TCPConn).SetReadBuffer(4096)
	if _, err := c.Write([]byte("GET /api/v1/events HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	// Publish steadily until the socket buffers are full; never read.
	big := strings.Repeat("x", 4000)
	deadline := time.Now().Add(10 * time.Second)
	for !returned.Load() && time.Now().Before(deadline) {
		for range 50 {
			bus.Publish("x", big)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !returned.Load() {
		t.Fatal("handler still blocked on a client that doesn't read")
	}
	bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	runtime.KeepAlive(c)
}

// blockingWriter blocks the first Write until unblock is closed.
type blockingWriter struct {
	header  http.Header
	body    strings.Builder
	unblock chan struct{}
	wrote   chan struct{}
	first   bool
}

func (w *blockingWriter) Header() http.Header { return w.header }
func (w *blockingWriter) WriteHeader(int)     {}
func (w *blockingWriter) Flush()              {}
func (w *blockingWriter) Write(p []byte) (int, error) {
	if !w.first {
		w.first = true
		w.wrote <- struct{}{}
		<-w.unblock
	}
	return w.body.Write(p)
}

// TC-502: the keepalive comment is sent on schedule.
func TestEventsKeepalive(t *testing.T) {
	bus := events.New("r", 10)
	ts := httptest.NewServer(New(nil, Options{Events: bus, Keepalive: 20 * time.Millisecond}))
	defer ts.Close()
	defer bus.Close()
	e := &env{t: t, srv: ts}
	s := e.stream("")
	for range 2 { // ": connected", then at least one keepalive
		select {
		case f := <-s.frames:
			if !f.comment {
				t.Fatalf("got %+v", f)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no keepalive")
		}
	}
}
