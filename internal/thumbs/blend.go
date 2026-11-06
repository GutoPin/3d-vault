// Package thumbs extrae y guarda las miniaturas de las vistas previas.
//
// Un .blend guardado con "Save Preview Images" lleva su miniatura en el bloque
// TEST, escrito justo después de los bloques REND y antes de GLOB. Se lee sin
// Blender: cabecera, bloques en orden y, al encontrar TEST, se corta la lectura.
// El diseño de la cabecera y de los BHead sigue a Blender 5.1
// (source/blender/blenloader_core/BLO_core_bhead.hh y blo_core_blend_header.cc).
package thumbs

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"

	"github.com/klauspost/compress/zstd"
)

var (
	// ErrNoThumbnail indica que el archivo no lleva miniatura.
	ErrNoThumbnail = errors.New("el archivo no tiene miniatura")
	// ErrInvalid indica que no es un .blend que se sepa leer, o que está truncado.
	ErrInvalid = errors.New("no es un archivo .blend válido")
)

const (
	// maxScan limita lo que se lee antes de rendirse. TEST va al principio: solo
	// lo preceden la cabecera y los bloques REND, de unos cientos de bytes.
	maxScan = 8 << 20
	// maxSide acota la miniatura. Blender las guarda de 128 × 128.
	maxSide = 1024

	// Blender comprime con ventanas de pocos MiB. El límite evita que un archivo
	// raro reserve mucha memoria.
	maxZstdWindow = 64 << 20
)

var (
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
	gzipMagic = []byte{0x1f, 0x8b}
)

// bheadFormat es una de las tres formas del BHead en disco.
type bheadFormat int

const (
	bhead4      bheadFormat = iota // code, len, old (4), SDNAnr, nr: 20 bytes
	smallBHead8                    // code, len, old (8), SDNAnr, nr: 24 bytes
	largeBHead8                    // code, SDNAnr, old (8), len (8), nr (8): 32 bytes
)

func (f bheadFormat) size() int {
	return [...]int{20, 24, 32}[f]
}

// BlendPNG lee el principio de un .blend, comprimido o no, y devuelve su
// miniatura en PNG.
func BlendPNG(r io.Reader) ([]byte, error) {
	img, err := blendThumbnail(r)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("codificar la miniatura: %w", err)
	}
	return buf.Bytes(), nil
}

func blendThumbnail(r io.Reader) (*image.NRGBA, error) {
	br := bufio.NewReader(r)
	magic, _ := br.Peek(4) // Si el archivo es más corto, lo dirá readHeader.
	var data io.Reader = br
	switch {
	case bytes.HasPrefix(magic, zstdMagic):
		dec, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxZstdWindow))
		if err != nil {
			return nil, fmt.Errorf("preparar la descompresión: %w", err)
		}
		defer dec.Close()
		data = dec
	case bytes.HasPrefix(magic, gzipMagic):
		dec, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("%w: gzip: %v", ErrInvalid, err)
		}
		data = dec
	}
	return scanBlocks(io.LimitReader(data, maxScan))
}

// truncated traduce un final inesperado a ErrInvalid. Cualquier otro error, de
// lectura o de descompresión, se devuelve tal cual.
func truncated(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: archivo truncado", ErrInvalid)
	}
	return err
}

// readHeader interpreta la cabecera: "BLENDER" seguido de 5 bytes en el formato
// antiguo (puntero, endian, versión) o de 10 en el de Blender 5.0
// ("17-01v0501": tamaño, formato del archivo, endian y versión).
func readHeader(r io.Reader) (bheadFormat, binary.ByteOrder, error) {
	head := make([]byte, 12)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, nil, truncated(err)
	}
	if string(head[:7]) != "BLENDER" {
		return 0, nil, ErrInvalid
	}

	switch head[7] {
	case '_', '-':
		format := smallBHead8
		if head[7] == '_' {
			format = bhead4
		}
		switch head[8] {
		case 'v':
			return format, binary.LittleEndian, nil
		case 'V':
			return format, binary.BigEndian, nil
		}
		return 0, nil, fmt.Errorf("%w: orden de bytes desconocido", ErrInvalid)
	}

	// Formato nuevo: "BLENDER" + "17" + "-" + "01" + "v" + versión de 4 cifras.
	if string(head[7:12]) != "17-01" {
		return 0, nil, fmt.Errorf("%w: formato de cabecera desconocido", ErrInvalid)
	}
	rest := make([]byte, 5)
	if _, err := io.ReadFull(r, rest); err != nil {
		return 0, nil, truncated(err)
	}
	if rest[0] != 'v' {
		return 0, nil, fmt.Errorf("%w: orden de bytes desconocido", ErrInvalid)
	}
	return largeBHead8, binary.LittleEndian, nil
}

// scanBlocks recorre los bloques hasta TEST. Se rinde al llegar a GLOB, que
// Blender escribe justo después, o al final del archivo.
func scanBlocks(r io.Reader) (*image.NRGBA, error) {
	format, order, err := readHeader(r)
	if err != nil {
		return nil, err
	}
	head := make([]byte, format.size())
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			return nil, truncated(err)
		}
		code := string(head[:4])
		var length int64
		if format == largeBHead8 {
			length = int64(order.Uint64(head[16:24]))
		} else {
			length = int64(int32(order.Uint32(head[4:8])))
		}
		if length < 0 {
			return nil, fmt.Errorf("%w: bloque de tamaño negativo", ErrInvalid)
		}
		if length > maxScan {
			return nil, ErrNoThumbnail
		}

		switch code {
		case "TEST":
			data := make([]byte, length)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, truncated(err)
			}
			return decodeTest(data, order)
		case "GLOB", "DNA1", "ENDB":
			return nil, ErrNoThumbnail
		}
		if _, err := io.CopyN(io.Discard, r, length); err != nil {
			return nil, truncated(err)
		}
	}
}

// decodeTest convierte el bloque TEST: ancho y alto como int32 y después los
// píxeles RGBA de 8 bits, con las filas de abajo arriba.
func decodeTest(data []byte, order binary.ByteOrder) (*image.NRGBA, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("%w: miniatura incompleta", ErrInvalid)
	}
	w, h := int(int32(order.Uint32(data[0:4]))), int(int32(order.Uint32(data[4:8])))
	if w <= 0 || h <= 0 || w > maxSide || h > maxSide {
		return nil, fmt.Errorf("%w: miniatura de %d × %d", ErrInvalid, w, h)
	}
	pixels := data[8:]
	stride := w * 4
	if len(pixels) < stride*h {
		return nil, fmt.Errorf("%w: miniatura incompleta", ErrInvalid)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		copy(img.Pix[y*img.Stride:y*img.Stride+stride], pixels[(h-1-y)*stride:])
	}
	return img, nil
}
