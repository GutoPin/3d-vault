-- Esquema inicial, de la sección 7 de PLAN.md. Las fechas son milisegundos Unix.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    display_name  TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    is_active     INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    name         TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
) STRICT;

CREATE INDEX api_tokens_user ON api_tokens(user_id);

CREATE TABLE projects (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    name_key    TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    created_by  INTEGER NOT NULL REFERENCES users(id),
    created_at  INTEGER NOT NULL,
    archived    INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE chunks (
    sha256      TEXT    PRIMARY KEY,
    size        INTEGER NOT NULL,
    stored_size INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE manifests (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_sha256 TEXT    NOT NULL UNIQUE,
    chunk_count INTEGER NOT NULL,
    size_bytes  INTEGER NOT NULL
) STRICT;

CREATE TABLE manifest_chunks (
    manifest_id  INTEGER NOT NULL REFERENCES manifests(id),
    idx          INTEGER NOT NULL,
    chunk_sha256 TEXT    NOT NULL REFERENCES chunks(sha256),
    size         INTEGER NOT NULL,
    PRIMARY KEY (manifest_id, idx)
) STRICT, WITHOUT ROWID;

CREATE INDEX manifest_chunks_chunk ON manifest_chunks(chunk_sha256);

CREATE TABLE assets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    path         TEXT    NOT NULL,
    path_key     TEXT    NOT NULL,
    ext          TEXT    NOT NULL,
    head_version INTEGER NOT NULL DEFAULT 0,
    created_by   INTEGER NOT NULL REFERENCES users(id),
    created_at   INTEGER NOT NULL,
    deleted_at   INTEGER
) STRICT;

CREATE UNIQUE INDEX assets_active_path ON assets(project_id, path_key) WHERE deleted_at IS NULL;
CREATE INDEX assets_project ON assets(project_id);

CREATE TABLE versions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id      INTEGER NOT NULL REFERENCES assets(id),
    number        INTEGER NOT NULL,
    file_sha256   TEXT    NOT NULL,
    size_bytes    INTEGER NOT NULL,
    manifest_id   INTEGER NOT NULL REFERENCES manifests(id),
    comment       TEXT    NOT NULL,
    uploaded_by   INTEGER NOT NULL REFERENCES users(id),
    created_at    INTEGER NOT NULL,
    restored_from INTEGER REFERENCES versions(id) ON DELETE SET NULL,
    UNIQUE (asset_id, number)
) STRICT;

CREATE INDEX versions_manifest ON versions(manifest_id);

CREATE TABLE locks (
    asset_id    INTEGER PRIMARY KEY REFERENCES assets(id),
    user_id     INTEGER NOT NULL REFERENCES users(id),
    locked_at   INTEGER NOT NULL,
    client_id   TEXT    NOT NULL,
    client_name TEXT    NOT NULL
) STRICT;

CREATE TABLE activity (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    action     TEXT    NOT NULL,
    project_id INTEGER REFERENCES projects(id),
    asset_id   INTEGER REFERENCES assets(id),
    version_id INTEGER REFERENCES versions(id),
    detail     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX activity_created ON activity(created_at);
CREATE INDEX activity_asset ON activity(asset_id);

CREATE TABLE events (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    kind         TEXT    NOT NULL,
    asset_id     INTEGER NOT NULL REFERENCES assets(id),
    payload_json TEXT    NOT NULL,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX events_project ON events(project_id, seq);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;
