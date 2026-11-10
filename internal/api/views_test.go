package api

import (
	"encoding/json"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-125: view CRUD, map per view, placements, groups per view, errors.
func TestViewsAPI(t *testing.T) {
	e := newEnv(t, "")
	var vs []apitypes.View
	e.want(200, "GET", "/views", nil, &vs)
	if len(vs) != 1 || vs[0].ID != 1 || vs[0].Kind != "network" {
		t.Fatalf("views: %+v", vs)
	}
	var v apitypes.View
	e.want(201, "POST", "/views", apitypes.ViewCreate{Name: "Payroll"}, &v)
	e.wantErr(409, apitypes.CodeConflict, "POST", "/views", apitypes.ViewCreate{Name: "Payroll"})
	e.wantErr(400, apitypes.CodeInvalidArgument, "POST", "/views", map[string]any{"name": "x", "kind": "network"})
	e.want(200, "PATCH", "/views/2", map[string]any{"name": "Pay", "order": 7}, &v)
	if v.Name != "Pay" || v.Order != 7 {
		t.Fatalf("patch: %+v", v)
	}
	e.want(200, "GET", "/views/2", nil, &v)
	e.wantErr(404, apitypes.CodeNotFound, "GET", "/views/9", nil)
	e.wantErr(400, apitypes.CodeInvalidArgument, "GET", "/views/x", nil)
	e.wantErr(409, apitypes.CodeConflict, "DELETE", "/views/1", nil)

	var net, app apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{CIDR: "10.0.0.0/24"}, &net)
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "app", ViewID: &v.ID, Collapsed: ptr(false)}, &app)
	if app.ViewID != 2 || net.ViewID != 1 {
		t.Fatalf("view ids: %+v %+v", net, app)
	}
	e.wantErr(400, apitypes.CodeInvalidArgument, "POST", "/groups", apitypes.GroupCreate{CIDR: "10.1.0.0/24", ViewID: &v.ID})
	e.wantErr(400, apitypes.CodeInvalidArgument, "PATCH", "/groups/2", map[string]any{"view_id": 1})
	var h apitypes.Host
	e.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "10.0.0.5"}, &h)

	var p apitypes.Placement
	e.want(200, "PUT", "/views/2/hosts/1", apitypes.PlacementSet{ParentID: &app.ID}, &p)
	if p.ViewID != 2 || p.HostID != 1 || *p.ParentID != app.ID {
		t.Fatalf("placement: %+v", p)
	}
	e.wantErr(400, apitypes.CodeInvalidArgument, "PUT", "/views/1/hosts/1", apitypes.PlacementSet{})
	e.wantErr(400, apitypes.CodeInvalidArgument, "PUT", "/views/2/hosts/1", apitypes.PlacementSet{ParentID: &net.ID})
	e.wantErr(404, apitypes.CodeNotFound, "PUT", "/views/2/hosts/9", apitypes.PlacementSet{})
	e.wantErr(404, apitypes.CodeNotFound, "PUT", "/views/9/hosts/1", apitypes.PlacementSet{})
	e.wantErr(400, apitypes.CodeInvalidArgument, "PUT", "/views/2/hosts/x", apitypes.PlacementSet{})
	e.wantErr(405, apitypes.CodeMethodNotAllowed, "GET", "/views/2/hosts/1", nil)

	var m apitypes.Map
	e.want(200, "GET", "/map?view=2", nil, &m)
	if m.ViewID != 2 || len(m.Items) != 2 || m.Items[1].Type != "host" || *m.Items[1].ParentID != app.ID {
		t.Fatalf("map?view=2: %+v", m)
	}
	e.want(200, "GET", "/map", nil, &m)
	if m.ViewID != 1 || len(m.Items) != 2 || *m.Items[1].ParentID != net.ID {
		t.Fatalf("map: %+v", m)
	}
	e.wantErr(404, apitypes.CodeNotFound, "GET", "/map?view=9", nil)
	e.wantErr(400, apitypes.CodeInvalidArgument, "GET", "/map?view=abc", nil)

	var gs []apitypes.Group
	e.want(200, "GET", "/groups?view=2", nil, &gs)
	if len(gs) != 1 || gs[0].ID != app.ID {
		t.Fatalf("groups?view=2: %+v", gs)
	}
	e.want(200, "GET", "/groups", nil, &gs)
	if len(gs) != 2 {
		t.Fatalf("groups: %+v", gs)
	}

	e.want(204, "DELETE", "/views/2/hosts/1", nil, nil)
	e.wantErr(404, apitypes.CodeNotFound, "DELETE", "/views/2/hosts/1", nil)
	e.want(204, "DELETE", "/views/2", nil, nil)
	e.wantErr(404, apitypes.CodeNotFound, "GET", "/groups/2", nil)

	// Everything survives a restart.
	e.want(201, "POST", "/views", apitypes.ViewCreate{Name: "Lab"}, &v)
	e.want(200, "PUT", "/views/3/hosts/1", apitypes.PlacementSet{X: ptr(1.0), Y: ptr(2.0)}, &p)
	e.restart()
	e.want(200, "GET", "/map?view=3", nil, &m)
	if len(m.Items) != 1 || m.Items[0].X != 1 || m.Items[0].Y != 2 {
		t.Fatalf("after restart: %+v", m)
	}
}

// TC-126: view and placement events.
func TestViewEvents(t *testing.T) {
	e := newEnv(t, "")
	st := e.stream("")
	var v apitypes.View
	e.want(201, "POST", "/views", apitypes.ViewCreate{Name: "A"}, &v)
	var g apitypes.Group
	e.want(201, "POST", "/groups", apitypes.GroupCreate{Name: "g", ViewID: &v.ID}, &g)
	e.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "192.0.2.1"}, nil)
	e.want(200, "PUT", "/views/2/hosts/1", apitypes.PlacementSet{ParentID: &g.ID}, nil)
	e.want(200, "PATCH", "/views/2", map[string]any{"name": "B"}, nil)
	e.want(204, "DELETE", "/groups/1?children=cascade", nil, nil)
	e.want(200, "PUT", "/views/2/hosts/1", apitypes.PlacementSet{}, nil)
	e.want(204, "DELETE", "/views/2/hosts/1", nil, nil)
	e.want(204, "DELETE", "/views/2", nil, nil)

	var gotV apitypes.View
	json.Unmarshal([]byte(st.want(apitypes.EventViewCreated).data), &gotV)
	var gotG apitypes.Group
	json.Unmarshal([]byte(st.want(apitypes.EventGroupCreated).data), &gotG)
	if gotV.Name != "A" || gotG.ViewID != 2 {
		t.Fatalf("created: %+v %+v", gotV, gotG)
	}
	st.want(apitypes.EventHostCreated)
	var p apitypes.Placement
	json.Unmarshal([]byte(st.want(apitypes.EventPlacementUpdated).data), &p)
	if p.ViewID != 2 || p.HostID != 1 || *p.ParentID != g.ID {
		t.Fatalf("placement.updated: %+v", p)
	}
	st.want(apitypes.EventViewUpdated)
	// Cascade in a custom view: placement.deleted and group.deleted, no host.deleted.
	var pd apitypes.PlacementDeletedEvent
	json.Unmarshal([]byte(st.want(apitypes.EventPlacementDeleted).data), &pd)
	if pd.ViewID != 2 || pd.HostID != 1 {
		t.Fatalf("placement.deleted: %+v", pd)
	}
	st.want(apitypes.EventGroupDeleted)
	st.want(apitypes.EventPlacementUpdated)
	st.want(apitypes.EventPlacementDeleted)
	f := st.want(apitypes.EventViewDeleted)
	if f.data != `{"id":2}` {
		t.Fatalf("view.deleted: %s", f.data)
	}
}
