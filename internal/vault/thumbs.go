package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"3d-vault/internal/manifest"
	"3d-vault/internal/store"
	"3d-vault/internal/thumbs"
)

// thumbTimeout acota una generación en segundo plano. Solo se lee el principio
// del archivo, así que basta de sobra.
const thumbTimeout = 2 * time.Minute

// Thumbnail devuelve la miniatura PNG de una versión de un .blend. Si falta, por
// ejemplo en versiones anteriores a esta función, la genera en el momento.
func (s *Service) Thumbnail(ctx context.Context, versionID int64) ([]byte, error) {
	noThumb := notFound("La miniatura")
	if s.thumbs == nil {
		return nil, noThumb
	}
	file, err := s.GetVersionFile(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if !isBlend(file.Path) {
		return nil, noThumb
	}
	data, err := s.thumbs.Get(file.Version.FileSHA256)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = s.generateThumbnail(ctx, file.Version.FileSHA256, file.Manifest)
	}
	if errors.Is(err, thumbs.ErrNoThumbnail) {
		return nil, noThumb
	}
	return data, err
}

// HasThumbnail indica si una versión de un .blend tiene miniatura. Si aún no se
// sabe, la genera; solo entonces lee el manifiesto, que en un archivo grande
// tiene miles de trozos.
func (s *Service) HasThumbnail(ctx context.Context, v Version) (bool, error) {
	if s.thumbs == nil {
		return false, nil
	}
	_, err := s.thumbs.Get(v.FileSHA256)
	if errors.Is(err, fs.ErrNotExist) {
		var m manifest.Manifest
		err = s.db.ReadTx(ctx, func(tx *sql.Tx) error {
			var err error
			m, err = loadManifest(ctx, tx, v.manifestID)
			return err
		})
		if err == nil {
			_, err = s.generateThumbnail(ctx, v.FileSHA256, m)
		}
	}
	if errors.Is(err, thumbs.ErrNoThumbnail) {
		return false, nil
	}
	return err == nil, err
}

// WaitBackground espera a que terminen las miniaturas en curso. Se llama al
// detener el servidor, antes de cerrar el almacén.
func (s *Service) WaitBackground() {
	s.background.Wait()
}

func isBlend(p string) bool {
	return strings.EqualFold(path.Ext(p), ".blend")
}

// thumbnailLater genera en segundo plano la miniatura de una versión recién
// confirmada, sin hacer esperar a quien la subió.
func (s *Service) thumbnailLater(asset Asset, v Version) {
	if s.thumbs == nil || !isBlend(asset.Path) || s.thumbs.Known(v.FileSHA256) {
		return
	}
	s.background.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), thumbTimeout)
		defer cancel()
		var m manifest.Manifest
		err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
			var err error
			m, err = loadManifest(ctx, tx, v.manifestID)
			return err
		})
		if err == nil {
			// Sin registro: si falla, se reintenta al pedirla y allí se registra.
			_, _ = s.generateThumbnail(ctx, v.FileSHA256, m)
		}
	})
}

// generateThumbnail reconstruye solo el principio del archivo y guarda el
// resultado, también cuando no hay miniatura. Los errores de lectura no se
// guardan: el siguiente intento puede tener éxito.
func (s *Service) generateThumbnail(ctx context.Context, sha string, m manifest.Manifest) ([]byte, error) {
	select {
	case s.thumbSem <- struct{}{}:
		defer func() { <-s.thumbSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	reader := &chunkReader{ctx: ctx, store: s.store, chunks: m.Chunks}
	data, err := thumbs.BlendPNG(reader)
	if reader.err != nil {
		// Un fallo al leer los trozos no dice nada del contenido: no se guarda.
		return nil, fmt.Errorf("generar miniatura: %w", reader.err)
	}
	if errors.Is(err, thumbs.ErrInvalid) {
		err = thumbs.ErrNoThumbnail
	}
	if err != nil && !errors.Is(err, thumbs.ErrNoThumbnail) {
		return nil, fmt.Errorf("generar miniatura: %w", err)
	}
	if putErr := s.thumbs.Put(sha, data); putErr != nil {
		return nil, putErr
	}
	return data, err
}

// chunkReader lee un archivo trozo a trozo, solo hasta donde se pida.
type chunkReader struct {
	ctx    context.Context
	store  *store.Store
	chunks []manifest.Chunk
	buf    []byte
	err    error
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if len(r.chunks) == 0 {
			return 0, io.EOF
		}
		err := r.ctx.Err()
		var data []byte
		if err == nil {
			data, err = r.store.Get(r.chunks[0].SHA256)
		}
		if err != nil {
			r.err = fmt.Errorf("leer trozo: %w", err)
			return 0, r.err
		}
		r.buf, r.chunks = data, r.chunks[1:]
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
