// Package api serves the go-ined REST API under /api/v1 (SPEC-501,
// SPEC-504, SPEC-802) and the Server-Sent Events stream (SPEC-502).
// Discovery endpoints (SPEC-201..205) are served when Options.Discovery is
// set.
package api

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/maarten-boot/go-ined/internal/events"
	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Prefix is the path prefix of every API route.
const Prefix = "/api/v1"

// MaxBodyBytes limits request bodies; a 64 KiB note plus JSON overhead fits.
const MaxBodyBytes = 1 << 20

// Options configures the handler.
type Options struct {
	Token string // empty = no authentication
	// ReadToken allows GET, HEAD and the event stream only (SPEC-808);
	// empty = none. Needs Token.
	ReadToken string
	Version   string                          // reported by /health
	RunID     string                          // reported by /health (authenticated)
	DBPing    func(ctx context.Context) error // for /health; nil = not checked
	Logger    *slog.Logger                    // nil = slog.Default()
	Events    *events.Bus                     // for /events; nil = endpoint answers 404
	// ICMP reports the monitor's ICMP state for /health (SPEC-304).
	// nil = "not started".
	ICMP func() ICMPInfo
	// Discovery runs discovery jobs; nil = the endpoints answer 404.
	Discovery Discovery
	// Services checks a host's services on request (SPEC-309); nil = the
	// endpoint answers 503 (services.enabled = false).
	Services ServiceChecker
	// SNMP checks a host's SNMP agent on request (SPEC-312); nil = SNMP is
	// off and the endpoint answers 503.
	SNMP SNMPChecker
	// Keepalive is the SSE keepalive interval; 0 = 30 s (SPEC-502).
	Keepalive time.Duration
	// WriteTimeout disconnects an SSE client that accepts no data for this
	// long; 0 = 10 s (SPEC-502, SPEC-804).
	WriteTimeout time.Duration
}

// Discovery is what the API needs from discovery.Manager.
type Discovery interface {
	Start(ctx context.Context, in apitypes.DiscoveryCreate) (apitypes.Discovery, error)
	List() []apitypes.Discovery
	Get(id string) (apitypes.Discovery, error)
	Cancel(id string) (apitypes.Discovery, error)
}

// SNMPChecker is what the API needs from snmp.Manager.
type SNMPChecker interface {
	Check(ctx context.Context, id int64) (apitypes.Host, error)
	StartCheck(in apitypes.SNMPCheckCreate) (apitypes.SNMPCheck, error)
	Checks() []apitypes.SNMPCheck
	GetCheck(id string) (apitypes.SNMPCheck, error)
	CancelCheck(id string) (apitypes.SNMPCheck, error)
}

// ServiceChecker is what the API needs from services.Checker.
type ServiceChecker interface {
	Check(ctx context.Context, id int64) (apitypes.Host, error)
}

// ICMPInfo is the ICMP state shown by /health. Error set and Mode
// "unavailable" means no family works: /health then reports "degraded".
type ICMPInfo struct {
	Mode      string
	Error     string // IPv4 problem, or why no mode works at all
	IPv6Error string
}

type server struct {
	svc    *inventory.Service
	opts   Options
	log    *slog.Logger
	routes []string // "METHOD /path" as registered, for the OpenAPI test
}

// openAPI is the API description served at /openapi.json (SPEC-505).
//
//go:embed openapi.json
var openAPIDoc []byte

// logged logs every request at debug level (SPEC-807).
func (s *server) logged(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h(w, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start).Round(time.Microsecond))
	}
}

// New returns the API handler.
func New(svc *inventory.Service, opts Options) http.Handler {
	_, h := newServer(svc, opts)
	return h
}

func newServer(svc *inventory.Service, opts Options) (*server, http.Handler) {
	if opts.Keepalive <= 0 {
		opts.Keepalive = 30 * time.Second
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 10 * time.Second
	}
	s := &server{svc: svc, opts: opts, log: opts.Logger}
	if s.log == nil {
		s.log = slog.Default()
	}
	mux := http.NewServeMux()
	route := func(path string, methods map[string]http.HandlerFunc) {
		allowed := make([]string, 0, len(methods))
		for m, h := range methods {
			mux.HandleFunc(m+" "+Prefix+path, s.logged(h))
			s.routes = append(s.routes, m+" "+path)
			allowed = append(allowed, m)
		}
		// Method-less fallback: JSON 405 instead of the mux's plain-text one.
		mux.HandleFunc(Prefix+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeError(w, http.StatusMethodNotAllowed, apitypes.CodeMethodNotAllowed,
				fmt.Sprintf("method %s not allowed", r.Method))
		})
	}
	route("/health", map[string]http.HandlerFunc{"GET": s.health})
	route("/openapi.json", map[string]http.HandlerFunc{"GET": s.openAPI})
	route("/map", map[string]http.HandlerFunc{"GET": s.getMap})
	if opts.Events != nil {
		route("/events", map[string]http.HandlerFunc{"GET": s.events})
	}
	if opts.Discovery != nil {
		route("/discovery", map[string]http.HandlerFunc{"GET": s.listDiscovery, "POST": s.startDiscovery})
		route("/discovery/{id}", map[string]http.HandlerFunc{"GET": s.getDiscovery, "DELETE": s.cancelDiscovery})
	}
	route("/hosts", map[string]http.HandlerFunc{"GET": s.listHosts, "POST": s.createHost})
	route("/hosts/{id}", map[string]http.HandlerFunc{"GET": s.getHost, "PATCH": s.patchHost, "DELETE": s.deleteHost})
	route("/hosts/{id}/check-services", map[string]http.HandlerFunc{"POST": s.checkServices})
	route("/hosts/{id}/check-snmp", map[string]http.HandlerFunc{"POST": s.checkSNMP})
	route("/snmp/checks", map[string]http.HandlerFunc{"GET": s.listSNMPChecks, "POST": s.startSNMPCheck})
	route("/snmp/checks/{id}", map[string]http.HandlerFunc{"GET": s.getSNMPCheck, "DELETE": s.cancelSNMPCheck})
	route("/groups", map[string]http.HandlerFunc{"GET": s.listGroups, "POST": s.createGroup})
	route("/groups/{id}", map[string]http.HandlerFunc{"GET": s.getGroup, "PATCH": s.patchGroup, "DELETE": s.deleteGroup})
	route("/groups/{id}/move", map[string]http.HandlerFunc{"POST": s.moveGroup})
	route("/groups/{id}/export", map[string]http.HandlerFunc{"GET": s.exportGroup})
	route("/import", map[string]http.HandlerFunc{"POST": s.importNetwork})
	route("/views", map[string]http.HandlerFunc{"GET": s.listViews, "POST": s.createView})
	route("/views/{id}", map[string]http.HandlerFunc{"GET": s.getView, "PATCH": s.patchView, "DELETE": s.deleteView})
	route("/views/{id}/hosts/{host_id}", map[string]http.HandlerFunc{"PUT": s.placeHost, "DELETE": s.unplaceHost})
	route("/views/{id}/arrange", map[string]http.HandlerFunc{"POST": s.arrange})
	route("/views/{id}/copy-group", map[string]http.HandlerFunc{"POST": s.copyGroup})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, apitypes.CodeNotFound, "no such endpoint: "+r.URL.Path)
	})
	return s, s.auth(mux)
}

// openAPI serves the API description; it needs no token (SPEC-505).
func (s *server) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(openAPIDoc)
}

// ---- middleware ----

type ctxKey int

const authedKey ctxKey = 1

// isAuthed reports whether the request has a valid token, and with what
// access: apitypes.AccessFull or AccessRead; "" without one.
func isAuthed(r *http.Request) bool { return access(r) != "" }

func access(r *http.Request) string { v, _ := r.Context().Value(authedKey).(string); return v }

// auth checks the bearer token (SPEC-802). /health is reachable without a
// token but then returns reduced information. The read-only token may only
// read (SPEC-808).
func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		acc := ""
		switch {
		case s.opts.Token == "" || tokenMatches(h, s.opts.Token):
			acc = apitypes.AccessFull
		case s.opts.ReadToken != "" && tokenMatches(h, s.opts.ReadToken):
			acc = apitypes.AccessRead
		}
		open := r.URL.Path == Prefix+"/health" || r.URL.Path == Prefix+"/openapi.json"
		if acc == "" && !open {
			w.Header().Set("WWW-Authenticate", `Bearer realm="go-ined"`)
			writeError(w, http.StatusUnauthorized, apitypes.CodeUnauthenticated, "missing or invalid bearer token")
			return
		}
		if acc == apitypes.AccessRead && r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusForbidden, apitypes.CodeForbidden, "this token can only read (server.read_token)")
			return
		}
		limit := int64(MaxBodyBytes)
		if r.URL.Path == Prefix+"/import" {
			limit = MaxImportBytes // a network with its notes (SPEC-513)
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authedKey, acc)))
	})
}

func tokenMatches(header, token string) bool {
	const p = "Bearer "
	if len(header) < len(p) || !strings.EqualFold(header[:len(p)], p) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(p):]), []byte(token)) == 1
}

// ---- handlers ----

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	h := apitypes.Health{Status: "ok", Version: s.opts.Version}
	if isAuthed(r) {
		h.Access = access(r)
		h.RunID = s.opts.RunID
		h.ICMPMode = apitypes.ICMPNotStarted
		if s.opts.ICMP != nil {
			i := s.opts.ICMP()
			h.ICMPMode, h.ICMPError, h.ICMP6Error = i.Mode, i.Error, i.IPv6Error
			if i.Mode == apitypes.ICMPUnavailable {
				h.Status = "degraded"
			}
		}
		h.DB = "ok"
		if s.opts.DBPing != nil {
			if err := s.opts.DBPing(r.Context()); err != nil {
				h.Status, h.DB = "degraded", "error: "+err.Error()
			}
		}
	}
	writeJSON(w, http.StatusOK, h)
}

// getMap serves GET /map?view=<id>; without view, the network view
// (SPEC-122).
func (s *server) getMap(w http.ResponseWriter, r *http.Request) {
	view, ok := queryView(w, r)
	if !ok {
		return
	}
	m, err := s.svc.MapOf(view)
	s.respond(w, http.StatusOK, m, err)
}

func (s *server) listHosts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Hosts())
}

func (s *server) getHost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	h, err := s.svc.Host(id)
	s.respond(w, http.StatusOK, h, err)
}

// checkServices checks a host's services now and answers the host
// (SPEC-309, SPEC-511). It takes about the connect timeout (1 s).
func (s *server) checkServices(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.opts.Services == nil {
		if _, err := s.svc.Host(id); err != nil {
			s.respond(w, http.StatusOK, nil, err)
			return
		}
		writeError(w, http.StatusServiceUnavailable, apitypes.CodeUnavailable,
			"service checks are turned off on the server (services.enabled = false)")
		return
	}
	h, err := s.opts.Services.Check(r.Context(), id)
	s.respond(w, http.StatusOK, h, err)
}

// checkSNMP finds a host's SNMP agent now and reads its values (SPEC-312,
// SPEC-514). Without an agent it takes every community and version times
// snmp.timeout.
func (s *server) checkSNMP(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.opts.SNMP == nil {
		if _, err := s.svc.Host(id); err != nil {
			s.respond(w, http.StatusOK, nil, err)
			return
		}
		writeError(w, http.StatusServiceUnavailable, apitypes.CodeUnavailable,
			"SNMP is turned off on the server (snmp.enabled = false)")
		return
	}
	h, err := s.opts.SNMP.Check(r.Context(), id)
	s.respond(w, http.StatusOK, h, err)
}

// snmpOff answers 503 when SNMP is off on the server.
func (s *server) snmpOff(w http.ResponseWriter) bool {
	if s.opts.SNMP != nil {
		return false
	}
	writeError(w, http.StatusServiceUnavailable, apitypes.CodeUnavailable,
		"SNMP is turned off on the server (snmp.enabled = false)")
	return true
}

// startSNMPCheck starts an SNMP check of a group or of hosts (SPEC-515).
func (s *server) startSNMPCheck(w http.ResponseWriter, r *http.Request) {
	var in apitypes.SNMPCheckCreate
	if !decode(w, r, &in) || s.snmpOff(w) {
		return
	}
	c, err := s.opts.SNMP.StartCheck(in)
	s.respond(w, http.StatusAccepted, c, err)
}

func (s *server) listSNMPChecks(w http.ResponseWriter, r *http.Request) {
	if s.snmpOff(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.opts.SNMP.Checks())
}

func (s *server) getSNMPCheck(w http.ResponseWriter, r *http.Request) {
	if s.snmpOff(w) {
		return
	}
	c, err := s.opts.SNMP.GetCheck(r.PathValue("id"))
	s.respond(w, http.StatusOK, c, err)
}

func (s *server) cancelSNMPCheck(w http.ResponseWriter, r *http.Request) {
	if s.snmpOff(w) {
		return
	}
	c, err := s.opts.SNMP.CancelCheck(r.PathValue("id"))
	s.respond(w, http.StatusOK, c, err)
}

func (s *server) createHost(w http.ResponseWriter, r *http.Request) {
	var in apitypes.HostCreate
	if !decode(w, r, &in) {
		return
	}
	h, err := s.svc.CreateHost(r.Context(), in)
	s.respond(w, http.StatusCreated, h, err)
}

func (s *server) patchHost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p apitypes.HostPatch
	if !decode(w, r, &p) {
		return
	}
	h, err := s.svc.UpdateHost(r.Context(), id, p)
	if err == nil && p.SNMPCommunities.Set { // never the strings (SPEC-315)
		s.log.Info("SNMP communities set", "host", id, "count", len(p.SNMPCommunities.Value))
	}
	s.respond(w, http.StatusOK, h, err)
}

func (s *server) deleteHost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.respond(w, http.StatusNoContent, nil, s.svc.DeleteHost(r.Context(), id))
}

// listGroups serves GET /groups: every group, or with ?view= one view's.
func (s *server) listGroups(w http.ResponseWriter, r *http.Request) {
	if !r.URL.Query().Has("view") {
		writeJSON(w, http.StatusOK, s.svc.Groups())
		return
	}
	view, ok := queryView(w, r)
	if !ok {
		return
	}
	gs, err := s.svc.GroupsIn(view)
	s.respond(w, http.StatusOK, gs, err)
}

func (s *server) getGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := s.svc.Group(id)
	s.respond(w, http.StatusOK, g, err)
}

func (s *server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in apitypes.GroupCreate
	if !decode(w, r, &in) {
		return
	}
	g, err := s.svc.CreateGroup(r.Context(), in)
	s.respond(w, http.StatusCreated, g, err)
}

func (s *server) patchGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p apitypes.GroupPatch
	if !decode(w, r, &p) {
		return
	}
	g, err := s.svc.UpdateGroup(r.Context(), id, p)
	if err == nil && p.SNMPCommunities.Set { // never the strings (SPEC-315)
		s.log.Info("SNMP communities set", "group", id, "count", len(p.SNMPCommunities.Value))
	}
	s.respond(w, http.StatusOK, g, err)
}

// moveGroup serves POST /groups/{id}/move (SPEC-507).
func (s *server) moveGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in apitypes.GroupMove
	if !decode(w, r, &in) {
		return
	}
	move := s.svc.MoveGroup
	if in.ContentsOnly {
		move = s.svc.MoveGroupContents
	}
	g, err := move(r.Context(), id, in.DX, in.DY)
	s.respond(w, http.StatusOK, g, err)
}

func (s *server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.svc.DeleteGroup(r.Context(), id, r.URL.Query().Get("children"))
	s.respond(w, http.StatusNoContent, nil, err)
}

func (s *server) startDiscovery(w http.ResponseWriter, r *http.Request) {
	var in apitypes.DiscoveryCreate
	if !decode(w, r, &in) {
		return
	}
	d, err := s.opts.Discovery.Start(r.Context(), in)
	s.respond(w, http.StatusAccepted, d, err)
}

// listDiscovery lists the jobs, scheduled ones too (SPEC-205, 206).
func (s *server) listDiscovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Discovery.List())
}

func (s *server) getDiscovery(w http.ResponseWriter, r *http.Request) {
	d, err := s.opts.Discovery.Get(r.PathValue("id"))
	s.respond(w, http.StatusOK, d, err)
}

// cancelDiscovery asks the job to stop; the response shows its state at
// that moment, and discovery.finished follows (SPEC-205).
func (s *server) cancelDiscovery(w http.ResponseWriter, r *http.Request) {
	d, err := s.opts.Discovery.Cancel(r.PathValue("id"))
	s.respond(w, http.StatusOK, d, err)
}

// ---- helpers ----

func (s *server) respond(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, v)
}

// writeDomainError maps inventory errors to the SPEC-504 table.
func (s *server) writeDomainError(w http.ResponseWriter, err error) {
	switch inventory.KindOf(err) {
	case inventory.Invalid:
		writeError(w, http.StatusBadRequest, apitypes.CodeInvalidArgument, err.Error())
	case inventory.NotFound:
		writeError(w, http.StatusNotFound, apitypes.CodeNotFound, err.Error())
	case inventory.Conflict:
		writeError(w, http.StatusConflict, apitypes.CodeConflict, err.Error())
	case inventory.TooLarge:
		writeError(w, http.StatusRequestEntityTooLarge, apitypes.CodeTooLarge, err.Error())
	case inventory.Unavailable:
		writeError(w, http.StatusServiceUnavailable, apitypes.CodeUnavailable, err.Error())
	default:
		s.log.Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, apitypes.CodeInternal, "internal server error")
	}
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) { return pathInt(w, r, "id") }

func pathInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, apitypes.CodeInvalidArgument, name+" must be a positive integer")
		return 0, false
	}
	return id, true
}

// queryView reads ?view=; absent means the network view.
func queryView(w http.ResponseWriter, r *http.Request) (int64, bool) {
	q := r.URL.Query()
	if !q.Has("view") {
		return apitypes.NetworkViewID, true
	}
	id, err := strconv.ParseInt(q.Get("view"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, apitypes.CodeInvalidArgument, "view must be a positive integer")
		return 0, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, apitypes.CodeTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", mbe.Limit))
			return false
		}
		writeError(w, http.StatusBadRequest, apitypes.CodeInvalidArgument, "invalid JSON body: "+err.Error())
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, apitypes.CodeInvalidArgument, "invalid JSON body: trailing data")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apitypes.ErrorBody{Error: apitypes.ErrorDetail{Code: code, Message: msg}})
}
