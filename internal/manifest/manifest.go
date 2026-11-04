// Package manifest describe una versión de un archivo como una lista ordenada de
// trozos más el hash del archivo completo.
package manifest

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"3d-vault/internal/chunker"
)

// Chunk es una entrada del manifiesto.
type Chunk struct {
	SHA256 chunker.Hash `json:"sha256"`
	Size   int64        `json:"size"`
}

// Manifest es la lista ordenada de trozos que forman un archivo.
type Manifest struct {
	FileSHA256 chunker.Hash `json:"file_sha256"`
	Chunks     []Chunk      `json:"chunks"`
}

// Size devuelve el tamaño total del archivo.
func (m Manifest) Size() int64 {
	var total int64
	for _, c := range m.Chunks {
		total += c.Size
	}
	return total
}

// Validate comprueba la forma del manifiesto sin leer los trozos. El límite
// inferior de cada trozo no se exige, para tolerar cambios de parámetros.
func (m Manifest) Validate(maxFileSize int64) error {
	var total int64
	for i, c := range m.Chunks {
		if c.Size <= 0 || c.Size > chunker.MaxSize {
			return fmt.Errorf("el trozo %d mide %d bytes, fuera del rango permitido", i, c.Size)
		}
		total += c.Size
		if total > maxFileSize {
			return fmt.Errorf("el archivo supera el máximo de %d bytes", maxFileSize)
		}
	}
	if len(m.Chunks) == 0 && m.FileSHA256 != sha256.Sum256(nil) {
		return errors.New("un manifiesto sin trozos debe corresponder a un archivo vacío")
	}
	return nil
}

// Build calcula el manifiesto de un flujo.
func Build(r io.Reader) (Manifest, error) {
	fileHash := sha256.New()
	c := chunker.New(io.TeeReader(r, fileHash))

	var m Manifest
	for {
		chunk, err := c.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
		m.Chunks = append(m.Chunks, Chunk{SHA256: chunk.Hash, Size: int64(len(chunk.Data))})
	}
	copy(m.FileSHA256[:], fileHash.Sum(nil))
	return m, nil
}
