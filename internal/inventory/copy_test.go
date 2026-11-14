package inventory

import (
	"fmt"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-740 (SPEC-729): copying a network group into a custom view makes the
// same structure there (name, labels, colour, collapsed; no CIDR), places
// every host of the subtree in the matching group at the same relative
// position, moves hosts already in the view into the copy, persists, and
// emits group.created and placement.updated; errors for the network view,
// the group's own view, unknown views and groups.
func TestCopyGroup(t *testing.T) {
	s, r := newSvc(t)
	rec := &recorder{}
	s.SetNotifier(rec)
	net := mustGroup(t, s, apitypes.GroupCreate{CIDR: "10.1.0.0/16", Labels: "ip", Color: "#4363D8", Collapsed: ptr(false), X: ptr(100.0), Y: ptr(100.0)})
	sub := mustGroup(t, s, apitypes.GroupCreate{Name: "servers", ParentID: &net.ID, X: ptr(120.0), Y: ptr(200.0)})
	a := mustHost(t, s, apitypes.HostCreate{IP: "10.1.0.1", ParentID: &net.ID, X: ptr(300.0), Y: ptr(150.0)})
	b := mustHost(t, s, apitypes.HostCreate{IP: "10.1.0.2", ParentID: &sub.ID, X: ptr(140.0), Y: ptr(260.0)})
	other := mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.9"})
	pay := mustView(t, s, "Payroll")
	mustPlace(t, s, pay.ID, b.ID, apitypes.PlacementSet{}) // already in Payroll
	mustPlace(t, s, pay.ID, other.ID, apitypes.PlacementSet{})
	rec.take()

	res, err := s.CopyGroup(ctx, pay.ID, net.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.ViewID != pay.ID || res.Groups != 2 || res.Hosts != 2 || res.Moved != 1 {
		t.Fatalf("result %+v", res)
	}
	top, err := s.Group(res.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	if top.ViewID != pay.ID || top.Name != "10.1.0.0/16" || top.CIDR != "" || top.Labels != "ip" || top.Color != "#4363D8" ||
		top.Collapsed || top.ParentID != nil {
		t.Fatalf("top copy %+v", top)
	}
	gs, _ := s.GroupsIn(pay.ID)
	var subCopy apitypes.Group
	for _, g := range gs {
		if g.Name == "servers" {
			subCopy = g
		}
	}
	if subCopy.ID == 0 || subCopy.ParentID == nil || *subCopy.ParentID != top.ID ||
		subCopy.X-top.X != sub.X-net.X || subCopy.Y-top.Y != sub.Y-net.Y {
		t.Fatalf("sub copy %+v (top %+v)", subCopy, top)
	}
	m, _ := s.MapOf(pay.ID)
	pos := map[int64]apitypes.MapItem{}
	for _, it := range m.Items {
		if it.Type == apitypes.TypeHost {
			pos[it.ID] = it
		}
	}
	if p := pos[a.ID]; p.ParentID == nil || *p.ParentID != top.ID || p.X-top.X != a.X-net.X || p.Y-top.Y != a.Y-net.Y {
		t.Errorf("a placed %+v", p)
	}
	if p := pos[b.ID]; p.ParentID == nil || *p.ParentID != subCopy.ID || p.X-top.X != b.X-net.X {
		t.Errorf("b placed %+v", p)
	}
	if p := pos[other.ID]; p.ParentID != nil {
		t.Errorf("other moved %+v", p)
	}
	// The originals are unchanged.
	if g, _ := s.Group(net.ID); g.ViewID != 1 || g.CIDR != "10.1.0.0/16" {
		t.Errorf("original %+v", g)
	}
	if log := rec.take(); strings.Count(log, apitypes.EventGroupCreated+" ") != 2 || strings.Count(log, apitypes.EventPlacementUpdated+" ") != 2 ||
		!strings.HasPrefix(log, fmt.Sprintf("%s %d ", apitypes.EventGroupCreated, top.ID)) {
		t.Errorf("events: %s", log)
	}

	// Persisted: a new service on the same store sees the copy.
	s2, err := NewService(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if g, err := s2.Group(subCopy.ID); err != nil || g.ViewID != pay.ID {
		t.Errorf("after reload: %+v %v", g, err)
	}
	if g := mustGroup(t, s2, apitypes.GroupCreate{Name: "next"}); g.ID <= subCopy.ID {
		t.Errorf("group id reused: %d", g.ID)
	}

	// A copy of the copy into another custom view works; errors.
	lab := mustView(t, s, "Lab")
	if res2, err := s.CopyGroup(ctx, lab.ID, top.ID); err != nil || res2.Groups != 2 || res2.Hosts != 2 || res2.Moved != 0 {
		t.Errorf("copy of copy: %+v %v", res2, err)
	}
	_, err = s.CopyGroup(ctx, 1, sub.ID)
	wantKind(t, err, Invalid)
	_, err = s.CopyGroup(ctx, pay.ID, top.ID)
	wantKind(t, err, Invalid)
	_, err = s.CopyGroup(ctx, 99, net.ID)
	wantKind(t, err, NotFound)
	_, err = s.CopyGroup(ctx, pay.ID, 999)
	wantKind(t, err, Invalid)
}
