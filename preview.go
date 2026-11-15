package main

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// previewImage es lo que muestra el panel de vista previa.
type previewImage struct {
	pix  []byte // píxeles BGRA de arriba abajo (para Windows)
	w, h int
	text string // vista previa de texto
	msg  string // mensaje cuando no hay vista previa
	ext  string // para el icono
}

const (
	pvNone = iota
	pvImage
	pvText
)

var textExts = map[string]bool{
	"txt": true, "log": true, "csv": true, "ini": true, "xml": true, "json": true, "html": true,
	"htm": true, "md": true, "cfg": true, "bat": true, "cmd": true, "ps1": true, "py": true,
	"js": true, "css": true, "sql": true, "srt": true, "vbs": true, "reg": true, "yml": true,
}

func previewKind(ext string) int {
	switch ext {
	case "jpg", "jpeg", "jpe", "jfif", "png", "gif":
		return pvImage
	}
	if textExts[ext] {
		return pvText
	}
	return pvNone
}

// makePreview prepara la vista previa a partir del contenido del archivo.
func makePreview(data []byte, ext string, maxW, maxH int) previewImage {
	p := previewImage{ext: ext}
	switch previewKind(ext) {
	case pvImage:
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil || img.Bounds().Empty() {
			p.msg = "No se puede mostrar: la imagen parece dañada o incompleta."
			return p
		}
		p.pix, p.w, p.h = scaleBGRA(img, maxW, maxH)
	case pvText:
		p.text = decodeText(data, 6000)
		if strings.TrimSpace(p.text) == "" {
			p.msg = "El archivo está vacío o no contiene texto legible."
			p.text = ""
		}
	default:
		p.msg = "Sin vista previa para este tipo de archivo."
	}
	return p
}

// scaleBGRA reduce la imagen para que quepa en maxW×maxH (sin ampliarla)
// promediando varias muestras por píxel, y la compone sobre fondo blanco.
func scaleBGRA(img image.Image, maxW, maxH int) ([]byte, int, int) {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if maxW < 1 {
		maxW = 1
	}
	if maxH < 1 {
		maxH = 1
	}
	s := math.Min(1, math.Min(float64(maxW)/float64(sw), float64(maxH)/float64(sh)))
	tw := int(math.Max(1, math.Round(float64(sw)*s)))
	th := int(math.Max(1, math.Round(float64(sh)*s)))
	n := int(math.Ceil(1 / s))
	if n > 3 {
		n = 3
	}
	out := make([]byte, tw*th*4)
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			var r, g, bl, cnt uint32
			for j := 0; j < n; j++ {
				sy := b.Min.Y + int((float64(y)+(float64(j)+0.5)/float64(n))/s)
				if sy >= b.Max.Y {
					sy = b.Max.Y - 1
				}
				for i := 0; i < n; i++ {
					sx := b.Min.X + int((float64(x)+(float64(i)+0.5)/float64(n))/s)
					if sx >= b.Max.X {
						sx = b.Max.X - 1
					}
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					white := 0xFFFF - ca // composición sobre blanco
					r += cr + white
					g += cg + white
					bl += cb + white
					cnt++
				}
			}
			o := (y*tw + x) * 4
			out[o] = byte(bl / cnt >> 8)
			out[o+1] = byte(g / cnt >> 8)
			out[o+2] = byte(r / cnt >> 8)
			out[o+3] = 255
		}
	}
	return out, tw, th
}

// decodeText interpreta el texto (UTF-8, UTF-16 o Windows-1252).
func decodeText(b []byte, maxRunes int) string {
	var s string
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		u := make([]uint16, (len(b)-2)/2)
		for i := range u {
			u[i] = le16(b[2+2*i:])
		}
		s = string(utf16.Decode(u))
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		s = string(b[3:])
	case utf8.Valid(b) || utf8.Valid(trimPartialRune(b)):
		s = string(trimPartialRune(b))
	default:
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c) // aproximación a Windows-1252 (Latin-1)
		}
		s = string(r)
	}
	if r := []rune(s); len(r) > maxRunes {
		s = string(r[:maxRunes]) + "…"
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7F {
			return r
		}
		if r == '\r' {
			return -1
		}
		return '·'
	}, s)
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// trimPartialRune quita una letra UTF-8 cortada al final del búfer.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0; i++ {
		if r, _ := utf8.DecodeLastRune(b); r != utf8.RuneError {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}
