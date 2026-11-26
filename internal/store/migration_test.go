package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateAddsRemovedFlagToExistingInventory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE devices (
		id TEXT PRIMARY KEY,
		workspace TEXT NOT NULL,
		connector TEXT NOT NULL,
		inventory TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '',
		profile TEXT NOT NULL DEFAULT '',
		grants TEXT NOT NULL DEFAULT '[]',
		enabled INTEGER NOT NULL DEFAULT 0,
		online_until BIGINT NOT NULL,
		generation BIGINT NOT NULL DEFAULT 0,
		active_session TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	opened, err := Open(t.Context(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	rows, err := opened.db.QueryContext(t.Context(), "PRAGMA table_info(devices)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			t.Fatal(err)
		}
		if name == "removed" {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("migration did not add the removed flag")
}

func TestMigratePersistedCompanionPairingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing-v1.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statement := "CREATE TABLE mobile_use_schema_migrations (version INTEGER PRIMARY KEY);" +
		"INSERT INTO mobile_use_schema_migrations(version) VALUES (1);" +
		"CREATE TABLE audit (id TEXT PRIMARY KEY, workspace TEXT NOT NULL, occurred_at BIGINT NOT NULL, payload TEXT NOT NULL);" +
		"CREATE TABLE companions (token_digest TEXT PRIMARY KEY, device TEXT NOT NULL UNIQUE, workspace TEXT NOT NULL, owner TEXT NOT NULL, label TEXT NOT NULL);" +
		"CREATE TABLE companion_pairings (digest TEXT PRIMARY KEY, workspace TEXT NOT NULL, owner TEXT NOT NULL, expires_at BIGINT NOT NULL);" +
		"INSERT INTO companion_pairings(digest,workspace,owner,expires_at) VALUES ('legacy-digest','legacy-workspace','owner',9999999999999);"
	_, err = db.Exec(statement)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	opened, err := Open(t.Context(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	var version int
	if err := opened.db.QueryRowContext(t.Context(), "SELECT MAX(version) FROM mobile_use_schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("schema version = %d, want 4", version)
	}
	var count int
	if err := opened.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM companion_pairings").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("legacy ephemeral pairings = %d, want none after upgrade", count)
	}
	rows, err := opened.db.QueryContext(t.Context(), "PRAGMA table_info(companion_pairings)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"id", "creator_role", "auto_enroll", "grant_agents", "state", "device", "label", "auto_enrolled"} {
		if !found[required] {
			t.Errorf("pairing migration did not add %q", required)
		}
	}
	var controlDeviceColumn bool
	rows, err = opened.db.QueryContext(t.Context(), "PRAGMA table_info(companions)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			t.Fatal(err)
		}
		if name == "control_device" {
			controlDeviceColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !controlDeviceColumn {
		t.Fatal("companion migration did not add the control_device link")
	}
}
