package store

import (
	"context"
	"database/sql"
	"fmt"
)

func activateRLS(ctx context.Context, tx *sql.Tx) error {
	// Table names are fixed schema identifiers. Workspace arguments are bound in scoped transactions.
	for _, table := range []string{"devices", "sessions", "operations", "audit", "viewers", "connector_routes", "companion_pairings", "companions"} {
		var enabled, forced, hasPolicy bool
		err := tx.QueryRowContext(ctx, `SELECT c.relrowsecurity,c.relforcerowsecurity,EXISTS (
			SELECT 1 FROM pg_policies p WHERE p.schemaname=current_schema() AND p.tablename=$1 AND p.policyname='mobile_use_workspace'
		) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relname=$1 AND n.nspname=current_schema()`, table).Scan(&enabled, &forced, &hasPolicy)
		if err != nil {
			return err
		}
		if !hasPolicy {
			condition := `(workspace = NULLIF(current_setting('mobile_use.workspace', true), '') OR current_setting('mobile_use.system', true) = 'true')`
			if _, err := tx.ExecContext(ctx, "CREATE POLICY mobile_use_workspace ON "+table+" USING "+condition+" WITH CHECK "+condition); err != nil {
				return err
			}
		}
		if !enabled {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ENABLE ROW LEVEL SECURITY"); err != nil {
				return err
			}
		}
		if !forced {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" FORCE ROW LEVEL SECURITY"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) requireRowPolicyRole(ctx context.Context) error {
	if !s.postgres {
		return nil
	}
	var bypass bool
	err := s.db.QueryRowContext(ctx, "SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user").Scan(&bypass)
	if err != nil {
		return err
	}
	if bypass {
		return fmt.Errorf("the PostgreSQL service role must have NOSUPERUSER and NOBYPASSRLS")
	}
	return nil
}
