// Package config loads the TOML configuration files for the server, the GUI
// and the CLI (SPEC-805).
//
// Precedence, highest first: command-line flags, environment variables
// (clients only: INED_URL, INED_TOKEN), the config file, built-in defaults.
// Flags are applied by the caller after Load*.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// AppDir is the per-application subdirectory under the XDG directories.
const AppDir = "go-ined"

// Server is the configuration of ined-server.
type Server struct {
	Server    ServerSection    `toml:"server"`
	Storage   StorageSection   `toml:"storage"`
	Monitor   MonitorSection   `toml:"monitor"`
	Discovery DiscoverySection `toml:"discovery"`
	DNS       DNSSection       `toml:"dns"`
	Events    EventsSection    `toml:"events"`
}

// ServerSection holds the listener settings.
type ServerSection struct {
	Listen  string `toml:"listen"`
	Token   string `toml:"token"`
	TLSCert string `toml:"tls_cert"`
	TLSKey  string `toml:"tls_key"`
}

// StorageSection holds the database settings.
type StorageSection struct {
	// DBPath is the SQLite file. Empty means DefaultDBPath().
	DBPath string `toml:"db_path"`
}

// MonitorSection holds the ping settings (SPEC-301, SPEC-302).
type MonitorSection struct {
	Enabled   bool          `toml:"enabled"`
	Interval  time.Duration `toml:"interval"`
	Count     int           `toml:"count"`
	Timeout   time.Duration `toml:"timeout"`
	DownAfter int           `toml:"down_after"`
}

// DiscoverySection holds the discovery settings (SPEC-203).
type DiscoverySection struct {
	MaxPrefix   int `toml:"max_prefix"`
	Rate        int `toml:"rate"`
	Concurrency int `toml:"concurrency"`
}

// DNSSection holds the reverse-DNS settings (SPEC-401).
type DNSSection struct {
	Enabled     bool          `toml:"enabled"`
	Refresh     time.Duration `toml:"refresh"`
	Concurrency int           `toml:"concurrency"`
}

// EventsSection holds the event buffer settings (SPEC-503).
type EventsSection struct {
	Buffer int `toml:"buffer"`
}

// Client is the configuration of inedctl (and the [server] part of the GUI).
type Client struct {
	Server ClientServer `toml:"server"`
}

// ClientServer holds the server address and token used by clients.
type ClientServer struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

// GUI is the configuration of ined-gui.
type GUI struct {
	Server   ClientServer    `toml:"server"`
	Contrast ContrastSection `toml:"contrast"`
}

// ContrastSection holds the contrast preset (SPEC-003, SPEC-005).
// Validation of the values belongs to the theme package.
type ContrastSection struct {
	Preset   string  `toml:"preset"`
	MinRatio float64 `toml:"min_ratio"`
}

// DefaultServer returns the built-in server defaults.
func DefaultServer() Server {
	return Server{
		Server:    ServerSection{Listen: "127.0.0.1:8470"},
		Monitor:   MonitorSection{Enabled: true, Interval: 60 * time.Second, Count: 3, Timeout: time.Second, DownAfter: 2},
		Discovery: DiscoverySection{MaxPrefix: 16, Rate: 200, Concurrency: 64},
		DNS:       DNSSection{Enabled: true, Refresh: time.Hour, Concurrency: 20},
		Events:    EventsSection{Buffer: 1000},
	}
}

// DefaultClient returns the built-in inedctl defaults.
func DefaultClient() Client {
	return Client{Server: ClientServer{URL: "http://127.0.0.1:8470"}}
}

// DefaultGUI returns the built-in GUI defaults.
func DefaultGUI() GUI {
	return GUI{
		Server:   DefaultClient().Server,
		Contrast: ContrastSection{Preset: "AA", MinRatio: 4.5},
	}
}

// ConfigHome returns $XDG_CONFIG_HOME, falling back to ~/.config.
func ConfigHome() (string, error) { return xdgDir("XDG_CONFIG_HOME", ".config") }

// DataHome returns $XDG_DATA_HOME, falling back to ~/.local/share.
func DataHome() (string, error) { return xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")) }

func xdgDir(env, fallback string) (string, error) {
	// The XDG spec says relative paths must be ignored.
	if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: cannot determine home directory: %w", err)
	}
	return filepath.Join(home, fallback), nil
}

// DefaultPath returns the default location of a config file, e.g. "server.toml".
func DefaultPath(name string) (string, error) {
	dir, err := ConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, AppDir, name), nil
}

// DefaultDBPath returns the default SQLite database location.
func DefaultDBPath() (string, error) {
	dir, err := DataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, AppDir, "ined.db"), nil
}

// LoadServer reads the server config from path on top of the defaults.
// An empty path means the default path; a missing default file is not an
// error, but a missing explicitly given file is.
func LoadServer(path string) (Server, error) {
	cfg := DefaultServer()
	if err := load(path, "server.toml", &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// LoadClient reads the inedctl config and applies INED_URL / INED_TOKEN.
func LoadClient(path string) (Client, error) {
	cfg := DefaultClient()
	if err := load(path, "ctl.toml", &cfg); err != nil {
		return cfg, err
	}
	applyEnv(&cfg.Server)
	return cfg, nil
}

// LoadGUI reads the GUI config and applies INED_URL / INED_TOKEN.
func LoadGUI(path string) (GUI, error) {
	cfg := DefaultGUI()
	if err := load(path, "gui.toml", &cfg); err != nil {
		return cfg, err
	}
	applyEnv(&cfg.Server)
	return cfg, nil
}

func applyEnv(s *ClientServer) {
	if v, ok := os.LookupEnv("INED_URL"); ok && v != "" {
		s.URL = v
	}
	if v, ok := os.LookupEnv("INED_TOKEN"); ok && v != "" {
		s.Token = v
	}
}

func load(path, defaultName string, into any) error {
	explicit := path != ""
	if !explicit {
		p, err := DefaultPath(defaultName)
		if err != nil {
			return err
		}
		path = p
	}
	md, err := toml.DecodeFile(path, into)
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("config: %s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return fmt.Errorf("config: %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	return nil
}

// Validate checks value ranges of the server config.
func (c Server) Validate() error {
	var errs []error
	if c.Server.Listen == "" {
		errs = append(errs, errors.New("server.listen must not be empty"))
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		errs = append(errs, errors.New("server.tls_cert and server.tls_key must be set together"))
	}
	if c.Monitor.Interval <= 0 || c.Monitor.Timeout <= 0 {
		errs = append(errs, errors.New("monitor.interval and monitor.timeout must be positive"))
	}
	if c.Monitor.Count < 1 || c.Monitor.DownAfter < 1 {
		errs = append(errs, errors.New("monitor.count and monitor.down_after must be at least 1"))
	}
	if c.Discovery.MaxPrefix < 0 || c.Discovery.MaxPrefix > 32 {
		errs = append(errs, errors.New("discovery.max_prefix must be between 0 and 32"))
	}
	if c.Discovery.Rate < 1 || c.Discovery.Concurrency < 1 {
		errs = append(errs, errors.New("discovery.rate and discovery.concurrency must be at least 1"))
	}
	if c.DNS.Refresh <= 0 || c.DNS.Concurrency < 1 {
		errs = append(errs, errors.New("dns.refresh must be positive and dns.concurrency at least 1"))
	}
	if c.Events.Buffer < 1 {
		errs = append(errs, errors.New("events.buffer must be at least 1"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return nil
}
