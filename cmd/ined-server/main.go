// Command ined-server is the go-ined inventory and monitoring daemon.
//
// Usage:
//
//	ined-server [-config server.toml] [-listen addr] [-db path] [-token tok]
//
// Flags override the config file (SPEC-805).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/maarten-boot/go-ined/internal/api"
	"github.com/maarten-boot/go-ined/internal/config"
	"github.com/maarten-boot/go-ined/internal/discovery"
	"github.com/maarten-boot/go-ined/internal/dnsname"
	"github.com/maarten-boot/go-ined/internal/events"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/monitor"
	"github.com/maarten-boot/go-ined/internal/store"
	"github.com/maarten-boot/go-ined/internal/version"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "ined-server:", err)
		os.Exit(1)
	}
}

// shutdownTimeout bounds the graceful shutdown (SPEC-804).
const shutdownTimeout = 10 * time.Second

// newProber finds the ICMP prober; tests replace it with a fake. If the
// prober is an io.Closer it is closed at shutdown, after the monitor.
var newProber = func() (monitor.Prober, monitor.Status, error) {
	p, err := monitor.NewICMPProber()
	if err != nil {
		return nil, monitor.Status{}, err
	}
	return p, p.Status(), nil
}

// newResolver returns the reverse-DNS resolver; tests replace it.
var newResolver = func() dnsname.Resolver { return net.DefaultResolver }

// run starts the server and blocks until ctx is cancelled. ready, if not
// nil, receives the bound address once the server accepts connections.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, ready func(addr string)) error {
	fs := flag.NewFlagSet("ined-server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default $XDG_CONFIG_HOME/go-ined/server.toml)")
	listen := fs.String("listen", "", "listen address, overrides server.listen")
	dbPath := fs.String("db", "", "database file, overrides storage.db_path")
	token := fs.String("token", "", "bearer token, overrides server.token")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintln(stdout, "ined-server", version.Version)
		return nil
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))

	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			cfg.Server.Listen = *listen
		case "db":
			cfg.Storage.DBPath = *dbPath
		case "token":
			cfg.Server.Token = *token
		}
	})
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.CheckListenSecurity(cfg); err != nil {
		return err
	}
	if cfg.Storage.DBPath == "" {
		if cfg.Storage.DBPath, err = config.DefaultDBPath(); err != nil {
			return err
		}
	}

	st, err := store.Open(ctx, cfg.Storage.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	svc, err := inventory.NewService(ctx, st)
	if err != nil {
		return err
	}
	for _, w := range svc.LoadWarnings() {
		log.Warn("database repaired at load", "detail", w)
	}

	runID := newRunID()
	bus := events.New(runID, cfg.Events.Buffer)
	svc.SetNotifier(bus)

	// ICMP (SPEC-304): without a working mode the server keeps running,
	// hosts stay unknown and /health reports the error. The prober is also
	// used by discovery, even when monitoring is turned off (SPEC-201).
	icmp := api.ICMPInfo{Mode: apitypes.ICMPUnavailable}
	prober, ps, perr := newProber()
	discoveryProber, whyNot := prober, ""
	switch {
	case perr != nil:
		icmp.Error, whyNot, discoveryProber = perr.Error(), perr.Error(), nil
		log.Error("ICMP unavailable: no monitoring, no discovery", "err", perr)
	default:
		icmp = api.ICMPInfo{Mode: ps.Mode, Error: ps.IPv4Error, IPv6Error: ps.IPv6Error}
		if ps.IPv4Error != "" {
			whyNot, discoveryProber = "IPv4 ICMP is unavailable: "+ps.IPv4Error, nil
			log.Warn("IPv4 hosts are not pinged", "err", ps.IPv4Error)
		}
		if ps.IPv6Error != "" {
			log.Warn("IPv6 hosts are not pinged", "err", ps.IPv6Error)
		}
	}

	// Background work: monitoring and reverse DNS.
	bgCtx, bgStop := context.WithCancel(context.Background())
	defer bgStop()
	var bgWG sync.WaitGroup
	goBG := func(f func(context.Context)) {
		bgWG.Add(1)
		go func() { defer bgWG.Done(); f(bgCtx) }()
	}
	switch {
	case !cfg.Monitor.Enabled:
		icmp.Mode = apitypes.ICMPDisabled
		log.Info("monitoring disabled by config (monitor.enabled = false)")
	case perr == nil:
		log.Info("monitoring started", "icmp_mode", ps.Mode, "interval", cfg.Monitor.Interval)
		goBG(monitor.New(prober, svc, monitor.Config{
			Interval: cfg.Monitor.Interval, Count: cfg.Monitor.Count,
			Timeout: cfg.Monitor.Timeout, DownAfter: cfg.Monitor.DownAfter,
		}, log).Run)
	}
	if cfg.DNS.Enabled {
		goBG(dnsname.New(newResolver(), svc, dnsname.Config{
			Refresh: cfg.DNS.Refresh, Concurrency: cfg.DNS.Concurrency,
		}, log).Run)
	} else {
		log.Info("reverse DNS disabled by config (dns.enabled = false)")
	}
	disc := discovery.New(svc, discoveryProber, whyNot, bus, runID, discovery.Config{
		MaxPrefix: cfg.Discovery.MaxPrefix, Rate: cfg.Discovery.Rate,
		Concurrency: cfg.Discovery.Concurrency, Timeout: cfg.Monitor.Timeout,
	}, log)

	// stopBackground follows the shutdown order of SPEC-804 up to the HTTP
	// server: discovery (saves what it found), DNS and monitor (save
	// pending results), the prober, then the SSE streams.
	stopBackground := func() {
		disc.Close()
		bgStop()
		bgWG.Wait()
		if c, ok := prober.(io.Closer); ok {
			c.Close()
		}
		bus.Close()
	}

	handler := api.New(svc, api.Options{
		Token: cfg.Server.Token, Version: version.Version, RunID: runID, DBPing: st.Ping, Logger: log,
		Events: bus, ICMP: func() api.ICMPInfo { return icmp }, Discovery: disc,
	})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No ReadTimeout/WriteTimeout: SSE streams are long-lived.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return err
	}
	tls := cfg.Server.TLSCert != ""
	log.Info("ined-server started", "version", version.Version, "listen", ln.Addr().String(),
		"tls", tls, "auth", cfg.Server.Token != "", "db", cfg.Storage.DBPath, "run_id", runID)

	errc := make(chan error, 1)
	go func() {
		if tls {
			errc <- srv.ServeTLS(ln, cfg.Server.TLSCert, cfg.Server.TLSKey)
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	if ready != nil {
		ready(ln.Addr().String())
	}

	select {
	case err := <-errc:
		stopBackground()
		return err
	case <-ctx.Done():
	}
	// Shutdown order (SPEC-804): background work and SSE streams, then the
	// HTTP server; the database is closed by the deferred Close.
	log.Info("shutting down")
	stopBackground()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped")
	return nil
}

func newRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b)
}
