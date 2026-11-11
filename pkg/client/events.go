package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Event is one event of GET /events (SPEC-502). Type is one of the
// apitypes.Event* constants; Data is the JSON payload.
type Event struct {
	ID   string
	Type string
	Data json.RawMessage
}

// Decode unmarshals the payload into v.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Data, v) }

// StreamOptions configures Events. Zero values get defaults.
type StreamOptions struct {
	// LastEventID resumes after this event (SPEC-503).
	LastEventID string
	// MinBackoff is the first wait before reconnecting (default 1 s); it
	// doubles after each failed attempt up to MaxBackoff (default 30 s).
	MinBackoff, MaxBackoff time.Duration
	// IdleTimeout drops a connection that sends nothing, not even a
	// keepalive, for this long, and reconnects (default 75 s: two and a
	// half server keepalives).
	IdleTimeout time.Duration
	// FailFirst ends the stream with the error if the very first
	// connection attempt fails, instead of retrying. Later failures are
	// always retried. inedctl uses this to report an unreachable server.
	FailFirst bool
	// OnState, if set, is called on every connect and disconnect, from the
	// stream's goroutine. It must not block.
	OnState func(StreamState)
}

// StreamState describes a connection change of a Stream.
type StreamState struct {
	Connected bool
	Err       error         // why the connection ended or failed; nil on connect
	RetryIn   time.Duration // wait before the next attempt (disconnects only)
}

// Stream is a live event stream that reconnects by itself (SPEC-506).
// Read events from C. After C is closed, Err tells why.
type Stream struct {
	c      *Client
	opts   StreamOptions
	ch     chan Event
	cancel context.CancelFunc
	ready  chan struct{}

	mu      sync.Mutex
	lastID  string
	err     error
	isReady bool
}

// Events opens the event stream. It returns at once; the connection is
// made in the background. A resync event is passed on like any other: the
// caller reloads its state (GET /map) and keeps reading, and the stream
// resumes after the resync event's id.
//
// The stream ends, and C is closed, when ctx is cancelled, Close is
// called, or the server refuses the stream with a client error (4xx, for
// example 401 for a wrong token). Server errors (5xx, such as 503 during a
// server shutdown) and connection errors are retried.
func (c *Client) Events(ctx context.Context, opts StreamOptions) *Stream {
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = time.Second
	}
	if opts.MaxBackoff < opts.MinBackoff {
		opts.MaxBackoff = 30 * time.Second
		if opts.MaxBackoff < opts.MinBackoff {
			opts.MaxBackoff = opts.MinBackoff
		}
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 75 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Stream{c: c, opts: opts, ch: make(chan Event, 64), cancel: cancel,
		ready: make(chan struct{}), lastID: opts.LastEventID}
	go s.run(ctx)
	return s
}

// C delivers the events. It is closed when the stream ends.
func (s *Stream) C() <-chan Event { return s.ch }

// Ready is closed once the first connection is established, so a caller
// can start an action whose events it must not miss.
func (s *Stream) Ready() <-chan struct{} { return s.ready }

// Err returns why the stream ended: nil after Close or a cancelled
// context, otherwise the error (an *APIError for a refused stream). It is
// meaningful once C is closed.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// LastID returns the id of the newest event received.
func (s *Stream) LastID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastID
}

// Close ends the stream.
func (s *Stream) Close() { s.cancel() }

func (s *Stream) state(st StreamState) {
	if s.opts.OnState != nil {
		s.opts.OnState(st)
	}
}

func (s *Stream) run(ctx context.Context) {
	defer close(s.ch)
	defer s.cancel()
	wait := s.opts.MinBackoff
	for attempt := 0; ; attempt++ {
		connected, err := s.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 {
			s.fail(err) // retrying won't help
			return
		}
		if attempt == 0 && !connected && s.opts.FailFirst {
			s.fail(err)
			return
		}
		if connected {
			wait = s.opts.MinBackoff
		}
		if err == nil {
			err = io.EOF // the server ended the stream
		}
		s.state(StreamState{Err: err, RetryIn: wait})
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if !connected {
			wait = min(wait*2, s.opts.MaxBackoff)
		}
	}
}

func (s *Stream) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// connect makes one connection and reads events until it ends. connected
// reports whether the server accepted the stream.
func (s *Stream) connect(ctx context.Context) (connected bool, err error) {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := s.c.newRequest(rctx, http.MethodGet, "/events", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if id := s.LastID(); id != "" {
		req.Header.Set("Last-Event-ID", id)
	}
	// The idle timer also covers a server that accepts the connection but
	// never answers.
	idle := time.AfterFunc(s.opts.IdleTimeout, cancel)
	defer idle.Stop()
	resp, err := s.c.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return false, apiError(resp.StatusCode, data)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return false, fmt.Errorf("client: /events answered %q, not an event stream", ct)
	}
	s.mu.Lock()
	if !s.isReady {
		s.isReady = true
		close(s.ready)
	}
	s.mu.Unlock()
	s.state(StreamState{Connected: true})

	err = s.read(ctx, resp.Body, func() { idle.Reset(s.opts.IdleTimeout) })
	if ctx.Err() == nil && rctx.Err() != nil {
		err = fmt.Errorf("client: no data from the server for %s", s.opts.IdleTimeout)
	}
	return true, err
}

// read parses the SSE stream. alive is called for every line received.
func (s *Stream) read(ctx context.Context, body io.Reader, alive func()) error {
	br := bufio.NewReader(body)
	var (
		typ, id string
		hasID   bool
		data    []string
	)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		alive()
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // dispatch
			if hasID {
				s.mu.Lock()
				s.lastID = id
				s.mu.Unlock()
			}
			if len(data) > 0 {
				if typ == "" {
					typ = "message"
				}
				ev := Event{ID: id, Type: typ, Data: json.RawMessage(strings.Join(data, "\n"))}
				select {
				case s.ch <- ev:
				case <-ctx.Done():
					return nil
				}
			}
			typ, id, hasID, data = "", "", false, nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment, e.g. keepalive
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			typ = value
		case "id":
			id, hasID = value, true
		case "data":
			data = append(data, value)
		}
	}
}

// Short returns a one-line summary of an event for logs and inedctl.
func (e Event) Short() string {
	switch e.Type {
	case apitypes.EventHostCreated, apitypes.EventHostUpdated, apitypes.EventHostStatus:
		var h apitypes.Host
		if e.Decode(&h) == nil {
			return fmt.Sprintf("host %d %s %s %s", h.ID, h.IP, h.DisplayName, h.Status)
		}
	case apitypes.EventGroupCreated, apitypes.EventGroupUpdated:
		var g apitypes.Group
		if e.Decode(&g) == nil {
			s := fmt.Sprintf("group %d %s", g.ID, g.Name)
			if g.CIDR != "" && g.CIDR != g.Name {
				s += " " + g.CIDR
			}
			return s + " " + g.Status
		}
	case apitypes.EventHostDeleted, apitypes.EventGroupDeleted:
		var d apitypes.DeletedEvent
		if e.Decode(&d) == nil {
			return fmt.Sprintf("%s %d", strings.TrimSuffix(e.Type, ".deleted"), d.ID)
		}
	case apitypes.EventGroupMoved:
		var m apitypes.GroupMovedEvent
		if e.Decode(&m) == nil {
			return fmt.Sprintf("group %d contents moved by %g,%g in view %d", m.ID, m.DX, m.DY, m.ViewID)
		}
	case apitypes.EventGroupStatus:
		var g apitypes.GroupStatusEvent
		if e.Decode(&g) == nil {
			return fmt.Sprintf("group %d %s", g.ID, g.Status)
		}
	case apitypes.EventDiscoveryProgress, apitypes.EventDiscoveryFinished:
		var d apitypes.Discovery
		if e.Decode(&d) == nil {
			return DiscoveryLine(d)
		}
	case apitypes.EventViewCreated, apitypes.EventViewUpdated:
		var v apitypes.View
		if e.Decode(&v) == nil {
			return fmt.Sprintf("view %d %s order %d", v.ID, v.Name, v.Order)
		}
	case apitypes.EventViewDeleted:
		var d apitypes.DeletedEvent
		if e.Decode(&d) == nil {
			return fmt.Sprintf("view %d", d.ID)
		}
	case apitypes.EventPlacementUpdated:
		var p apitypes.Placement
		if e.Decode(&p) == nil {
			parent := "root"
			if p.ParentID != nil {
				parent = fmt.Sprintf("group %d", *p.ParentID)
			}
			return fmt.Sprintf("view %d host %d in %s", p.ViewID, p.HostID, parent)
		}
	case apitypes.EventPlacementDeleted:
		var p apitypes.PlacementDeletedEvent
		if e.Decode(&p) == nil {
			return fmt.Sprintf("view %d host %d removed", p.ViewID, p.HostID)
		}
	case apitypes.EventResync:
		var r apitypes.ResyncEvent
		if e.Decode(&r) == nil {
			return "reload: " + r.Reason
		}
	}
	return string(e.Data)
}

// DiscoveryLine is a one-line summary of a discovery job.
func DiscoveryLine(d apitypes.Discovery) string {
	s := fmt.Sprintf("job %s %s %s probed %d/%d found %d added %d", d.ID, d.CIDR, d.State, d.Probed, d.Total, d.Found, d.Added)
	if d.Errors > 0 {
		s += fmt.Sprintf(" errors %d", d.Errors)
	}
	if d.Error != "" {
		s += ": " + d.Error
	}
	return s
}
