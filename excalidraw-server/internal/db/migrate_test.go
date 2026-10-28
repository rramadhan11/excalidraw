package db

// Runs the workspaces migration (00002) against data written under the
// original schema (00001), the way it will run on an existing install.
// Needs TEST_DATABASE_URL; uses its own scratch database next to it.

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

const migrateTestDB = "excalidraw_migrate_test"

func scratchDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	for _, stmt := range []string{
		"DROP DATABASE IF EXISTS " + migrateTestDB + " WITH (FORCE)",
		"CREATE DATABASE " + migrateTestDB,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + migrateTestDB
	return u.String()
}

func TestWorkspacesMigrationBackfill(t *testing.T) {
	dbURL := scratchDatabase(t)
	ctx := context.Background()
	sqlDB, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	provider, err := newProvider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}

	// data as written by the first release
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := sqlDB.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	mustExec(`INSERT INTO users (id, email, name, password_hash) VALUES
		('aaaaaaaa-0000-4000-8000-000000000001', 'alice@example.com', 'Alice', 'x'),
		('aaaaaaaa-0000-4000-8000-000000000002', 'bob@example.com', 'Bob', 'x'),
		('aaaaaaaa-0000-4000-8000-000000000003', 'carol@example.com', 'Carol', 'x')`)
	mustExec(`INSERT INTO boards (id, owner_id, name, scene, version, updated_at) VALUES
		('bbbbbbbb-0000-4000-8000-000000000001', 'aaaaaaaa-0000-4000-8000-000000000001', 'A1', '{"elements":[{"id":"x"}],"appState":{}}', 7, '2026-01-02'),
		('bbbbbbbb-0000-4000-8000-000000000002', 'aaaaaaaa-0000-4000-8000-000000000001', 'A2', '{"elements":[],"appState":{}}', 1, '2026-01-03'),
		('bbbbbbbb-0000-4000-8000-000000000003', 'aaaaaaaa-0000-4000-8000-000000000002', 'B1', '{"elements":[],"appState":{}}', 3, '2026-01-04')`)
	mustExec(`INSERT INTO board_files (board_id, file_id, mime_type, data_url, file_created)
		VALUES ('bbbbbbbb-0000-4000-8000-000000000001', 'f1', 'image/png', 'data:image/png;base64,AAAA', 1)`)

	if _, err := provider.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}

	// every user, even one without boards, owns exactly one workspace
	rows, err := sqlDB.QueryContext(ctx,
		`SELECT u.name, count(m.workspace_id), min(w.name), bool_and(m.role = 'owner')
		   FROM users u
		   LEFT JOIN workspace_members m ON m.user_id = u.id
		   LEFT JOIN workspaces w ON w.id = m.workspace_id
		  GROUP BY u.name ORDER BY u.name`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Alice": "Alice's workspace", "Bob": "Bob's workspace", "Carol": "Carol's workspace"}
	for rows.Next() {
		var name, wsName string
		var count int
		var owner bool
		if err := rows.Scan(&name, &count, &wsName, &owner); err != nil {
			t.Fatal(err)
		}
		if count != 1 || wsName != want[name] || !owner {
			t.Errorf("%s: %d workspaces, name %q, owner %v", name, count, wsName, owner)
		}
	}
	rows.Close()

	// boards land in their creator's workspace, untouched otherwise
	type boardRow struct {
		creator, wsOwner, scene string
		version                 int
		collection              sql.NullString
	}
	got := map[string]boardRow{}
	rows, err = sqlDB.QueryContext(ctx,
		`SELECT b.name, b.created_by, m.user_id, b.scene::text, b.version, b.collection_id
		   FROM boards b JOIN workspace_members m ON m.workspace_id = b.workspace_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		var r boardRow
		if err := rows.Scan(&name, &r.creator, &r.wsOwner, &r.scene, &r.version, &r.collection); err != nil {
			t.Fatal(err)
		}
		got[name] = r
	}
	rows.Close()
	if len(got) != 3 {
		t.Fatalf("boards after migration: %+v", got)
	}
	for name, r := range got {
		if r.creator != r.wsOwner || r.collection.Valid {
			t.Errorf("%s: creator %s, workspace owner %s, collection %v", name, r.creator, r.wsOwner, r.collection)
		}
	}
	if got["A1"].version != 7 || got["A1"].scene != `{"elements":[{"id":"x"}],"appState":{}}` {
		t.Errorf("A1 content changed: %+v", got["A1"])
	}
	var files int
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM board_files`).Scan(&files); err != nil || files != 1 {
		t.Errorf("files after migration: %d %v", files, err)
	}

	// Down restores the original ownership; Up again is consistent
	if _, err := provider.DownTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var owned int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM boards WHERE owner_id = 'aaaaaaaa-0000-4000-8000-000000000001'`).Scan(&owned); err != nil || owned != 2 {
		t.Fatalf("alice's boards after down: %d %v", owned, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
