package main

// Rescate de las piezas de un archivo ZIP dañado. Los documentos de Office
// modernos (docx, xlsx, pptx) y de LibreOffice son ZIP por dentro: cada
// pieza (el texto, cada hoja, cada imagen...) va comprimida por separado y
// con su propia suma de control (CRC-32). Aunque una parte del archivo se
// haya sobrescrito, las piezas que no se tocaron siguen enteras, y de la
// pieza dañada se puede leer lo que hay antes del daño.

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
	"io"
	"time"
	"unicode/utf8"
)

// zipPart es una pieza rescatada de un ZIP.
type zipPart struct {
	Name     string
	Method   uint16
	CRC      uint32
	USize    uint64
	Raw      []byte // datos comprimidos tal cual estaban (solo si OK)
	Data     []byte // datos descomprimidos: completos si OK; si no, lo que se pudo leer
	OK       bool   // descomprime bien y su suma de control coincide
	AfterGap bool   // apareció después de una zona dañada (sin índice que lo confirme)
	Modified time.Time
}

const maxPartSize = 512 << 20

// salvageZip devuelve las piezas del ZIP en su orden. Usa el índice central
// si está sano; si no (suele estar al final, y el final es lo primero que se
// pierde), recorre las cabeceras locales de cada pieza.
func salvageZip(data []byte) []*zipPart {
	if parts := zipFromCentral(data); parts != nil {
		return parts
	}
	return zipFromLocal(data)
}

func zipFromCentral(data []byte) []*zipPart {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) == 0 {
		return nil
	}
	var parts []*zipPart
	seen := map[string]bool{}
	for _, f := range zr.File {
		if seen[f.Name] || f.Flags&1 != 0 {
			continue
		}
		seen[f.Name] = true
		p := &zipPart{Name: f.Name, Method: f.Method, CRC: f.CRC32, USize: f.UncompressedSize64, Modified: f.Modified}
		off, err := f.DataOffset()
		if err == nil && off >= 0 && f.CompressedSize64 <= uint64(len(data)) && off+int64(f.CompressedSize64) <= int64(len(data)) {
			raw := data[off : off+int64(f.CompressedSize64)]
			p.Data, p.OK = inflatePart(raw, f.Method, f.CRC32, f.UncompressedSize64)
			if p.OK {
				p.Raw = raw
			} else {
				p.salvage(raw)
			}
		}
		parts = append(parts, p)
	}
	return parts
}

// zipFromLocal recorre las cabeceras locales («PK\x03\x04») del archivo.
func zipFromLocal(data []byte) []*zipPart {
	var parts []*zipPart
	seen := map[string]bool{}
	expect := -1 // dónde debería empezar la pieza siguiente
	gap := false
	for pos := 0; pos+30 <= len(data); {
		i := bytes.Index(data[pos:], []byte("PK\x03\x04"))
		if i < 0 {
			break
		}
		o := pos + i
		h, ok := parseLocalHeader(data, o)
		if !ok {
			pos = o + 4
			continue
		}
		if expect >= 0 && o != expect {
			gap = true
		}
		// Otro documento escrito encima del nuestro: se reconoce porque vuelve
		// a empezar ([Content_Types].xml o «mimetype») o repite una pieza.
		if seen[h.name] || len(parts) > 0 && (h.name == "[Content_Types].xml" || h.name == "mimetype") {
			break
		}
		seen[h.name] = true
		p := &zipPart{Name: h.name, Method: h.method, CRC: h.crc, USize: h.usize, AfterGap: gap, Modified: h.mod}
		ds := o + 30 + h.nameLen + h.extraLen
		end := -1
		switch {
		case h.csize > 0 && ds+int(h.csize) <= len(data) && h.flags&8 == 0:
			end = ds + int(h.csize)
		case h.method == 8:
			// Tamaño desconocido: se descomprime hasta el final del flujo.
			br := bytes.NewReader(data[ds:])
			fr := flate.NewReader(br)
			_, err := io.Copy(io.Discard, io.LimitReader(fr, maxPartSize))
			fr.Close()
			if err == nil {
				end = len(data) - br.Len()
			}
		case h.method == 0 && h.flags&8 != 0:
			// Guardado sin comprimir y con descriptor: se busca el descriptor.
			for k := ds; k+16 <= len(data); k++ {
				if k2 := bytes.Index(data[k:], []byte("PK\x07\x08")); k2 >= 0 {
					k += k2
					if int(binary.LittleEndian.Uint32(data[k+8:])) == k-ds {
						end = k
						break
					}
				} else {
					break
				}
			}
		}
		if end < 0 {
			// Datos dañados: se rescata lo que se pueda hasta la pieza siguiente.
			lim := min(len(data), ds+int(min(h.csize, uint64(maxPartSize))))
			if h.csize == 0 || h.flags&8 != 0 {
				lim = nextLocalHeader(data, ds)
			}
			p.salvage(data[ds:lim])
			parts = append(parts, p)
			expect = -1
			gap = true
			pos = ds
			continue
		}
		next := end
		if h.flags&8 != 0 && end+12 <= len(data) { // descriptor: [firma] crc csize usize
			d := end
			if string(data[d:d+4]) == "PK\x07\x08" {
				d += 4
			}
			if d+12 <= len(data) {
				p.CRC = binary.LittleEndian.Uint32(data[d:])
				if h.usize == 0 {
					p.USize = uint64(binary.LittleEndian.Uint32(data[d+8:]))
				}
				next = d + 12
			}
		}
		raw := data[ds:end]
		p.Data, p.OK = inflatePart(raw, h.method, p.CRC, p.USize)
		if p.OK {
			p.Raw = raw
		} else {
			p.salvage(raw)
		}
		parts = append(parts, p)
		expect = next
		pos = max(next, o+30)
	}
	return parts
}

// salvage guarda de una pieza dañada lo que se puede leer antes del daño.
// (Lo de después no es fiable: en un flujo comprimido casi todo son
// repeticiones de datos anteriores, que se perdieron con el daño.)
func (p *zipPart) salvage(raw []byte) {
	if p.Method == 8 {
		// Unos datos comprimidos sanos nunca tienen una racha de ceros: si
		// la hay, ahí empieza la zona vaciada por el disco. Se corta antes,
		// para que el descompresor no «invente» datos a partir de los ceros.
		if i := bytes.Index(raw, make([]byte, 32)); i >= 0 {
			raw = raw[:i]
		}
	}
	p.Data, _ = inflatePart(raw, p.Method, p.CRC, p.USize)
}

// nextLocalHeader devuelve dónde empieza la pieza siguiente (o el índice).
func nextLocalHeader(data []byte, from int) int {
	for pos := from; pos+4 <= len(data); {
		i := bytes.Index(data[pos:], []byte("PK"))
		if i < 0 {
			break
		}
		o := pos + i
		if o+4 <= len(data) && (string(data[o:o+4]) == "PK\x01\x02" || string(data[o:o+4]) == "PK\x05\x06") {
			return o
		}
		if _, ok := parseLocalHeader(data, o); ok && string(data[o:o+4]) == "PK\x03\x04" {
			return o
		}
		pos = o + 2
	}
	return len(data)
}

type localHeader struct {
	flags, method     uint16
	crc               uint32
	csize, usize      uint64
	nameLen, extraLen int
	name              string
	mod               time.Time
}

func parseLocalHeader(data []byte, o int) (localHeader, bool) {
	var h localHeader
	b := data[o:]
	if len(b) < 30 {
		return h, false
	}
	h.flags = binary.LittleEndian.Uint16(b[6:])
	h.method = binary.LittleEndian.Uint16(b[8:])
	h.crc = binary.LittleEndian.Uint32(b[14:])
	h.csize = uint64(binary.LittleEndian.Uint32(b[18:]))
	h.usize = uint64(binary.LittleEndian.Uint32(b[22:]))
	h.nameLen = int(binary.LittleEndian.Uint16(b[26:]))
	h.extraLen = int(binary.LittleEndian.Uint16(b[28:]))
	if h.method != 0 && h.method != 8 || h.nameLen == 0 || h.nameLen > 1024 || 30+h.nameLen+h.extraLen > len(b) || h.flags&1 != 0 {
		return h, false
	}
	name := b[30 : 30+h.nameLen]
	if !utf8.Valid(name) || bytes.IndexByte(name, 0) >= 0 {
		return h, false
	}
	for _, c := range name {
		if c < 0x20 {
			return h, false
		}
	}
	h.name = string(name)
	t, d := binary.LittleEndian.Uint16(b[10:]), binary.LittleEndian.Uint16(b[12:])
	h.mod = time.Date(1980+int(d>>9), time.Month(d>>5&0xF), int(d&0x1F), int(t>>11), int(t>>5&0x3F), int(t&0x1F)*2, 0, time.Local)
	return h, true
}

// inflatePart descomprime una pieza y comprueba su suma de control. Si está
// dañada devuelve lo que se pudo leer antes del daño.
func inflatePart(raw []byte, method uint16, crc uint32, usize uint64) ([]byte, bool) {
	var out []byte
	var err error
	switch method {
	case 0:
		out = raw
		if usize > 0 && uint64(len(out)) > usize {
			out = out[:usize]
		}
	case 8:
		fr := flate.NewReader(bytes.NewReader(raw))
		out, err = io.ReadAll(io.LimitReader(fr, maxPartSize))
		fr.Close()
	default:
		return nil, false
	}
	ok := err == nil && crc32.ChecksumIEEE(out) == crc && (usize == 0 || uint64(len(out)) == usize)
	return out, ok
}

// zipOut escribe un ZIP nuevo: las piezas sanas se copian tal cual
// (comprimidas como estaban); las nuevas o arregladas se comprimen.
type zipOut struct {
	buf bytes.Buffer
	w   *zip.Writer
}

func newZipOut() *zipOut {
	z := &zipOut{}
	z.w = zip.NewWriter(&z.buf)
	return z
}

func (z *zipOut) raw(p *zipPart) error {
	w, err := z.w.CreateRaw(&zip.FileHeader{Name: p.Name, Method: p.Method, CRC32: p.CRC,
		CompressedSize64: uint64(len(p.Raw)), UncompressedSize64: uint64(len(p.Data)), Modified: p.Modified})
	if err != nil {
		return err
	}
	_, err = w.Write(p.Raw)
	return err
}

func (z *zipOut) add(name string, data []byte, method uint16) error {
	w, err := z.w.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func (z *zipOut) bytes() ([]byte, error) {
	if err := z.w.Close(); err != nil {
		return nil, err
	}
	return z.buf.Bytes(), nil
}
