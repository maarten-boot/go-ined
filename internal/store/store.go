// Package store persists the inventory in SQLite via modernc.org/sqlite
// (pure Go, no cgo) (SPEC-601, SPEC-602).
//
// The inventory Service is the only writer and enforces the tree rules, so
// the schema has no foreign keys; UNIQUE constraints on hosts.ip and
// groups.cidr are kept as a safety net.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"

	"modernc.org/sqlite" // also registers the "sqlite" driver
)

// ErrLocked means another process holds the database (SPEC-604).
var ErrLocked = errors.New("database is in use by another process")

// sqliteBusy is SQLITE_BUSY.
const sqliteBusy = 5

// migrations[i] upgrades the schema from version i to i+1. The schema
// version is kept in PRAGMA user_version. Never edit a released migration;
// append a new one.
var migrations = []string{
	// 1: initial schema.
	`CREATE TABLE groups (
		id          INTEGER PRIMARY KEY,
		name        TEXT    NOT NULL,
		description TEXT    NOT NULL DEFAULT '',
		note        TEXT    NOT NULL DEFAULT '',
		cidr        TEXT    UNIQUE,
		parent_id   INTEGER NOT NULL DEFAULT 0,
		collapsed   INTEGER NOT NULL DEFAULT 1,
		x           REAL    NOT NULL DEFAULT 0,
		y           REAL    NOT NULL DEFAULT 0,
		created_at  TEXT    NOT NULL,
		updated_at  TEXT    NOT NULL
	);
	CREATE TABLE hosts (
		id          INTEGER PRIMARY KEY,
		ip          TEXT    NOT NULL UNIQUE,
		ptr_name    TEXT    NOT NULL DEFAULT '',
		name        TEXT    NOT NULL DEFAULT '',
		description TEXT    NOT NULL DEFAULT '',
		note        TEXT    NOT NULL DEFAULT '',
		parent_id   INTEGER NOT NULL DEFAULT 0,
		monitored   INTEGER NOT NULL DEFAULT 1,
		status      TEXT    NOT NULL DEFAULT 'unknown',
		rtt_ms      REAL,
		loss_pct    REAL,
		last_seen   TEXT,
		last_change TEXT,
		x           REAL    NOT NULL DEFAULT 0,
		y           REAL    NOT NULL DEFAULT 0,
		created_at  TEXT    NOT NULL,
		updated_at  TEXT    NOT NULL
	);
	CREATE INDEX hosts_parent ON hosts(parent_id);
	CREATE INDEX groups_parent ON groups(parent_id);`,

	// 2: id sequences, so ids are never reused (SPEC-116).
	`CREATE TABLE meta (
		key   TEXT    PRIMARY KEY,
		value INTEGER NOT NULL
	);
	INSERT INTO meta (key, value) VALUES
		('next_host_id',  (SELECT COALESCE(MAX(id), 0) + 1 FROM hosts)),
		('next_group_id', (SELECT COALESCE(MAX(id), 0) + 1 FROM groups));`,

	// 3: views and custom-view placements (SPEC-117..123). Hosts keep their
	// network-view placement in hosts.parent_id/x/y; all existing groups
	// go into the network view.
	`CREATE TABLE views (
		id         INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL UNIQUE,
		kind       TEXT    NOT NULL,
		ord        INTEGER NOT NULL DEFAULT 0,
		created_at TEXT    NOT NULL,
		updated_at TEXT    NOT NULL
	);
	INSERT INTO views (id, name, kind, ord, created_at, updated_at) VALUES
		(1, 'Networks', 'network', 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
	ALTER TABLE groups ADD COLUMN view_id INTEGER NOT NULL DEFAULT 1;
	CREATE INDEX groups_view ON groups(view_id);
	CREATE TABLE placements (
		view_id   INTEGER NOT NULL,
		host_id   INTEGER NOT NULL,
		parent_id INTEGER NOT NULL DEFAULT 0,
		x         REAL    NOT NULL DEFAULT 0,
		y         REAL    NOT NULL DEFAULT 0,
		PRIMARY KEY (view_id, host_id)
	);
	CREATE INDEX placements_host ON placements(host_id);
	INSERT INTO meta (key, value) VALUES ('next_view_id', 2);`,
	// 3 → 4: a group's host label mode (SPEC-725).
	`ALTER TABLE groups ADD COLUMN labels TEXT NOT NULL DEFAULT '';`,
	// 4 → 5: the colour of a group's top bar (SPEC-727).
	`ALTER TABLE groups ADD COLUMN color TEXT NOT NULL DEFAULT '';`,
}

// Store is a SQLite-backed inventory.Repository.
type Store struct {
	db *sql.DB
}

var _ inventory.Repository = (*Store)(nil)

// Open opens (creating if needed) the database at path, takes an exclusive
// lock on it (SPEC-604) and migrates it to the latest schema version. If
// another process holds the database, the error wraps ErrLocked.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(abs))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", abs, err)
	}
	// One connection that is never closed while the store is open: it holds
	// the exclusive lock.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)

	// In EXCLUSIVE locking mode the lock taken by this write transaction is
	// kept until the connection closes. busy_timeout is 0, so a second
	// process fails at once instead of waiting.
	if _, err := db.ExecContext(ctx, "BEGIN EXCLUSIVE; COMMIT;"); err != nil {
		db.Close()
		var se *sqlite.Error
		if errors.As(err, &se) && se.Code()&0xff == sqliteBusy {
			return nil, fmt.Errorf("store: %s: %w", abs, ErrLocked)
		}
		return nil, fmt.Errorf("store: open %s: %w", abs, err)
	}
	if err := migrate(ctx, db, migrations); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// dsn builds the driver DSN. The path goes through url.URL so that '?',
// '#' and '%' in file names are escaped (SQLite decodes the URI).
func dsn(absPath string) string {
	u := url.URL{Scheme: "file", Path: absPath}
	return u.String() + "?_pragma=locking_mode(EXCLUSIVE)&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(0)&_pragma=synchronous(NORMAL)"
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database connection (for /health).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// SchemaVersion returns PRAGMA user_version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) { return schemaVersion(ctx, s.db) }

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

func migrate(ctx context.Context, db *sql.DB, steps []string) error {
	v, err := schemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if v > len(steps) {
		return fmt.Errorf("store: database schema version %d is newer than this program (%d)", v, len(steps))
	}
	for i := v; i < len(steps); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, steps[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		// PRAGMA does not take parameters; i+1 is an int we control.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: set schema version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

const tsLayout = time.RFC3339Nano

func fmtTime(t time.Time) string { return t.UTC().Format(tsLayout) }

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return fmtTime(t)
}

func parseTime(s sql.NullString) (time.Time, error) {
	if !s.Valid || s.String == "" {
		return time.Time{}, nil
	}
	return time.Parse(tsLayout, s.String)
}

// Load implements inventory.Repository.
func (s *Store) Load(ctx context.Context) (inventory.State, error) {
	var st inventory.State
	var err error
	if st.Views, err = s.loadViews(ctx); err != nil {
		return st, err
	}
	if st.Groups, err = s.loadGroups(ctx); err != nil {
		return st, err
	}
	if st.Placements, err = s.loadPlacements(ctx); err != nil {
		return st, err
	}
	if st.Hosts, err = s.loadHosts(ctx); err != nil {
		return st, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM meta`)
	if err != nil {
		return st, fmt.Errorf("store: load meta: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return st, fmt.Errorf("store: scan meta: %w", err)
		}
		switch k {
		case "next_host_id":
			st.NextHostID = v
		case "next_group_id":
			st.NextGroupID = v
		case "next_view_id":
			st.NextViewID = v
		}
	}
	return st, rows.Err()
}

func (s *Store) loadViews(ctx context.Context) ([]inventory.View, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, ord, created_at, updated_at FROM views ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: load views: %w", err)
	}
	defer rows.Close()
	var out []inventory.View
	for rows.Next() {
		var v inventory.View
		var created, updated sql.NullString
		if err := rows.Scan(&v.ID, &v.Name, &v.Kind, &v.Order, &created, &updated); err != nil {
			return nil, fmt.Errorf("store: scan view: %w", err)
		}
		if v.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("store: view %d: %w", v.ID, err)
		}
		if v.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, fmt.Errorf("store: view %d: %w", v.ID, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) loadPlacements(ctx context.Context) ([]inventory.Placement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT view_id, host_id, parent_id, x, y FROM placements ORDER BY view_id, host_id`)
	if err != nil {
		return nil, fmt.Errorf("store: load placements: %w", err)
	}
	defer rows.Close()
	var out []inventory.Placement
	for rows.Next() {
		var p inventory.Placement
		if err := rows.Scan(&p.ViewID, &p.HostID, &p.ParentID, &p.X, &p.Y); err != nil {
			return nil, fmt.Errorf("store: scan placement: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) loadGroups(ctx context.Context) ([]inventory.Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, note, cidr, view_id, parent_id, collapsed, labels, color, x, y, created_at, updated_at FROM groups ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: load groups: %w", err)
	}
	defer rows.Close()
	var out []inventory.Group
	for rows.Next() {
		var g inventory.Group
		var cidr, created, updated sql.NullString
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Note, &cidr, &g.ViewID, &g.ParentID, &g.Collapsed, &g.Labels, &g.Color, &g.X, &g.Y, &created, &updated); err != nil {
			return nil, fmt.Errorf("store: scan group: %w", err)
		}
		if cidr.Valid && cidr.String != "" {
			if g.CIDR, err = netip.ParsePrefix(cidr.String); err != nil {
				return nil, fmt.Errorf("store: group %d: bad cidr %q: %w", g.ID, cidr.String, err)
			}
		}
		if g.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("store: group %d: %w", g.ID, err)
		}
		if g.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, fmt.Errorf("store: group %d: %w", g.ID, err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) loadHosts(ctx context.Context) ([]inventory.Host, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ip, ptr_name, name, description, note, parent_id, monitored, status,
		rtt_ms, loss_pct, last_seen, last_change, x, y, created_at, updated_at FROM hosts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: load hosts: %w", err)
	}
	defer rows.Close()
	var out []inventory.Host
	for rows.Next() {
		var h inventory.Host
		var ip string
		var rtt, loss sql.NullFloat64
		var seen, change, created, updated sql.NullString
		if err := rows.Scan(&h.ID, &ip, &h.PTRName, &h.Name, &h.Description, &h.Note, &h.ParentID, &h.Monitored, &h.Status,
			&rtt, &loss, &seen, &change, &h.X, &h.Y, &created, &updated); err != nil {
			return nil, fmt.Errorf("store: scan host: %w", err)
		}
		if h.IP, err = netip.ParseAddr(ip); err != nil {
			return nil, fmt.Errorf("store: host %d: bad ip %q: %w", h.ID, ip, err)
		}
		if rtt.Valid {
			h.RTTms = &rtt.Float64
		}
		if loss.Valid {
			h.LossPct = &loss.Float64
		}
		for _, p := range []struct {
			dst *time.Time
			src sql.NullString
		}{{&h.LastSeen, seen}, {&h.LastChange, change}, {&h.CreatedAt, created}, {&h.UpdatedAt, updated}} {
			if *p.dst, err = parseTime(p.src); err != nil {
				return nil, fmt.Errorf("store: host %d: %w", h.ID, err)
			}
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Apply implements inventory.Repository: all changes in one transaction.
func (s *Store) Apply(ctx context.Context, c inventory.Changes) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	for _, id := range c.DeleteHosts {
		if _, err = tx.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete host %d: %w", id, err)
		}
	}
	for _, id := range c.DeleteGroups {
		if _, err = tx.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete group %d: %w", id, err)
		}
	}
	for _, k := range c.DeletePlacements {
		if _, err = tx.ExecContext(ctx, `DELETE FROM placements WHERE view_id = ? AND host_id = ?`, k.ViewID, k.HostID); err != nil {
			return fmt.Errorf("store: delete placement %d/%d: %w", k.ViewID, k.HostID, err)
		}
	}
	for _, id := range c.DeleteViews {
		if _, err = tx.ExecContext(ctx, `DELETE FROM views WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete view %d: %w", id, err)
		}
	}
	for _, v := range c.Views {
		_, err = tx.ExecContext(ctx, `INSERT INTO views (id, name, kind, ord, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, ord=excluded.ord, updated_at=excluded.updated_at`,
			v.ID, v.Name, v.Kind, v.Order, fmtTime(v.CreatedAt), fmtTime(v.UpdatedAt))
		if err != nil {
			return fmt.Errorf("store: upsert view %d: %w", v.ID, err)
		}
	}
	for _, g := range c.Groups {
		var cidr any
		if g.IsNetwork() {
			cidr = g.CIDR.String()
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO groups (id, name, description, note, cidr, view_id, parent_id, collapsed, labels, color, x, y, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, description=excluded.description, note=excluded.note,
				cidr=excluded.cidr, view_id=excluded.view_id, parent_id=excluded.parent_id, collapsed=excluded.collapsed, labels=excluded.labels, color=excluded.color, x=excluded.x, y=excluded.y,
				updated_at=excluded.updated_at`,
			g.ID, g.Name, g.Description, g.Note, cidr, max(g.ViewID, 1), g.ParentID, g.Collapsed, g.Labels, g.Color, g.X, g.Y, fmtTime(g.CreatedAt), fmtTime(g.UpdatedAt))
		if err != nil {
			return fmt.Errorf("store: upsert group %d: %w", g.ID, err)
		}
	}
	for _, h := range c.Hosts {
		_, err = tx.ExecContext(ctx, `INSERT INTO hosts (id, ip, ptr_name, name, description, note, parent_id, monitored, status,
				rtt_ms, loss_pct, last_seen, last_change, x, y, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET ip=excluded.ip, ptr_name=excluded.ptr_name, name=excluded.name,
				description=excluded.description, note=excluded.note, parent_id=excluded.parent_id, monitored=excluded.monitored,
				status=excluded.status, rtt_ms=excluded.rtt_ms, loss_pct=excluded.loss_pct, last_seen=excluded.last_seen,
				last_change=excluded.last_change, x=excluded.x, y=excluded.y, updated_at=excluded.updated_at`,
			h.ID, h.IP.String(), h.PTRName, h.Name, h.Description, h.Note, h.ParentID, h.Monitored, h.Status,
			h.RTTms, h.LossPct, nullTime(h.LastSeen), nullTime(h.LastChange), h.X, h.Y, fmtTime(h.CreatedAt), fmtTime(h.UpdatedAt))
		if err != nil {
			return fmt.Errorf("store: upsert host %d: %w", h.ID, err)
		}
	}
	for _, p := range c.Placements {
		_, err = tx.ExecContext(ctx, `INSERT INTO placements (view_id, host_id, parent_id, x, y) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(view_id, host_id) DO UPDATE SET parent_id=excluded.parent_id, x=excluded.x, y=excluded.y`,
			p.ViewID, p.HostID, p.ParentID, p.X, p.Y)
		if err != nil {
			return fmt.Errorf("store: upsert placement %d/%d: %w", p.ViewID, p.HostID, err)
		}
	}
	for _, kv := range []struct {
		key string
		val int64
	}{{"next_host_id", c.NextHostID}, {"next_group_id", c.NextGroupID}, {"next_view_id", c.NextViewID}} {
		if kv.val == 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = ?`, kv.val, kv.key); err != nil {
			return fmt.Errorf("store: update %s: %w", kv.key, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
