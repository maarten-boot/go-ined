package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// TC-513: GET /groups/{id}/export, also with the read-only token; POST
// /import into another server: a dry run changes nothing, the import is
// kept over a restart, the same file again is 409 with the report, update
// is 200; bad query values and files are 400; the read token gets 403;
// /import takes bodies over 1 MiB, up to 32 MiB.
func TestNetworkExportAPI(t *testing.T) {
	a := &env{t: t, token: "full", read: "reader", db: filepath.Join(t.TempDir(), "a.db")}
	a.start()
	var lan apitypes.Group
	a.want(201, "POST", "/groups", apitypes.GroupCreate{CIDR: "10.0.1.0/24", Name: "lan"}, &lan)
	a.want(201, "POST", "/hosts", apitypes.HostCreate{IP: "10.0.1.10", Name: "web", Note: "front"}, nil)
	a.want(200, "PATCH", "/hosts/1", map[string]any{"watch": []string{"ssh"}}, nil)
	var f apitypes.NetworkExport
	if c := a.doTok("reader", "GET", "/groups/1/export", nil, &f); c != 200 {
		t.Fatalf("export with the read token: %d", c)
	}
	if f.Format != apitypes.ExportFormat || f.ServerVersion != "test" || len(f.Group.Hosts) != 1 || f.Group.Hosts[0].Watch[0] != "ssh" {
		t.Fatalf("export: %+v", f)
	}
	a.wantErr(404, apitypes.CodeNotFound, "GET", "/groups/9/export", nil)

	b := &env{t: t, token: "full", read: "reader", db: filepath.Join(t.TempDir(), "b.db")}
	b.start()
	var rep apitypes.ImportReport
	b.want(200, "POST", "/import?dry_run=true", f, &rep)
	if !rep.DryRun || rep.Done || rep.Create.Hosts != 1 || rep.Create.Groups != 1 {
		t.Fatalf("dry run: %+v", rep)
	}
	var hosts []apitypes.Host
	b.want(200, "GET", "/hosts", nil, &hosts)
	if len(hosts) != 0 {
		t.Fatal("the dry run made hosts")
	}
	var eb apitypes.ErrorBody
	if c := b.doTok("reader", "POST", "/import", f, &eb); c != 403 {
		t.Fatalf("read token: %d", c)
	}
	b.want(200, "POST", "/import?x=500&y=600", f, &rep)
	if !rep.Done || rep.GroupID == nil {
		t.Fatalf("import: %+v", rep)
	}
	b.restart()
	var g apitypes.Group
	b.want(200, "GET", "/groups/1", nil, &g)
	b.want(200, "GET", "/hosts", nil, &hosts)
	if g.CIDR != "10.0.1.0/24" || g.X != 500 || len(hosts) != 1 || hosts[0].Note != "front" || hosts[0].Watch[0].Name != "ssh" ||
		hosts[0].ParentID == nil || *hosts[0].ParentID != g.ID {
		t.Fatalf("after a restart: %+v %+v", g, hosts)
	}

	var ic apitypes.ImportConflict
	if c := b.do("POST", "/import", f, &ic); c != 409 || len(ic.Report.Conflicts) != 2 || ic.Report.Done ||
		ic.Error.Message != "2 conflicts with what is there; nothing was changed (see report.conflicts; update=true updates them)" {
		t.Fatalf("again: %d %+v", c, ic)
	}
	f.Group.Hosts[0].Name = "web2"
	b.want(200, "POST", "/import?update=true", f, &rep)
	b.want(200, "GET", "/hosts", nil, &hosts)
	if rep.Update.Hosts != 1 || hosts[0].Name != "web2" {
		t.Fatalf("update: %+v %+v", rep, hosts)
	}

	for _, q := range []string{"parent_id=x", "parent_id=0", "x=1e999", "dry_run=maybe"} {
		b.wantErr(400, apitypes.CodeInvalidArgument, "POST", "/import?"+q, f)
	}
	b.wantErr(400, apitypes.CodeInvalidArgument, "POST", "/import?parent_id=77", f)
	b.wantErr(400, apitypes.CodeInvalidArgument, "POST", "/import", `{"format": "go-ined-network", "version": 1, "extra": 1}`)
	f.Version = 9
	b.wantErr(400, apitypes.CodeInvalidArgument, "POST", "/import", f)

	// Bodies: 2 MiB is fine for /import, not for other requests; 33 MiB
	// is too large.
	f.Version = 1
	body, _ := json.Marshal(f)
	pad := func(n int) string { return string(body[:len(body)-1]) + strings.Repeat(" ", n) + "}" }
	b.want(200, "POST", "/import?dry_run=true", pad(2<<20), nil)
	b.wantErr(413, apitypes.CodeTooLarge, "POST", "/hosts", `{"ip": "10.0.0.1"`+strings.Repeat(" ", 2<<20)+"}")
	b.wantErr(413, apitypes.CodeTooLarge, "POST", "/import", pad(33<<20))
}
