// Package client is the Go client of the go-ined REST and SSE API
// (SPEC-506). The GUI and inedctl both use it.
//
// Every call takes a context. API errors are returned as *APIError; any
// other error means the server could not be reached or answered something
// that isn't the API.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Prefix is the path prefix of the API.
const Prefix = "/api/v1"

// DefaultTimeout limits one REST call when Options.Timeout is zero. The
// event stream has no overall limit (see StreamOptions.IdleTimeout).
const DefaultTimeout = 30 * time.Second

// Options configures a Client.
type Options struct {
	// URL is the server's base URL, for example "http://127.0.0.1:8470".
	URL string
	// Token is the bearer token; empty sends no Authorization header.
	Token string
	// CACert is a PEM file with extra trusted certificates, for a server
	// with a self-signed certificate (server.ca_cert, SPEC-805). The system
	// roots stay trusted.
	CACert string
	// Timeout limits one REST call; 0 = DefaultTimeout.
	Timeout time.Duration
	// Transport replaces the HTTP transport (tests); CACert is then ignored.
	Transport http.RoundTripper
}

// Client talks to one go-ined server. It is safe for concurrent use.
type Client struct {
	base    string // scheme://host[:port][/path], without trailing slash
	token   string
	timeout time.Duration
	hc      *http.Client // no overall timeout; calls use contexts
}

// New returns a client. It checks the URL and reads CACert, but doesn't
// contact the server.
func New(opts Options) (*Client, error) {
	u, err := url.Parse(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("client: invalid server URL %q: %w", opts.URL, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("client: invalid server URL %q: want http://host:port or https://host:port", opts.URL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("client: invalid server URL %q: no query or fragment allowed", opts.URL)
	}
	rt := opts.Transport
	if rt == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		if opts.CACert != "" {
			pool, err := certPool(opts.CACert)
			if err != nil {
				return nil, err
			}
			t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		rt = t
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		base:    strings.TrimRight(u.String(), "/"),
		token:   opts.Token,
		timeout: timeout,
		hc:      &http.Client{Transport: rt},
	}, nil
}

func certPool(file string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("client: CA certificate: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("client: CA certificate: no PEM certificate in %s", file)
	}
	return pool, nil
}

// URL returns the server's base URL.
func (c *Client) URL() string { return c.base }

// TokenInClear reports whether the client sends a token over plain HTTP to
// a host that isn't loopback, where others on the network can read it.
// Clients warn about this (SPEC-802).
func (c *Client) TokenInClear() bool {
	u, err := url.Parse(c.base)
	if err != nil || c.token == "" || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return false
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return !ip.Unmap().IsLoopback()
	}
	return net.ParseIP(h) == nil // a name other than localhost
}

// APIError is an error response of the API (SPEC-504).
type APIError struct {
	Status  int    // HTTP status
	Code    string // apitypes.Code*; empty if the body wasn't an API error
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("%s (HTTP %d): %s", e.Code, e.Status, e.Message)
}

// IsCode reports whether err is an *APIError with this code.
func IsCode(err error, code string) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == code
}

// Do sends one request to Prefix+path and decodes a JSON response into
// out. body, if not nil, is sent as JSON. out may be nil (the body is
// discarded) or a *json.RawMessage (the body is kept unchanged). A
// response other than 2xx is returned as *APIError.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("client: encoding request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("client: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp.StatusCode, data)
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *json.RawMessage:
		*o = append((*o)[:0], bytes.TrimRight(data, "\n")...)
		return nil
	default:
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("client: decoding response of %s %s: %w", method, path, err)
		}
		return nil
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+Prefix+path, body)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

func apiError(status int, data []byte) error {
	var eb apitypes.ErrorBody
	if json.Unmarshal(data, &eb) == nil && eb.Error.Code != "" {
		return &APIError{Status: status, Code: eb.Error.Code, Message: eb.Error.Message}
	}
	msg := strings.TrimSpace(string(data))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &APIError{Status: status, Message: msg}
}

func id(n int64) string { return strconv.FormatInt(n, 10) }

// ---- health and map ----

// Health returns GET /health. Without a valid token only Status and Version
// are filled in.
func (c *Client) Health(ctx context.Context) (apitypes.Health, error) {
	var h apitypes.Health
	return h, c.Do(ctx, http.MethodGet, "/health", nil, &h)
}

// Map returns GET /map: every host and group with its position.
func (c *Client) Map(ctx context.Context) (apitypes.Map, error) {
	var m apitypes.Map
	return m, c.Do(ctx, http.MethodGet, "/map", nil, &m)
}

// ---- hosts ----

// Hosts lists all hosts, ordered by ID.
func (c *Client) Hosts(ctx context.Context) ([]apitypes.Host, error) {
	var hs []apitypes.Host
	return hs, c.Do(ctx, http.MethodGet, "/hosts", nil, &hs)
}

// Host returns one host.
func (c *Client) Host(ctx context.Context, hostID int64) (apitypes.Host, error) {
	var h apitypes.Host
	return h, c.Do(ctx, http.MethodGet, "/hosts/"+id(hostID), nil, &h)
}

// CreateHost adds a host.
func (c *Client) CreateHost(ctx context.Context, in apitypes.HostCreate) (apitypes.Host, error) {
	var h apitypes.Host
	return h, c.Do(ctx, http.MethodPost, "/hosts", in, &h)
}

// UpdateHost changes the fields set in p.
func (c *Client) UpdateHost(ctx context.Context, hostID int64, p apitypes.HostPatch) (apitypes.Host, error) {
	var h apitypes.Host
	return h, c.Do(ctx, http.MethodPatch, "/hosts/"+id(hostID), p, &h)
}

// DeleteHost removes a host.
func (c *Client) DeleteHost(ctx context.Context, hostID int64) error {
	return c.Do(ctx, http.MethodDelete, "/hosts/"+id(hostID), nil, nil)
}

// ---- groups ----

// Groups lists all groups, ordered by ID.
func (c *Client) Groups(ctx context.Context) ([]apitypes.Group, error) {
	var gs []apitypes.Group
	return gs, c.Do(ctx, http.MethodGet, "/groups", nil, &gs)
}

// Group returns one group.
func (c *Client) Group(ctx context.Context, groupID int64) (apitypes.Group, error) {
	var g apitypes.Group
	return g, c.Do(ctx, http.MethodGet, "/groups/"+id(groupID), nil, &g)
}

// CreateGroup adds a group.
func (c *Client) CreateGroup(ctx context.Context, in apitypes.GroupCreate) (apitypes.Group, error) {
	var g apitypes.Group
	return g, c.Do(ctx, http.MethodPost, "/groups", in, &g)
}

// UpdateGroup changes the fields set in p.
func (c *Client) UpdateGroup(ctx context.Context, groupID int64, p apitypes.GroupPatch) (apitypes.Group, error) {
	var g apitypes.Group
	return g, c.Do(ctx, http.MethodPatch, "/groups/"+id(groupID), p, &g)
}

// MoveGroup moves a group and everything in it by (dx, dy) (SPEC-507).
func (c *Client) MoveGroup(ctx context.Context, groupID int64, dx, dy float64) (apitypes.Group, error) {
	var g apitypes.Group
	return g, c.Do(ctx, http.MethodPost, "/groups/"+id(groupID)+"/move", apitypes.GroupMove{DX: dx, DY: dy}, &g)
}

// Arrange lays out the members of a group (in.ParentID nil: the view's
// root) on a grid (SPEC-510).
func (c *Client) Arrange(ctx context.Context, viewID int64, in apitypes.Arrange) (apitypes.ArrangeResult, error) {
	var r apitypes.ArrangeResult
	return r, c.Do(ctx, http.MethodPost, "/views/"+id(viewID)+"/arrange", in, &r)
}

// MoveGroupContents moves everything inside a group by (dx, dy), but not
// the group itself (v0.29).
func (c *Client) MoveGroupContents(ctx context.Context, groupID int64, dx, dy float64) (apitypes.Group, error) {
	var g apitypes.Group
	return g, c.Do(ctx, http.MethodPost, "/groups/"+id(groupID)+"/move", apitypes.GroupMove{DX: dx, DY: dy, ContentsOnly: true}, &g)
}

// DeleteGroup removes a group. children is "" (the group must be empty),
// apitypes.ChildrenReparent or apitypes.ChildrenCascade (SPEC-104).
func (c *Client) DeleteGroup(ctx context.Context, groupID int64, children string) error {
	return c.Do(ctx, http.MethodDelete, GroupDeletePath(groupID, children), nil, nil)
}

// GroupDeletePath is the path DeleteGroup uses.
func GroupDeletePath(groupID int64, children string) string {
	p := "/groups/" + id(groupID)
	if children != "" {
		p += "?children=" + url.QueryEscape(children)
	}
	return p
}

// ---- discovery ----

// StartDiscovery starts a discovery job (SPEC-201).
func (c *Client) StartDiscovery(ctx context.Context, in apitypes.DiscoveryCreate) (apitypes.Discovery, error) {
	var d apitypes.Discovery
	return d, c.Do(ctx, http.MethodPost, "/discovery", in, &d)
}

// Discovery returns a discovery job.
func (c *Client) Discovery(ctx context.Context, jobID string) (apitypes.Discovery, error) {
	var d apitypes.Discovery
	return d, c.Do(ctx, http.MethodGet, "/discovery/"+url.PathEscape(jobID), nil, &d)
}

// CancelDiscovery asks a job to stop. The result shows its state at that
// moment; discovery.finished follows.
func (c *Client) CancelDiscovery(ctx context.Context, jobID string) (apitypes.Discovery, error) {
	var d apitypes.Discovery
	return d, c.Do(ctx, http.MethodDelete, "/discovery/"+url.PathEscape(jobID), nil, &d)
}

// ---- views (SPEC-117..122) ----

// Views lists the views in tab order.
func (c *Client) Views(ctx context.Context) ([]apitypes.View, error) {
	var vs []apitypes.View
	return vs, c.Do(ctx, http.MethodGet, "/views", nil, &vs)
}

// View returns one view.
func (c *Client) View(ctx context.Context, viewID int64) (apitypes.View, error) {
	var v apitypes.View
	return v, c.Do(ctx, http.MethodGet, "/views/"+id(viewID), nil, &v)
}

// CreateView adds a custom view.
func (c *Client) CreateView(ctx context.Context, in apitypes.ViewCreate) (apitypes.View, error) {
	var v apitypes.View
	return v, c.Do(ctx, http.MethodPost, "/views", in, &v)
}

// UpdateView renames or reorders a view.
func (c *Client) UpdateView(ctx context.Context, viewID int64, p apitypes.ViewPatch) (apitypes.View, error) {
	var v apitypes.View
	return v, c.Do(ctx, http.MethodPatch, "/views/"+id(viewID), p, &v)
}

// DeleteView removes a custom view with its groups and placements.
func (c *Client) DeleteView(ctx context.Context, viewID int64) error {
	return c.Do(ctx, http.MethodDelete, "/views/"+id(viewID), nil, nil)
}

// ViewMap returns the items of one view. Map returns the network view.
func (c *Client) ViewMap(ctx context.Context, viewID int64) (apitypes.Map, error) {
	var m apitypes.Map
	return m, c.Do(ctx, http.MethodGet, "/map?view="+id(viewID), nil, &m)
}

// ViewGroups lists the groups of one view.
func (c *Client) ViewGroups(ctx context.Context, viewID int64) ([]apitypes.Group, error) {
	var gs []apitypes.Group
	return gs, c.Do(ctx, http.MethodGet, "/groups?view="+id(viewID), nil, &gs)
}

// PlaceHost adds a host to a custom view, or moves it there.
func (c *Client) PlaceHost(ctx context.Context, viewID, hostID int64, in apitypes.PlacementSet) (apitypes.Placement, error) {
	var p apitypes.Placement
	return p, c.Do(ctx, http.MethodPut, PlacementPath(viewID, hostID), in, &p)
}

// UnplaceHost removes a host from a custom view; the host stays.
func (c *Client) UnplaceHost(ctx context.Context, viewID, hostID int64) error {
	return c.Do(ctx, http.MethodDelete, PlacementPath(viewID, hostID), nil, nil)
}

// PlacementPath is the path PlaceHost and UnplaceHost use.
func PlacementPath(viewID, hostID int64) string {
	return "/views/" + id(viewID) + "/hosts/" + id(hostID)
}
