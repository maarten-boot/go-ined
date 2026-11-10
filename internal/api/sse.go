package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/maarten-boot/go-ined/internal/events"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// events serves GET /api/v1/events (SPEC-502, SPEC-503). The stream ends
// when the client goes away, when it falls behind (after a resync event),
// or when the bus is closed at shutdown (SPEC-804).
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	sub, replay, ok := s.opts.Events.Subscribe(r.Header.Get("Last-Event-ID"))
	if !ok {
		writeError(w, http.StatusServiceUnavailable, apitypes.CodeUnavailable, "server is shutting down")
		return
	}
	defer sub.Close()
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// write sends one frame. A client that accepts nothing for WriteTimeout
	// is cut off, so it can't block the handler or the shutdown (SPEC-804).
	write := func(frame func() error) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(s.opts.WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if err := frame(); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	send := func(ev events.Event) bool { return write(func() error { return writeEvent(w, ev) }) }
	comment := func(text string) bool {
		return write(func() error { _, err := io.WriteString(w, ": "+text+"\n\n"); return err })
	}
	// An initial comment makes the headers reach the client at once.
	if !comment("connected") {
		return
	}
	for _, ev := range replay {
		if !send(ev) {
			return
		}
	}
	ka := time.NewTicker(s.opts.Keepalive)
	defer ka.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-sub.C():
			if !open {
				if sub.Lagged() {
					send(s.opts.Events.Resync("client fell behind"))
				}
				return
			}
			if !send(ev) {
				return
			}
		case <-ka.C:
			if !comment("keepalive") {
				return
			}
		}
	}
}

// writeEvent writes one SSE frame. ev.Data is compact JSON and therefore a
// single line.
func writeEvent(w io.Writer, ev events.Event) error {
	_, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, ev.Data)
	return err
}
