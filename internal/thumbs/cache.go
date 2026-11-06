package thumbs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"3d-vault/internal/chunker"
)

// Cache guarda las miniaturas en disco, una por contenido: <dir>/<sha256>.png.
// Un archivo vacío recuerda que ese contenido no tiene miniatura, para no volver
// a leerlo en cada petición. Es una caché: borrar la carpeta no pierde nada.
type Cache struct {
	dir string
}

// NewCache abre la caché en dir y la crea si no existe.
func NewCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("crear la carpeta de miniaturas: %w", err)
	}
	return &Cache{dir: dir}, nil
}

// path solo acepta hashes: nunca se construye una ruta con texto del usuario.
func (c *Cache) path(sha string) (string, error) {
	h, err := chunker.ParseHash(sha)
	if err != nil {
		return "", fmt.Errorf("hash de miniatura no válido: %w", err)
	}
	return filepath.Join(c.dir, h.String()+".png"), nil
}

// Get devuelve la miniatura guardada. Si falta, el error cumple
// errors.Is(err, fs.ErrNotExist); si se sabe que no hay, es ErrNoThumbnail.
func (c *Cache) Get(sha string) ([]byte, error) {
	name, err := c.path(sha)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, ErrNoThumbnail
	}
	return data, nil
}

// Known indica si ya se intentó generar: hay miniatura o se sabe que no hay.
func (c *Cache) Known(sha string) bool {
	name, err := c.path(sha)
	if err != nil {
		return false
	}
	_, err = os.Stat(name)
	return !errors.Is(err, fs.ErrNotExist)
}

// Put guarda la miniatura de forma atómica: temporal, fsync y rename. Con data
// vacío, recuerda que no hay. Sin fsync de la carpeta: si un corte de luz pierde
// el nombre, la miniatura se vuelve a generar.
func (c *Cache) Put(sha string, data []byte) (err error) {
	name, err := c.path(sha)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("crear temporal: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("escribir miniatura: %w", err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		return fmt.Errorf("permisos de la miniatura: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sincronizar miniatura: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cerrar miniatura: %w", err)
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		return fmt.Errorf("mover miniatura: %w", err)
	}
	return nil
}
