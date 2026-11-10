package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"os"
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
	st, err := s.Load(ctx)
	gs, hs := st.Groups, st.Hosts
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
	st, _ = s.Load(ctx)
	gs, hs = st.Groups, st.Hosts
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
	if st, _ := s.Load(ctx); len(st.Groups) != 1 {
		t.Errorf("transaction not rolled back: %d groups", len(gs))
	}
}

// TC-603: paths with URI-special characters open exactly that file.
func TestSpecialCharacterPaths(t *testing.T) {
	for _, name := range []string{"a?b.db", "a#b.db", "a%20b.db", "a b.db", "ü.db"} {
		dir := t.TempDir()
		s, err := Open(ctx, filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		s.Close()
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%q: file not created under its own name: %v", name, err)
		}
	}
	// A relative path is made absolute.
	t.Chdir(t.TempDir())
	s, err := Open(ctx, "rel.db")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat("rel.db"); err != nil {
		t.Errorf("relative path: %v", err)
	}
}

// TC-604: a second opener is refused at once with ErrLocked; the lock is
// released on Close.
func TestExclusiveLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.db")
	a, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = Open(ctx, p)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second open: want ErrLocked, got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("refusal took %v; want immediate", d)
	}
	// The first store still works.
	if err := a.Apply(ctx, inventory.Changes{NextHostID: 5}); err != nil {
		t.Fatalf("first store after refused second open: %v", err)
	}
	a.Close()
	b, err := Open(ctx, p)
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	b.Close()
}

// TC-113 (store part): sequences are persisted; migration 2 seeds them
// from existing rows.
func TestSequences(t *testing.T) {
	p := filepath.Join(t.TempDir(), "seq.db")
	s, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load(ctx)
	if st.NextHostID != 1 || st.NextGroupID != 1 {
		t.Errorf("fresh sequences: %+v", st)
	}
	if err := s.Apply(ctx, inventory.Changes{NextHostID: 9, NextGroupID: 4}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, _ = Open(ctx, p)
	st, _ = s.Load(ctx)
	s.Close()
	if st.NextHostID != 9 || st.NextGroupID != 4 {
		t.Errorf("persisted sequences: %+v", st)
	}
}

// TC-601: migration 2 on a version-1 database with data seeds the
// sequences from MAX(id).
func TestMigration2Seeds(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hosts (id, ip, created_at, updated_at) VALUES (7, '192.0.2.7', 'x', 'x');
		INSERT INTO groups (id, name, created_at, updated_at) VALUES (3, 'g', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var h, g int64
	if err := s.db.QueryRow(`SELECT (SELECT value FROM meta WHERE key='next_host_id'), (SELECT value FROM meta WHERE key='next_group_id')`).Scan(&h, &g); err != nil {
		t.Fatal(err)
	}
	if h != 8 || g != 4 {
		t.Errorf("seeded sequences: host %d group %d, want 8 and 4", h, g)
	}
}
