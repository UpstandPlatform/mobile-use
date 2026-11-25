package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"mobile-use/internal/wire"
)

func (s *Store) ClaimRoute(ctx context.Context, workspace, connector, connectionID, relay string, now time.Time) error {
	return s.scoped(ctx, workspace, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.query(`INSERT INTO connector_routes(workspace, connector, connection_id, relay_url, deadline)
		VALUES(?,?,?,?,?) ON CONFLICT(workspace, connector) DO UPDATE SET connection_id = excluded.connection_id, relay_url = excluded.relay_url, deadline = excluded.deadline
		WHERE connector_routes.deadline <= ?`), workspace, connector, connectionID, relay, now.Add(ConnectorTTL).UnixMilli(), now.UnixMilli())
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return wire.Error("CONNECTOR_IN_USE", "The connector identity already has a live connection.", 409)
		}
		rows, err := tx.QueryContext(ctx, s.query(`SELECT sessions.payload FROM sessions JOIN devices ON devices.id = sessions.device
			WHERE sessions.workspace = ? AND devices.connector = ? AND sessions.state = ?`), workspace, connector, wire.Active)
		if err != nil {
			return err
		}
		var prior []wire.Session
		for rows.Next() {
			session, err := decodeSession(rows)
			if err != nil {
				rows.Close()
				return err
			}
			prior = append(prior, session)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, session := range prior {
			if err := s.closeLocked(ctx, tx, workspace, &session, wire.Expired, "connector_replaced"); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, s.query("UPDATE devices SET online_until = ? WHERE workspace = ? AND connector = ?"), now.UnixMilli(), workspace, connector)
		return err
	})
}

func (s *Store) RenewRoute(ctx context.Context, workspace, connector, connectionID string, now time.Time) error {
	return s.scoped(ctx, workspace, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.query("UPDATE connector_routes SET deadline = ? WHERE workspace = ? AND connector = ? AND connection_id = ? AND deadline > ?"),
			now.Add(ConnectorTTL).UnixMilli(), workspace, connector, connectionID, now.UnixMilli())
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return wire.ErrExpired
		}
		return nil
	})
}

func (s *Store) Route(ctx context.Context, workspace, connector string, now time.Time) (string, error) {
	var address string
	err := s.scoped(ctx, workspace, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, s.query("SELECT relay_url FROM connector_routes WHERE workspace = ? AND connector = ? AND deadline > ?"), workspace, connector, now.UnixMilli()).Scan(&address)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", wire.ErrOffline
	}
	return address, err
}

func (s *Store) DropRoute(ctx context.Context, workspace, connector, connectionID string, now time.Time) error {
	return s.scoped(ctx, workspace, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.query("DELETE FROM connector_routes WHERE workspace = ? AND connector = ? AND connection_id = ?"), workspace, connector, connectionID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, s.query("UPDATE devices SET online_until = ? WHERE workspace = ? AND connector = ?"), now.UnixMilli(), workspace, connector)
		return err
	})
}
