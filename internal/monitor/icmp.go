package monitor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// ICMP protocol numbers for icmp.ParseMessage.
const (
	protoICMP   = 1
	protoICMPv6 = 58
)

// ICMPProber pings with real ICMP echo requests (SPEC-304). Each round uses
// its own socket, so concurrent rounds don't see each other's replies.
type ICMPProber struct {
	privileged bool
}

// NewICMPProber finds a working ICMP mode: unprivileged ("ping") sockets
// first, then raw sockets (SPEC-304). The error says why neither works.
func NewICMPProber() (*ICMPProber, error) {
	var errs []error
	for _, priv := range []bool{false, true} {
		c, err := icmp.ListenPacket(network(priv, true), "0.0.0.0")
		if err == nil {
			c.Close()
			return &ICMPProber{privileged: priv}, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", modeName(priv), err))
	}
	return nil, fmt.Errorf("no ICMP mode available (%w); allow unprivileged ICMP with sysctl net.ipv4.ping_group_range, or run with CAP_NET_RAW", errors.Join(errs...))
}

// Mode returns apitypes.ICMPUnprivileged or apitypes.ICMPPrivileged.
func (p *ICMPProber) Mode() string { return modeName(p.privileged) }

func modeName(priv bool) string {
	if priv {
		return apitypes.ICMPPrivileged
	}
	return apitypes.ICMPUnprivileged
}

func network(priv, v4 bool) string {
	switch {
	case !priv && v4:
		return "udp4"
	case !priv:
		return "udp6"
	case v4:
		return "ip4:icmp"
	default:
		return "ip6:ipv6-icmp"
	}
}

// Ping implements Prober.
func (p *ICMPProber) Ping(ctx context.Context, ip netip.Addr, count int, timeout time.Duration) (Result, error) {
	ip = ip.Unmap()
	v4 := ip.Is4()
	laddr, proto := "::", protoICMPv6
	var reqType, replyType icmp.Type = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	if v4 {
		laddr, proto = "0.0.0.0", protoICMP
		reqType, replyType = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	}
	c, err := icmp.ListenPacket(network(p.privileged, v4), laddr)
	if err != nil {
		return Result{}, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()

	var dst net.Addr = &net.IPAddr{IP: ip.AsSlice()}
	if !p.privileged {
		dst = &net.UDPAddr{IP: ip.AsSlice()}
	}
	// A random token in the payload identifies our replies. In unprivileged
	// mode the kernel replaces the echo id, so only raw mode checks it.
	var token [16]byte
	rand.Read(token[:])
	id := int(binary.BigEndian.Uint16(token[:2]))

	var res Result
	buf := make([]byte, 1500)
	for seq := 1; seq <= count; seq++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		msg := icmp.Message{Type: reqType, Body: &icmp.Echo{ID: id, Seq: seq, Data: token[:]}}
		b, err := msg.Marshal(nil) // the kernel fills in the ICMPv6 checksum
		if err != nil {
			return res, err
		}
		sent := time.Now()
		res.Sent++
		if _, err := c.WriteTo(b, dst); err != nil {
			return res, err
		}
		if err := c.SetReadDeadline(sent.Add(timeout)); err != nil {
			return res, err
		}
		if ctx.Err() != nil { // AfterFunc may have run before the deadline was set
			return res, ctx.Err()
		}
		for {
			n, peer, err := c.ReadFrom(buf)
			if err != nil {
				if ctx.Err() != nil {
					return res, ctx.Err()
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break // lost
				}
				return res, err
			}
			if !samePeer(peer, ip) {
				continue
			}
			m, err := icmp.ParseMessage(proto, buf[:n])
			if err != nil || m.Type != replyType {
				continue
			}
			e, ok := m.Body.(*icmp.Echo)
			if !ok || e.Seq != seq || !bytes.Equal(e.Data, token[:]) || (p.privileged && e.ID != id) {
				continue
			}
			res.Received++
			res.RTTs = append(res.RTTs, time.Since(sent))
			break
		}
	}
	return res, nil
}

func samePeer(a net.Addr, ip netip.Addr) bool {
	var raw net.IP
	switch a := a.(type) {
	case *net.UDPAddr:
		raw = a.IP
	case *net.IPAddr:
		raw = a.IP
	default:
		return false
	}
	pa, ok := netip.AddrFromSlice(raw)
	return ok && pa.Unmap() == ip
}
