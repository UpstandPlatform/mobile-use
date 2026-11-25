package store

import (
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"mobile-use/internal/wire"
)

func TestPostgresWorkspacePolicy(t *testing.T) {
	url := os.Getenv("MOBILE_USE_TEST_POSTGRES")
	if url == "" {
		t.Skip("set MOBILE_USE_TEST_POSTGRES to exercise Postgres row policies")
	}
	ctx := t.Context()
	db, err := Open(ctx, "", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.db.SetMaxOpenConns(1)
	one, two := "rls-one-"+wire.NewID(), "rls-two-"+wire.NewID()
	items := []wire.Inventory{{ID: wire.NewID(), Label: "Isolated device", Mode: "adb", Platform: "android", Capabilities: wire.Capabilities{wire.Screen: true}}}
	if err := db.Register(ctx, one, "relay", items, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertCount := func(tx *sql.Tx, want int) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE id=$1", items[0].ID).Scan(&count); err != nil {
			return err
		}
		if count != want {
			t.Errorf("visible row count=%d want=%d", count, want)
		}
		return nil
	}
	if err := db.scoped(ctx, one, func(tx *sql.Tx) error { return assertCount(tx, 1) }); err != nil {
		t.Fatal(err)
	}
	if err := db.scoped(ctx, two, func(tx *sql.Tx) error {
		if err := assertCount(tx, 0); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE devices SET label='unexpected' WHERE id=$1", items[0].ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 0 {
			t.Error("cross-workspace update was permitted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.transaction(ctx, func(tx *sql.Tx) error { return assertCount(tx, 0) }); err != nil {
		t.Fatal(err)
	}
	if err := db.scoped(ctx, "", func(tx *sql.Tx) error { return assertCount(tx, 0) }); !errors.Is(err, wire.ErrForbidden) {
		t.Fatalf("missing workspace accepted: %v", err)
	}
}
