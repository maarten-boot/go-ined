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
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

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

// TC-802: SIGTERM/SIGINT (context cancel) shuts down cleanly; the database
// is closed and data is kept. SSE stream closing is added in M2.
func TestCleanShutdown(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	addr, stop, log := startServer(t, "-listen", "127.0.0.1:0", "-db", db)

	resp, err := http.Post("http://"+addr+"/api/v1/groups", "application/json", strings.NewReader(`{"name":"g"}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("create: %v %v", err, resp)
	}
	resp.Body.Close()

	if err := stop(); err != nil {
		t.Fatalf("shutdown error: %v\n%s", err, log)
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
