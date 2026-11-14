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
	g := inventory.Group{ID: 1, Name: "net", Description: "d", Note: "n", CIDR: netip.MustParsePrefix("10.0.0.0/24"), ViewID: 1,
		Collapsed: true, X: 1, Y: 2, CreatedAt: now, UpdatedAt: now}
	g2 := inventory.Group{ID: 2, Name: "plain", ViewID: 1, ParentID: 1, CreatedAt: now, UpdatedAt: now}
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

// TC-127: migration 3 upgrades a schema-2 database: view 1 exists, every
// group is in it, hosts keep parent and position, sequences are intact.
func TestMigration3(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:2]); err != nil {
		t.Fatal(err)
	}
	ts := "2026-11-01T10:00:00Z"
	if _, err := db.Exec(`INSERT INTO groups (id, name, cidr, parent_id, x, y, created_at, updated_at) VALUES
			(3, 'net', '10.0.0.0/24', 0, 5, 6, ?1, ?1), (4, 'sub', NULL, 3, 7, 8, ?1, ?1);
		INSERT INTO hosts (id, ip, parent_id, x, y, created_at, updated_at) VALUES (9, '10.0.0.9', 4, 11, 12, ?1, ?1);
		UPDATE meta SET value = 20 WHERE key = 'next_host_id'`, ts); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Views) != 1 || st.Views[0].ID != 1 || st.Views[0].Kind != "network" || st.Views[0].Name != "Networks" ||
		st.Views[0].CreatedAt.IsZero() {
		t.Fatalf("views: %+v", st.Views)
	}
	if len(st.Groups) != 2 || st.Groups[0].ViewID != 1 || st.Groups[1].ViewID != 1 || st.Groups[1].ParentID != 3 ||
		st.Groups[0].X != 5 || st.Groups[0].CIDR.String() != "10.0.0.0/24" {
		t.Fatalf("groups: %+v", st.Groups)
	}
	if len(st.Hosts) != 1 || st.Hosts[0].ParentID != 4 || st.Hosts[0].X != 11 || st.Hosts[0].Y != 12 {
		t.Fatalf("hosts: %+v", st.Hosts)
	}
	if st.NextHostID != 20 || st.NextGroupID != 1 || st.NextViewID != 2 || len(st.Placements) != 0 {
		t.Fatalf("sequences %d %d %d, placements %v", st.NextHostID, st.NextGroupID, st.NextViewID, st.Placements)
	}
}

// Views and placements: insert, update, delete, sequence.
func TestRoundTripViews(t *testing.T) {
	s, err := Open(ctx, filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	v := inventory.View{ID: 2, Name: "Payroll", Kind: "custom", Order: 5, CreatedAt: now, UpdatedAt: now}
	g := inventory.Group{ID: 1, Name: "app", ViewID: 2, CreatedAt: now, UpdatedAt: now}
	h := inventory.Host{ID: 1, IP: netip.MustParseAddr("192.0.2.1"), Status: "unknown", CreatedAt: now, UpdatedAt: now}
	pl := inventory.Placement{ViewID: 2, HostID: 1, ParentID: 1, X: 3, Y: 4}
	if err := s.Apply(ctx, inventory.Changes{Views: []inventory.View{v}, Groups: []inventory.Group{g}, Hosts: []inventory.Host{h},
		Placements: []inventory.Placement{pl}, NextViewID: 3}); err != nil {
		t.Fatal(err)
	}
	v.Name, pl.X = "Pay", 9
	if err := s.Apply(ctx, inventory.Changes{Views: []inventory.View{v}, Placements: []inventory.Placement{pl}}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Views) != 2 || st.Views[1] != v || len(st.Placements) != 1 || st.Placements[0] != pl ||
		st.Groups[0].ViewID != 2 || st.NextViewID != 3 {
		t.Fatalf("state: %+v", st)
	}
	if err := s.Apply(ctx, inventory.Changes{DeleteViews: []int64{2}, DeleteGroups: []int64{1},
		DeletePlacements: []inventory.PlacementKey{{ViewID: 2, HostID: 1}}}); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Load(ctx)
	if len(st.Views) != 1 || len(st.Groups) != 0 || len(st.Placements) != 0 || len(st.Hosts) != 1 {
		t.Fatalf("after delete: %+v", st)
	}
	// Names are unique in the database too.
	dup := inventory.View{ID: 5, Name: "Networks", Kind: "custom", CreatedAt: now, UpdatedAt: now}
	if err := s.Apply(ctx, inventory.Changes{Views: []inventory.View{dup}}); err == nil {
		t.Fatal("duplicate view name accepted")
	}
}

// TC-728 (store part): migration 4 upgrades a schema-3 database: every
// group gets the label mode "" (as the parent); a mode is saved and read.
func TestMigration4(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v3.db")
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:3]); err != nil {
		t.Fatal(err)
	}
	ts := "2026-11-01T10:00:00Z"
	if _, err := db.Exec(`INSERT INTO groups (id, name, parent_id, x, y, created_at, updated_at) VALUES (3, 'lan', 0, 5, 6, ?1, ?1)`, ts); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Load(ctx)
	if err != nil || len(st.Groups) != 1 || st.Groups[0].Labels != "" || st.Groups[0].Name != "lan" {
		t.Fatalf("groups after migration: %+v %v", st.Groups, err)
	}
	g := st.Groups[0]
	g.Labels = "both"
	if err := s.Apply(ctx, inventory.Changes{Groups: []inventory.Group{g}}); err != nil {
		t.Fatal(err)
	}
	if st, err = s.Load(ctx); err != nil || st.Groups[0].Labels != "both" {
		t.Fatalf("saved mode: %+v %v", st.Groups, err)
	}
}

// TC-735 (store part): migration 5 upgrades a schema-4 database: every
// group gets no bar colour; a colour is saved and read (SPEC-727).
func TestMigration5(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v4.db")
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:4]); err != nil {
		t.Fatal(err)
	}
	ts := "2026-11-01T10:00:00Z"
	if _, err := db.Exec(`INSERT INTO groups (id, name, parent_id, labels, x, y, created_at, updated_at) VALUES (3, 'lan', 0, 'ip', 5, 6, ?1, ?1)`, ts); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Load(ctx)
	if err != nil || len(st.Groups) != 1 || st.Groups[0].Color != "" || st.Groups[0].Labels != "ip" {
		t.Fatalf("groups after migration: %+v %v", st.Groups, err)
	}
	g := st.Groups[0]
	g.Color = "#3CB44B"
	if err := s.Apply(ctx, inventory.Changes{Groups: []inventory.Group{g}}); err != nil {
		t.Fatal(err)
	}
	if st, err = s.Load(ctx); err != nil || st.Groups[0].Color != "#3CB44B" {
		t.Fatalf("saved colour: %+v %v", st.Groups, err)
	}
}
