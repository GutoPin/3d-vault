-- Qué programa usa cada PC para hablar con 3D-Vault (Mi carpeta en un navegador
-- o el complemento de Blender) y en qué versión. Sirve para ver desde la web
-- quién tiene una versión vieja. Una fila por usuario, PC y programa.
CREATE TABLE clients (
    user_id     INTEGER NOT NULL REFERENCES users(id),
    client_id   TEXT    NOT NULL,
    app         TEXT    NOT NULL,
    client_name TEXT    NOT NULL,
    version     TEXT    NOT NULL,
    browser     TEXT    NOT NULL,
    last_seen   INTEGER NOT NULL,
    PRIMARY KEY (user_id, client_id, app)
) STRICT;
