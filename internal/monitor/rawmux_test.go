package monitor

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// fakeRaw behaves like a raw ICMPv4 socket that sees every reply: it
// answers echo requests to "alive" addresses after a random delay, and
// also delivers noise (replies with a wrong token or id).
type fakeRaw struct {
	alive  func(netip.Addr) bool
	in     chan pkt
	closed chan struct{}
	once   sync.Once
}

type pkt struct {
	b    []byte
	from net.Addr
}

func newFakeRaw(alive func(netip.Addr) bool) *fakeRaw {
	return &fakeRaw{alive: alive, in: make(chan pkt, 10000), closed: make(chan struct{})}
}

func (f *fakeRaw) WriteTo(b []byte, dst net.Addr) (int, error) {
	m, err := icmp.ParseMessage(protoICMP, b)
	if err != nil {
		return 0, err
	}
	e := m.Body.(*icmp.Echo)
	ip, _ := netip.AddrFromSlice(dst.(*net.IPAddr).IP)
	ip = ip.Unmap()
	if !f.alive(ip) {
		return len(b), nil
	}
	reply := func(id int, data []byte) []byte {
		out, _ := (&icmp.Message{Type: ipv4.ICMPTypeEchoReply, Body: &icmp.Echo{ID: id, Seq: e.Seq, Data: data}}).Marshal(nil)
		return out
	}
	from := &net.IPAddr{IP: dst.(*net.IPAddr).IP}
	go func() {
		time.Sleep(time.Duration(rand.IntN(3000)) * time.Microsecond)
		f.in <- pkt{reply(e.ID, []byte("not the token....")), from} // noise: wrong token
		f.in <- pkt{reply(e.ID^0x5555, e.Data), from}               // noise: wrong id
		f.in <- pkt{reply(e.ID, e.Data), from}
	}()
	return len(b), nil
}

func (f *fakeRaw) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case p := <-f.in:
		return copy(b, p.b), p.from, nil
	case <-f.closed:
		return 0, nil, net.ErrClosed
	}
}

func (f *fakeRaw) Close() error { f.once.Do(func() { close(f.closed) }); return nil }

func rawProber(conn packetConn) *ICMPProber {
	v4 := &family{v4: true, mode: apitypes.ICMPPrivileged}
	v4.mux = newRawMux(conn, true)
	return &ICMPProber{v4: v4, v6: &family{err: errors.New("off")}, gap: time.Millisecond}
}

// TC-314: in raw mode, concurrent rounds share one socket and each gets
// exactly its own replies, despite noise.
func TestRawMuxConcurrentRounds(t *testing.T) {
	alive := func(ip netip.Addr) bool { return ip.As4()[3]%2 == 0 }
	p := rawProber(newFakeRaw(alive))
	defer p.Close()
	var wg sync.WaitGroup
	for i := 1; i <= 250; i++ {
		ip := netip.AddrFrom4([4]byte{10, 0, byte(i / 256), byte(i % 256)})
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := p.Ping(context.Background(), ip, 3, 200*time.Millisecond)
			if err != nil {
				t.Errorf("%s: %v", ip, err)
				return
			}
			want := 0
			if alive(ip) {
				want = 3
			}
			if res.Sent != 3 || res.Received != want || len(res.RTTs) != want || (want > 0) == res.LastReply.IsZero() {
				t.Errorf("%s: %+v, want %d replies", ip, res, want)
			}
		}()
	}
	wg.Wait()
	m := p.v4.mux
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inUse) != 0 || len(m.waiters) != 0 {
		t.Errorf("leaked ids %d, waiters %d", len(m.inUse), len(m.waiters))
	}
}

// A failing shared socket fails the rounds instead of hanging them, and
// after Close the prober refuses work.
func TestRawMuxSocketFailure(t *testing.T) {
	conn := newFakeRaw(func(netip.Addr) bool { return false })
	p := rawProber(conn)
	conn.Close()
	<-p.v4.mux.done
	if _, err := newMuxRound(p.v4.mux, netip.MustParseAddr("10.0.0.1")); err == nil {
		t.Fatal("round on a dead socket")
	}
	p.Close()
	if _, err := p.Ping(context.Background(), netip.MustParseAddr("10.0.0.1"), 1, time.Second); !errors.Is(err, errClosed) {
		t.Fatalf("after Close: %v", err)
	}
}

// TC-313: a family without a working mode isn't pinged.
func TestFamilyUnavailable(t *testing.T) {
	p := rawProber(newFakeRaw(func(netip.Addr) bool { return true }))
	defer p.Close()
	if _, err := p.Ping(context.Background(), netip.MustParseAddr("2001:db8::1"), 1, time.Second); !errors.Is(err, ErrFamilyUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if s := p.Status(); s.Mode != apitypes.ICMPPrivileged || s.IPv6Error != "off" || s.IPv4Error != "" {
		t.Fatalf("status = %+v", s)
	}
}

// fakeRound answers every request at once and records send times.
type fakeRound struct{ sent []time.Time }

func (r *fakeRound) send(int) error { r.sent = append(r.sent, time.Now()); return nil }
func (r *fakeRound) wait(context.Context, int, time.Time) (time.Time, bool, error) {
	return time.Now(), true, nil
}
func (r *fakeRound) close() {}

// TC-312: the requests of a round are at least the gap apart.
func TestRoundGap(t *testing.T) {
	r := &fakeRound{}
	res, err := runRound(context.Background(), r, 3, time.Second, MinGap)
	if err != nil || res.Received != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	for i := 1; i < len(r.sent); i++ {
		if d := r.sent[i].Sub(r.sent[i-1]); d < MinGap {
			t.Errorf("request %d only %v after the previous one", i+1, d)
		}
	}
	// A cancelled context stops the wait for the gap.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := runRound(ctx, &fakeRound{}, 3, time.Second, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
