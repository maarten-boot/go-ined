package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/internal/events"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

type openAPIDocument struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title, Version string
	} `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"schemas"`
	} `json:"components"`
}

// TC-505: the OpenAPI document is valid, matches the routes, covers every
// JSON field of pkg/apitypes, and is served without a token.
func TestOpenAPI(t *testing.T) {
	var doc openAPIDocument
	if err := json.Unmarshal(openAPIDoc, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") || doc.Info.Title == "" || doc.Info.Version == "" {
		t.Errorf("header: %+v", doc)
	}

	// Paths and methods equal the registered routes.
	s, _ := newServer(nil, Options{Events: nil, Discovery: fakeDiscovery{}})
	s2, _ := newServer(nil, Options{Events: events.New("r", 10), Discovery: fakeDiscovery{}})
	routes := s2.routes
	if len(routes) <= len(s.routes) {
		t.Fatal("events route missing")
	}
	var documented []string
	for path, ops := range doc.Paths {
		for m := range ops {
			documented = append(documented, strings.ToUpper(m)+" "+path)
		}
	}
	sort.Strings(routes)
	sort.Strings(documented)
	if !slices.Equal(routes, documented) {
		t.Errorf("routes and document differ:\nroutes:   %v\ndocument: %v", routes, documented)
	}

	// Every JSON field of the API types is in the schema of the same name,
	// and nothing else.
	for name, v := range map[string]any{
		"Host": apitypes.Host{}, "Group": apitypes.Group{}, "MapItem": apitypes.MapItem{}, "Map": apitypes.Map{},
		"HostCreate": apitypes.HostCreate{}, "HostPatch": apitypes.HostPatch{}, "GroupCreate": apitypes.GroupCreate{},
		"GroupPatch": apitypes.GroupPatch{}, "GroupMove": apitypes.GroupMove{}, "View": apitypes.View{},
		"ViewCreate": apitypes.ViewCreate{}, "ViewPatch": apitypes.ViewPatch{}, "Placement": apitypes.Placement{},
		"PlacementSet": apitypes.PlacementSet{}, "DiscoveryCreate": apitypes.DiscoveryCreate{},
		"Discovery": apitypes.Discovery{}, "Health": apitypes.Health{}, "ErrorBody": apitypes.ErrorBody{},
		"ErrorDetail": apitypes.ErrorDetail{}, "DeletedEvent": apitypes.DeletedEvent{},
		"GroupStatusEvent": apitypes.GroupStatusEvent{}, "GroupMovedEvent": apitypes.GroupMovedEvent{},
		"PlacementDeletedEvent": apitypes.PlacementDeletedEvent{}, "ResyncEvent": apitypes.ResyncEvent{},
	} {
		sc, ok := doc.Components.Schemas[name]
		if !ok {
			t.Errorf("no schema %s", name)
			continue
		}
		var fields []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			tag, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			if tag != "" && tag != "-" {
				fields = append(fields, tag)
			}
		}
		var props []string
		for p := range sc.Properties {
			props = append(props, p)
		}
		sort.Strings(fields)
		sort.Strings(props)
		if !slices.Equal(fields, props) {
			t.Errorf("schema %s: properties %v, Go fields %v", name, props, fields)
		}
		for _, r := range sc.Required {
			if _, ok := sc.Properties[r]; !ok {
				t.Errorf("schema %s requires unknown %q", name, r)
			}
		}
	}
	if len(doc.Components.Schemas) != 24 {
		t.Errorf("%d schemas, want the 24 checked", len(doc.Components.Schemas))
	}

	// Served without a token, as JSON.
	srv := httptest.NewServer(New(nil, Options{Token: "secret"}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + Prefix + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || json.NewDecoder(resp.Body).Decode(&got) != nil {
		t.Errorf("GET openapi.json: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
