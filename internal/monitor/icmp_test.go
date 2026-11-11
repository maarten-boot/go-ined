package monitor

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-309, TC-312: the real prober finds a mode and gets replies from loopback.
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
			if ip.Is6() && p.Status().IPv6Error != "" {
				t.Skipf("IPv6 ICMP unavailable: %s", p.Status().IPv6Error)
			}
			start := time.Now()
			res, err := p.Ping(context.Background(), ip, 3, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if res.Sent != 3 || res.Received != 3 || len(res.RTTs) != 3 || res.LastReply.Before(start) {
				t.Fatalf("result = %+v", res)
			}
			// TC-312: requests at least MinGap apart.
			if d := time.Since(start); d < 2*MinGap {
				t.Fatalf("3 requests took only %v", d)
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

// TC-309 (raw mode, SPEC-007): with INED_TEST_RAW_ICMP=1 and root or
// CAP_NET_RAW (the CI job "raw-icmp"), the raw-socket mode is forced and
// must answer for 127.0.0.1 and ::1, with concurrent rounds on the shared
// socket each getting their own replies.
func TestICMPRawLoopback(t *testing.T) {
	if os.Getenv("INED_TEST_RAW_ICMP") == "" {
		t.Skip("set INED_TEST_RAW_ICMP=1 and run as root to test raw ICMP")
	}
	p, err := newICMPProber(true)
	if err != nil {
		t.Fatalf("raw ICMP unavailable: %v", err)
	}
	defer p.Close()
	if p.Mode() != apitypes.ICMPPrivileged {
		t.Fatalf("mode = %q", p.Mode())
	}
	addrs := []string{"127.0.0.1"}
	if hasIPv6Loopback() {
		if e := p.Status().IPv6Error; e != "" {
			t.Fatalf("raw ICMPv6 unavailable: %s", e)
		}
		addrs = append(addrs, "::1")
	}
	var wg sync.WaitGroup
	for _, a := range addrs {
		for range 3 { // concurrent rounds per family
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := p.Ping(context.Background(), netip.MustParseAddr(a), 3, time.Second)
				if err != nil || res.Sent != 3 || res.Received != 3 {
					t.Errorf("%s: %+v %v", a, res, err)
				}
			}()
		}
	}
	wg.Wait()
}
