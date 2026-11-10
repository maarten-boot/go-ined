// Package testserver runs a real go-ined API server (SQLite store,
// inventory, event bus, discovery with a fake prober) inside a test. It is
// used by the tests of pkg/client and cmd/inedctl.
package testserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/api"
	"github.com/maarten-boot/go-ined/internal/discovery"
	"github.com/maarten-boot/go-ined/internal/events"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/monitor"
	"github.com/maarten-boot/go-ined/internal/store"
)

// Options configures Start.
type Options struct {
	Token string
	TLS   bool
	// Answer decides which addresses answer discovery probes; nil = every
	// address. Fail, if set, makes every probe fail with this error.
	Answer func(netip.Addr) bool
	Fail   error
	// Gate, if not nil, holds every probe until it is closed.
	Gate chan struct{}
	// Discovery tuning; zero values get fast test defaults.
	Discovery discovery.Config
	// Keepalive and WriteTimeout of the SSE stream (0 = API defaults).
	Keepalive, WriteTimeout time.Duration
}

// Server is a running test server. Its URL stays the same across Restart.
type Server struct {
	t    *testing.T
	opts Options
	db   string
	HTTP *httptest.Server

	h    atomic.Pointer[http.Handler]
	mu   sync.Mutex
	runs int
	st   *store.Store
	Svc  *inventory.Service
	Bus  *events.Bus
	disc *discovery.Manager
}

// Start starts a server; it is stopped when the test ends.
func Start(t *testing.T, opts Options) *Server {
	t.Helper()
	if opts.Discovery.Rate == 0 {
		opts.Discovery.Rate = 10000
	}
	if opts.Discovery.Flush == 0 {
		opts.Discovery.Flush = 20 * time.Millisecond
	}
	if opts.Discovery.Progress == 0 {
		opts.Discovery.Progress = 20 * time.Millisecond
	}
	if opts.Discovery.SecondTry == 0 {
		opts.Discovery.SecondTry = time.Millisecond
	}
	s := &Server{t: t, opts: opts, db: filepath.Join(t.TempDir(), "ined.db")}
	front := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := s.h.Load(); h != nil {
			(*h).ServeHTTP(w, r)
			return
		}
		http.Error(w, "down", http.StatusBadGateway)
	})
	if opts.TLS {
		s.HTTP = httptest.NewTLSServer(front)
	} else {
		s.HTTP = httptest.NewServer(front)
	}
	s.boot()
	t.Cleanup(func() {
		s.shutdown()
		s.HTTP.Close()
	})
	return s
}

// URL is the base URL, for client.Options.URL.
func (s *Server) URL() string { return s.HTTP.URL }

// RunID is the current run id ("run1", "run2", …).
func (s *Server) RunID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "run" + strconv.Itoa(s.runs)
}

func (s *Server) boot() {
	s.t.Helper()
	st, err := store.Open(context.Background(), s.db)
	if err != nil {
		s.t.Fatal(err)
	}
	svc, err := inventory.NewService(context.Background(), st)
	if err != nil {
		s.t.Fatal(err)
	}
	s.mu.Lock()
	s.runs++
	run := "run" + strconv.Itoa(s.runs)
	s.mu.Unlock()
	bus := events.New(run, 1000)
	svc.SetNotifier(bus)
	disc := discovery.New(svc, prober{s.opts}, "", bus, run, s.opts.Discovery, nil)
	var h http.Handler = api.New(svc, api.Options{
		Token: s.opts.Token, Version: "test", RunID: run, DBPing: st.Ping, Events: bus,
		Discovery: disc, Keepalive: s.opts.Keepalive, WriteTimeout: s.opts.WriteTimeout,
		ICMP: func() api.ICMPInfo { return api.ICMPInfo{Mode: "unprivileged"} },
	})
	s.mu.Lock()
	s.st, s.Svc, s.Bus, s.disc = st, svc, bus, disc
	s.mu.Unlock()
	s.h.Store(&h)
}

func (s *Server) shutdown() {
	s.mu.Lock()
	st, bus, disc := s.st, s.Bus, s.disc
	s.st = nil
	s.mu.Unlock()
	if st == nil {
		return
	}
	s.h.Store(nil)
	disc.Close()
	bus.Close() // ends the SSE streams
	s.HTTP.CloseClientConnections()
	st.Close()
}

// Down stops the server's API: requests get 502 and streams end, but the
// listener stays, so the URL doesn't change. Up starts it again.
func (s *Server) Down() { s.shutdown() }

// Up starts the API again after Down, with a new run id.
func (s *Server) Up() { s.boot() }

// Restart is Down then Up: a server restart on the same database.
func (s *Server) Restart() { s.Down(); s.Up() }

// BreakStore closes the database under the running server, so the next
// write fails (to test failed discovery jobs).
func (s *Server) BreakStore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.st.Close()
}

// prober is a fake discovery prober.
type prober struct{ o Options }

func (p prober) Ping(ctx context.Context, ip netip.Addr, count int, _ time.Duration) (monitor.Result, error) {
	if p.o.Gate != nil {
		select {
		case <-p.o.Gate:
		case <-ctx.Done():
			return monitor.Result{}, ctx.Err()
		}
	}
	if p.o.Fail != nil {
		return monitor.Result{}, p.o.Fail
	}
	if p.o.Answer != nil && !p.o.Answer(ip) {
		return monitor.Result{Sent: count}, nil
	}
	return monitor.Result{Sent: count, Received: count, RTTs: []time.Duration{time.Millisecond}, LastReply: time.Now()}, nil
}
