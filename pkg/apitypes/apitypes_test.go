package apitypes

import (
	"encoding/json"
	"testing"
)

// PATCH semantics: absent field = not set; null = set to zero; value = set.
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
