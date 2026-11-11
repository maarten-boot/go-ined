package monitor

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
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

// MinGap is the minimum time between the requests of one round (SPEC-302).
const MinGap = 200 * time.Millisecond

// ErrFamilyUnavailable is returned by Ping for an address family that has
// no working ICMP mode. The monitor skips such hosts (SPEC-304).
var ErrFamilyUnavailable = errors.New("ICMP is unavailable for this address family")

// errClosed is returned by Ping after Close.
var errClosed = errors.New("ICMP prober is closed")

// Status describes the ICMP modes for /health (SPEC-304).
type Status struct {
	Mode      string // mode of IPv4, or of IPv6 when only IPv6 works
	IPv4Error string // set when IPv4 has no working mode
	IPv6Error string // set when IPv6 has no working mode
}

// family is the ICMP setup of one address family.
type family struct {
	v4   bool
	mode string // apitypes.ICMPUnprivileged, apitypes.ICMPPrivileged or "" (unavailable)
	err  error  // why mode is ""

	mu  sync.Mutex
	mux *rawMux // privileged mode only; opened on first use
}

// ICMPProber pings with real ICMP and ICMPv6 echo requests (SPEC-304).
// In unprivileged mode each round uses its own socket, and the kernel hands
// each reply to the socket of its round. In raw mode every raw socket would
// receive every ICMP packet, so all rounds of a family share one socket and
// a reader goroutine hands each reply to its round.
type ICMPProber struct {
	v4, v6 *family
	gap    time.Duration

	mu     sync.Mutex
	closed bool
}

// NewICMPProber finds a working ICMP mode for each address family:
// unprivileged ("ping") sockets first, then raw sockets. It fails only when
// neither family has a working mode.
func NewICMPProber() (*ICMPProber, error) { return newICMPProber(false) }

// newICMPProber is NewICMPProber; rawOnly skips the unprivileged mode, so
// tests can exercise raw sockets where both work (SPEC-007, TC-309).
func newICMPProber(rawOnly bool) (*ICMPProber, error) {
	p := &ICMPProber{v4: detect(true, rawOnly), v6: detect(false, rawOnly), gap: MinGap}
	if p.v4.mode == "" && p.v6.mode == "" {
		return nil, fmt.Errorf("no ICMP mode available (IPv4: %v; IPv6: %v); allow unprivileged ICMP with sysctl net.ipv4.ping_group_range, or run with CAP_NET_RAW", p.v4.err, p.v6.err)
	}
	return p, nil
}

func detect(v4, rawOnly bool) *family {
	f := &family{v4: v4}
	var errs []error
	modes := []bool{false, true}
	if rawOnly {
		modes = []bool{true}
	}
	for _, priv := range modes {
		c, err := icmp.ListenPacket(network(priv, v4), anyAddr(v4))
		if err == nil {
			c.Close()
			f.mode = modeName(priv)
			return f
		}
		errs = append(errs, fmt.Errorf("%s: %w", modeName(priv), err))
	}
	f.err = errors.Join(errs...)
	return f
}

// Mode returns the mode of IPv4, or of IPv6 when only IPv6 works.
func (p *ICMPProber) Mode() string { return p.Status().Mode }

// Status reports the modes and the per-family errors.
func (p *ICMPProber) Status() Status {
	var s Status
	if p.v4.mode != "" {
		s.Mode = p.v4.mode
	} else {
		s.Mode = p.v6.mode
		s.IPv4Error = p.v4.err.Error()
	}
	if p.v6.mode == "" {
		s.IPv6Error = p.v6.err.Error()
	}
	return s
}

// Close releases the shared raw sockets. Later Pings fail.
func (p *ICMPProber) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	for _, f := range []*family{p.v4, p.v6} {
		f.mu.Lock()
		if f.mux != nil {
			f.mux.close()
			f.mux = nil
		}
		f.mu.Unlock()
	}
	return nil
}

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

func anyAddr(v4 bool) string {
	if v4 {
		return "0.0.0.0"
	}
	return "::"
}

// msgTypes returns the protocol number and the echo request/reply types.
func msgTypes(v4 bool) (proto int, req, reply icmp.Type) {
	if v4 {
		return protoICMP, ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	}
	return protoICMPv6, ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
}

func newToken() []byte {
	t := make([]byte, 16)
	rand.Read(t)
	return t
}

func echoBytes(typ icmp.Type, id, seq int, token []byte) ([]byte, error) {
	m := icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: token}}
	return m.Marshal(nil) // the kernel fills in the ICMPv6 checksum
}

// round is one ping round on some transport.
type round interface {
	send(seq int) error
	// wait returns the arrival time of the reply to seq, or ok = false at
	// the deadline.
	wait(ctx context.Context, seq int, deadline time.Time) (at time.Time, ok bool, err error)
	close()
}

// Ping implements Prober.
func (p *ICMPProber) Ping(ctx context.Context, ip netip.Addr, count int, timeout time.Duration) (Result, error) {
	ip = ip.Unmap()
	f := p.v6
	if ip.Is4() {
		f = p.v4
	}
	if f.mode == "" {
		return Result{}, ErrFamilyUnavailable
	}
	var (
		r   round
		err error
	)
	if f.mode == apitypes.ICMPPrivileged {
		r, err = p.rawRound(f, ip)
	} else {
		r, err = newSocketRound(ip)
	}
	if err != nil {
		return Result{}, err
	}
	defer r.close()
	return runRound(ctx, r, count, timeout, p.gap)
}

// runRound sends count requests, at least gap apart, each waiting up to
// timeout for its reply (SPEC-302, SPEC-303).
func runRound(ctx context.Context, r round, count int, timeout, gap time.Duration) (Result, error) {
	var (
		res  Result
		last time.Time
	)
	for seq := 1; seq <= count; seq++ {
		if seq > 1 {
			if d := time.Until(last.Add(gap)); d > 0 {
				t := time.NewTimer(d)
				select {
				case <-ctx.Done():
					t.Stop()
					return res, ctx.Err()
				case <-t.C:
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		sent := time.Now()
		last = sent
		res.Sent++
		if err := r.send(seq); err != nil {
			return res, err
		}
		at, ok, err := r.wait(ctx, seq, sent.Add(timeout))
		if err != nil {
			return res, err
		}
		if ok {
			res.Received++
			res.RTTs = append(res.RTTs, at.Sub(sent))
			res.LastReply = at
		}
	}
	return res, nil
}

// ---- unprivileged: one socket per round ----

type socketRound struct {
	c        *icmp.PacketConn
	ip       netip.Addr
	dst      net.Addr
	proto    int
	req, rep icmp.Type
	token    []byte
	buf      []byte
}

func newSocketRound(ip netip.Addr) (*socketRound, error) {
	v4 := ip.Is4()
	c, err := icmp.ListenPacket(network(false, v4), anyAddr(v4))
	if err != nil {
		return nil, err
	}
	proto, req, rep := msgTypes(v4)
	return &socketRound{
		c: c, ip: ip, dst: &net.UDPAddr{IP: ip.AsSlice()}, proto: proto, req: req, rep: rep,
		token: newToken(), buf: make([]byte, 1500),
	}, nil
}

func (r *socketRound) send(seq int) error {
	// The kernel replaces the echo id with the socket's own; the token in
	// the payload identifies our replies.
	b, err := echoBytes(r.req, 0, seq, r.token)
	if err != nil {
		return err
	}
	_, err = r.c.WriteTo(b, r.dst)
	return err
}

func (r *socketRound) wait(ctx context.Context, seq int, deadline time.Time) (time.Time, bool, error) {
	if err := r.c.SetReadDeadline(deadline); err != nil {
		return time.Time{}, false, err
	}
	stop := context.AfterFunc(ctx, func() { r.c.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()
	for {
		n, peer, err := r.c.ReadFrom(r.buf)
		at := time.Now()
		if err != nil {
			if ctx.Err() != nil {
				return time.Time{}, false, ctx.Err()
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return time.Time{}, false, nil // lost
			}
			return time.Time{}, false, err
		}
		if !samePeer(peer, r.ip) {
			continue
		}
		m, err := icmp.ParseMessage(r.proto, r.buf[:n])
		if err != nil || m.Type != r.rep {
			continue
		}
		if e, ok := m.Body.(*icmp.Echo); ok && e.Seq == seq && bytes.Equal(e.Data, r.token) {
			return at, true, nil
		}
	}
}

func (r *socketRound) close() { r.c.Close() }

// ---- privileged: one shared raw socket per family ----

// packetConn is the part of *icmp.PacketConn the raw mux uses; tests fake it.
type packetConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, dst net.Addr) (int, error)
	Close() error
}

type muxKey struct{ id, seq int }

type waiter struct {
	ip    netip.Addr
	token []byte
	c     chan time.Time // capacity 1
}

// rawMux owns one raw socket and hands each echo reply to the waiting
// round, matched by (id, seq), peer address and token.
type rawMux struct {
	conn     packetConn
	proto    int
	req, rep icmp.Type

	mu      sync.Mutex
	inUse   map[int]bool // echo ids of open rounds
	next    int
	waiters map[muxKey]*waiter
	done    chan struct{} // closed when the reader stops
	err     error
}

func newRawMux(conn packetConn, v4 bool) *rawMux {
	m := &rawMux{conn: conn, inUse: map[int]bool{}, waiters: map[muxKey]*waiter{}, done: make(chan struct{})}
	m.proto, m.req, m.rep = msgTypes(v4)
	go m.read()
	return m
}

func (m *rawMux) read() {
	buf := make([]byte, 1500)
	for {
		n, peer, err := m.conn.ReadFrom(buf)
		at := time.Now()
		if err != nil {
			m.mu.Lock()
			m.err = err
			m.mu.Unlock()
			close(m.done)
			return
		}
		msg, err := icmp.ParseMessage(m.proto, buf[:n])
		if err != nil || msg.Type != m.rep {
			continue
		}
		e, ok := msg.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		k := muxKey{e.ID, e.Seq}
		m.mu.Lock()
		if w := m.waiters[k]; w != nil && samePeer(peer, w.ip) && bytes.Equal(e.Data, w.token) {
			delete(m.waiters, k)
			w.c <- at
		}
		m.mu.Unlock()
	}
}

// alloc reserves an echo id that no open round uses.
func (m *rawMux) alloc() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return 0, m.err
	}
	for range 0xffff {
		m.next = m.next%0xffff + 1 // 1..65535
		if !m.inUse[m.next] {
			m.inUse[m.next] = true
			return m.next, nil
		}
	}
	return 0, errors.New("no free ICMP echo id")
}

func (m *rawMux) free(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.inUse, id)
	for k := range m.waiters {
		if k.id == id {
			delete(m.waiters, k)
		}
	}
}

func (m *rawMux) dead() bool {
	select {
	case <-m.done:
		return true
	default:
		return false
	}
}

func (m *rawMux) close() { m.conn.Close() }

// rawRound returns a round on the family's shared socket, opening (or
// reopening after a failure) the socket as needed.
func (p *ICMPProber) rawRound(f *family, ip netip.Addr) (round, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, errClosed
	}
	f.mu.Lock()
	if f.mux == nil || f.mux.dead() {
		c, err := icmp.ListenPacket(network(true, f.v4), anyAddr(f.v4))
		if err != nil {
			f.mu.Unlock()
			return nil, err
		}
		f.mux = newRawMux(c, f.v4)
	}
	m := f.mux
	f.mu.Unlock()
	return newMuxRound(m, ip)
}

type muxRound struct {
	m     *rawMux
	id    int
	ip    netip.Addr
	dst   net.Addr
	token []byte
	w     *waiter
}

func newMuxRound(m *rawMux, ip netip.Addr) (*muxRound, error) {
	id, err := m.alloc()
	if err != nil {
		return nil, err
	}
	return &muxRound{m: m, id: id, ip: ip, dst: &net.IPAddr{IP: ip.AsSlice()}, token: newToken()}, nil
}

func (r *muxRound) send(seq int) error {
	b, err := echoBytes(r.m.req, r.id, seq, r.token)
	if err != nil {
		return err
	}
	// Register before sending, so a fast reply isn't missed.
	r.w = &waiter{ip: r.ip, token: r.token, c: make(chan time.Time, 1)}
	k := muxKey{r.id, seq}
	r.m.mu.Lock()
	r.m.waiters[k] = r.w
	r.m.mu.Unlock()
	if _, err := r.m.conn.WriteTo(b, r.dst); err != nil {
		r.m.mu.Lock()
		delete(r.m.waiters, k)
		r.m.mu.Unlock()
		return err
	}
	return nil
}

func (r *muxRound) wait(ctx context.Context, seq int, deadline time.Time) (time.Time, bool, error) {
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	defer func() {
		r.m.mu.Lock()
		delete(r.m.waiters, muxKey{r.id, seq})
		r.m.mu.Unlock()
	}()
	select {
	case at := <-r.w.c:
		return at, true, nil
	case <-t.C:
		return time.Time{}, false, nil
	case <-ctx.Done():
		return time.Time{}, false, ctx.Err()
	case <-r.m.done:
		return time.Time{}, false, fmt.Errorf("raw ICMP socket failed: %w", r.m.err)
	}
}

func (r *muxRound) close() { r.m.free(r.id) }

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
