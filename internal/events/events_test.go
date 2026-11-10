package events

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

func ids(evs []Event) string {
	s := make([]string, len(evs))
	for i, e := range evs {
		s[i] = e.ID
	}
	return strings.Join(s, ",")
}

func drain(s *Sub, n int) []Event {
	out := make([]Event, 0, n)
	for range n {
		out = append(out, <-s.C())
	}
	return out
}

// TC-506: ids are <run>-<seq>, increasing within the run.
func TestEventIDs(t *testing.T) {
	b := New("r1", 10)
	s, replay, ok := b.Subscribe("")
	if !ok || len(replay) != 0 {
		t.Fatalf("fresh subscribe: ok=%v replay=%v", ok, replay)
	}
	b.Publish(apitypes.EventHostDeleted, apitypes.DeletedEvent{ID: 7})
	b.Publish(apitypes.EventGroupStatus, apitypes.GroupStatusEvent{ID: 1, Status: "up"})
	got := drain(s, 2)
	if ids(got) != "r1-1,r1-2" {
		t.Fatalf("ids = %s", ids(got))
	}
	if got[0].Type != apitypes.EventHostDeleted || string(got[0].Data) != `{"id":7}` {
		t.Fatalf("event 1 = %+v", got[0])
	}
	if b.LastID() != "r1-2" {
		t.Fatalf("LastID = %s", b.LastID())
	}
}

// TC-502: reconnecting with Last-Event-ID replays exactly the missed events.
func TestReplay(t *testing.T) {
	b := New("r1", 5)
	for range 4 {
		b.Publish("x", 1)
	}
	for _, tc := range []struct{ last, want string }{
		{"r1-4", ""},
		{"r1-2", "r1-3,r1-4"},
		{"r1-0", "r1-1,r1-2,r1-3,r1-4"},
	} {
		s, replay, _ := b.Subscribe(tc.last)
		if ids(replay) != tc.want {
			t.Errorf("Subscribe(%s) replay = %s, want %s", tc.last, ids(replay), tc.want)
		}
		s.Close()
	}
}

// TC-502, TC-506: ids from another run, dropped out of the buffer, or
// malformed get a resync that carries the newest id.
func TestResync(t *testing.T) {
	b := New("r2", 3)
	for range 5 { // buffer holds 3..5
		b.Publish("x", 1)
	}
	for _, last := range []string{"r1-4", "r2-1", "r2-99", "garbage", "r2-x"} {
		s, replay, _ := b.Subscribe(last)
		if len(replay) != 1 || replay[0].Type != apitypes.EventResync || replay[0].ID != "r2-5" {
			t.Errorf("Subscribe(%s) = %+v, want one resync with id r2-5", last, replay)
			continue
		}
		var p apitypes.ResyncEvent
		if err := json.Unmarshal(replay[0].Data, &p); err != nil || p.Reason == "" {
			t.Errorf("Subscribe(%s): resync payload %s", last, replay[0].Data)
		}
		s.Close()
	}
	// The oldest buffered predecessor still replays.
	_, replay, _ := b.Subscribe("r2-2")
	if ids(replay) != "r2-3,r2-4,r2-5" {
		t.Errorf("Subscribe(r2-2) = %s", ids(replay))
	}
}

// TC-507: a subscriber that falls behind is dropped and marked Lagged;
// others keep receiving.
func TestLaggedSubscriber(t *testing.T) {
	b := New("r", 10)
	slow, _, _ := b.Subscribe("")
	fast, _, _ := b.Subscribe("")
	for i := range SubBuffer + 1 {
		b.Publish("x", i)
		<-fast.C()
	}
	n := 0
	for range slow.C() {
		n++
	}
	if n != SubBuffer || !slow.Lagged() {
		t.Fatalf("slow got %d events, lagged=%v", n, slow.Lagged())
	}
	b.Publish("x", 0)
	if ev := <-fast.C(); ev.ID != "r-"+strconv.Itoa(SubBuffer+2) {
		t.Fatalf("fast got %s", ev.ID)
	}
	if fast.Lagged() {
		t.Fatal("fast marked lagged")
	}
}

// TC-802: Close ends every subscription; later calls are no-ops.
func TestClose(t *testing.T) {
	b := New("r", 10)
	s, _, _ := b.Subscribe("")
	b.Close()
	if _, open := <-s.C(); open {
		t.Fatal("channel still open")
	}
	if s.Lagged() {
		t.Fatal("closed sub marked lagged")
	}
	s.Close() // no panic
	b.Publish("x", 1)
	if _, _, ok := b.Subscribe(""); ok {
		t.Fatal("subscribe after close succeeded")
	}
}
