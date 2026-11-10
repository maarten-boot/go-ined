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
	"syscall"
	"time"

	"github.com/maarten-boot/go-ined/internal/api"
	"github.com/maarten-boot/go-ined/internal/config"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/store"
	"github.com/maarten-boot/go-ined/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr, nil); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "ined-server:", err)
		os.Exit(1)
	}
}

// shutdownTimeout bounds the graceful shutdown (SPEC-804).
const shutdownTimeout = 10 * time.Second

// run starts the server and blocks until ctx is cancelled. ready, if not
// nil, receives the bound address once the server accepts connections.
func run(ctx context.Context, args []string, stderr io.Writer, ready func(addr string)) error {
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
		fmt.Fprintln(stderr, "ined-server", version.Version)
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

	runID := newRunID()
	handler := api.New(svc, api.Options{
		Token: cfg.Server.Token, Version: version.Version, RunID: runID, DBPing: st.Ping, Logger: log,
	})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
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
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
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
