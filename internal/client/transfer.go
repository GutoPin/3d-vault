package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"3d-vault/internal/chunker"
	"3d-vault/internal/manifest"
	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

// maxParallel es el máximo de trozos en tránsito a la vez (sección 8 de PLAN.md).
const maxParallel = 4

// Scan es un archivo local cortado en trozos, con la posición de cada uno.
type Scan struct {
	Manifest manifest.Manifest
	// offsets guarda dónde está cada trozo distinto.
	offsets map[chunker.Hash]int64
	unique  []chunker.Hash
}

// ScanFile corta un archivo y calcula su manifiesto.
func ScanFile(path string) (Scan, error) {
	f, err := os.Open(path)
	if err != nil {
		return Scan{}, fmt.Errorf("abrir el archivo: %w", err)
	}
	defer f.Close()

	fileHash := sha256.New()
	c := chunker.New(io.TeeReader(f, fileHash))
	scan := Scan{offsets: make(map[chunker.Hash]int64)}
	for {
		chunk, err := c.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Scan{}, fmt.Errorf("cortar el archivo: %w", err)
		}
		scan.Manifest.Chunks = append(scan.Manifest.Chunks, manifest.Chunk{SHA256: chunk.Hash, Size: int64(len(chunk.Data))})
		if _, seen := scan.offsets[chunk.Hash]; !seen {
			scan.offsets[chunk.Hash] = chunk.Offset
			scan.unique = append(scan.unique, chunk.Hash)
		}
	}
	copy(scan.Manifest.FileSHA256[:], fileHash.Sum(nil))
	return scan, nil
}

func (s Scan) chunkSizes() map[chunker.Hash]int64 {
	sizes := make(map[chunker.Hash]int64, len(s.unique))
	for _, c := range s.Manifest.Chunks {
		sizes[c.SHA256] = c.Size
	}
	return sizes
}

// PushInput describe una subida.
type PushInput struct {
	ProjectID  int64
	RemotePath string
	LocalPath  string
	Comment    string
	// BaseVersion es la versión sobre la que se trabajó. Con 0 se usa la última:
	// lo adecuado para la herramienta de prueba, pero no para el agente, que debe
	// indicar la versión que tiene descargada.
	BaseVersion int64
	ReleaseLock bool
}

// PushResult resume una subida.
type PushResult struct {
	Commit         vault.CommitResult
	TotalChunks    int
	UploadedChunks int
	TotalBytes     int64
	UploadedBytes  int64
}

// ReusedPercent es la parte del archivo que no hizo falta enviar.
func (r PushResult) ReusedPercent() float64 {
	if r.TotalBytes == 0 {
		return 0
	}
	return float64(r.TotalBytes-r.UploadedBytes) / float64(r.TotalBytes) * 100
}

// Push sube un archivo enviando solo los trozos que faltan. Es reanudable sin
// estado local: si se corta, al reintentar el servidor indica qué falta aún.
func (c *Client) Push(ctx context.Context, in PushInput) (PushResult, error) {
	if _, err := rules.CheckComment(in.Comment); err != nil {
		return PushResult{}, err
	}
	path, err := rules.NormalizePath(in.RemotePath)
	if err != nil {
		return PushResult{}, err
	}
	scan, err := ScanFile(in.LocalPath)
	if err != nil {
		return PushResult{}, err
	}

	result := PushResult{TotalChunks: len(scan.Manifest.Chunks), TotalBytes: scan.Manifest.Size()}
	stats, err := c.UploadChunks(ctx, in.LocalPath, scan, nil)
	result.UploadedChunks, result.UploadedBytes = stats.Chunks, stats.Bytes
	if err != nil {
		return result, err
	}

	asset, found, err := c.findAsset(ctx, in.ProjectID, path)
	if err != nil {
		return result, err
	}
	if !found {
		result.Commit, err = c.CreateAsset(ctx, in.ProjectID, path.Clean, scan.Manifest, in.Comment)
		return result, err
	}
	base := in.BaseVersion
	if base == 0 {
		base = asset.HeadVersion
	}
	result.Commit, err = c.CommitVersion(ctx, CommitInput{
		AssetID:     asset.ID,
		BaseVersion: base,
		Manifest:    scan.Manifest,
		Comment:     in.Comment,
		ReleaseLock: in.ReleaseLock,
	})
	return result, err
}

func (c *Client) findAsset(ctx context.Context, projectID int64, path rules.Path) (vault.Asset, bool, error) {
	snap, err := c.Snapshot(ctx, projectID)
	if err != nil {
		return vault.Asset{}, false, err
	}
	for _, asset := range snap.Assets {
		other, err := rules.NormalizePath(asset.Path)
		if err == nil && other.Key == path.Key {
			return asset, true, nil
		}
	}
	return vault.Asset{}, false, nil
}

// TransferStats resume los trozos que viajaron por la red.
type TransferStats struct {
	Chunks int
	Bytes  int64
}

// UploadChunks sube los trozos que le faltan al servidor. onBytes, si no es nil, se
// llama desde varias goroutines cada vez que termina un trozo.
func (c *Client) UploadChunks(ctx context.Context, localPath string, scan Scan, onBytes func(int64)) (TransferStats, error) {
	missing, err := c.MissingChunks(ctx, scan.unique)
	if err != nil {
		return TransferStats{}, err
	}
	if len(missing) == 0 {
		return TransferStats{}, nil
	}

	f, err := os.Open(localPath)
	if err != nil {
		return TransferStats{}, fmt.Errorf("abrir el archivo: %w", err)
	}
	defer f.Close()

	sizes := scan.chunkSizes()
	var uploadedChunks, uploadedBytes atomic.Int64
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallel)
	for _, h := range missing {
		offset, known := scan.offsets[h]
		if !known {
			return TransferStats{}, fmt.Errorf("el servidor pidió un trozo desconocido: %s", h)
		}
		group.Go(func() error {
			data := make([]byte, sizes[h])
			if _, err := f.ReadAt(data, offset); err != nil {
				return fmt.Errorf("leer el archivo: %w", err)
			}
			// Detecta que el archivo cambió mientras se subía, por ejemplo si alguien
			// guarda desde Blender justo ahora.
			if chunker.Hash(sha256.Sum256(data)) != h {
				return ErrFileChanged
			}
			if err := c.PutChunk(groupCtx, h, data); err != nil {
				return err
			}
			uploadedChunks.Add(1)
			uploadedBytes.Add(int64(len(data)))
			if onBytes != nil {
				onBytes(int64(len(data)))
			}
			return nil
		})
	}
	err = group.Wait()
	return TransferStats{Chunks: int(uploadedChunks.Load()), Bytes: uploadedBytes.Load()}, err
}

// ErrFileChanged indica que el archivo local cambió durante la subida.
var ErrFileChanged = errors.New("el archivo cambió durante la subida: vuelve a intentarlo cuando deje de cambiar")

// PullInput describe una descarga.
type PullInput struct {
	ProjectID  int64
	RemotePath string
	LocalPath  string
	// Overwrite permite reemplazar un archivo local distinto. Sin él, Pull se niega:
	// nunca se pisa trabajo local en silencio.
	Overwrite bool
}

// PullResult resume una descarga.
type PullResult struct {
	Version          vault.Version
	AlreadyUpToDate  bool
	TotalChunks      int
	DownloadedChunks int
	TotalBytes       int64
	DownloadedBytes  int64
}

// ReusedPercent es la parte del archivo que no hizo falta descargar.
func (r PullResult) ReusedPercent() float64 {
	if r.TotalBytes == 0 {
		return 0
	}
	return float64(r.TotalBytes-r.DownloadedBytes) / float64(r.TotalBytes) * 100
}

// ErrLocalFileDiffers indica que ya existe un archivo local con otro contenido.
var ErrLocalFileDiffers = errors.New("ya existe un archivo local distinto: usa la opción de sobrescribir si quieres reemplazarlo")

// Pull descarga la última versión aprovechando los trozos del archivo local. Solo
// reemplaza el archivo tras verificar el resultado completo.
func (c *Client) Pull(ctx context.Context, in PullInput) (PullResult, error) {
	path, err := rules.NormalizePath(in.RemotePath)
	if err != nil {
		return PullResult{}, err
	}
	asset, found, err := c.findAsset(ctx, in.ProjectID, path)
	if err != nil {
		return PullResult{}, err
	}
	if !found {
		return PullResult{}, &rules.Error{Code: rules.CodeNotFound, Message: "El archivo no existe en el servidor."}
	}
	m, err := c.Manifest(ctx, asset.HeadVersionID)
	if err != nil {
		return PullResult{}, err
	}

	result := PullResult{
		Version:     vault.Version{ID: asset.HeadVersionID, AssetID: asset.ID, Number: asset.HeadVersion, FileSHA256: asset.FileSHA256, SizeBytes: asset.SizeBytes},
		TotalChunks: len(m.Chunks),
		TotalBytes:  m.Size(),
	}

	var local *Scan
	if _, statErr := os.Stat(in.LocalPath); statErr == nil {
		scan, err := ScanFile(in.LocalPath)
		if err != nil {
			return result, err
		}
		if scan.Manifest.FileSHA256 == m.FileSHA256 {
			result.AlreadyUpToDate = true
			return result, nil
		}
		if !in.Overwrite {
			return result, ErrLocalFileDiffers
		}
		local = &scan
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return result, fmt.Errorf("consultar el archivo local: %w", statErr)
	}

	if err := os.MkdirAll(filepath.Dir(in.LocalPath), 0o750); err != nil {
		return result, fmt.Errorf("crear la carpeta: %w", err)
	}
	tmpPath, stats, err := c.Assemble(ctx, m, filepath.Dir(in.LocalPath), in.LocalPath, local, nil)
	result.DownloadedChunks, result.DownloadedBytes = stats.Chunks, stats.Bytes
	if err != nil {
		return result, err
	}
	if err := os.Rename(tmpPath, in.LocalPath); err != nil {
		_ = os.Remove(tmpPath)
		return result, fmt.Errorf("reemplazar el archivo: %w", err)
	}
	return result, nil
}

// Assemble reconstruye un archivo en un temporal dentro de tmpDir y verifica su
// hash, sin tocar el destino. Aprovecha los trozos de basePath si base no es nil.
// Devuelve la ruta del temporal: quien llama decide cómo y cuándo reemplazar.
func (c *Client) Assemble(ctx context.Context, m manifest.Manifest, tmpDir, basePath string, base *Scan, onBytes func(int64)) (tmpPath string, stats TransferStats, err error) {
	tmp, err := os.CreateTemp(tmpDir, ".3dvault-tmp-*")
	if err != nil {
		return "", TransferStats{}, fmt.Errorf("crear el temporal: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Truncate(m.Size()); err != nil {
		return "", TransferStats{}, fmt.Errorf("reservar espacio: %w", err)
	}

	var src *os.File
	if base != nil {
		src, err = os.Open(basePath)
		if err != nil {
			return "", TransferStats{}, fmt.Errorf("abrir el archivo local: %w", err)
		}
		defer src.Close()
	}

	var downloadedChunks, downloadedBytes atomic.Int64
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallel)
	var offset int64
	for _, chunk := range m.Chunks {
		at := offset
		offset += chunk.Size
		group.Go(func() error {
			data, reused := readLocalChunk(src, base, chunk)
			if !reused {
				var err error
				data, err = c.GetChunk(groupCtx, chunk.SHA256)
				if err != nil {
					return err
				}
				downloadedChunks.Add(1)
				downloadedBytes.Add(chunk.Size)
			}
			if _, err := tmp.WriteAt(data, at); err != nil {
				return fmt.Errorf("escribir el temporal: %w", err)
			}
			if onBytes != nil {
				onBytes(chunk.Size)
			}
			return nil
		})
	}
	err = group.Wait()
	stats = TransferStats{Chunks: int(downloadedChunks.Load()), Bytes: downloadedBytes.Load()}
	if err != nil {
		return "", stats, err
	}

	if err := verifyFile(tmp, m.FileSHA256); err != nil {
		return "", stats, err
	}
	if err := tmp.Sync(); err != nil {
		return "", stats, fmt.Errorf("sincronizar el temporal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", stats, fmt.Errorf("cerrar el temporal: %w", err)
	}
	return tmp.Name(), stats, nil
}

// readLocalChunk intenta sacar un trozo del archivo local. Si no coincide con su
// hash, lo trata como ausente: el archivo pudo cambiar desde que se leyó.
func readLocalChunk(src *os.File, local *Scan, chunk manifest.Chunk) ([]byte, bool) {
	if local == nil || src == nil {
		return nil, false
	}
	offset, ok := local.offsets[chunk.SHA256]
	if !ok {
		return nil, false
	}
	data := make([]byte, chunk.Size)
	if _, err := src.ReadAt(data, offset); err != nil {
		return nil, false
	}
	if chunker.Hash(sha256.Sum256(data)) != chunk.SHA256 {
		return nil, false
	}
	return data, true
}

func verifyFile(f *os.File, want chunker.Hash) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("verificar el temporal: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("verificar el temporal: %w", err)
	}
	if chunker.Hash(h.Sum(nil)) != want {
		return errors.New("el archivo descargado no coincide con el servidor")
	}
	return nil
}
