package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/internal/store"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

type env struct {
	t     *testing.T
	srv   *httptest.Server
	token string
	db    string
	st    *store.Store
}

// newEnv starts a real server (SQLite store + inventory + HTTP).
func newEnv(t *testing.T, token string) *env {
	t.Helper()
	e := &env{t: t, token: token, db: filepath.Join(t.TempDir(), "ined.db")}
	e.start()
	return e
}

func (e *env) start() {
	e.t.Helper()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	svc, err := inventory.NewService(context.Background(), st)
	if err != nil {
		e.t.Fatal(err)
	}
	e.st = st
	e.srv = httptest.NewServer(New(svc, Options{Token: e.token, Version: "test", RunID: "run1", DBPing: st.Ping}))
	e.t.Cleanup(e.stop)
}

func (e *env) stop() {
	if e.srv != nil {
		e.srv.Close()
		e.st.Close()
		e.srv = nil
	}
}

// restart simulates a server restart on the same database.
func (e *env) restart() { e.stop(); e.start() }

// do sends a request with the env's token and decodes the JSON response
// into out (if non-nil). It returns the status code.
func (e *env) do(method, path string, body any, out any) int {
	return e.doTok(e.token, method, path, body, out)
}

func (e *env) doTok(token, method, path string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, e.srv.URL+Prefix+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// Every error must have the SPEC-504 shape.
		var eb apitypes.ErrorBody
		if err := json.Unmarshal(data, &eb); err != nil || eb.Error.Code == "" || eb.Error.Message == "" {
			e.t.Errorf("%s %s: status %d with non-conforming error body %q", method, path, resp.StatusCode, data)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			e.t.Errorf("%s %s: error content type %q", method, path, ct)
		}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return resp.StatusCode
}

func (e *env) want(code int, method, path string, body any, out any) {
	e.t.Helper()
	if got := e.do(method, path, body, out); got != code {
		e.t.Fatalf("%s %s: status %d, want %d", method, path, got, code)
	}
}

func (e *env) wantErr(code int, errCode, method, path string, body any) {
	e.t.Helper()
	var eb apitypes.ErrorBody
	if got := e.do(method, path, body, &eb); got != code || eb.Error.Code != errCode {
		e.t.Fatalf("%s %s: got %d %q, want %d %q (%s)", method, path, got, eb.Error.Code, code, errCode, eb.Error.Message)
	}
}

// TC-501, TC-101, TC-107: CRUD round trip and error codes.
func TestCRUD(t *testing.T) {
	e := newEnv(t, "")
	var net apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{CIDR: "192.0.2.0/24", Description: "lab"}, &net)
	if net.Name != "192.0.2.0/24" || net.CIDR != "192.0.2.0/24" || !net.Collapsed || net.Status != "unknown" {
		t.Errorf("created group: %+v", net)
	}
	var plain apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "servers"}, &plain)

	var h apitypes.Host
	e.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "192.0.2.10", Name: "web"}, &h)
	if h.ParentID == nil || *h.ParentID != net.ID || h.DisplayName != "web" || !h.Monitored || h.Status != "unknown" {
		t.Errorf("created host: %+v", h)
	}
	e.wantErr(409, "conflict", "POST", "/hosts", apitypes.HostCreate{IP: "192.0.2.10"})
	e.wantErr(400, "invalid_argument", "POST", "/hosts", apitypes.HostCreate{IP: "300.1.1.1"})
	e.wantErr(409, "conflict", "POST", "/groups", apitypes.GroupCreate{CIDR: "192.0.2.0/24"})
	e.wantErr(400, "invalid_argument", "POST", "/groups", apitypes.GroupCreate{CIDR: "192.0.2.5/24"})

	var got apitypes.Host
	e.want(200, "GET", "/hosts/"+itoa(h.ID), nil, &got)
	if got.IP != "192.0.2.10" {
		t.Errorf("GET host: %+v", got)
	}
	e.want(200, "PATCH", "/hosts/"+itoa(h.ID), `{"parent_id": `+itoa(plain.ID)+`, "description": "nginx"}`, &got)
	if *got.ParentID != plain.ID || got.Description != "nginx" || got.Name != "web" {
		t.Errorf("PATCH host: %+v", got)
	}
	e.want(200, "PATCH", "/hosts/"+itoa(h.ID), `{"parent_id": null}`, &got)
	if got.ParentID != nil {
		t.Errorf("parent_id null: %+v", got.ParentID)
	}

	var hosts []apitypes.Host
	e.want(200, "GET", "/hosts", nil, &hosts)
	var groups []apitypes.Group
	e.want(200, "GET", "/groups", nil, &groups)
	if len(hosts) != 1 || len(groups) != 2 {
		t.Errorf("lists: %d hosts, %d groups", len(hosts), len(groups))
	}

	e.want(204, "DELETE", "/hosts/"+itoa(h.ID), nil, nil)
	e.wantErr(404, "not_found", "GET", "/hosts/"+itoa(h.ID), nil)
	e.wantErr(404, "not_found", "PATCH", "/groups/999", apitypes.GroupPatch{})
	e.wantErr(400, "invalid_argument", "GET", "/hosts/abc", nil)
	e.wantErr(400, "invalid_argument", "GET", "/hosts/0", nil)
}

// TC-501: request-level errors.
func TestRequestErrors(t *testing.T) {
	e := newEnv(t, "")
	e.wantErr(400, "invalid_argument", "POST", "/hosts", `{"ip": "192.0.2.1", "bogus": 1}`)
	e.wantErr(400, "invalid_argument", "POST", "/hosts", `{"ip": `)
	e.wantErr(400, "invalid_argument", "POST", "/hosts", `{"ip": "192.0.2.1"} {}`)
	e.wantErr(413, "too_large", "POST", "/hosts", `{"ip": "`+strings.Repeat("x", MaxBodyBytes)+`"}`)
	e.wantErr(404, "not_found", "GET", "/nope", nil)
	e.wantErr(405, "method_not_allowed", "PUT", "/hosts", nil)
	e.wantErr(405, "method_not_allowed", "POST", "/hosts/1", nil)
}

// TC-104: delete group modes through the API.
func TestDeleteGroupAPI(t *testing.T) {
	e := newEnv(t, "")
	var top, mid apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "top"}, &top)
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "mid", ParentID: &top.ID}, &mid)
	var h apitypes.Host
	e.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "198.51.100.1", ParentID: &mid.ID}, &h)

	e.wantErr(409, "conflict", "DELETE", "/groups/"+itoa(mid.ID), nil)
	e.wantErr(400, "invalid_argument", "DELETE", "/groups/"+itoa(mid.ID)+"?children=all", nil)
	e.want(204, "DELETE", "/groups/"+itoa(mid.ID)+"?children=reparent", nil, nil)
	e.want(200, "GET", "/hosts/"+itoa(h.ID), nil, &h)
	if *h.ParentID != top.ID {
		t.Errorf("reparent: %v", h.ParentID)
	}
	e.want(204, "DELETE", "/groups/"+itoa(top.ID)+"?children=cascade", nil, nil)
	e.wantErr(404, "not_found", "GET", "/hosts/"+itoa(h.ID), nil)
}

// TC-109, TC-103 through the API: nesting and cycles are 409.
func TestTreeRulesAPI(t *testing.T) {
	e := newEnv(t, "")
	var n16, n24 apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{CIDR: "10.1.0.0/16"}, &n16)
	e.want(201, "POST", "/groups", apitypes.GroupCreate{CIDR: "10.1.2.0/24", ParentID: &n16.ID}, &n24)
	e.wantErr(409, "conflict", "POST", "/groups", apitypes.GroupCreate{CIDR: "10.2.0.0/24", ParentID: &n16.ID})
	e.wantErr(409, "conflict", "PATCH", "/groups/"+itoa(n16.ID), `{"parent_id": `+itoa(n24.ID)+`}`)
}

// TC-106, TC-110, TC-112 (persistence part): positions, notes and the
// collapsed flag are saved and survive a restart. TC-602 (partial): loaded
// hosts are stale.
func TestPersistence(t *testing.T) {
	e := newEnv(t, "")
	var g apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "g"}, &g)
	var h apitypes.Host
	e.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "2001:db8::1", ParentID: &g.ID}, &h)
	if h.Stale {
		t.Error("new host stale")
	}
	e.want(200, "PATCH", "/groups/"+itoa(g.ID), `{"collapsed": false, "x": 10, "y": 20, "note": "rack 4"}`, nil)
	e.want(200, "PATCH", "/hosts/"+itoa(h.ID), `{"x": 30.5, "y": 40}`, nil)
	e.wantErr(413, "too_large", "PATCH", "/hosts/"+itoa(h.ID), apitypes.HostPatch{Note: apitypes.Some(strings.Repeat("n", inventory.MaxNoteBytes+1))})

	e.restart()
	e.want(200, "GET", "/groups/"+itoa(g.ID), nil, &g)
	if g.Collapsed || g.X != 10 || g.Y != 20 || g.Note != "rack 4" {
		t.Errorf("group after restart: %+v", g)
	}
	e.want(200, "GET", "/hosts/"+itoa(h.ID), nil, &h)
	if h.X != 30.5 || h.Y != 40 || !h.Stale || h.IP != "2001:db8::1" {
		t.Errorf("host after restart: %+v", h)
	}
	// IDs continue after a restart.
	var h2 apitypes.Host
	e.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "2001:db8::2"}, &h2)
	if h2.ID != h.ID+1 {
		t.Errorf("id after restart = %d, want %d", h2.ID, h.ID+1)
	}
}

// TC-504: /map contains every item with the specified fields.
func TestMap(t *testing.T) {
	e := newEnv(t, "")
	var net, plain apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{CIDR: "10.0.0.0/24", Note: "n"}, &net)
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "plain", ParentID: &net.ID, Collapsed: ptr(false)}, &plain)
	var h apitypes.Host
	e.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "192.0.2.1", ParentID: &plain.ID}, &h)

	// Check the raw JSON keys, not only the decoded struct.
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	e.want(200, "GET", "/map", nil, &raw)
	if len(raw.Items) != 3 {
		t.Fatalf("map has %d items, want 3", len(raw.Items))
	}
	common := []string{"type", "id", "parent_id", "x", "y", "name", "status", "stale", "has_note", "outside_cidr", "collapsed"}
	for _, it := range raw.Items {
		for _, k := range common {
			if _, ok := it[k]; !ok {
				t.Errorf("item %v/%v lacks %q", it["type"], it["id"], k)
			}
		}
	}
	var m apitypes.Map
	e.want(200, "GET", "/map", nil, &m)
	byKey := map[string]apitypes.MapItem{}
	for _, it := range m.Items {
		byKey[it.Type+"/"+itoa(it.ID)] = it
	}
	gn := byKey["group/"+itoa(net.ID)]
	if gn.CIDR != "10.0.0.0/24" || !gn.HasNote || !gn.Collapsed || gn.ParentID != nil {
		t.Errorf("network group item: %+v", gn)
	}
	gp := byKey["group/"+itoa(plain.ID)]
	if gp.Collapsed || gp.CIDR != "" || *gp.ParentID != net.ID {
		t.Errorf("plain group item: %+v", gp)
	}
	hi := byKey["host/"+itoa(h.ID)]
	if hi.IP != "192.0.2.1" || !hi.OutsideCIDR || hi.Name != "192.0.2.1" || hi.Status != "unknown" {
		t.Errorf("host item: %+v", hi)
	}
}

// TC-801 (401 part), TC-806: token handling and the reduced /health.
func TestAuthAndHealth(t *testing.T) {
	e := newEnv(t, "s3cret")
	var eb apitypes.ErrorBody
	if c := e.doTok("", "GET", "/map", nil, &eb); c != 401 || eb.Error.Code != "unauthenticated" {
		t.Errorf("no token: %d %+v", c, eb)
	}
	if c := e.doTok("wrong", "GET", "/hosts", nil, nil); c != 401 {
		t.Errorf("wrong token: %d", c)
	}
	if c := e.doTok("s3cret", "GET", "/hosts", nil, nil); c != 200 {
		t.Errorf("right token: %d", c)
	}

	var raw map[string]any
	if c := e.doTok("", "GET", "/health", nil, &raw); c != 200 {
		t.Fatalf("health without token: %d", c)
	}
	if len(raw) != 2 || raw["status"] != "ok" || raw["version"] != "test" {
		t.Errorf("reduced health: %v", raw)
	}
	var full apitypes.Health
	e.doTok("s3cret", "GET", "/health", nil, &full)
	if full.DB != "ok" || full.RunID != "run1" || full.ICMPMode == "" {
		t.Errorf("full health: %+v", full)
	}

	// No token configured: everything is open, including the full /health.
	open := newEnv(t, "")
	open.doTok("", "GET", "/health", nil, &full)
	if full.RunID != "run1" {
		t.Errorf("open server health: %+v", full)
	}
}

// /health reports a broken database.
func TestHealthDegraded(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc, _ := inventory.NewService(context.Background(), st)
	h := New(svc, Options{DBPing: func(context.Context) error { return errors.New("boom") }})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", Prefix+"/health", nil))
	var hv apitypes.Health
	_ = json.Unmarshal(rec.Body.Bytes(), &hv)
	if hv.Status != "degraded" || !strings.Contains(hv.DB, "boom") {
		t.Errorf("got %+v", hv)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func ptr[T any](v T) *T { return &v }
