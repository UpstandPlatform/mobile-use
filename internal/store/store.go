package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"mobile-use/internal/wire"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

const (
	schemaVersion  = 2
	ConnectorTTL   = 10 * time.Second
	PresenceTTL    = 15 * time.Second
	IdleTimeout    = 5 * time.Minute
	PersonalLimit  = 30 * time.Minute
	DedicatedLimit = 8 * time.Hour
	ApprovalLimit  = 2 * time.Minute
)

type Store struct {
	db       *sql.DB
	postgres bool
}

func Open(ctx context.Context, sqlitePath, postgresDSN string) (*Store, error) {
	driver, dsn := "sqlite", sqlitePath
	if postgresDSN != "" {
		driver, dsn = "pgx", postgresDSN
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, postgres: driver == "pgx"}
	if !store.postgres {
		db.SetMaxOpenConns(1)
	}
	if err := store.requireRowPolicyRole(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	if !s.postgres {
		for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL"} {
			if _, err := s.db.ExecContext(ctx, pragma); err != nil {
				return err
			}
		}
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if s.postgres {
			if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(73654129)"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "SELECT set_config('mobile_use.workspace','',true), set_config('mobile_use.system','true',true)"); err != nil {
				return err
			}
		}
		current, err := s.currentSchemaVersion(ctx, tx)
		if err != nil {
			return err
		}
		if current < 1 {
			for _, statement := range strings.Split(schema, ";") {
				if strings.TrimSpace(statement) == "" {
					continue
				}
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
			if err := s.ensureDeviceRemovedColumn(ctx, tx); err != nil {
				return err
			}
			if s.postgres {
				if err := activateRLS(ctx, tx); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, s.query("INSERT INTO mobile_use_schema_migrations(version) VALUES (?) ON CONFLICT DO NOTHING"), 1); err != nil {
				return err
			}
			current = 1
		}
		if current < schemaVersion {
			if err := s.ensureCompanionPairingColumns(ctx, tx); err != nil {
				return err
			}
			// Pairing codes issued before pairing state was durable cannot be tracked safely after restart.
			if _, err := tx.ExecContext(ctx, "DELETE FROM companion_pairings"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "CREATE UNIQUE INDEX IF NOT EXISTS companion_pairings_id ON companion_pairings(id)"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS companion_pairings_owner_state ON companion_pairings(workspace, owner, state, expires_at)"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.query("INSERT INTO mobile_use_schema_migrations(version) VALUES (?) ON CONFLICT DO NOTHING"), schemaVersion); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) currentSchemaVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var exists bool
	if s.postgres {
		if err := tx.QueryRowContext(ctx, "SELECT to_regclass(current_schema() || '.mobile_use_schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
			return 0, err
		}
	} else if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='mobile_use_schema_migrations')").Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var version int
	err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM mobile_use_schema_migrations").Scan(&version)
	return version, err
}

func (s *Store) ensureDeviceRemovedColumn(ctx context.Context, tx *sql.Tx) error {
	if s.postgres {
		_, err := tx.ExecContext(ctx, "ALTER TABLE devices ADD COLUMN IF NOT EXISTS removed INTEGER NOT NULL DEFAULT 0")
		return err
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(devices)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		if name == "removed" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = tx.ExecContext(ctx, "ALTER TABLE devices ADD COLUMN removed INTEGER NOT NULL DEFAULT 0")
	return err
}

func (s *Store) ensureCompanionPairingColumns(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		name    string
		typeDef string
	}{
		{"id", "TEXT NOT NULL DEFAULT ''"},
		{"creator_role", "TEXT NOT NULL DEFAULT 'owner'"},
		{"auto_enroll", "INTEGER NOT NULL DEFAULT 0"},
		{"grant_agents", "TEXT NOT NULL DEFAULT '[]'"},
		{"state", "TEXT NOT NULL DEFAULT 'waiting'"},
		{"device", "TEXT NOT NULL DEFAULT ''"},
		{"label", "TEXT NOT NULL DEFAULT ''"},
		{"auto_enrolled", "INTEGER NOT NULL DEFAULT 0"},
		{"auto_enroll_error", "TEXT NOT NULL DEFAULT ''"},
	}
	if s.postgres {
		for _, column := range columns {
			statement := "ALTER TABLE companion_pairings ADD COLUMN IF NOT EXISTS " + column.name + " " + column.typeDef
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}

	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(companion_pairings)")
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(columns))
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range columns {
		if found[column.name] {
			continue
		}
		statement := "ALTER TABLE companion_pairings ADD COLUMN " + column.name + " " + column.typeDef
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) query(statement string) string {
	if !s.postgres {
		return statement
	}
	parts := strings.Split(statement, "?")
	var result strings.Builder
	for index, part := range parts {
		if index > 0 {
			result.WriteString("$" + strconv.Itoa(index))
		}
		result.WriteString(part)
	}
	return result.String()
}

func (s *Store) lock(statement string) string {
	if s.postgres {
		statement += " FOR UPDATE"
	}
	return s.query(statement)
}

func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) scoped(ctx context.Context, workspace string, fn func(*sql.Tx) error) error {
	if !wire.ValidID(workspace) {
		return wire.ErrForbidden
	}
	return s.inScope(ctx, workspace, "false", fn)
}

func (s *Store) systemScope(ctx context.Context, fn func(*sql.Tx) error) error {
	return s.inScope(ctx, "", "true", fn)
}

func (s *Store) inScope(ctx context.Context, workspace, system string, fn func(*sql.Tx) error) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if s.postgres {
			if _, err := tx.ExecContext(ctx, "SELECT set_config('mobile_use.workspace',$1,true), set_config('mobile_use.system',$2,true)", workspace, system); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func (s *Store) readRows(ctx context.Context, workspace, query string, args []any, read func(*sql.Rows) error) error {
	return s.scoped(ctx, workspace, func(tx *sql.Tx) error {
		return readRowsTx(ctx, tx, s.query(query), args, read)
	})
}

func readRowsTx(ctx context.Context, tx *sql.Tx, query string, args []any, read func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := read(rows); err != nil {
		return err
	}
	return rows.Err()
}

type deviceRow struct {
	wire.Device
	until      time.Time
	generation int64
	active     string
	removed    bool
}

type scanner interface{ Scan(...any) error }

const deviceCols = "inventory, connector, label, owner, profile, grants, enabled, online_until, generation, active_session, removed"

func readDevice(row scanner, now time.Time) (deviceRow, error) {
	var d deviceRow
	var inventory, grants, label string
	var onlineUntil int64
	var enabled, removed int
	err := row.Scan(&inventory, &d.Connector, &label, &d.Owner, &d.Profile, &grants, &enabled, &onlineUntil, &d.generation, &d.active, &removed)
	if errors.Is(err, sql.ErrNoRows) {
		return d, wire.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal([]byte(inventory), &d.Inventory); err != nil {
		return d, err
	}
	if err := json.Unmarshal([]byte(grants), &d.Grants); err != nil {
		return d, err
	}
	if label != "" {
		d.Label = label
	}
	d.until = time.UnixMilli(onlineUntil)
	d.Online = now.Before(d.until)
	d.Enabled = enabled == 1
	d.removed = removed == 1
	return d, nil
}

func (s *Store) deviceLocked(ctx context.Context, tx *sql.Tx, workspace, id string, now time.Time) (deviceRow, error) {
	row := tx.QueryRowContext(ctx, s.lock("SELECT "+deviceCols+" FROM devices WHERE workspace = ? AND id = ?"), workspace, id)
	return readDevice(row, now)
}

func (s *Store) Register(ctx context.Context, workspace, connector string, items []wire.Inventory, now time.Time) error {
	if len(items) > 100 {
		return wire.Error("INVENTORY_LIMIT", "A connector can report at most 100 devices.", 400)
	}
	return s.scoped(ctx, workspace, func(tx *sql.Tx) error {
		for _, item := range items {
			if !wire.ValidID(item.ID) || len(item.Label) > 128 || len(item.Catalog) > 64 || !slices.Contains([]string{"android", "ios"}, item.Platform) {
				return wire.Error("INVALID_INVENTORY", "The device inventory is invalid.", 400)
			}
			var removed int
			lookupErr := tx.QueryRowContext(ctx, s.query("SELECT removed FROM devices WHERE id = ? AND workspace = ? AND connector = ?"), item.ID, workspace, connector).Scan(&removed)
			if lookupErr == nil && removed == 1 {
				continue
			}
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return lookupErr
			}
			result, err := tx.ExecContext(ctx, s.query(`INSERT INTO devices(id, workspace, connector, inventory, online_until)
				VALUES(?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET inventory = excluded.inventory, online_until = excluded.online_until
				WHERE devices.workspace = excluded.workspace AND devices.connector = excluded.connector`),
				item.ID, workspace, connector, wire.Marshal(item), now.Add(ConnectorTTL).UnixMilli())
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed == 0 {
				return wire.ErrForbidden
			}
		}
		return nil
	})
}

func (s *Store) Devices(ctx context.Context, actor wire.Actor, now time.Time) ([]wire.Device, error) {
	devices := []wire.Device{}
	err := s.readRows(ctx, actor.Workspace, "SELECT "+deviceCols+" FROM devices WHERE workspace = ? AND removed = 0 ORDER BY label, id", []any{actor.Workspace}, func(rows *sql.Rows) error {
		for rows.Next() {
			d, err := readDevice(rows, now)
			if err != nil {
				return err
			}
			if actor.CanManage() || actor.Role == wire.Auditor || d.Owner == actor.Subject && actor.Role == wire.Owner {
				if !actor.CanManage() {
					d.Catalog = nil
				}
				devices = append(devices, d.Device)
				continue
			}
			grant := deviceGrant(d.Device, actor.Subject)
			if actor.Role != wire.Agent || !d.Enabled || grant == nil {
				continue
			}
			d.Grants, d.Connector, d.Owner = nil, "", ""
			if d.Profile != wire.Dedicated || !slices.Contains(grant.Capabilities, wire.AppManagement) {
				d.Catalog = nil
			}
			caps := make(wire.Capabilities, len(grant.Capabilities))
			for _, cap := range grant.Capabilities {
				caps[cap] = d.Capabilities[cap]
			}
			d.Capabilities = caps
			devices = append(devices, d.Device)
		}
		return nil
	})
	return devices, err
}

func deviceGrant(d wire.Device, principal string) *wire.Grant {
	for _, grant := range d.Grants {
		if grant.Subject == principal {
			return &grant
		}
	}
	return nil
}

func (s *Store) Enroll(ctx context.Context, actor wire.Actor, id string, update wire.Enrollment, now time.Time) (wire.Device, Target, error) {
	var result wire.Device
	var stopped Target
	if !actor.CanManage() {
		return result, stopped, wire.ErrForbidden
	}
	if !wire.ValidID(update.Owner) || len(update.Label) > 128 || (update.Profile != wire.Personal && update.Profile != wire.Dedicated) || len(update.Grants) > 100 {
		return result, stopped, wire.Error("INVALID_ENROLLMENT", "Set a valid owner, profile, label and bounded grants.", 400)
	}
	seen := make(map[string]bool)
	for _, grant := range update.Grants {
		if !wire.ValidID(grant.Subject) || seen[grant.Subject] {
			return result, stopped, wire.Error("INVALID_GRANT", "Each grant must have a unique agent identity.", 400)
		}
		if err := wire.ValidateCapabilities(grant.Capabilities); err != nil {
			return result, stopped, err
		}
		seen[grant.Subject] = true
	}
	err := s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		d, err := s.deviceLocked(ctx, tx, actor.Workspace, id, now)
		if err != nil {
			return err
		}
		if d.removed {
			return wire.ErrNotFound
		}
		if d.Mode == "companion" && update.Profile != wire.Personal {
			return wire.Error("INVALID_PROFILE", "Companion-only enrollment requires an attended personal profile.", 400)
		}
		if d.active != "" {
			session, err := s.sessionTx(ctx, tx, actor.Workspace, d.active)
			if err != nil {
				return err
			}
			if err := s.closeLocked(ctx, tx, actor.Workspace, &session, wire.Ended, "enrollment_changed"); err != nil {
				return err
			}
			stopped = Target{actor.Workspace, d.Connector, id, session.ID, session.Generation}
		}
		pending, err := tx.QueryContext(ctx, s.query("SELECT payload FROM sessions WHERE workspace = ? AND device = ? AND state = ?"), actor.Workspace, id, wire.Pending)
		if err != nil {
			return err
		}
		var requests []wire.Session
		for pending.Next() {
			request, err := decodeSession(pending)
			if err != nil {
				pending.Close()
				return err
			}
			requests = append(requests, request)
		}
		if err := pending.Err(); err != nil {
			pending.Close()
			return err
		}
		pending.Close()
		for _, request := range requests {
			if err := s.closeLocked(ctx, tx, actor.Workspace, &request, wire.Ended, "enrollment_changed"); err != nil {
				return err
			}
		}
		enabled := 1
		if update.Enabled != nil && !*update.Enabled {
			enabled = 0
		}
		_, err = tx.ExecContext(ctx, s.query(`UPDATE devices SET owner = ?, label = ?, profile = ?, grants = ?, enabled = ? WHERE id = ? AND workspace = ?`),
			update.Owner, update.Label, update.Profile, wire.Marshal(update.Grants), enabled, id, actor.Workspace)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, actor, "enrollment_updated", id, "", "", "", now); err != nil {
			return err
		}
		d, err = s.deviceLocked(ctx, tx, actor.Workspace, id, now)
		result = d.Device
		return err
	})
	return result, stopped, err
}

func (s *Store) RemoveDevice(ctx context.Context, actor wire.Actor, id string, now time.Time) (Target, error) {
	var target Target
	err := s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		device, err := s.deviceLocked(ctx, tx, actor.Workspace, id, now)
		if err != nil {
			return err
		}
		if !actor.CanManage() && !(actor.Role == wire.Owner && actor.Subject == device.Owner) {
			return wire.ErrForbidden
		}
		if device.removed {
			return wire.ErrNotFound
		}
		target = Target{Workspace: actor.Workspace, Connector: device.Connector, Device: id, Session: device.active, Generation: device.generation}
		if device.active != "" {
			session, err := s.sessionTx(ctx, tx, actor.Workspace, device.active)
			if err != nil {
				return err
			}
			if err := s.closeLocked(ctx, tx, actor.Workspace, &session, wire.Ended, "device_removed"); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, s.query("SELECT payload FROM sessions WHERE workspace = ? AND device = ? AND state = ?"), actor.Workspace, id, wire.Pending)
		if err != nil {
			return err
		}
		var pending []wire.Session
		for rows.Next() {
			session, err := decodeSession(rows)
			if err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, session)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, session := range pending {
			if err := s.closeLocked(ctx, tx, actor.Workspace, &session, wire.Ended, "device_removed"); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.query("DELETE FROM companions WHERE workspace = ? AND device = ?"), actor.Workspace, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.query("UPDATE devices SET enabled = 0, online_until = 0, removed = 1 WHERE workspace = ? AND id = ?"), actor.Workspace, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "device_removed", id, "", "", "removed", now)
	})
	return target, err
}

func (s *Store) sessionTx(ctx context.Context, tx *sql.Tx, workspace, id string) (wire.Session, error) {
	return decodeSession(tx.QueryRowContext(ctx, s.query("SELECT payload FROM sessions WHERE workspace = ? AND id = ?"), workspace, id))
}

func decodeSession(row scanner) (wire.Session, error) {
	var session wire.Session
	var payload string
	err := row.Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return session, wire.ErrNotFound
	}
	if err != nil {
		return session, err
	}
	err = json.Unmarshal([]byte(payload), &session)
	return session, err
}

func saveSessionArgs(workspace string, session wire.Session) []any {
	return []any{wire.Marshal(session), session.State, session.ExpiresAt.UnixMilli(), workspace, session.ID}
}

func (s *Store) saveSession(ctx context.Context, tx *sql.Tx, workspace string, session wire.Session) error {
	_, err := tx.ExecContext(ctx, s.query("UPDATE sessions SET payload = ?, state = ?, expires_at = ? WHERE workspace = ? AND id = ?"), saveSessionArgs(workspace, session)...)
	return err
}

func canSeeSession(actor wire.Actor, session wire.Session) bool {
	if actor.CanManage() || actor.Role == wire.Auditor {
		return true
	}
	return actor.Role == wire.Owner && actor.Subject == session.Owner || actor.Role == wire.Agent && actor.Subject == session.Requester
}

func (s *Store) Session(ctx context.Context, actor wire.Actor, id string) (wire.Session, error) {
	var session wire.Session
	err := s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		var err error
		session, err = s.sessionTx(ctx, tx, actor.Workspace, id)
		return err
	})
	if err != nil {
		return session, err
	}
	if !canSeeSession(actor, session) {
		return wire.Session{}, wire.ErrNotFound
	}
	return session, nil
}

func (s *Store) Sessions(ctx context.Context, actor wire.Actor) ([]wire.Session, error) {
	result := []wire.Session{}
	err := s.readRows(ctx, actor.Workspace, "SELECT payload FROM sessions WHERE workspace = ? AND state IN (?, ?) ORDER BY expires_at", []any{actor.Workspace, wire.Active, wire.Pending}, func(rows *sql.Rows) error {
		for rows.Next() {
			session, err := decodeSession(rows)
			if err != nil {
				return err
			}
			if canSeeSession(actor, session) {
				result = append(result, session)
			}
		}
		return nil
	})
	return result, err
}

func isLive(d deviceRow, session wire.Session, now time.Time) bool {
	return session.State == wire.Active && d.active == session.ID && d.generation == session.Generation && d.Enabled && d.Online &&
		now.Before(session.ExpiresAt) && now.Before(session.LastActivity.Add(IdleTimeout)) &&
		(session.Profile == wire.Dedicated || now.Before(session.OwnerPresentUntil))
}

func (s *Store) reserve(ctx context.Context, tx *sql.Tx, workspace string, d deviceRow, session *wire.Session, now time.Time) error {
	if d.active != "" {
		existing, err := s.sessionTx(ctx, tx, workspace, d.active)
		if err != nil {
			return err
		}
		if isLive(d, existing, now) {
			return wire.ErrBusy
		}
		if err := s.closeLocked(ctx, tx, workspace, &existing, wire.Expired, "lease_expired"); err != nil {
			return err
		}
	}
	if !d.Online {
		return wire.ErrOffline
	}
	if !d.Enabled || !d.Capabilities.Allows(session.Capabilities) {
		return wire.ErrUnsupported
	}
	session.State, session.Generation, session.LastActivity = wire.Active, d.generation+1, now
	session.ExpiresAt = now.Add(DedicatedLimit)
	if session.Profile == wire.Personal {
		session.ExpiresAt, session.OwnerPresentUntil = now.Add(PersonalLimit), now.Add(PresenceTTL)
	}
	_, err := tx.ExecContext(ctx, s.query("UPDATE devices SET generation = ?, active_session = ? WHERE workspace = ? AND id = ?"), session.Generation, session.ID, workspace, d.ID)
	return err
}

func (s *Store) Start(ctx context.Context, actor wire.Actor, req wire.StartSession, now time.Time) (wire.Session, error) {
	var session wire.Session
	if actor.Role != wire.Agent && actor.Role != wire.Owner && !actor.CanManage() {
		return session, wire.ErrForbidden
	}
	if !wire.ValidID(req.DeviceID) || len(strings.TrimSpace(req.Purpose)) < 3 || len(req.Purpose) > 256 {
		return session, wire.Error("INVALID_SESSION", "Set a device and a purpose of 3–256 characters.", 400)
	}
	if err := wire.ValidateCapabilities(req.Capabilities); err != nil {
		return session, err
	}
	err := s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		d, err := s.deviceLocked(ctx, tx, actor.Workspace, req.DeviceID, now)
		if err != nil {
			return err
		}
		if !d.Enabled {
			return wire.ErrNotFound
		}
		if actor.Role == wire.Owner && d.Owner != actor.Subject {
			return wire.ErrNotFound
		}
		if actor.Role == wire.Agent {
			grant := deviceGrant(d.Device, actor.Subject)
			if grant == nil {
				return wire.ErrNotFound
			}
			for _, cap := range req.Capabilities {
				if !slices.Contains(grant.Capabilities, cap) {
					return wire.ErrForbidden
				}
			}
		}
		if !d.Online {
			return wire.ErrOffline
		}
		if !d.Capabilities.Allows(req.Capabilities) {
			return wire.ErrUnsupported
		}
		session = wire.Session{ID: wire.NewID(), DeviceID: d.ID, Requester: actor.Subject, Controller: actor.Subject,
			Owner: d.Owner, Purpose: strings.TrimSpace(req.Purpose), Capabilities: req.Capabilities, Profile: d.Profile,
			State: wire.Pending, CreatedAt: now, ExpiresAt: now.Add(ApprovalLimit), LastActivity: now}
		if d.Profile == wire.Dedicated {
			if err := s.reserve(ctx, tx, actor.Workspace, d, &session, now); err != nil {
				return err
			}
		} else {
			var pending int
			err := tx.QueryRowContext(ctx, s.query("SELECT COUNT(*) FROM sessions WHERE workspace = ? AND device = ? AND state = ? AND expires_at > ?"), actor.Workspace, d.ID, wire.Pending, now.UnixMilli()).Scan(&pending)
			if err != nil {
				return err
			}
			if pending >= 8 {
				return wire.Error("APPROVAL_QUEUE_FULL", "This device has too many pending requests.", 429)
			}
		}
		_, err = tx.ExecContext(ctx, s.query("INSERT INTO sessions(id, workspace, device, payload, state, expires_at) VALUES (?, ?, ?, ?, ?, ?)"),
			session.ID, actor.Workspace, d.ID, wire.Marshal(session), session.State, session.ExpiresAt.UnixMilli())
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "session_requested", d.ID, session.ID, "", session.State, now)
	})
	return session, err
}

func (s *Store) closeLocked(ctx context.Context, tx *sql.Tx, workspace string, session *wire.Session, state, reason string) error {
	if session.State != wire.Active && session.State != wire.Pending {
		return nil
	}
	session.State, session.Reason = state, reason
	_, err := tx.ExecContext(ctx, s.query("UPDATE devices SET active_session = '' WHERE workspace = ? AND id = ? AND active_session = ?"), workspace, session.DeviceID, session.ID)
	if err != nil {
		return err
	}
	return s.saveSession(ctx, tx, workspace, *session)
}

type Target struct {
	Workspace  string `json:"workspace"`
	Connector  string `json:"connector"`
	Device     string `json:"device_id"`
	Session    string `json:"session_id"`
	Generation int64  `json:"generation"`
}

func (s *Store) ChangeSession(ctx context.Context, actor wire.Actor, id, verb string, now time.Time) (wire.Session, Target, error) {
	var session wire.Session
	var target Target
	err := s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		initial, err := s.sessionTx(ctx, tx, actor.Workspace, id)
		if err != nil {
			return err
		}
		if !canSeeSession(actor, initial) {
			return wire.ErrNotFound
		}
		d, err := s.deviceLocked(ctx, tx, actor.Workspace, initial.DeviceID, now)
		if err != nil {
			return err
		}
		session, err = s.sessionTx(ctx, tx, actor.Workspace, id)
		if err != nil {
			return err
		}
		target = Target{actor.Workspace, d.Connector, d.ID, id, session.Generation}
		isOwner := actor.Subject == session.Owner && (actor.Role == wire.Owner || actor.CanManage())
		switch verb {
		case "approve", "deny":
			if !isOwner {
				return wire.ErrForbidden
			}
			if session.State != wire.Pending || !now.Before(session.ExpiresAt) {
				return wire.ErrExpired
			}
			if !d.Enabled || d.Owner != session.Owner || d.Profile != session.Profile {
				return wire.ErrForbidden
			}
			if verb == "deny" {
				if err := s.closeLocked(ctx, tx, actor.Workspace, &session, wire.Denied, "owner_denied"); err != nil {
					return err
				}
			} else {
				if err := s.reserve(ctx, tx, actor.Workspace, d, &session, now); err != nil {
					return err
				}
				if err := s.saveSession(ctx, tx, actor.Workspace, session); err != nil {
					return err
				}
			}
		case "presence", "takeover", "pause", "resume":
			if !isOwner {
				return wire.ErrForbidden
			}
			if !isLive(d, session, now) {
				return wire.ErrExpired
			}
			session.OwnerPresentUntil = now.Add(PresenceTTL)
			if verb == "takeover" && (session.Controller != actor.Subject || session.Paused) || verb == "pause" && !session.Paused || verb == "resume" && session.Paused {
				if verb == "takeover" {
					session.Controller = actor.Subject
				}
				session.Paused, session.Generation = verb == "pause", d.generation+1
				_, err := tx.ExecContext(ctx, s.query("UPDATE devices SET generation = ? WHERE workspace = ? AND id = ?"), session.Generation, actor.Workspace, d.ID)
				if err != nil {
					return err
				}
			}
			if err := s.saveSession(ctx, tx, actor.Workspace, session); err != nil {
				return err
			}
		case "end":
			if !isOwner && !actor.CanManage() && (actor.Role != wire.Agent || actor.Subject != session.Requester) {
				return wire.ErrForbidden
			}
			if err := s.closeLocked(ctx, tx, actor.Workspace, &session, wire.Ended, "stopped"); err != nil {
				return err
			}
		default:
			return wire.ErrNotFound
		}
		if verb == "presence" {
			return nil
		}
		return s.audit(ctx, tx, actor, "session_"+verb, d.ID, session.ID, "", session.State, now)
	})
	return session, target, err
}

type Ticket struct {
	Target   Target
	Dispatch wire.Dispatch
	Previous *wire.Result
}

func (s *Store) BeginOperation(ctx context.Context, actor wire.Actor, id string, op wire.Operation, digest string, now time.Time) (Ticket, error) {
	var ticket Ticket
	required, err := op.Required()
	if err != nil {
		return ticket, err
	}
	budget := ConnectorTTL
	if op.IsAppMaintenance() {
		budget = wire.AppMaintenanceTimeout
	}
	err = s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		initial, err := s.sessionTx(ctx, tx, actor.Workspace, id)
		if err != nil {
			return err
		}
		if !canSeeSession(actor, initial) {
			return wire.ErrNotFound
		}
		d, err := s.deviceLocked(ctx, tx, actor.Workspace, initial.DeviceID, now)
		if err != nil {
			return err
		}
		session, err := s.sessionTx(ctx, tx, actor.Workspace, id)
		if err != nil {
			return err
		}
		if !isLive(d, session, now) {
			return wire.ErrExpired
		}
		if op.IsAppMaintenance() {
			allowed := actor.CanManage() && session.Profile == wire.Dedicated
			if actor.Role == wire.Agent && session.Profile == wire.Dedicated {
				grant := deviceGrant(d.Device, actor.Subject)
				allowed = grant != nil && slices.Contains(grant.Capabilities, wire.AppManagement)
			}
			if !allowed {
				return wire.ErrForbidden
			}
			if !slices.ContainsFunc(d.Catalog, func(item wire.CatalogItem) bool { return item.ID == op.Maintenance.ArtifactID }) {
				return wire.ErrUnsupported
			}
		}
		owns := actor.Subject == session.Owner && (actor.Role == wire.Owner || actor.CanManage())
		controller := actor.Subject == session.Controller && (actor.Role == wire.Agent || owns || actor.CanManage())
		if !controller && !(owns && op.Kind == "observe") {
			return wire.ErrForbidden
		}
		if session.Paused && !(owns && op.Kind == "observe") {
			return wire.Error("SESSION_PAUSED", "The device owner paused this session.", 409)
		}
		if !d.Capabilities.Allows(required) {
			return wire.ErrUnsupported
		}
		for _, cap := range required {
			if !slices.Contains(session.Capabilities, cap) {
				return wire.ErrForbidden
			}
		}
		result, err := tx.ExecContext(ctx, s.query(`INSERT INTO operations(workspace, id, session_id, actor, digest, state, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(workspace, id) DO NOTHING`), actor.Workspace, op.ID, id, actor.Subject, digest, wire.Executing, now.UnixMilli())
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 0 {
			var oldSession, oldActor, oldDigest, state, code string
			var updated int64
			err := tx.QueryRowContext(ctx, s.query("SELECT session_id, actor, digest, state, error_code, updated_at FROM operations WHERE workspace = ? AND id = ?"), actor.Workspace, op.ID).Scan(&oldSession, &oldActor, &oldDigest, &state, &code, &updated)
			if err != nil {
				return err
			}
			if oldSession != id || oldActor != actor.Subject || oldDigest != digest {
				return wire.ErrConflict
			}
			if state == wire.Executing && now.Sub(time.UnixMilli(updated)) > budget {
				state, code = wire.Unknown, wire.ErrUnknown.Code
			}
			ticket.Previous = &wire.Result{ID: op.ID, State: state}
			if code != "" {
				ticket.Previous.Fault = wire.Error(code, "The previous call ended with this code.", 409)
			}
			return nil
		}
		deadline := minTime(now.Add(budget), session.ExpiresAt)
		if session.Profile == wire.Personal {
			deadline = minTime(deadline, session.OwnerPresentUntil)
		}
		if !op.IsAppMaintenance() {
			deadline = minTime(deadline, d.until)
		}
		ticket.Target = Target{actor.Workspace, d.Connector, d.ID, session.ID, session.Generation}
		ticket.Dispatch = wire.Dispatch{DeviceID: d.ID, SessionID: session.ID, Generation: session.Generation, Deadline: deadline, Personal: session.Profile == wire.Personal, Operation: op}
		session.LastActivity = now
		if err := s.saveSession(ctx, tx, actor.Workspace, session); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "operation_admitted", d.ID, id, op.ID, op.Kind, now)
	})
	return ticket, err
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Store) Confirm(ctx context.Context, target Target, now time.Time) error {
	return s.scoped(ctx, target.Workspace, func(tx *sql.Tx) error {
		d, err := s.deviceLocked(ctx, tx, target.Workspace, target.Device, now)
		if err != nil {
			return err
		}
		session, err := s.sessionTx(ctx, tx, target.Workspace, target.Session)
		if err != nil {
			return err
		}
		if !isLive(d, session, now) || d.Connector != target.Connector || session.Generation != target.Generation {
			return wire.ErrExpired
		}
		return nil
	})
}

func (s *Store) FinishOperation(ctx context.Context, actor wire.Actor, target Target, result wire.Result, now time.Time) error {
	code := ""
	if result.Fault != nil {
		code = result.Fault.Code
	}
	return s.scoped(ctx, actor.Workspace, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.query("UPDATE operations SET state = ?, error_code = ?, updated_at = ? WHERE workspace = ? AND id = ? AND actor = ? AND state = ?"),
			result.State, code, now.UnixMilli(), actor.Workspace, result.ID, actor.Subject, wire.Executing)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "operation_"+result.State, target.Device, target.Session, result.ID, code, now)
	})
}

func (s *Store) audit(ctx context.Context, tx *sql.Tx, actor wire.Actor, event, device, session, operation, code string, now time.Time) error {
	entry := wire.AuditEvent{At: now, Actor: actor.Subject, Event: event, Device: device, Session: session, Operation: operation, Code: code}
	_, err := tx.ExecContext(ctx, s.query("INSERT INTO audit(id, workspace, occurred_at, payload) VALUES (?, ?, ?, ?)"), wire.NewID(), actor.Workspace, now.UnixMilli(), wire.Marshal(entry))
	return err
}

func (s *Store) Audit(ctx context.Context, actor wire.Actor, limit int) ([]wire.AuditEvent, error) {
	if actor.Role != wire.Admin && actor.Role != wire.Auditor {
		return nil, wire.ErrForbidden
	}
	result := []wire.AuditEvent{}
	err := s.readRows(ctx, actor.Workspace, "SELECT payload FROM audit WHERE workspace = ? ORDER BY occurred_at DESC LIMIT ?", []any{actor.Workspace, limit}, func(rows *sql.Rows) error {
		for rows.Next() {
			var raw string
			var entry wire.AuditEvent
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &entry); err != nil {
				return err
			}
			result = append(result, entry)
		}
		return nil
	})
	return result, err
}

func (s *Store) Sweep(ctx context.Context, now time.Time) ([]Target, error) {
	type candidate struct {
		workspace string
		session   wire.Session
	}
	var candidates []candidate
	err := s.systemScope(ctx, func(tx *sql.Tx) error {
		return readRowsTx(ctx, tx, s.query("SELECT workspace, payload FROM sessions WHERE state IN (?, ?)"), []any{wire.Active, wire.Pending}, func(rows *sql.Rows) error {
			for rows.Next() {
				var c candidate
				var raw string
				if err := rows.Scan(&c.workspace, &raw); err != nil {
					return err
				}
				if err := json.Unmarshal([]byte(raw), &c.session); err != nil {
					return err
				}
				candidates = append(candidates, c)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	var stopped []Target
	for _, c := range candidates {
		if c.session.State == wire.Pending && now.Before(c.session.ExpiresAt) {
			continue
		}
		err := s.scoped(ctx, c.workspace, func(tx *sql.Tx) error {
			d, err := s.deviceLocked(ctx, tx, c.workspace, c.session.DeviceID, now)
			if err != nil {
				return err
			}
			current, err := s.sessionTx(ctx, tx, c.workspace, c.session.ID)
			if err != nil {
				return err
			}
			if current.State == wire.Active && !isLive(d, current, now) || current.State == wire.Pending && !now.Before(current.ExpiresAt) {
				if err := s.closeLocked(ctx, tx, c.workspace, &current, wire.Expired, "lease_expired"); err != nil {
					return err
				}
				if current.Generation > 0 {
					stopped = append(stopped, Target{c.workspace, d.Connector, d.ID, current.ID, current.Generation})
				}
				return s.audit(ctx, tx, wire.Actor{Workspace: c.workspace, Subject: "system"}, "session_expired", d.ID, current.ID, "", "lease_expired", now)
			}
			return nil
		})
		if err != nil {
			return stopped, err
		}
	}
	return stopped, nil
}

func (s *Store) Prune(ctx context.Context, now time.Time) error {
	return s.systemScope(ctx, func(tx *sql.Tx) error {
		for _, expiry := range []struct {
			query string
			age   time.Duration
		}{
			{"DELETE FROM viewers WHERE expires_at < ?", 0},
			{"DELETE FROM companion_pairings WHERE expires_at < ?", 15 * time.Minute},
			{"DELETE FROM operations WHERE updated_at < ?", 24 * time.Hour},
			{"DELETE FROM audit WHERE occurred_at < ?", 90 * 24 * time.Hour},
			{"DELETE FROM sessions WHERE state NOT IN ('active','pending_approval') AND expires_at < ? AND NOT EXISTS (SELECT 1 FROM operations WHERE operations.session_id = sessions.id)", 90 * 24 * time.Hour},
		} {
			if _, err := tx.ExecContext(ctx, s.query(expiry.query), now.Add(-expiry.age).UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) String() string { return fmt.Sprintf("store(postgres=%t)", s.postgres) }
