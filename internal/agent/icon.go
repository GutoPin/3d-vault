package agent

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
)

const iconSize = 32

func stateColor(state State) color.RGBA {
	switch state {
	case StateOK:
		return color.RGBA{46, 125, 50, 255}
	case StateTransfer:
		return color.RGBA{29, 95, 191, 255}
	case StatePending:
		return color.RGBA{217, 119, 6, 255}
	case StateError:
		return color.RGBA{196, 36, 42, 255}
	default:
		return color.RGBA{107, 107, 107, 255}
	}
}

// drawIcon dibuja un cubo sólido con su cara derecha en el color del estado. Se dibuja
// aquí para no depender de archivos de imagen y para generar los dos formatos.
func drawIcon(state State) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	accent := stateColor(state)
	top := color.RGBA{uint8(min(int(accent.R)+70, 255)), uint8(min(int(accent.G)+70, 255)), uint8(min(int(accent.B)+70, 255)), 255}
	left := color.RGBA{uint8(int(accent.R) * 7 / 10), uint8(int(accent.G) * 7 / 10), uint8(int(accent.B) * 7 / 10), 255}

	// Cubo isométrico de lado 13: tres caras alrededor del punto central (16, 16).
	for y := range iconSize {
		for x := range iconSize {
			dx := x - 16
			dy := y - 16
			half := dx
			if half < 0 {
				half = -half
			}
			switch {
			case half <= 13 && dy <= 0 && dy >= -14+half/2+half/2 && dy >= -13 && dy*2 >= -half-14 && dy*2 <= 14-half*2+half:
				if dy*2+half <= 0 || dy*2 >= -14 {
					img.SetRGBA(x, y, top)
				}
			case dx < 0 && dx >= -13 && dy > 0 && dy*2-half <= 26-2*half+half:
				if dy <= 14-half/2 {
					img.SetRGBA(x, y, left)
				}
			case dx >= 0 && dx <= 13 && dy > 0:
				if dy <= 14-half/2 {
					img.SetRGBA(x, y, accent)
				}
			}
		}
	}
	return img
}

// IconICO devuelve el ícono en formato ICO con mapa de bits, el que entiende la
// bandeja de Windows en cualquier versión.
func IconICO(state State) []byte {
	img := drawIcon(state)
	var pixels bytes.Buffer
	// Un ICO guarda las filas de abajo arriba y los colores como BGRA.
	for y := iconSize - 1; y >= 0; y-- {
		for x := range iconSize {
			c := img.RGBAAt(x, y)
			pixels.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	mask := make([]byte, iconSize*4) // Máscara vacía: la transparencia va en el alfa.

	const (
		headerSize = 6 + 16
		bmpHeader  = 40
	)
	imageSize := bmpHeader + pixels.Len() + len(mask)

	var out bytes.Buffer
	write := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	write(uint16(0))
	write(uint16(1)) // Tipo: ícono.
	write(uint16(1)) // Una imagen.
	out.WriteByte(iconSize)
	out.WriteByte(iconSize)
	out.WriteByte(0) // Sin paleta.
	out.WriteByte(0)
	write(uint16(1))  // Planos.
	write(uint16(32)) // Bits por píxel.
	write(uint32(imageSize))
	write(uint32(headerSize))

	write(uint32(bmpHeader))
	write(int32(iconSize))
	write(int32(iconSize * 2)) // Alto doble: incluye la máscara.
	write(uint16(1))
	write(uint16(32))
	write(uint32(0)) // Sin compresión.
	write(uint32(pixels.Len() + len(mask)))
	write(int32(0))
	write(int32(0))
	write(uint32(0))
	write(uint32(0))
	out.Write(pixels.Bytes())
	out.Write(mask)
	return out.Bytes()
}

// IconPNG devuelve el ícono en PNG, para las plataformas que no usan ICO.
func IconPNG(state State) []byte {
	var out bytes.Buffer
	if err := png.Encode(&out, drawIcon(state)); err != nil {
		return nil
	}
	return out.Bytes()
}
