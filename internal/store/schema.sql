CREATE TABLE IF NOT EXISTS devices (
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
    active_session TEXT NOT NULL DEFAULT '',
    removed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS devices_workspace ON devices(workspace, connector);

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    workspace TEXT NOT NULL,
    device TEXT NOT NULL REFERENCES devices(id),
    payload TEXT NOT NULL,
    state TEXT NOT NULL,
    expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_workspace ON sessions(workspace, state, expires_at);
CREATE INDEX IF NOT EXISTS sessions_device ON sessions(device, state);

CREATE TABLE IF NOT EXISTS operations (
    workspace TEXT NOT NULL,
    id TEXT NOT NULL,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    actor TEXT NOT NULL,
    digest TEXT NOT NULL,
    state TEXT NOT NULL,
    error_code TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (workspace, id)
);
CREATE INDEX IF NOT EXISTS operations_expiry ON operations(updated_at);

CREATE TABLE IF NOT EXISTS audit (
    id TEXT PRIMARY KEY,
    workspace TEXT NOT NULL,
    occurred_at BIGINT NOT NULL,
    payload TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_workspace ON audit(workspace, occurred_at);

CREATE TABLE IF NOT EXISTS viewers (
    id TEXT PRIMARY KEY,
    workspace TEXT NOT NULL,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS viewers_session ON viewers(session_id, expires_at);

CREATE TABLE IF NOT EXISTS connector_routes (
    workspace TEXT NOT NULL,
    connector TEXT NOT NULL,
    connection_id TEXT NOT NULL,
    relay_url TEXT NOT NULL,
    deadline BIGINT NOT NULL,
    PRIMARY KEY (workspace, connector)
);

CREATE TABLE IF NOT EXISTS companion_pairings (
    digest TEXT PRIMARY KEY,
    workspace TEXT NOT NULL,
    owner TEXT NOT NULL,
    expires_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS companions (
    token_digest TEXT PRIMARY KEY,
    device TEXT NOT NULL UNIQUE,
    workspace TEXT NOT NULL,
    owner TEXT NOT NULL,
    label TEXT NOT NULL
);
