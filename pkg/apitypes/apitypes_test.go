package apitypes

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// PATCH semantics: absent field = not set; null = set to nil (pointer
// fields only, see TestOptNull); value = set.
func TestOpt(t *testing.T) {
	var p HostPatch
	if err := json.Unmarshal([]byte(`{"parent_id": null, "note": "x"}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.ParentID.Set || p.ParentID.Value != nil {
		t.Errorf("parent_id null: %+v", p.ParentID)
	}
	if !p.Note.Set || p.Note.Value != "x" {
		t.Errorf("note: %+v", p.Note)
	}
	if p.Name.Set || p.X.Set {
		t.Error("absent fields marked as set")
	}
	if err := json.Unmarshal([]byte(`{"parent_id": 5}`), &p); err != nil || *p.ParentID.Value != 5 {
		t.Errorf("parent_id 5: %v %+v", err, p.ParentID)
	}

	// Marshal omits unset fields (omitzero), keeps set ones including null.
	out, _ := json.Marshal(HostPatch{ParentID: Some[*int64](nil), Monitored: Some(false)})
	if string(out) != `{"parent_id":null,"monitored":false}` {
		t.Errorf("marshal: %s", out)
	}
}

// A null on a field that can't be null is an error, not a zero value
// (SPEC-504, crosscheck 2026-11-11-01 C1).
func TestOptNull(t *testing.T) {
	for _, tc := range []struct {
		body string
		into any
	}{
		{`{"monitored": null}`, &HostPatch{}},
		{`{"x": null}`, &HostPatch{}},
		{`{"name": null}`, &HostPatch{}},
		{`{"collapsed": null}`, &GroupPatch{}},
		{`{"cidr": null}`, &GroupPatch{}},
		{`{"order": null}`, &ViewPatch{}},
	} {
		err := json.Unmarshal([]byte(tc.body), tc.into)
		if err == nil {
			t.Errorf("%s: no error", tc.body)
			continue
		}
		t.Logf("%s: %v", tc.body, err)
	}
	var g GroupPatch
	if err := json.Unmarshal([]byte(`{"parent_id": null}`), &g); err != nil || !g.ParentID.Set || g.ParentID.Value != nil {
		t.Errorf("group parent_id null: %v %+v", err, g.ParentID)
	}
}

// TC-320: watch lists: known names and tcp:N in canonical form; unknown
// names, bad ports, a port twice and too many entries are refused.
func TestParseWatchList(t *testing.T) {
	ps, err := ParseWatchList([]string{" SSH ", "vnc:3", "tcp:05432", "https"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ServicePort{{"ssh", 22}, {"vnc:3", 5903}, {"tcp:5432", 5432}, {"https", 443}}
	if !slices.Equal(ps, want) {
		t.Fatalf("got %v, want %v", ps, want)
	}
	if ps, err := ParseWatchList(nil); err != nil || len(ps) != 0 {
		t.Fatalf("empty: %v %v", ps, err)
	}
	many := make([]string, MaxWatch+1)
	for i := range many {
		many[i] = "tcp:" + strconv.Itoa(1000+i)
	}
	if _, err := ParseWatchList(many[:MaxWatch]); err != nil {
		t.Fatalf("%d entries: %v", MaxWatch, err)
	}
	for in, msg := range map[string]string{
		"ftp": `unknown service "ftp"`, "vnc:10": "unknown service", "": "empty service name",
		"tcp:0": "from 1 to 65535", "tcp:65536": "from 1 to 65535", "tcp:x": "from 1 to 65535",
		"ssh,ssh": `"ssh" is given twice`, "http,tcp:80": `"http" and "tcp:80" are the same port 80`,
		strings.Join(many, ","): "21 services; at most 20",
	} {
		if _, err := ParseWatchList(strings.Split(in, ",")); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v, want %q", in, err, msg)
		}
	}
}
