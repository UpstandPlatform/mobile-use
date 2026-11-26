package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mobile-use/internal/wire"
)

type Companion struct {
	ID, Workspace, Owner, Label string
	ControlDevice               string
	Pairing                     CompanionPairing
}

type CompanionPairing struct {
	ID, Workspace, Owner, CreatorRole string
	State, DeviceID, Label            string
	AutoEnroll                        bool
	GrantAgents                       []string
	AutoEnrolled                      bool
	AutoEnrollError                   string
	ExpiresAt                         time.Time
}

func (s *Store) PairCompanion(ctx context.Context, actor wire.Actor, id, digest string, expiry time.Time, autoEnroll bool, grantAgents []string) error {
	if !actor.CanManage() && actor.Role != wire.Owner {
		return wire.ErrForbidden
	}
	autoEnrollFlag := 0
	if autoEnroll {
		autoEnrollFlag = 1
	}
	return s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		now := time.Now()
		if _, err := tx.ExecContext(ctx, s.query("DELETE FROM companion_pairings WHERE workspace = ? AND expires_at <= ? AND state = ?"), actor.Workspace, now.UnixMilli(), "waiting"); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, s.query("SELECT COUNT(*) FROM companion_pairings WHERE workspace = ? AND owner = ? AND state = ? AND expires_at > ?"), actor.Workspace, actor.Subject, "waiting", now.UnixMilli()).Scan(&count); err != nil {
			return err
		}
		if count >= 5 {
			return wire.Error("PAIRING_LIMIT", "Wait for an existing pairing code to expire.", 429)
		}
		_, err := tx.ExecContext(ctx, s.query("INSERT INTO companion_pairings(digest,id,workspace,owner,creator_role,expires_at,auto_enroll,grant_agents,state) VALUES(?,?,?,?,?,?,?,?,?)"), digest, id, actor.Workspace, actor.Subject, actor.Role, expiry.UnixMilli(), autoEnrollFlag, wire.Marshal(grantAgents), "waiting")
		return err
	})
}

func (s *Store) CompanionPairing(ctx context.Context, actor wire.Actor, id string, now time.Time) (CompanionPairing, error) {
	var pairing CompanionPairing
	err := s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		var expiresAt int64
		var autoEnroll, autoEnrolled int
		var agentsJSON string
		err := tx.QueryRowContext(ctx, s.query("SELECT id,workspace,owner,creator_role,state,device,label,auto_enroll,grant_agents,auto_enrolled,auto_enroll_error,expires_at FROM companion_pairings WHERE workspace = ? AND id = ?"), actor.Workspace, id).Scan(
			&pairing.ID, &pairing.Workspace, &pairing.Owner, &pairing.CreatorRole, &pairing.State, &pairing.DeviceID,
			&pairing.Label, &autoEnroll, &agentsJSON, &autoEnrolled, &pairing.AutoEnrollError, &expiresAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return wire.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !actor.CanManage() && (actor.Role != wire.Owner || actor.Subject != pairing.Owner) {
			return wire.ErrNotFound
		}
		if err := json.Unmarshal([]byte(agentsJSON), &pairing.GrantAgents); err != nil {
			return err
		}
		pairing.AutoEnroll = autoEnroll != 0
		pairing.AutoEnrolled = autoEnrolled != 0
		pairing.ExpiresAt = time.UnixMilli(expiresAt)
		if pairing.State == "waiting" && !now.Before(pairing.ExpiresAt) {
			pairing.State = "expired"
		}
		return nil
	})
	return pairing, err
}

func (s *Store) CancelCompanionPairing(ctx context.Context, actor wire.Actor, id string, now time.Time) error {
	return s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		var owner, state string
		var expiresAt int64
		err := tx.QueryRowContext(ctx, s.lock("SELECT owner,state,expires_at FROM companion_pairings WHERE workspace = ? AND id = ?"), actor.Workspace, id).Scan(&owner, &state, &expiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return wire.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !actor.CanManage() && (actor.Role != wire.Owner || actor.Subject != owner) {
			return wire.ErrNotFound
		}
		if state == "paired" {
			return wire.Error("PAIRING_COMPLETED", "This phone has already paired. Generate a new code to pair another phone.", 409)
		}
		if state != "waiting" {
			return nil
		}
		next := "cancelled"
		if !now.Before(time.UnixMilli(expiresAt)) {
			next = "expired"
		}
		_, err = tx.ExecContext(ctx, s.query("UPDATE companion_pairings SET state = ? WHERE workspace = ? AND id = ? AND state = ?"), next, actor.Workspace, id, "waiting")
		return err
	})
}

func (s *Store) MarkCompanionAutoEnroll(ctx context.Context, actor wire.Actor, id string, enrolled bool, failure string) error {
	enrolledFlag := 0
	if enrolled {
		enrolledFlag = 1
	}
	if len(failure) > 500 {
		failure = failure[:500]
	}
	return s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		var owner string
		err := tx.QueryRowContext(ctx, s.query("SELECT owner FROM companion_pairings WHERE workspace = ? AND id = ?"), actor.Workspace, id).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return wire.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !actor.CanManage() && (actor.Role != wire.Owner || actor.Subject != owner) {
			return wire.ErrNotFound
		}
		_, err = tx.ExecContext(ctx, s.query("UPDATE companion_pairings SET auto_enrolled = ?, auto_enroll_error = ? WHERE workspace = ? AND id = ? AND state = ?"), enrolledFlag, strings.TrimSpace(failure), actor.Workspace, id, "paired")
		return err
	})
}

func (s *Store) EnrollCompanion(ctx context.Context, digest, token, label string, now time.Time) (Companion, error) {
	companion := Companion{ID: wire.NewID(), Label: label}
	err := s.systemScope(ctx, func(tx *sql.Tx) error {
		var expiresAt int64
		var autoEnroll int
		var agentsJSON string
		err := tx.QueryRowContext(ctx, s.lock("SELECT id,workspace,owner,creator_role,expires_at,auto_enroll,grant_agents FROM companion_pairings WHERE digest = ? AND state = ? AND expires_at > ?"), digest, "waiting", now.UnixMilli()).Scan(
			&companion.Pairing.ID, &companion.Workspace, &companion.Owner, &companion.Pairing.CreatorRole, &expiresAt, &autoEnroll, &agentsJSON,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return wire.Error("PAIRING_EXPIRED", "The pairing code is invalid, cancelled, or expired. Generate a fresh code in the console.", 403)
			}
			return err
		}
		if err := json.Unmarshal([]byte(agentsJSON), &companion.Pairing.GrantAgents); err != nil {
			return err
		}
		companion.Pairing.Workspace = companion.Workspace
		companion.Pairing.Owner = companion.Owner
		companion.Pairing.AutoEnroll = autoEnroll != 0
		companion.Pairing.ExpiresAt = time.UnixMilli(expiresAt)

		result, err := tx.ExecContext(ctx, s.query("UPDATE companion_pairings SET state = ?, device = ?, label = ? WHERE digest = ? AND state = ?"), "paired", companion.ID, label, digest, "waiting")
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return err
		} else if changed != 1 {
			return wire.Error("PAIRING_EXPIRED", "This pairing code has already been used. Generate a fresh code in the console.", 403)
		}
		if _, err := tx.ExecContext(ctx, s.query("INSERT INTO companions(token_digest,device,workspace,owner,label) VALUES(?,?,?,?,?)"), token, companion.ID, companion.Workspace, companion.Owner, label); err != nil {
			return err
		}

		caps := wire.Capabilities{wire.Screen: true, wire.Camera: true, wire.Microphone: true}
		inventory := wire.Inventory{ID: companion.ID, Label: label, Platform: "android", Mode: "companion", Capabilities: caps}
		_, err = tx.ExecContext(ctx, s.query("INSERT INTO devices(id,workspace,connector,inventory,online_until) VALUES(?,?,?,?,?)"), companion.ID, companion.Workspace, "companion."+companion.ID, wire.Marshal(inventory), now.Add(ConnectorTTL).UnixMilli())
		return err
	})
	return companion, err
}

func (s *Store) Companion(ctx context.Context, token string) (Companion, error) {
	var phone Companion
	err := s.systemScope(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, s.query("SELECT device,workspace,owner,label,control_device FROM companions WHERE token_digest = ?"), token).Scan(&phone.ID, &phone.Workspace, &phone.Owner, &phone.Label, &phone.ControlDevice)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return phone, wire.ErrForbidden
	}
	return phone, err
}

func (s *Store) CompanionControlDevice(ctx context.Context, actor wire.Actor, id string) (string, error) {
	var owner, target string
	err := s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, s.query("SELECT owner,control_device FROM companions WHERE workspace = ? AND device = ?"), actor.Workspace, id).Scan(&owner, &target); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return wire.ErrNotFound
			}
			return err
		}
		if !actor.CanManage() && (actor.Role != wire.Owner || actor.Subject != owner) {
			return wire.ErrNotFound
		}
		return nil
	})
	return target, err
}

func (s *Store) SetCompanionControlDevice(ctx context.Context, actor wire.Actor, companionID, targetID string, now time.Time) error {
	return s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		var owner string
		if err := tx.QueryRowContext(ctx, s.query("SELECT owner FROM companions WHERE workspace = ? AND device = ?"), actor.Workspace, companionID).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return wire.ErrNotFound
			}
			return err
		}
		if !actor.CanManage() && (actor.Role != wire.Owner || actor.Subject != owner) {
			return wire.ErrNotFound
		}
		if targetID != "" {
			phone, err := s.deviceLocked(ctx, tx, actor.Workspace, companionID, now)
			if err != nil {
				return err
			}
			target, err := s.deviceLocked(ctx, tx, actor.Workspace, targetID, now)
			if err != nil {
				return err
			}
			if phone.Platform != "android" || phone.Mode != "companion" || target.Platform != "android" || target.Mode != "adb" ||
				target.Owner != owner || phone.Owner != owner || !target.Enabled {
				return wire.Error("INVALID_GESTURE_TARGET", "Choose an enrolled Android ADB device assigned to the same owner as this companion.", 400)
			}
			var linked string
			err = tx.QueryRowContext(ctx, s.query("SELECT device FROM companions WHERE workspace = ? AND control_device = ? AND device <> ?"), actor.Workspace, targetID, companionID).Scan(&linked)
			if err == nil {
				return wire.Error("GESTURE_TARGET_IN_USE", "This ADB device is already linked to another companion.", 409)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.query("UPDATE companions SET control_device = ? WHERE workspace = ? AND device = ?"), targetID, actor.Workspace, companionID); err != nil {
			if strings.Contains(err.Error(), "companions_control_device_unique") || strings.Contains(err.Error(), "companions.control_device") {
				return wire.Error("GESTURE_TARGET_IN_USE", "This ADB device is already linked to another companion.", 409)
			}
			return err
		}
		return s.audit(ctx, tx, actor, "companion_gesture_target_changed", companionID, "", "", targetID, now)
	})
}

func (s *Store) CompanionForControlDevice(ctx context.Context, workspace, targetID string) (string, error) {
	var companionID string
	err := s.scoped(ctx, workspace, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, s.query("SELECT device FROM companions WHERE workspace = ? AND control_device = ?"), workspace, targetID).Scan(&companionID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return companionID, err
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
