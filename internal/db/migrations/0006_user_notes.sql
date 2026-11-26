-- Cosas que una persona ya hizo o confirmó una vez, por ejemplo «entendí que
-- guardar en la carpeta no es subir» en la puesta al día. Una fila por nota.
CREATE TABLE user_notes (
    user_id INTEGER NOT NULL REFERENCES users(id),
    note    TEXT    NOT NULL,
    at      INTEGER NOT NULL,
    PRIMARY KEY (user_id, note)
) STRICT;
