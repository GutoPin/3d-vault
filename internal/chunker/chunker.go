// Package chunker corta un flujo de bytes en trozos definidos por su contenido
// usando FastCDC, de modo que un cambio local solo afecta a los trozos cercanos.
//
// El algoritmo está adaptado de github.com/jotfs/fastcdc-go (Apache 2.0, ver
// LICENSE.fastcdc-go). Respecto al original: parámetros fijos, tabla de solo
// lectura (el original la modifica de forma global al crear cada instancia),
// desplazamientos de 64 bits y SHA-256 calculado para cada trozo.
package chunker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Límites de tamaño de un trozo. Cambiarlos, o cambiar gearTable, no corrompe
// datos ya guardados, pero los archivos existentes dejan de deduplicarse con
// sus versiones anteriores durante una versión.
const (
	MinSize = 256 << 10 // 256 KiB
	AvgSize = 1 << 20   // 1 MiB
	MaxSize = 4 << 20   // 4 MiB

	bufSize = 2 * MaxSize

	// Normalización de nivel 2 sobre 20 bits, el logaritmo de AvgSize: más
	// difícil cortar antes del promedio, más fácil después.
	maskSmall uint64 = 1<<22 - 1
	maskLarge uint64 = 1<<18 - 1
)

// Hash es el SHA-256 del contenido sin comprimir de un trozo.
type Hash [sha256.Size]byte

// String devuelve el hash en hexadecimal.
func (h Hash) String() string {
	return hex.EncodeToString(h[:])
}

// ParseHash interpreta un hash en hexadecimal.
func ParseHash(s string) (Hash, error) {
	var h Hash
	if len(s) != hex.EncodedLen(len(h)) {
		return Hash{}, fmt.Errorf("hash de longitud incorrecta: %d", len(s))
	}
	if _, err := hex.Decode(h[:], []byte(s)); err != nil {
		return Hash{}, fmt.Errorf("hash no válido: %w", err)
	}
	return h, nil
}

// MarshalText permite usar Hash en JSON como hexadecimal.
func (h Hash) MarshalText() ([]byte, error) {
	return []byte(h.String()), nil
}

// UnmarshalText permite leer Hash desde JSON.
func (h *Hash) UnmarshalText(text []byte) error {
	parsed, err := ParseHash(string(text))
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

// Chunk es un trozo del flujo.
type Chunk struct {
	// Offset es la posición del trozo dentro del flujo.
	Offset int64
	// Data es el contenido. Solo es válido hasta la siguiente llamada a Next;
	// hay que copiarlo para conservarlo o usarlo en otra goroutine.
	Data []byte
	// Hash identifica el contenido.
	Hash Hash
}

// Chunker lee un flujo y lo devuelve trozo a trozo. No es seguro para uso
// concurrente, pero se pueden usar varias instancias a la vez.
type Chunker struct {
	r      io.Reader
	buf    []byte
	start  int
	end    int
	offset int64
	eof    bool
}

// New devuelve un Chunker que lee de r.
func New(r io.Reader) *Chunker {
	return &Chunker{r: r, buf: make([]byte, bufSize)}
}

// Next devuelve el siguiente trozo, o io.EOF cuando no quedan más.
func (c *Chunker) Next() (Chunk, error) {
	if err := c.fill(); err != nil {
		return Chunk{}, err
	}
	if c.start == c.end {
		return Chunk{}, io.EOF
	}

	data := c.buf[c.start:c.end]
	data = data[:cutPoint(data)]
	chunk := Chunk{Offset: c.offset, Data: data, Hash: sha256.Sum256(data)}

	c.start += len(data)
	c.offset += int64(len(data))
	return chunk, nil
}

// fill garantiza que el búfer tenga al menos MaxSize bytes, salvo al final del
// flujo, para que los cortes no dependan de cómo llegan los datos.
func (c *Chunker) fill() error {
	if c.eof || c.end-c.start >= MaxSize {
		return nil
	}

	n := copy(c.buf, c.buf[c.start:c.end])
	c.start, c.end = 0, n

	m, err := io.ReadFull(c.r, c.buf[n:])
	c.end += m
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		c.eof = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("leer datos en la posición %d: %w", c.offset+int64(c.end), err)
	}
	return nil
}

// cutPoint devuelve la longitud del trozo que empieza al inicio de data.
func cutPoint(data []byte) int {
	if len(data) <= MinSize {
		return len(data)
	}

	n := min(len(data), MaxSize)
	var fp uint64
	i := MinSize

	for ; i < min(n, AvgSize); i++ {
		fp = fp<<1 + gearTable[data[i]]
		if fp&maskSmall == 0 {
			return i + 1
		}
	}
	for ; i < n; i++ {
		fp = fp<<1 + gearTable[data[i]]
		if fp&maskLarge == 0 {
			return i + 1
		}
	}
	return n
}
