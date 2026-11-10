package monitor

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-309: the real prober finds a mode and gets replies from loopback.
// Skipped where the machine allows no ICMP socket at all.
func TestICMPLoopback(t *testing.T) {
	p, err := NewICMPProber()
	if err != nil {
		t.Skipf("no ICMP mode available: %v", err)
	}
	if m := p.Mode(); m != apitypes.ICMPUnprivileged && m != apitypes.ICMPPrivileged {
		t.Fatalf("mode = %q", m)
	}
	t.Logf("mode: %s", p.Mode())
	for _, addr := range []string{"127.0.0.1", "::1"} {
		t.Run(addr, func(t *testing.T) {
			ip := netip.MustParseAddr(addr)
			if ip.Is6() && !hasIPv6Loopback() {
				t.Skip("no IPv6 loopback")
			}
			res, err := p.Ping(context.Background(), ip, 3, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if res.Sent != 3 || res.Received != 3 || len(res.RTTs) != 3 {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

// A cancelled context stops a round promptly.
func TestICMPCancel(t *testing.T) {
	p, err := NewICMPProber()
	if err != nil {
		t.Skipf("no ICMP mode available: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	// 192.0.2.1 (TEST-NET-1) never answers.
	_, err = p.Ping(ctx, netip.MustParseAddr("192.0.2.1"), 3, 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		var ne net.Error
		if errors.As(err, &ne) {
			t.Skipf("no route to the test address: %v", err)
		}
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v after cancel", d)
	}
}

func hasIPv6Loopback() bool {
	c, err := net.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		return false
	}
	c.Close()
	return true
}
