// Package services finds out which remote-access and web services a host
// offers, by trying a TCP connection to a short, fixed list of ports
// (SPEC-309). It is not a port scan: one connect per port, no banner.
package services

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Port is a service and its TCP port.
type Port struct {
	Name string
	Port uint16
}

// DefaultPorts are the ports checked: SSH, RDP, VNC displays 0–9, HTTP and
// HTTPS. VNC display n is "vnc:n" on port 5900+n; display 0 is "vnc".
func DefaultPorts() []Port {
	ps := []Port{{"ssh", 22}, {"rdp", 3389}, {"vnc", 5900}}
	for n := 1; n <= 9; n++ {
		ps = append(ps, Port{"vnc:" + strconv.Itoa(n), uint16(5900 + n)})
	}
	return append(ps, Port{"http", 80}, Port{"https", 443})
}

// Inventory is the part of inventory.Service the checker uses.
type Inventory interface {
	ServiceTargets() []inventory.ServiceTarget
	ServiceTarget(id int64) (inventory.ServiceTarget, error)
	SetServices(ctx context.Context, id int64, ip netip.Addr, services []string, at time.Time) (apitypes.Host, bool, error)
}

// Config holds the check settings. Zero values get defaults.
type Config struct {
	Ports       []Port        // default DefaultPorts()
	Refresh     time.Duration // between checks of a monitored host (default 6 h)
	Timeout     time.Duration // per connect (default 1 s)
	Concurrency int           // hosts checked at once (default 10)
	Tick        time.Duration // how often due hosts are looked for (default 10 s)
	Dial        func(ctx context.Context, network, addr string) (net.Conn, error)
	Now         func() time.Time
}

func (c *Config) defaults() {
	if len(c.Ports) == 0 {
		c.Ports = DefaultPorts()
	}
	if c.Refresh <= 0 {
		c.Refresh = 6 * time.Hour
	}
	if c.Timeout <= 0 {
		c.Timeout = time.Second
	}
	if c.Concurrency < 1 {
		c.Concurrency = 10
	}
	if c.Tick <= 0 {
		c.Tick = 10 * time.Second
	}
	if c.Dial == nil {
		c.Dial = (&net.Dialer{}).DialContext
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Checker checks the services of monitored hosts when they are new (never
// checked) and then every Refresh, and of any host on request (Check).
type Checker struct {
	inv Inventory
	cfg Config
	log *slog.Logger

	mu   sync.Mutex
	busy map[int64]bool // hosts being checked by Run
}

// New returns a checker. log may be nil.
func New(inv Inventory, cfg Config, log *slog.Logger) *Checker {
	cfg.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Checker{inv: inv, cfg: cfg, log: log, busy: map[int64]bool{}}
}

// Probe tries every port of ip at once and returns the names of those that
// accepted a connection, in the order of Config.Ports.
func (c *Checker) Probe(ctx context.Context, ip netip.Addr) []string {
	open := make([]bool, len(c.cfg.Ports))
	var wg sync.WaitGroup
	for i, p := range c.cfg.Ports {
		wg.Go(func() {
			dctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
			defer cancel()
			conn, err := c.cfg.Dial(dctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(int(p.Port))))
			if err == nil {
				conn.Close()
				open[i] = true
			}
		})
	}
	wg.Wait()
	out := []string{}
	for i, ok := range open {
		if ok {
			out = append(out, c.cfg.Ports[i].Name)
		}
	}
	return out
}

// Check checks one host now and saves the result (SPEC-309, "Check
// services now"). It takes about Config.Timeout.
func (c *Checker) Check(ctx context.Context, id int64) (apitypes.Host, error) {
	t, err := c.inv.ServiceTarget(id)
	if err != nil {
		return apitypes.Host{}, err
	}
	h, err := c.check(ctx, t)
	if err == nil && h.ID == 0 {
		err = inventory.Errorf(inventory.Conflict, "host %d was changed or deleted during the check; try again", id)
	}
	return h, err
}

// check probes t and saves the result; a zero Host means the result was
// dropped because the host changed meanwhile.
func (c *Checker) check(ctx context.Context, t inventory.ServiceTarget) (apitypes.Host, error) {
	found := c.Probe(ctx, t.IP)
	if err := ctx.Err(); err != nil {
		return apitypes.Host{}, err
	}
	h, ok, err := c.inv.SetServices(ctx, t.ID, t.IP, found, c.cfg.Now())
	if err != nil {
		return apitypes.Host{}, fmt.Errorf("saving services of host %d: %w", t.ID, err)
	}
	if !ok {
		return apitypes.Host{}, nil
	}
	c.log.Debug("services checked", "host", t.ID, "ip", t.IP, "services", found)
	return h, nil
}

// due reports whether Run should check t now.
func (c *Checker) due(t inventory.ServiceTarget, now time.Time) bool {
	return t.Monitored && (t.CheckedAt.IsZero() || now.Sub(t.CheckedAt) >= c.cfg.Refresh)
}

// Run checks due hosts until ctx is cancelled, at most Concurrency at once,
// then waits for the checks in flight. Hosts that are due while all slots
// are taken wait for a later tick.
func (c *Checker) Run(ctx context.Context) {
	sem := make(chan struct{}, c.cfg.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(c.cfg.Tick)
	defer tick.Stop()
	for {
		now := c.cfg.Now()
	targets:
		for _, t := range c.inv.ServiceTargets() {
			c.mu.Lock()
			skip := c.busy[t.ID] || !c.due(t, now)
			c.mu.Unlock()
			if skip {
				continue
			}
			select {
			case sem <- struct{}{}:
			default:
				break targets
			}
			c.mu.Lock()
			c.busy[t.ID] = true
			c.mu.Unlock()
			wg.Go(func() {
				defer func() {
					c.mu.Lock()
					delete(c.busy, t.ID)
					c.mu.Unlock()
					<-sem
				}()
				if _, err := c.check(ctx, t); err != nil && ctx.Err() == nil {
					c.log.Error("service check failed", "host", t.ID, "err", err)
				}
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
