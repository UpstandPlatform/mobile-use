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
