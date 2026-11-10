package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/monitor"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TestMain keeps the server tests off the real network: every test uses a
// fake prober unless it sets its own.
func TestMain(m *testing.M) {
	newProber = func() (monitor.Prober, string, error) { return fakeProber{}, apitypes.ICMPUnprivileged, nil }
	os.Exit(m.Run())
}

// syncBuf is a goroutine-safe log buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// startServer runs the server in the background; stop cancels it and
// returns run's error.
func startServer(t *testing.T, args ...string) (addr string, stop func() error, log *syncBuf) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no stray user config
	ctx, cancel := context.WithCancel(context.Background())
	log = &syncBuf{}
	readyc := make(chan string, 1)
	errc := make(chan error, 1)
	go func() { errc <- run(ctx, args, log, log, func(a string) { readyc <- a }) }()
	select {
	case addr = <-readyc:
	case err := <-errc:
		cancel()
		t.Fatalf("server did not start: %v\n%s", err, log)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("timeout waiting for server")
	}
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() { cancel(); err = <-errc })
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return addr, stop, log
}

// TC-801: a non-loopback listener without a token refuses to start.
func TestRefuseNonLoopbackWithoutToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db := filepath.Join(t.TempDir(), "x.db")
	err := run(context.Background(), []string{"-listen", "0.0.0.0:0", "-db", db}, &bytes.Buffer{}, &bytes.Buffer{}, nil)
	if err == nil || !strings.Contains(err.Error(), "server.token") {
		t.Fatalf("want refusal, got %v", err)
	}
	if _, statErr := os.Stat(db); statErr == nil {
		t.Error("database created although start was refused")
	}
}

// TC-802: SIGTERM/SIGINT (context cancel) shuts down cleanly: open SSE
// streams are closed at once, the database is closed and data is kept.
func TestCleanShutdown(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	addr, stop, log := startServer(t, "-listen", "127.0.0.1:0", "-db", db)

	resp, err := http.Post("http://"+addr+"/api/v1/groups", "application/json", strings.NewReader(`{"name":"g"}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("create: %v %v", err, resp)
	}
	resp.Body.Close()

	ev, err := http.Get("http://" + addr + "/api/v1/events")
	if err != nil || ev.StatusCode != 200 {
		t.Fatalf("events: %v %v", err, ev)
	}
	defer ev.Body.Close()
	streamDone := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, ev.Body); streamDone <- err }()

	start := time.Now()
	if err := stop(); err != nil {
		t.Fatalf("shutdown error: %v\n%s", err, log)
	}
	if d := time.Since(start); d > shutdownTimeout/2 {
		t.Errorf("shutdown took %v: the SSE stream held it up", d)
	}
	select {
	case err := <-streamDone:
		if err != nil {
			t.Errorf("stream ended with %v, want a clean end", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("SSE stream still open after shutdown")
	}
	if !strings.Contains(log.String(), "stopped") {
		t.Errorf("no clean stop in log:\n%s", log)
	}
	if _, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		t.Error("listener still open")
	}

	// Restart on the same database: the group is still there.
	addr2, _, _ := startServer(t, "-listen", "127.0.0.1:0", "-db", db)
	resp, err = http.Get("http://" + addr2 + "/api/v1/groups")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var gs []apitypes.Group
	if err := json.NewDecoder(resp.Body).Decode(&gs); err != nil || len(gs) != 1 {
		t.Errorf("after restart: %v %+v", err, gs)
	}
}

// fakeProber answers every ping; gate, if not nil, holds rounds until it
// is closed.
type fakeProber struct{ gate chan struct{} }

func (p fakeProber) Ping(ctx context.Context, _ netip.Addr, count int, _ time.Duration) (monitor.Result, error) {
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return monitor.Result{}, ctx.Err()
		}
	}
	return monitor.Result{Sent: count, Received: count, RTTs: []time.Duration{time.Millisecond}}, nil
}

func useProber(t *testing.T, p monitor.Prober, err error) {
	old := newProber
	newProber = func() (monitor.Prober, string, error) { return p, apitypes.ICMPUnprivileged, err }
	t.Cleanup(func() { newProber = old })
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func waitHost(t *testing.T, url string, cond func(apitypes.Host) bool) apitypes.Host {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var h apitypes.Host
		getJSON(t, url, &h)
		if cond(h) {
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout; host = %+v", h)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TC-602, TC-303: ping results are saved; after a restart the last status
// is served with stale = true until the first round, which clears it.
func TestMonitoringPersistsAndStale(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "x.db")
	cfg := filepath.Join(dir, "s.toml")
	if err := os.WriteFile(cfg, []byte("[monitor]\ninterval = \"1h\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	useProber(t, fakeProber{}, nil)
	addr, stop, _ := startServer(t, "-config", cfg, "-listen", "127.0.0.1:0", "-db", db)
	resp, err := http.Post("http://"+addr+"/api/v1/hosts", "application/json", strings.NewReader(`{"ip":"192.0.2.7"}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("create: %v %v", err, resp)
	}
	resp.Body.Close()
	h := waitHost(t, "http://"+addr+"/api/v1/hosts/1", func(h apitypes.Host) bool { return h.Status == apitypes.StatusUp })
	if h.RTTms == nil || *h.RTTms != 1 || h.LossPct == nil || *h.LossPct != 0 || h.LastSeen == nil || h.LastChange == nil {
		t.Fatalf("round not recorded: %+v", h)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	gate := make(chan struct{})
	useProber(t, fakeProber{gate: gate}, nil)
	addr, _, _ = startServer(t, "-config", cfg, "-listen", "127.0.0.1:0", "-db", db)
	var h2 apitypes.Host
	getJSON(t, "http://"+addr+"/api/v1/hosts/1", &h2)
	if !h2.Stale || h2.Status != apitypes.StatusUp || h2.RTTms == nil || !h2.LastSeen.Equal(*h.LastSeen) {
		t.Fatalf("after restart: %+v", h2)
	}
	close(gate)
	waitHost(t, "http://"+addr+"/api/v1/hosts/1", func(h apitypes.Host) bool { return !h.Stale && h.Status == apitypes.StatusUp })
}

// TC-304: without any ICMP mode the server runs, hosts stay unknown and
// /health reports the error.
func TestNoICMP(t *testing.T) {
	useProber(t, nil, errors.New("no ICMP mode available (test)"))
	addr, _, log := startServer(t, "-listen", "127.0.0.1:0", "-db", filepath.Join(t.TempDir(), "x.db"))
	resp, err := http.Post("http://"+addr+"/api/v1/hosts", "application/json", strings.NewReader(`{"ip":"192.0.2.7"}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("create: %v %v", err, resp)
	}
	resp.Body.Close()
	var hv apitypes.Health
	getJSON(t, "http://"+addr+"/api/v1/health", &hv)
	if hv.Status != "degraded" || hv.ICMPMode != apitypes.ICMPUnavailable || !strings.Contains(hv.ICMPError, "no ICMP mode") {
		t.Errorf("health: %+v", hv)
	}
	var h apitypes.Host
	getJSON(t, "http://"+addr+"/api/v1/hosts/1", &h)
	if h.Status != apitypes.StatusUnknown {
		t.Errorf("host: %+v", h)
	}
	if !strings.Contains(log.String(), "monitoring disabled") {
		t.Errorf("no log line:\n%s", log)
	}
}

// TC-803: with tls_cert and tls_key the server speaks HTTPS.
func TestTLS(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := selfSigned(t)
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "server.toml")
	conf := "[server]\nlisten = \"127.0.0.1:0\"\ntls_cert = \"" + certFile + "\"\ntls_key = \"" + keyFile + "\"\n" +
		"[storage]\ndb_path = \"" + filepath.Join(dir, "x.db") + "\"\n"
	if err := os.WriteFile(cfg, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, _, _ := startServer(t, "-config", cfg)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := client.Get("https://" + addr + "/api/v1/health")
	if err != nil {
		t.Fatalf("https: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.TLS == nil {
		t.Errorf("status %d, tls %v", resp.StatusCode, resp.TLS != nil)
	}
	// Plain HTTP to the TLS port must not work.
	if resp, err := http.Get("http://" + addr + "/api/v1/health"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Error("plain HTTP accepted on TLS port")
		}
	}
}

// TC-807: -version prints to stdout and exits without error.
func TestVersionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"-version"}, &out, &errOut, nil); err != nil || !strings.Contains(out.String(), "ined-server") {
		t.Errorf("version: %v stdout=%q", err, out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr not empty: %q", errOut.String())
	}
}

// TC-603: with no db_path the database is created at the XDG default.
func TestDefaultDBPath(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	startServer(t, "-listen", "127.0.0.1:0")
	if _, err := os.Stat(filepath.Join(data, "go-ined", "ined.db")); err != nil {
		t.Errorf("database not at XDG default: %v", err)
	}
}

// TC-604: a second server on the same database refuses to start.
func TestSecondServerRefused(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	startServer(t, "-listen", "127.0.0.1:0", "-db", db)
	err := run(context.Background(), []string{"-listen", "127.0.0.1:0", "-db", db}, &bytes.Buffer{}, &bytes.Buffer{}, nil)
	if err == nil || !strings.Contains(err.Error(), "in use by another process") {
		t.Fatalf("second server: want refusal, got %v", err)
	}
}

func selfSigned(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "go-ined test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}
