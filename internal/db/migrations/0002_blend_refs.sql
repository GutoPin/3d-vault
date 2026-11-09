-- Archivos externos que usa cada .blend: bibliotecas enlazadas, texturas,
-- sonidos… Van por contenido (file_sha256), así que una restauración o el mismo
-- archivo en dos rutas no se vuelven a analizar.

CREATE TABLE blend_scans (
    file_sha256 TEXT    PRIMARY KEY,
    scanned_at  INTEGER NOT NULL,
    -- Vacío si se pudo leer. Si no, el motivo; el archivo cuenta como sin enlaces.
    error       TEXT    NOT NULL DEFAULT ''
) STRICT, WITHOUT ROWID;

CREATE TABLE blend_refs (
    file_sha256 TEXT    NOT NULL REFERENCES blend_scans(file_sha256),
    idx         INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    -- La ruta tal como la guarda Blender ("//../maestros/camion.blend").
    raw_path    TEXT    NOT NULL,
    -- 1 si la ruta es una secuencia de imágenes o una textura UDIM.
    pattern     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (file_sha256, idx)
) STRICT, WITHOUT ROWID;
