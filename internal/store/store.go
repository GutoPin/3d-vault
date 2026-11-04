// Package store guarda trozos en disco, direccionados por el hash de su contenido.
//
// Un trozo vive en <raíz>/ab/cd/<hash>. Su primer byte indica el formato del
// resto: datos tal cual o comprimidos con zstd. El prefijo es necesario porque el
// contenido de un .blend comprimido también empieza por la firma de zstd.
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"

	"3d-vault/internal/chunker"
)

const (
	formatRaw  byte = 0
	formatZstd byte = 1

	tmpDirName = "tmp"
	dirPerm    = 0o750
	filePerm   = 0o640

	maxDecodedSize = 2 * chunker.MaxSize
)

var (
	// ErrNotFound indica que el trozo no está en el almacén.
	ErrNotFound = errors.New("trozo no encontrado")
	// ErrCorrupt indica que el contenido guardado no coincide con su hash.
	ErrCorrupt = errors.New("trozo corrupto")
	// ErrHashMismatch indica que los datos recibidos no coinciden con el hash declarado.
	ErrHashMismatch = errors.New("el contenido no coincide con el hash")
)

// Store es seguro para uso concurrente.
type Store struct {
	root string
	enc  *zstd.Encoder
	dec  *zstd.Decoder
}

// New abre el almacén en root y lo crea si no existe.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, tmpDirName), dirPerm); err != nil {
		return nil, fmt.Errorf("crear el almacén de trozos: %w", err)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, fmt.Errorf("preparar la compresión: %w", err)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecodedSize))
	if err != nil {
		return nil, fmt.Errorf("preparar la descompresión: %w", err)
	}
	return &Store{root: root, enc: enc, dec: dec}, nil
}

// Close libera los recursos del compresor.
func (s *Store) Close() error {
	s.dec.Close()
	return s.enc.Close()
}

// CleanTemp borra temporales de escrituras interrumpidas. Solo debe llamarse
// cuando ningún otro proceso escribe en el almacén.
func (s *Store) CleanTemp() error {
	dir := filepath.Join(s.root, tmpDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("leer temporales: %w", err)
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("borrar temporal: %w", err)
		}
	}
	return nil
}

func (s *Store) path(h chunker.Hash) string {
	name := h.String()
	return filepath.Join(s.root, name[:2], name[2:4], name)
}

// Has indica si el trozo está en disco.
func (s *Store) Has(h chunker.Hash) (bool, error) {
	_, err := os.Stat(s.path(h))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("consultar trozo: %w", err)
	}
	return true, nil
}

// Put guarda data si coincide con h y devuelve el tamaño ocupado en disco. Es
// idempotente. Al volver sin error, el trozo ha llegado al disco de forma
// duradera, así que ya se puede registrar en la base de datos.
func (s *Store) Put(h chunker.Hash, data []byte) (int64, error) {
	if chunker.Hash(sha256.Sum256(data)) != h {
		return 0, ErrHashMismatch
	}

	dst := s.path(h)
	if info, err := os.Stat(dst); err == nil {
		return info.Size(), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("consultar trozo: %w", err)
	}

	format, body := s.encode(data)
	if err := s.writeAtomic(dst, format, body); err != nil {
		return 0, err
	}
	return int64(len(body)) + 1, nil
}

// encode comprime solo cuando reduce el tamaño: los datos ya comprimidos se
// guardan tal cual para no gastar CPU al leerlos.
func (s *Store) encode(data []byte) (byte, []byte) {
	compressed := s.enc.EncodeAll(data, make([]byte, 0, len(data)/2))
	if len(compressed) < len(data) {
		return formatZstd, compressed
	}
	return formatRaw, data
}

func (s *Store) writeAtomic(dst string, format byte, body []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, tmpDirName), "trozo-*")
	if err != nil {
		return fmt.Errorf("crear temporal: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err := tmp.Write([]byte{format}); err != nil {
		return fmt.Errorf("escribir trozo: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		return fmt.Errorf("escribir trozo: %w", err)
	}
	if err := tmp.Chmod(filePerm); err != nil {
		return fmt.Errorf("permisos del trozo: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sincronizar trozo: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cerrar trozo: %w", err)
	}

	if err := s.ensureDir(filepath.Dir(dst)); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("mover trozo: %w", err)
	}
	return syncDir(filepath.Dir(dst))
}

// ensureDir crea las carpetas del trozo y hace duradero su registro en la
// carpeta superior, para que un corte de luz no las haga desaparecer.
func (s *Store) ensureDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("crear carpeta de trozos: %w", err)
	}
	for current := dir; current != s.root; current = filepath.Dir(current) {
		if err := syncDir(filepath.Dir(current)); err != nil {
			return err
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("abrir carpeta: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sincronizar carpeta: %w", err)
	}
	return nil
}

// Get devuelve el contenido sin comprimir y verifica su hash.
func (s *Store) Get(h chunker.Hash) ([]byte, error) {
	raw, err := os.ReadFile(s.path(h))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("leer trozo: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: %s vacío", ErrCorrupt, h)
	}

	var data []byte
	switch raw[0] {
	case formatRaw:
		data = raw[1:]
	case formatZstd:
		data, err = s.dec.DecodeAll(raw[1:], nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, h, err)
		}
	default:
		return nil, fmt.Errorf("%w: %s: formato %d desconocido", ErrCorrupt, h, raw[0])
	}

	if chunker.Hash(sha256.Sum256(data)) != h {
		return nil, fmt.Errorf("%w: %s", ErrCorrupt, h)
	}
	return data, nil
}

// Walk llama a fn por cada trozo guardado.
func (s *Store) Walk(ctx context.Context, fn func(chunker.Hash) error) error {
	return s.walkFiles(ctx, func(h chunker.Hash, _ string) error { return fn(h) })
}

func (s *Store) walkFiles(ctx context.Context, fn func(chunker.Hash, string) error) error {
	tmp := filepath.Join(s.root, tmpDirName)
	return filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("recorrer trozos: %w", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if entry.IsDir() {
			if path == tmp {
				return filepath.SkipDir
			}
			return nil
		}
		h, err := chunker.ParseHash(entry.Name())
		if err != nil {
			return fmt.Errorf("archivo inesperado en el almacén %s: %w", path, err)
		}
		return fn(h, path)
	})
}

// CopyStats resume una copia entre almacenes.
type CopyStats struct {
	Copied  int
	Skipped int
}

// CopyMissingTo copia a dst los trozos que aún no tiene, sin borrar nada en el
// destino. Los trozos son inmutables, así que basta con comprobar que existen.
func (s *Store) CopyMissingTo(ctx context.Context, dst *Store) (CopyStats, error) {
	var stats CopyStats
	err := s.walkFiles(ctx, func(h chunker.Hash, path string) error {
		exists, err := dst.Has(h)
		if err != nil {
			return err
		}
		if exists {
			stats.Skipped++
			return nil
		}
		if err := dst.copyFile(path, dst.path(h)); err != nil {
			return err
		}
		stats.Copied++
		return nil
	})
	return stats, err
}

func (s *Store) copyFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("leer trozo: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("%w: %s vacío", ErrCorrupt, src)
	}
	return s.writeAtomic(dst, raw[0], raw[1:])
}
