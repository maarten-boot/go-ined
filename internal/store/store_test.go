package store

import (
	"context"
	"database/sql"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
)

var ctx = context.Background()

// TC-601: fresh database gets the latest schema.
func TestMigrateFresh(t *testing.T) {
	s, err := Open(ctx, filepath.Join(t.TempDir(), "a", "b", "ined.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != len(migrations) {
		t.Fatalf("version %d (%v), want %d", v, err, len(migrations))
	}
}

// TC-601: a database at an older version is upgraded step by step, data kept;
// a newer database is refused.
func TestMigrateFromOlder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	steps := []string{
		`CREATE TABLE t (a INTEGER); INSERT INTO t VALUES (1);`,
		`ALTER TABLE t ADD COLUMN b TEXT NOT NULL DEFAULT 'x';`,
	}
	if err := migrate(ctx, db, steps[:1]); err != nil {
		t.Fatal(err)
	}
	if v, _ := schemaVersion(ctx, db); v != 1 {
		t.Fatalf("version %d, want 1", v)
	}
	if err := migrate(ctx, db, steps); err != nil {
		t.Fatal(err)
	}
	var a int
	var b string
	if err := db.QueryRow(`SELECT a, b FROM t`).Scan(&a, &b); err != nil || a != 1 || b != "x" {
		t.Fatalf("data after upgrade: %d %q %v", a, b, err)
	}
	if v, _ := schemaVersion(ctx, db); v != 2 {
		t.Fatalf("version %d, want 2", v)
	}
	if err := migrate(ctx, db, steps[:1]); err == nil {
		t.Fatal("newer schema must be refused")
	}
}

// A failing migration leaves the version unchanged.
func TestMigrateFailureRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(ctx, db, []string{`CREATE TABLE ok (a);`, `THIS IS NOT SQL;`}); err == nil {
		t.Fatal("want error")
	}
	if v, _ := schemaVersion(ctx, db); v != 1 {
		t.Fatalf("version %d, want 1", v)
	}
}

// Round trip of every field, including NULL handling, upsert and delete.
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rt.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 123_000_000, time.UTC)
	rtt, loss := 1.25, 33.3
	g := inventory.Group{ID: 1, Name: "net", Description: "d", Note: "n", CIDR: netip.MustParsePrefix("10.0.0.0/24"),
		Collapsed: true, X: 1, Y: 2, CreatedAt: now, UpdatedAt: now}
	g2 := inventory.Group{ID: 2, Name: "plain", ParentID: 1, CreatedAt: now, UpdatedAt: now}
	h := inventory.Host{ID: 7, IP: netip.MustParseAddr("2001:db8::1"), PTRName: "p.", Name: "m", Description: "d", Note: "n",
		ParentID: 2, Monitored: true, Status: "up", RTTms: &rtt, LossPct: &loss, LastSeen: now, LastChange: now,
		X: 3, Y: 4, CreatedAt: now, UpdatedAt: now}
	h2 := inventory.Host{ID: 8, IP: netip.MustParseAddr("10.0.0.2"), Status: "unknown", CreatedAt: now, UpdatedAt: now}
	if err := s.Apply(ctx, inventory.Changes{Groups: []inventory.Group{g, g2}, Hosts: []inventory.Host{h, h2}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gs, hs, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gs, []inventory.Group{g, g2}) {
		t.Errorf("groups:\n got %+v\nwant %+v", gs, []inventory.Group{g, g2})
	}
	if !reflect.DeepEqual(hs, []inventory.Host{h, h2}) {
		t.Errorf("hosts:\n got %+v\nwant %+v", hs, []inventory.Host{h, h2})
	}

	// Upsert changes, delete removes.
	h.Note = "changed"
	if err := s.Apply(ctx, inventory.Changes{Hosts: []inventory.Host{h}, DeleteHosts: []int64{8}, DeleteGroups: []int64{2}}); err != nil {
		t.Fatal(err)
	}
	gs, hs, _ = s.Load(ctx)
	if len(gs) != 1 || len(hs) != 1 || hs[0].Note != "changed" {
		t.Errorf("after update: %d groups, %d hosts, %+v", len(gs), len(hs), hs)
	}

	// The UNIQUE safety net rolls back the whole transaction.
	dup := h2
	dup.ID, dup.IP = 9, h.IP
	g3 := g2
	g3.ID = 3
	if err := s.Apply(ctx, inventory.Changes{Groups: []inventory.Group{g3}, Hosts: []inventory.Host{dup}}); err == nil {
		t.Fatal("duplicate IP accepted")
	}
	if gs, _, _ := s.Load(ctx); len(gs) != 1 {
		t.Errorf("transaction not rolled back: %d groups", len(gs))
	}
}
