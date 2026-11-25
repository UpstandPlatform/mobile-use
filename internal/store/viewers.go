package store

import (
	"context"
	"database/sql"
	"mobile-use/internal/wire"
	"slices"
	"time"
)

const ViewerTTL = 10 * time.Second

func (s *Store) VideoTarget(ctx context.Context, actor wire.Actor, id string, now time.Time) (Target, error) {
	return s.viewer(ctx, actor, id, "", now)
}

func (s *Store) OpenViewer(ctx context.Context, actor wire.Actor, id string, now time.Time) (Target, string, error) {
	viewer := wire.NewID()
	target, err := s.viewer(ctx, actor, id, viewer, now)
	return target, viewer, err
}

func (s *Store) viewer(ctx context.Context, actor wire.Actor, id, viewer string, now time.Time) (Target, error) {
	var target Target
	err := s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		initial, err := s.sessionTx(ctx, tx, actor.Workspace, id)
		if err != nil {
			return err
		}
		if !canSeeSession(actor, initial) {
			return wire.ErrNotFound
		}
		device, err := s.deviceLocked(ctx, tx, actor.Workspace, initial.DeviceID, now)
		if err != nil {
			return err
		}
		session, err := s.sessionTx(ctx, tx, actor.Workspace, id)
		if err != nil {
			return err
		}
		owns := actor.Subject == session.Owner && (actor.Role == wire.Owner || actor.CanManage())
		controls := actor.Subject == session.Controller && (actor.Role == wire.Agent || owns || actor.CanManage())
		if !owns && !controls {
			return wire.ErrForbidden
		}
		if !isLive(device, session, now) || session.Paused {
			return wire.ErrExpired
		}
		if !device.LiveVideo || !device.Capabilities[wire.Screen] || !slices.Contains(session.Capabilities, wire.Screen) {
			return wire.ErrUnsupported
		}
		target = Target{actor.Workspace, device.Connector, device.ID, id, session.Generation}
		if viewer == "" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, s.query("DELETE FROM viewers WHERE session_id = ? AND expires_at <= ?"), id, now.UnixMilli()); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, s.query("SELECT COUNT(*) FROM viewers WHERE workspace = ? AND session_id = ? AND expires_at > ?"), actor.Workspace, id, now.UnixMilli()).Scan(&count); err != nil {
			return err
		}
		if count >= 2 {
			return wire.Error("VIEWER_LIMIT", "This phone already has two live viewers.", 429)
		}
		if _, err := tx.ExecContext(ctx, s.query("INSERT INTO viewers(id,workspace,session_id,expires_at) VALUES(?,?,?,?)"), viewer, actor.Workspace, id, now.Add(ViewerTTL).UnixMilli()); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "video_opened", device.ID, id, "", "", now)
	})
	return target, err
}

func (s *Store) RenewViewer(ctx context.Context, target Target, id string, now time.Time) error {
	return s.scoped(ctx, target.Workspace, func(tx *sql.Tx) error {
		d, err := s.deviceLocked(ctx, tx, target.Workspace, target.Device, now)
		if err != nil {
			return err
		}
		session, err := s.sessionTx(ctx, tx, target.Workspace, target.Session)
		if err != nil {
			return err
		}
		if !isLive(d, session, now) || session.Paused || !d.LiveVideo || d.Connector != target.Connector || session.Generation != target.Generation {
			return wire.ErrExpired
		}
		result, err := tx.ExecContext(ctx, s.query("UPDATE viewers SET expires_at = ? WHERE workspace = ? AND session_id = ? AND id = ? AND expires_at > ?"), now.Add(ViewerTTL).UnixMilli(), target.Workspace, target.Session, id, now.UnixMilli())
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

func (s *Store) CloseViewer(ctx context.Context, target Target, id string) {
	_ = s.scoped(ctx, target.Workspace, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.query("DELETE FROM viewers WHERE workspace = ? AND session_id = ? AND id = ?"), target.Workspace, target.Session, id)
		return err
	})
}
