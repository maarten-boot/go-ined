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

// icmpRecheck is how often the server looks again for a missing ICMP
// mode (SPEC-304); tests shorten it.
var icmpRecheck = time.Minute

// newProber finds the ICMP prober; tests replace it with a fake. If the
// prober is an io.Closer it is closed at shutdown, after the monitor.
var newProber = func() (monitor.Prober, monitor.Status, error) {
	p, err := monitor.NewICMPProber()
	if err != nil {
		return nil, monitor.Status{}, err
	}
	return p, p.Status(), nil
}

// newLogger builds the server log from [log] (SPEC-807); Validate has
// checked the values.
func newLogger(c config.LogSection, w io.Writer) *slog.Logger {
	lv, _ := c.SlogLevel()
	opts := &slog.HandlerOptions{Level: lv}
	if c.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
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
	logLevel := fs.String("log-level", "", "debug, info, warn or error; overrides log.level")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintln(stdout, "ined-server", version.Version)
		return nil
	}
	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "log-level":
			cfg.Log.Level = *logLevel
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
	log := newLogger(cfg.Log, stderr)
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
	// While a mode is missing, the Rechecker tries again (every
	// icmpRecheck, and before each discovery job), so a fixed sysctl or a
	// new capability works without a restart (v0.27).
	var icmpMu sync.Mutex
	var icmp api.ICMPInfo
	setICMP := func(st monitor.Status, err error) {
		icmpMu.Lock()
		defer icmpMu.Unlock()
		icmp = api.ICMPInfo{Mode: st.Mode, Error: st.IPv4Error, IPv6Error: st.IPv6Error}
		if err != nil {
			icmp = api.ICMPInfo{Mode: apitypes.ICMPUnavailable, Error: err.Error()}
		}
		if !cfg.Monitor.Enabled {
			icmp.Mode = apitypes.ICMPDisabled
		}
	}
	prober := monitor.NewRechecker(newProber, icmpRecheck, func(st monitor.Status, err error) {
		setICMP(st, err)
		log.Info("ICMP found again", "icmp_mode", st.Mode, "ipv4_error", st.IPv4Error, "ipv6_error", st.IPv6Error)
	})
	ps, perr := prober.State()
	setICMP(ps, perr)
	switch {
	case perr != nil:
		log.Error("ICMP unavailable: no monitoring, no discovery until it works; checking again every "+icmpRecheck.String(), "err", perr)
	default:
		if ps.IPv4Error != "" {
			log.Warn("IPv4 hosts are not pinged", "err", ps.IPv4Error)
		}
		if ps.IPv6Error != "" {
			log.Warn("IPv6 hosts are not pinged", "err", ps.IPv6Error)
		}
	}
	// discoveryReady finds ICMP again if needed before a job (SPEC-201).
	discoveryReady := func() string {
		st, err := prober.Recheck()
		switch {
		case err != nil:
			return err.Error()
		case st.IPv4Error != "":
			return "IPv4 ICMP is unavailable: " + st.IPv4Error
		}
		return ""
	}

	// Background work: monitoring and reverse DNS.
	bgCtx, bgStop := context.WithCancel(context.Background())
	defer bgStop()
	var bgWG sync.WaitGroup
	goBG := func(f func(context.Context)) {
		bgWG.Add(1)
		go func() { defer bgWG.Done(); f(bgCtx) }()
	}
	if cfg.Monitor.Enabled {
		// Started even without ICMP: hosts of a family without a mode are
		// skipped until the Rechecker finds one (SPEC-304).
		log.Info("monitoring started", "icmp_mode", ps.Mode, "interval", cfg.Monitor.Interval)
		goBG(monitor.New(prober, svc, monitor.Config{
			Interval: cfg.Monitor.Interval, Count: cfg.Monitor.Count,
			Timeout: cfg.Monitor.Timeout, DownAfter: cfg.Monitor.DownAfter,
		}, log).Run)
	} else {
		log.Info("monitoring disabled by config (monitor.enabled = false)")
	}
	goBG(func(ctx context.Context) { // look for missing ICMP modes
		t := time.NewTicker(icmpRecheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !prober.Complete() {
					prober.Recheck()
				}
			}
		}
	})
	if cfg.DNS.Enabled {
		goBG(dnsname.New(newResolver(), svc, dnsname.Config{
			Refresh: cfg.DNS.Refresh, Concurrency: cfg.DNS.Concurrency,
		}, log).Run)
	} else {
		log.Info("reverse DNS disabled by config (dns.enabled = false)")
	}
	disc := discovery.New(svc, prober, "", bus, runID, discovery.Config{
		MaxPrefix: cfg.Discovery.MaxPrefix, Rate: cfg.Discovery.Rate,
		Concurrency: cfg.Discovery.Concurrency, Timeout: cfg.Monitor.Timeout,
		Ready: discoveryReady,
	}, log)

	// stopBackground follows the shutdown order of SPEC-804 up to the HTTP
	// server: discovery (saves what it found), DNS and monitor (save
	// pending results), the prober, then the SSE streams.
	stopBackground := func() {
		disc.Close()
		bgStop()
		bgWG.Wait()
		prober.Close()
		bus.Close()
	}

	handler := api.New(svc, api.Options{
		Token: cfg.Server.Token, Version: version.Version, RunID: runID, DBPing: st.Ping, Logger: log,
		Events: bus, Discovery: disc,
		ICMP: func() api.ICMPInfo {
			icmpMu.Lock()
			defer icmpMu.Unlock()
			return icmp
		},
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
