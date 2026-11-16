package main

// Fotos y archivos de texto dañados.
//
// Fotos JPG: las cámaras y los teléfonos guardan dentro de cada foto una
// miniatura (y muchas cámaras, una vista previa grande). Están al principio
// del archivo, así que suelen sobrevivir aunque la foto principal se haya
// perdido: se extraen como un JPG aparte.
//
// Texto (txt, csv, xml...): se quitan los trozos que dejaron de ser texto
// (datos de otros archivos escritos encima) y se marca dónde faltan.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"strings"
)

// jpegPreviews devuelve las imágenes JPG incrustadas que se pueden decodificar
// (la vista previa grande primero, luego la miniatura).
func jpegPreviews(data []byte) [][]byte {
	var out [][]byte
	add := func(b []byte) {
		if len(b) < 100 || !bytes.HasPrefix(b, []byte{0xFF, 0xD8}) {
			return
		}
		if _, err := jpeg.Decode(bytes.NewReader(b)); err == nil {
			out = append(out, b)
		}
	}
	// Recorre los segmentos de la cabecera (hasta el comienzo de la imagen).
	for p := 2; p+4 <= len(data) && data[p] == 0xFF; {
		m := data[p+1]
		if m == 0xDA || m == 0xD9 {
			break
		}
		l := int(binary.BigEndian.Uint16(data[p+2:]))
		if l < 2 || p+2+l > len(data) {
			break
		}
		seg := data[p+4 : p+2+l]
		switch {
		case m == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")):
			if t := exifThumbnail(seg[6:]); t != nil {
				add(t)
			}
		case m == 0xE2 && bytes.HasPrefix(seg, []byte("MPF\x00")):
			base := p + 8 // las posiciones del MPF cuentan desde su cabecera TIFF
			for _, img := range mpfImages(seg[4:]) {
				if img[0] > 0 && base+img[0]+img[1] <= len(data) {
					add(data[base+img[0] : base+img[0]+img[1]])
				}
			}
		}
		p += 2 + l
	}
	// Vista previa grande antes que miniatura.
	if len(out) > 1 && len(out[0]) < len(out[len(out)-1]) {
		out[0], out[len(out)-1] = out[len(out)-1], out[0]
	}
	return out
}

func tiffOrder(t []byte) binary.ByteOrder {
	if len(t) >= 2 && t[0] == 'M' {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// exifThumbnail devuelve la miniatura de un bloque EXIF (IFD1).
func exifThumbnail(t []byte) []byte {
	if len(t) < 8 {
		return nil
	}
	bo := tiffOrder(t)
	ifd0 := int(bo.Uint32(t[4:]))
	if ifd0+2 > len(t) {
		return nil
	}
	n := int(bo.Uint16(t[ifd0:]))
	next := ifd0 + 2 + 12*n
	if next+4 > len(t) {
		return nil
	}
	ifd1 := int(bo.Uint32(t[next:]))
	if ifd1 <= 0 || ifd1+2 > len(t) {
		return nil
	}
	off, ln := 0, 0
	n = int(bo.Uint16(t[ifd1:]))
	for i := 0; i < n && ifd1+2+12*i+12 <= len(t); i++ {
		e := t[ifd1+2+12*i:]
		switch bo.Uint16(e) {
		case 0x0201:
			off = int(bo.Uint32(e[8:]))
		case 0x0202:
			ln = int(bo.Uint32(e[8:]))
		}
	}
	if off <= 0 || ln <= 0 || off+ln > len(t) {
		return nil
	}
	return t[off : off+ln]
}

// mpfImages devuelve (posición, tamaño) de las imágenes extra del MPF.
func mpfImages(t []byte) [][2]int {
	if len(t) < 8 {
		return nil
	}
	bo := tiffOrder(t)
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return nil
	}
	n := int(bo.Uint16(t[ifd:]))
	var entries, count int
	for i := 0; i < n && ifd+2+12*i+12 <= len(t); i++ {
		e := t[ifd+2+12*i:]
		switch bo.Uint16(e) {
		case 0xB001:
			count = int(bo.Uint32(e[8:]))
		case 0xB002:
			entries = int(bo.Uint32(e[8:]))
		}
	}
	var out [][2]int
	for i := 1; i < count && entries+16*i+16 <= len(t); i++ { // la 0 es la foto principal
		e := t[entries+16*i:]
		out = append(out, [2]int{int(bo.Uint32(e[8:])), int(bo.Uint32(e[4:]))})
	}
	return out
}

func repairJPEG(data []byte) *repairOut {
	out := &repairOut{}
	pv := jpegPreviews(data)
	if len(pv) == 0 {
		out.Notes = append(out.Notes, "la foto no tiene una miniatura que se pueda rescatar")
		return out
	}
	img, _ := jpeg.DecodeConfig(bytes.NewReader(pv[0]))
	out.Rescue, out.RescueExt, out.RescueTag = pv[0], "jpg", "vista previa"
	out.Notes = append(out.Notes, fmt.Sprintf("se rescató la vista previa que la cámara guardó dentro de la foto (%d×%d)", img.Width, img.Height))
	return out
}

// repairText quita de un archivo de texto los trozos que ya no son texto.
func repairText(data []byte, ext string) *repairOut {
	out := &repairOut{}
	utf16 := len(data) >= 2 && (data[0] == 0xFF && data[1] == 0xFE || data[0] == 0xFE && data[1] == 0xFF)
	if utf16 {
		out.Notes = append(out.Notes, "texto en UTF-16: no se puede limpiar")
		return out
	}
	var b bytes.Buffer
	lost, kept := 0, 0
	inGap := false
	for off := 0; off < len(data); off += 4096 {
		blk := data[off:min(off+4096, len(data))]
		if !allZero(blk) && looksLikeText(blk, false) && bytes.IndexByte(blk, 0) < 0 {
			if inGap {
				b.WriteString(gapMarker(ext))
				inGap = false
			}
			b.Write(blk)
			kept++
			continue
		}
		lost++
		inGap = true
	}
	if inGap {
		b.WriteString(gapMarker(ext))
	}
	if lost == 0 || kept == 0 {
		out.Notes = append(out.Notes, "no hay texto que separar de los datos dañados")
		return out
	}
	out.Repaired, out.RepairedExt = b.Bytes(), ext
	out.Notes = append(out.Notes, fmt.Sprintf("se quitaron %d KB que ya no eran texto (marcados con «[…]»)", lost*4))
	return out
}

func gapMarker(ext string) string {
	switch ext {
	case "csv":
		return "\r\n[... aquí faltan datos ...]\r\n"
	case "xml", "html", "htm", "svg":
		return "\r\n<!-- [... aquí faltan datos ...] -->\r\n"
	}
	return "\r\n[... aquí falta un trozo ...]\r\n"
}

func repairKind(ext string) string {
	switch {
	case zipExts[ext]:
		return "zip"
	case ext == "pdf":
		return "pdf"
	case oleExts[ext]:
		return "ole"
	case ext == "jpg" || ext == "jpeg" || ext == "jpe" || ext == "jfif":
		return "jpg"
	case plainExts[ext] && !strings.Contains("svg rtf", ext):
		return "text"
	}
	return ""
}
