-- Borrar un proyecto lo archiva (archived = 1): desaparece de las listas y de
-- Mi carpeta y no admite cambios, pero se puede recuperar. Estas columnas dicen
-- cuándo y quién lo borró.
ALTER TABLE projects ADD COLUMN deleted_at INTEGER;
ALTER TABLE projects ADD COLUMN deleted_by INTEGER REFERENCES users(id);
