-- Carpetas creadas a propósito, aunque estén vacías. Las que tienen archivos
-- existen igual por sus rutas; esta tabla solo hace falta para las vacías.

CREATE TABLE folders (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    path       TEXT    NOT NULL,
    path_key   TEXT    NOT NULL,
    created_by INTEGER NOT NULL REFERENCES users(id),
    created_at INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX folders_path ON folders(project_id, path_key);

-- Los eventos de carpetas no tienen archivo: asset_id pasa a ser opcional.
-- SQLite no cambia una columna, así que se reconstruye la tabla conservando los
-- números de evento, de los que dependen los agentes para no perder cambios.
CREATE TABLE events_new (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    kind         TEXT    NOT NULL,
    asset_id     INTEGER REFERENCES assets(id),
    payload_json TEXT    NOT NULL,
    created_at   INTEGER NOT NULL
) STRICT;

INSERT INTO events_new (seq, project_id, kind, asset_id, payload_json, created_at)
    SELECT seq, project_id, kind, asset_id, payload_json, created_at FROM events;

-- El siguiente número sigue al último que se dio, aunque ese evento ya se haya
-- podado.
DELETE FROM sqlite_sequence WHERE name = 'events_new';
INSERT INTO sqlite_sequence (name, seq)
    SELECT 'events_new', MAX(seq) FROM (
        SELECT seq FROM sqlite_sequence WHERE name = 'events'
        UNION ALL SELECT COALESCE(MAX(seq), 0) FROM events_new);

DROP TABLE events;
ALTER TABLE events_new RENAME TO events;
CREATE INDEX events_project ON events(project_id, seq);
