package inventory

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// lanRig builds: dc (plain, 100,100) › lan 10.0.1.0/24 (200,300) with
// web (watching ssh, https; a note) and printers (plain) › p1 (not
// monitored); a host outside lan; a custom view Payroll › "app servers"
// with web placed in it.
func lanRig(t *testing.T) (s *Service, lan apitypes.Group, web apitypes.Host) {
	t.Helper()
	s, _ = newSvc(t)
	dc := mustGroup(t, s, apitypes.GroupCreate{Name: "dc", X: ptr(100.0), Y: ptr(100.0)})
	lan = mustGroup(t, s, apitypes.GroupCreate{Name: "lan", CIDR: "10.0.1.0/24", ParentID: &dc.ID, Labels: "both", Color: "#4363d8",
		Note: "the LAN", Collapsed: ptr(false), X: ptr(200.0), Y: ptr(300.0)})
	pr := mustGroup(t, s, apitypes.GroupCreate{Name: "printers", ParentID: &lan.ID, Description: "floor 2", X: ptr(260.0), Y: ptr(420.0)})
	web = mustHost(t, s, apitypes.HostCreate{IP: "10.0.1.10", Name: "web", Note: "front end", ParentID: &lan.ID, X: ptr(240.0), Y: ptr(360.0)})
	if _, err := s.UpdateHost(ctx, web.ID, apitypes.HostPatch{Watch: apitypes.Some([]string{"ssh", "https"})}); err != nil {
		t.Fatal(err)
	}
	mustHost(t, s, apitypes.HostCreate{IP: "10.0.1.20", Name: "p1", Monitored: ptr(false), ParentID: &pr.ID, X: ptr(270.0), Y: ptr(440.0)})
	mustHost(t, s, apitypes.HostCreate{IP: "192.0.2.1", ParentID: &dc.ID})
	pay := mustView(t, s, "Payroll")
	app := mustGroup(t, s, apitypes.GroupCreate{Name: "app servers", ViewID: &pay.ID, Color: "#e6194b", X: ptr(500.0), Y: ptr(50.0)})
	mustPlace(t, s, pay.ID, web.ID, apitypes.PlacementSet{ParentID: &app.ID, X: ptr(520.0), Y: ptr(90.0)})
	return s, lan, web
}

// TC-130: an export holds the group, its subgroups and hosts with their
// own fields, positions relative to the group, watch lists by name and
// custom-view placements by view name and path, with the groups on those
// paths; nothing the server measures. Only network-view groups.
func TestExportGroup(t *testing.T) {
	s, lan, _ := lanRig(t)
	f, err := s.ExportGroup(lan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.Format != "go-ined-network" || f.Version != 1 || f.From != "Networks › dc › lan" || f.ExportedAt.IsZero() {
		t.Fatalf("header: %+v", f)
	}
	g := f.Group
	if g.Name != "lan" || g.CIDR != "10.0.1.0/24" || g.Labels != "both" || g.Color != "#4363D8" || g.Note != "the LAN" ||
		g.Collapsed || g.X != 0 || g.Y != 0 || len(g.Groups) != 1 || len(g.Hosts) != 1 {
		t.Fatalf("group: %+v", g)
	}
	h := g.Hosts[0]
	if h.IP != "10.0.1.10" || h.Name != "web" || h.Note != "front end" || !h.Monitored || h.X != 40 || h.Y != 60 ||
		!reflect.DeepEqual(h.Watch, []string{"ssh", "https"}) {
		t.Fatalf("host: %+v", h)
	}
	if want := []apitypes.ExportPlacement{{View: "Payroll", Path: []string{"app servers"}, X: 520, Y: 90}}; !reflect.DeepEqual(h.Views, want) {
		t.Fatalf("views: %+v", h.Views)
	}
	pr := g.Groups[0]
	if pr.Name != "printers" || pr.Description != "floor 2" || pr.X != 60 || pr.Y != 120 || len(pr.Hosts) != 1 ||
		pr.Hosts[0].Monitored || len(pr.Hosts[0].Views) != 0 || pr.Hosts[0].Watch == nil {
		t.Fatalf("printers: %+v", pr)
	}
	if want := []apitypes.ExportViewGroup{{View: "Payroll", Path: []string{"app servers"}, Color: "#E6194B", Collapsed: true, X: 500, Y: 50}}; !reflect.DeepEqual(f.ViewGroups, want) {
		t.Fatalf("view groups: %+v", f.ViewGroups)
	}
	b, _ := json.Marshal(f)
	for _, gone := range []string{"status", "rtt", "last_seen", "ptr_name", "services", `"id"`} {
		if strings.Contains(string(b), gone) {
			t.Errorf("the file has %q", gone)
		}
	}
	if _, err := s.ExportGroup(999); KindOf(err) != NotFound {
		t.Errorf("unknown group: %v", err)
	}
	pay := s.Groups()
	for _, cg := range pay {
		if cg.Name == "app servers" {
			_, err := s.ExportGroup(cg.ID)
			wantKind(t, err, Invalid)
		}
	}
}

// TC-131: the round trip: an export imported into an empty server (with
// the custom view there, its group missing) and exported again gives the
// same group tree and view groups; the hosts are new and unknown; events.
func TestImportRoundTrip(t *testing.T) {
	a, lan, _ := lanRig(t)
	f, err := a.ExportGroup(lan.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newSvc(t)
	rec := &recorder{}
	b.SetNotifier(rec)
	pay := mustView(t, b, "Payroll")
	rep, err := b.Import(ctx, &f, ImportOptions{X: ptr(1000.0), Y: ptr(2000.0)})
	if err != nil {
		t.Fatal(err)
	}
	want := apitypes.ImportCounts{Groups: 2, Hosts: 2, ViewGroups: 1, Placements: 1}
	if !rep.Done || rep.DryRun || rep.Create != want || rep.Update != (apitypes.ImportCounts{}) || len(rep.Conflicts) != 0 || rep.GroupID == nil {
		t.Fatalf("report: %+v", rep)
	}
	top, _ := b.Group(*rep.GroupID)
	if top.X != 1000 || top.Y != 2000 || top.ParentID != nil || top.CIDR != "10.0.1.0/24" {
		t.Fatalf("top: %+v", top)
	}
	for _, h := range b.Hosts() {
		if h.Status != apitypes.StatusUnknown || h.LastSeen != nil {
			t.Errorf("host %s: %+v", h.IP, h)
		}
	}
	ev := rec.take()
	for _, w := range []string{"group.created", "host.created", "placement.updated " + itoa(pay.ID)} {
		if !strings.Contains(ev, w) {
			t.Errorf("events %q: no %s", ev, w)
		}
	}
	g, err := b.ExportGroup(*rep.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Group, f.Group) || !reflect.DeepEqual(g.ViewGroups, f.ViewGroups) {
		a, _ := json.MarshalIndent(f.Group, "", " ")
		c, _ := json.MarshalIndent(g.Group, "", " ")
		t.Fatalf("round trip differs:\n%s\n---\n%s", a, c)
	}
}

// TC-132: importing into the server it came from conflicts (CIDR and every
// IP address) and changes nothing; a dry run lists the conflicts without
// an error; "update existing" updates in place, moves matching hosts into
// their group from the file and changes nothing else.
func TestImportConflictsAndUpdate(t *testing.T) {
	s, lan, web := lanRig(t)
	f, _ := s.ExportGroup(lan.ID)
	before, _ := json.Marshal(s.Snapshot())

	rep, err := s.Import(ctx, &f, ImportOptions{})
	if !errors.Is(err, ErrImportConflicts) || rep.Done || len(rep.Conflicts) != 4 || rep.Update != (apitypes.ImportCounts{}) {
		t.Fatalf("conflicts: %v %+v", err, rep)
	}
	if !strings.HasPrefix(rep.Conflicts[0], "network group 10.0.1.0/24 exists already: group ") ||
		!strings.Contains(rep.Conflicts[1], `group "printers" exists already in Networks › dc › lan: group 3`) ||
		!strings.Contains(rep.Conflicts[2], `IP address 10.0.1.10 exists already: host 1 "web" in Networks › dc › lan`) {
		t.Fatalf("messages: %q", rep.Conflicts)
	}
	dry, err := s.Import(ctx, &f, ImportOptions{DryRun: true})
	if err != nil || !dry.DryRun || dry.Done || len(dry.Conflicts) != 4 {
		t.Fatalf("dry run: %v %+v", err, dry)
	}
	after, _ := json.Marshal(s.Snapshot())
	if string(before) != string(after) {
		t.Fatal("a refused import or a dry run changed the inventory")
	}

	// Update: web was moved out of lan and renamed meanwhile; the file puts
	// it back. The host outside the file stays.
	if _, err := s.UpdateHost(ctx, web.ID, apitypes.HostPatch{Name: apitypes.Some("old"), ParentID: apitypes.Some[*int64](nil)}); err != nil {
		t.Fatal(err)
	}
	f.Group.Hosts[0].Watch = []string{"ssh"}
	f.Group.Description = "updated"
	rep, err = s.Import(ctx, &f, ImportOptions{Update: true, ParentID: Root})
	if err != nil || !rep.Done || rep.Update != (apitypes.ImportCounts{Groups: 2, Hosts: 2, Placements: 1}) || rep.Create != (apitypes.ImportCounts{}) {
		t.Fatalf("update: %v %+v", err, rep)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "is updated where it is, in Networks › dc, not moved below the top level") {
		t.Fatalf("warnings: %q", rep.Warnings)
	}
	h, _ := s.Host(web.ID)
	g, _ := s.Group(lan.ID)
	if h.Name != "web" || h.ParentID == nil || *h.ParentID != lan.ID || len(h.Watch) != 1 || g.Description != "updated" || *rep.GroupID != lan.ID {
		t.Fatalf("after update: host %+v group %+v", h, g)
	}
	if n := len(s.Hosts()); n != 3 {
		t.Fatalf("%d hosts, want 3", n)
	}
}

// TC-132: with "update existing", a network group matched below the top
// keeps its place too, with a warning; the new group above it in the file
// stays empty (C4 of crosscheck 2026-11-17-01).
func TestImportUpdateKeepsPlace(t *testing.T) {
	a, _ := newSvc(t)
	site := mustGroup(t, a, apitypes.GroupCreate{Name: "site"})
	mustGroup(t, a, apitypes.GroupCreate{Name: "lan", CIDR: "10.0.1.0/24", ParentID: &site.ID})
	f, _ := a.ExportGroup(site.ID)

	b, _ := newSvc(t)
	lan := mustGroup(t, b, apitypes.GroupCreate{Name: "lan", CIDR: "10.0.1.0/24", X: ptr(70.0), Y: ptr(80.0)})
	rep, err := b.Import(ctx, &f, ImportOptions{Update: true})
	if err != nil || rep.Create.Groups != 1 || rep.Update.Groups != 1 {
		t.Fatalf("update: %v %+v", err, rep)
	}
	want := `"lan" (10.0.1.0/24) is updated where it is, in the top level, not moved below group "site" as in the file`
	if len(rep.Warnings) != 1 || !strings.HasSuffix(rep.Warnings[0], want) {
		t.Fatalf("warnings: %q", rep.Warnings)
	}
	g, _ := b.Group(lan.ID)
	if g.ParentID != nil || g.X != 70 || g.Y != 80 {
		t.Fatalf("lan moved: %+v", g)
	}
}

// TC-133: what lands where: a skipped view, a missing custom-view group
// made with defaults, a warning when a more specific network group
// exists, a network group that would not fit its enclosing one, a plain
// group conflicting by name, and the position as for a new group.
func TestImportPlacesAndWarns(t *testing.T) {
	a, lan, _ := lanRig(t)
	f, _ := a.ExportGroup(lan.ID)
	f.ViewGroups = nil
	f.Group.Hosts[0].Views = append(f.Group.Hosts[0].Views, apitypes.ExportPlacement{View: "Lab", Path: []string{}, X: 1, Y: 1})

	b, _ := newSvc(t)
	pay := mustView(t, b, "Payroll")
	mustGroup(t, b, apitypes.GroupCreate{CIDR: "10.0.0.0/16"})
	site := mustGroup(t, b, apitypes.GroupCreate{Name: "site"})
	rep, err := b.Import(ctx, &f, ImportOptions{ParentID: site.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Skipped, []string{`view "Lab" doesn't exist (1 placement)`}) {
		t.Errorf("skipped: %q", rep.Skipped)
	}
	if len(rep.Warnings) != 1 || rep.Warnings[0] != "10.0.0.0/16 (Networks › 10.0.0.0/16) would be a more specific home for 10.0.1.0/24 than Networks › site" {
		t.Errorf("warnings: %q", rep.Warnings)
	}
	top, _ := b.Group(*rep.GroupID)
	if *top.ParentID != site.ID {
		t.Errorf("parent: %+v", top)
	}
	gs, _ := b.GroupsIn(pay.ID)
	if len(gs) != 1 || gs[0].Name != "app servers" || !gs[0].Collapsed || gs[0].Color != "" {
		t.Errorf("made in Payroll: %+v", gs)
	}

	// Below a network group it doesn't fit in: an error, also with update.
	c, _ := newSvc(t)
	other := mustGroup(t, c, apitypes.GroupCreate{CIDR: "192.168.0.0/16"})
	for _, up := range []bool{false, true} {
		_, err = c.Import(ctx, &f, ImportOptions{ParentID: other.ID, Update: up})
		if KindOf(err) != Conflict || errors.Is(err, ErrImportConflicts) || !strings.Contains(err.Error(), "not inside its enclosing network group") {
			t.Errorf("update %v: %v", up, err)
		}
	}
	// A plain group with the same name under the same parent conflicts.
	d, _ := newSvc(t)
	mustGroup(t, d, apitypes.GroupCreate{Name: "printers"})
	pf, _ := a.ExportGroup(a.Groups()[2].ID)
	if pf.Group.Name != "printers" {
		t.Fatalf("exported %q", pf.Group.Name)
	}
	rep, err = d.Import(ctx, &pf, ImportOptions{})
	if !errors.Is(err, ErrImportConflicts) || len(rep.Conflicts) != 1 || !strings.HasPrefix(rep.Conflicts[0], `group "printers" exists already in the top level`) {
		t.Errorf("plain group: %v %q", err, rep.Conflicts)
	}
}

// TC-134: a file is checked like API input; nothing is changed.
func TestImportBadFiles(t *testing.T) {
	a, lan, _ := lanRig(t)
	good, _ := a.ExportGroup(lan.ID)
	clone := func() apitypes.NetworkExport {
		var c apitypes.NetworkExport
		b, _ := json.Marshal(good)
		json.Unmarshal(b, &c)
		return c
	}
	deep := clone()
	cur := &deep.Group
	for i := 0; i < apitypes.MaxImportDepth; i++ {
		cur.Groups = []apitypes.ExportGroup{{Name: "g"}}
		cur = &cur.Groups[0]
	}
	many := clone()
	for i := range apitypes.MaxImportItems {
		many.Group.Hosts = append(many.Group.Hosts, apitypes.ExportHost{IP: "10.9." + itoa(int64(i/250)) + "." + itoa(int64(i%250+1))})
	}
	for _, c := range []struct {
		edit func(f *apitypes.NetworkExport)
		want string
	}{
		{func(f *apitypes.NetworkExport) { f.Format = "tkined" }, `not a go-ined network export (format "tkined"`},
		{func(f *apitypes.NetworkExport) { f.Version = 2 }, "export file version 2; this server reads version 1"},
		{func(f *apitypes.NetworkExport) { f.Group.Groups[0].CIDR = "10.0.1.1/24" }, `group "lan" › "printers": CIDR "10.0.1.1/24" has host bits set`},
		{func(f *apitypes.NetworkExport) { f.Group.Hosts[0].IP = "10.0.1.300" }, `host 10.0.1.300: invalid IP address`},
		{func(f *apitypes.NetworkExport) { f.Group.Groups[0].Hosts[0].IP = "10.0.1.10" }, `IP address 10.0.1.10 is also in group "lan"`},
		{func(f *apitypes.NetworkExport) { f.Group.Hosts[0].Watch = []string{"ftp"} }, `watch: unknown service "ftp"`},
		{func(f *apitypes.NetworkExport) { f.Group.Hosts[0].Views[0].View = " " }, "a placement without a view name"},
		{func(f *apitypes.NetworkExport) { f.Group.Color = "red" }, `color must be #rrggbb`},
		{func(f *apitypes.NetworkExport) { f.Group.Name, f.Group.CIDR = "", "" }, "group needs a name or a CIDR"},
		{func(f *apitypes.NetworkExport) { f.ViewGroups[0].Path = nil }, "view group 1"},
		{func(f *apitypes.NetworkExport) {
			f.Group.Groups = append(f.Group.Groups, apitypes.ExportGroup{Name: " printers "})
		}, `group "lan" › "printers": group "printers" is given twice in the same group`},
		{func(f *apitypes.NetworkExport) { *f = deep }, "more than 32 levels of groups"},
		{func(f *apitypes.NetworkExport) { *f = many }, "more than 10000 groups and hosts"},
	} {
		f := clone()
		c.edit(&f)
		b, _ := newSvc(t)
		_, err := b.Import(ctx, &f, ImportOptions{})
		if KindOf(err) != Invalid || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
		if len(b.Groups()) != 0 {
			t.Errorf("%q: groups were made", c.want)
		}
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
