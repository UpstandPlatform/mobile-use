package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"mobile-use/internal/wire"
)

type Companion struct{ ID, Workspace, Owner, Label string }

func (s *Store) PairCompanion(ctx context.Context, actor wire.Actor, digest string, expiry time.Time) error {
	if !actor.CanManage() && actor.Role != wire.Owner {
		return wire.ErrForbidden
	}
	return s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.query("DELETE FROM companion_pairings WHERE workspace = ? AND expires_at <= ?"), actor.Workspace, time.Now().UnixMilli()); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, s.query("SELECT COUNT(*) FROM companion_pairings WHERE workspace = ? AND owner = ?"), actor.Workspace, actor.Subject).Scan(&count); err != nil {
			return err
		}
		if count >= 5 {
			return wire.Error("PAIRING_LIMIT", "Wait for an existing pairing code to expire.", 429)
		}
		_, err := tx.ExecContext(ctx, s.query("INSERT INTO companion_pairings(digest,workspace,owner,expires_at) VALUES(?,?,?,?)"), digest, actor.Workspace, actor.Subject, expiry.UnixMilli())
		return err
	})
}

func (s *Store) EnrollCompanion(ctx context.Context, pairing, token, label string, now time.Time) (Companion, error) {
	companion := Companion{ID: wire.NewID(), Label: label}
	err := s.systemScope(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, s.lock("SELECT workspace,owner FROM companion_pairings WHERE digest = ? AND expires_at > ?"), pairing, now.UnixMilli())
		if err := row.Scan(&companion.Workspace, &companion.Owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return wire.Error("PAIRING_EXPIRED", "The pairing code is invalid or expired.", 403)
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, s.query("DELETE FROM companion_pairings WHERE digest = ?"), pairing); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.query("INSERT INTO companions(token_digest,device,workspace,owner,label) VALUES(?,?,?,?,?)"), token, companion.ID, companion.Workspace, companion.Owner, label)
		return err
	})
	return companion, err
}

func (s *Store) Companion(ctx context.Context, token string) (Companion, error) {
	var phone Companion
	err := s.systemScope(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, s.query("SELECT device,workspace,owner,label FROM companions WHERE token_digest = ?"), token).Scan(&phone.ID, &phone.Workspace, &phone.Owner, &phone.Label)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return phone, wire.ErrForbidden
	}
	return phone, err
}

func (s *Store) RevokeCompanion(ctx context.Context, actor wire.Actor, id string) error {
	return s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		var owner string
		err := tx.QueryRowContext(ctx, s.lock("SELECT owner FROM companions WHERE workspace = ? AND device = ?"), actor.Workspace, id).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return wire.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !actor.CanManage() && !(actor.Role == wire.Owner && actor.Subject == owner) {
			return wire.ErrForbidden
		}
		if _, err := tx.ExecContext(ctx, s.query("DELETE FROM companions WHERE workspace = ? AND device = ?"), actor.Workspace, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.query("UPDATE devices SET enabled = 0, online_until = 0 WHERE workspace = ? AND id = ?"), actor.Workspace, id)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "companion_revoked", id, "", "", "", time.Now())
	})
}
