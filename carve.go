package main

import (
	"bytes"
	"encoding/binary"
	"time"
)

// Recuperación por firma ("carving"): cuando ya no existe el registro NTFS
// de un archivo, se localiza por la cabecera característica de su formato
// y se calcula su tamaño analizando su estructura interna.

const (
	kJPEG uint8 = iota + 1
	kPNG
	kGIF
	kBMP
	kTIFF
	kPSD
	kPDF
	kZIP
	kOLE
	kRTF
	kBMFF
	kRIFF
	kMKV
	kASF
	kTS
	kM2TS
	kMP3
	kOGG
	kRAR
	k7Z
	kSQLITE
	kPST
	kMax
)

var kindNames = [kMax]string{
	kJPEG: "jpg", kPNG: "png", kGIF: "gif", kBMP: "bmp", kTIFF: "tif/raw", kPSD: "psd", kPDF: "pdf",
	kZIP: "zip/docx/xlsx/pptx", kOLE: "doc/xls/ppt", kRTF: "rtf", kBMFF: "mp4/mov/heic", kRIFF: "avi/wav/webp",
	kMKV: "mkv/webm", kASF: "wmv/wma", kTS: "ts", kM2TS: "mts", kMP3: "mp3", kOGG: "ogg", kRAR: "rar",
	k7Z: "7z", kSQLITE: "sqlite", kPST: "pst",
}

// carveResult describe un archivo localizado por firma.
type carveResult struct {
	size    int64
	ext     string
	walked  bool      // estructura verificada de principio a fin
	partial bool      // solo se pudo rescatar una parte
	stamp   string    // fecha interna (EXIF, etc.) para el nombre
	mtime   time.Time // fecha interna
}

type carveFn func(s *src, off, max int64) (carveResult, bool)

var carvers [kMax]carveFn

func init() {
	carvers = [kMax]carveFn{
		kJPEG: carveJPEG, kPNG: carvePNG, kGIF: carveGIF, kBMP: carveBMP, kTIFF: carveTIFF, kPSD: carvePSD,
		kPDF: carvePDF, kZIP: carveZIP, kOLE: carveOLE, kRTF: carveRTF, kBMFF: carveBMFF, kRIFF: carveRIFF,
		kMKV: carveMKV, kASF: carveASF, kTS: carveTS(188, 0, "ts"), kM2TS: carveTS(192, 4, "mts"),
		kMP3: carveMP3, kOGG: carveOGG, kRAR: carveRAR, k7Z: carve7Z, kSQLITE: carveSQLite, kPST: carvePST,
	}
}

var (
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	oleMagic  = []byte("\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1")
	asfMagic  = []byte("\x30\x26\xB2\x75\x8E\x66\xCF\x11\xA6\xD9\x00\xAA\x00\x62\xCE\x6C")
	sqlMagic  = []byte("SQLite format 3\x00")
	sevenZMag = []byte("7z\xBC\xAF\x27\x1C")
)

// sniff examina el inicio de un sector (b tiene al menos 512 bytes) y
// devuelve el tipo de archivo que parece empezar ahí, o 0.
func sniff(b []byte) uint8 {
	// Contenedores ISO (MP4/MOV/HEIC): el tipo de átomo está en el byte 4.
	switch string(b[4:8]) {
	case "ftyp":
		if sz := be32(b); sz >= 12 && sz <= 1024 && sz%4 == 0 {
			return kBMFF
		}
	case "moov", "mdat":
		if sz := be32(b); sz == 1 || sz >= 8 {
			return kBMFF
		}
	case "wide":
		if be32(b) == 8 && string(b[12:16]) == "mdat" {
			return kBMFF
		}
	}
	switch b[0] {
	case 0xFF:
		if b[1] == 0xD8 && b[2] == 0xFF && b[3] >= 0xC0 && b[3] != 0xD8 && b[3] != 0xD9 {
			return kJPEG
		}
		if b[1]&0xE0 == 0xE0 && mp3Sniff(b) {
			return kMP3
		}
	case 0x89:
		if bytes.Equal(b[:8], pngMagic) {
			return kPNG
		}
	case 'G':
		if string(b[:4]) == "GIF8" && (b[4] == '7' || b[4] == '9') && b[5] == 'a' {
			return kGIF
		}
		if b[188] == 0x47 && b[376] == 0x47 && len(b) > 752 && b[564] == 0x47 && b[752] == 0x47 {
			return kTS
		}
	case 'B':
		if b[1] == 'M' && bmpPlausible(b) {
			return kBMP
		}
	case '%':
		if string(b[:5]) == "%PDF-" {
			return kPDF
		}
	case 'P':
		if b[1] == 'K' && b[2] == 3 && b[3] == 4 {
			return kZIP
		}
	case 0xD0:
		if bytes.Equal(b[:8], oleMagic) {
			return kOLE
		}
	case '{':
		if string(b[:6]) == "{\\rtf1" {
			return kRTF
		}
	case 'R':
		if string(b[:4]) == "RIFF" && riffExt(string(b[8:12])) != "" {
			return kRIFF
		}
		if string(b[:6]) == "Rar!\x1a\x07" {
			return kRAR
		}
	case 'I':
		if string(b[:3]) == "ID3" && b[3] >= 2 && b[3] <= 4 && b[4] == 0 && b[5]&0x0F == 0 &&
			b[6]|b[7]|b[8]|b[9] < 0x80 {
			return kMP3
		}
		if b[1] == 'I' && ((b[2] == '*' && b[3] == 0) || (b[2] == 'R' && (b[3] == 'O' || b[3] == 'S')) || (b[2] == 'U' && b[3] == 0)) {
			if o := le32(b[4:]); o >= 8 && o < 1<<28 {
				return kTIFF
			}
		}
	case 'M':
		if b[1] == 'M' && b[2] == 0 && b[3] == '*' {
			if o := be32(b[4:]); o >= 8 && o < 1<<28 {
				return kTIFF
			}
		}
	case '8':
		if string(b[:4]) == "8BPS" && b[4] == 0 && (b[5] == 1 || b[5] == 2) {
			return kPSD
		}
	case '7':
		if bytes.Equal(b[:6], sevenZMag) {
			return k7Z
		}
	case 'S':
		if bytes.Equal(b[:16], sqlMagic) {
			return kSQLITE
		}
	case 0x1A:
		if b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3 {
			return kMKV
		}
	case 0x30:
		if bytes.Equal(b[:16], asfMagic) {
			return kASF
		}
	case 'O':
		if string(b[:4]) == "OggS" && b[4] == 0 && b[5]&0x02 != 0 {
			return kOGG
		}
	case '!':
		if string(b[:4]) == "!BDN" && b[8] == 'S' && (b[9] == 'M' || b[9] == 'O') {
			return kPST
		}
	}
	if b[4] == 0x47 && b[196] == 0x47 && b[388] == 0x47 && len(b) > 772 && b[580] == 0x47 && b[772] == 0x47 {
		return kM2TS
	}
	return 0
}

// ---------------------------------------------------------------- JPEG

// exifDate extrae la fecha de captura de un bloque EXIF (estructura TIFF).
func exifDate(t []byte) (time.Time, bool) {
	if len(t) < 8 {
		return time.Time{}, false
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return time.Time{}, false
	}
	readIFD := func(off int) (sub int, date string) {
		if off < 8 || off+2 > len(t) {
			return
		}
		n := int(bo.Uint16(t[off:]))
		for i := 0; i < n; i++ {
			e := off + 2 + 12*i
			if e+12 > len(t) {
				break
			}
			tag := bo.Uint16(t[e:])
			cnt := int(bo.Uint32(t[e+4:]))
			val := int(bo.Uint32(t[e+8:]))
			switch tag {
			case 0x8769:
				sub = val
			case 0x0132, 0x9003, 0x9004:
				if cnt == 20 && val > 0 && val+19 <= len(t) && (tag == 0x9003 || date == "") {
					date = string(t[val : val+19])
				}
			}
		}
		return
	}
	sub, date := readIFD(int(bo.Uint32(t[4:])))
	if sub > 0 {
		if _, d := readIFD(sub); d != "" {
			date = d
		}
	}
	tm, err := time.ParseInLocation("2006:01:02 15:04:05", date, time.Local)
	if err != nil || tm.Year() < 1990 || tm.Year() > 2100 {
		return time.Time{}, false
	}
	return tm, true
}

func carveJPEG(s *src, off, max int64) (carveResult, bool) {
	if max > 100<<20 {
		max = 100 << 20
	}
	res := carveResult{ext: "jpg", walked: true}
	p := off + 2
	sof, scans := false, 0
	fail := func() (carveResult, bool) {
		// Rescate parcial: una foto cortada sigue mostrando su parte superior.
		if scans > 0 && p-off >= 64<<10 {
			res.size = (p - off) &^ 511
			res.partial = true
			return res, true
		}
		return res, false
	}
	for p-off < max {
		b := s.at(p, 4)
		if b == nil || b[0] != 0xFF {
			return fail()
		}
		m := b[1]
		if m == 0xFF {
			p++
			continue
		}
		if m == 0xD9 {
			if scans == 0 {
				return res, false
			}
			res.size = p + 2 - off
			return res, true
		}
		if m < 0xC0 || (m >= 0xD0 && m <= 0xD8) {
			return fail()
		}
		l := int64(be16(b[2:]))
		if l < 2 {
			return fail()
		}
		if m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			sof = true
		}
		if m == 0xE1 && res.stamp == "" && scans == 0 && l > 16 {
			if seg := s.at(p+4, int(l-2)); seg != nil && string(seg[:6]) == "Exif\x00\x00" {
				if t, ok := exifDate(seg[6:]); ok {
					res.mtime = t
					res.stamp = t.Format("20060102_150405")
				}
			}
		}
		p += 2 + l
		if m != 0xDA {
			continue
		}
		if !sof {
			return res, false
		}
		scans++
		// Datos comprimidos hasta el siguiente marcador.
		lastFF := p
		for {
			if p-off >= max {
				return fail()
			}
			w := s.upTo(p, 256<<10)
			if len(w) < 2 {
				return fail()
			}
			i, found := 0, false
			for i < len(w)-1 {
				j := bytes.IndexByte(w[i:len(w)-1], 0xFF)
				if j < 0 {
					i = len(w) - 1
					break
				}
				i += j
				lastFF = p + int64(i)
				n := w[i+1]
				if n == 0x00 || (n >= 0xD0 && n <= 0xD7) {
					i += 2
					continue
				}
				if n == 0xFF {
					i++
					continue
				}
				found = true
				break
			}
			p += int64(i)
			if found {
				break
			}
			if p-lastFF > 128<<10 {
				p = lastFF // demasiado tramo sin ningún 0xFF: ya no son datos JPEG
				return fail()
			}
		}
	}
	return fail()
}

// ---------------------------------------------------------------- PNG / GIF / BMP

func isAlpha4(b []byte) bool {
	for _, c := range b[:4] {
		if !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

func carvePNG(s *src, off, max int64) (carveResult, bool) {
	if max > 512<<20 {
		max = 512 << 20
	}
	res := carveResult{ext: "png", walked: true}
	p := off + 8
	first := true
	for p-off < max {
		b := s.at(p, 8)
		if b == nil || !isAlpha4(b[4:]) {
			return res, false
		}
		l := int64(be32(b))
		typ := string(b[4:8])
		if l > 1<<30 {
			return res, false
		}
		if first && (typ != "IHDR" || l != 13) {
			return res, false
		}
		first = false
		p += 12 + l
		if typ == "IEND" {
			if l != 0 || p-off > max {
				return res, false
			}
			res.size = p - off
			return res, true
		}
	}
	return res, false
}

func carveGIF(s *src, off, max int64) (carveResult, bool) {
	if max > 256<<20 {
		max = 256 << 20
	}
	res := carveResult{ext: "gif", walked: true}
	lsd := s.at(off+6, 7)
	if lsd == nil {
		return res, false
	}
	p := off + 13
	if lsd[4]&0x80 != 0 {
		p += 3 << uint(lsd[4]&7+1)
	}
	skipBlocks := func() bool {
		for p-off < max {
			b := s.at(p, 1)
			if b == nil {
				return false
			}
			n := int64(b[0])
			p += 1 + n
			if n == 0 {
				return true
			}
		}
		return false
	}
	images := 0
	for p-off < max {
		b := s.at(p, 1)
		if b == nil {
			return res, false
		}
		switch b[0] {
		case 0x3B:
			if images == 0 {
				return res, false
			}
			res.size = p + 1 - off
			return res, true
		case 0x21:
			p += 2
			if !skipBlocks() {
				return res, false
			}
		case 0x2C:
			d := s.at(p, 10)
			if d == nil {
				return res, false
			}
			f := d[9]
			p += 10
			if f&0x80 != 0 {
				p += 3 << uint(f&7+1)
			}
			p++ // tamaño mínimo de código LZW
			if !skipBlocks() {
				return res, false
			}
			images++
		default:
			return res, false
		}
	}
	return res, false
}

func bmpPlausible(b []byte) bool {
	size := le32(b[2:])
	dataOfs := le32(b[10:])
	dib := le32(b[14:])
	if le32(b[6:]) != 0 || size <= dataOfs || size > 1<<30 {
		return false
	}
	switch dib {
	case 40, 52, 56, 108, 124:
	default:
		return false
	}
	if dataOfs < 14+dib || dataOfs > 14+dib+1024+16 {
		return false
	}
	w := int32(le32(b[18:]))
	h := int32(le32(b[22:]))
	if h < 0 {
		h = -h
	}
	bpp := le16(b[28:])
	if w <= 0 || w > 32768 || h <= 0 || h > 32768 || le16(b[26:]) != 1 {
		return false
	}
	switch bpp {
	case 1, 4, 8, 16, 24, 32:
		return true
	}
	return false
}

func carveBMP(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "bmp"}
	b := s.at(off, 54)
	if b == nil || !bmpPlausible(b) {
		return res, false
	}
	size := int64(le32(b[2:]))
	dataOfs := int64(le32(b[10:]))
	w := int64(int32(le32(b[18:])))
	h := int64(int32(le32(b[22:])))
	if h < 0 {
		h = -h
	}
	bpp := int64(le16(b[28:]))
	if comp := le32(b[30:]); comp == 0 || comp == 3 {
		want := dataOfs + (w*bpp+31)/32*4*h
		if size < want || size > want+4096 {
			return res, false
		}
	}
	if size > max {
		return res, false
	}
	res.size = size
	return res, true
}

// ---------------------------------------------------------------- TIFF y RAW

var tiffTypeSize = [14]int64{0, 1, 1, 2, 4, 8, 1, 1, 2, 4, 8, 4, 8, 4}

func carveTIFF(s *src, off, max int64) (carveResult, bool) {
	if max > 2<<30 {
		max = 2 << 30
	}
	res := carveResult{ext: "tif"}
	h := s.copyAt(off, 16)
	if h == nil {
		return res, false
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if h[0] == 'M' {
		bo = binary.BigEndian
	}
	switch {
	case h[2] == 'R':
		res.ext = "orf"
	case h[2] == 'U':
		res.ext = "rw2"
	}
	isCR2 := h[8] == 'C' && h[9] == 'R' && h[10] == 2
	maxEnd := int64(8)
	grow := func(end int64) {
		if end > maxEnd {
			maxEnd = end
		}
	}
	// readVals lee una lista de valores SHORT/LONG de una entrada.
	readVals := func(e []byte, limit int) []int64 {
		typ := bo.Uint16(e[2:])
		cnt := int(bo.Uint32(e[4:]))
		if cnt <= 0 || cnt > limit || (typ != 3 && typ != 4 && typ != 13) {
			return nil
		}
		sz := 4
		if typ == 3 {
			sz = 2
		}
		data := e[8:12]
		if cnt*sz > 4 {
			data = s.copyAt(off+int64(bo.Uint32(e[8:])), cnt*sz)
			if data == nil {
				return nil
			}
		}
		out := make([]int64, cnt)
		for i := range out {
			if sz == 2 {
				out[i] = int64(bo.Uint16(data[2*i:]))
			} else {
				out[i] = int64(bo.Uint32(data[4*i:]))
			}
		}
		return out
	}
	queue := []int64{int64(bo.Uint32(h[4:]))}
	visited := map[int64]bool{}
	maker := ""
	isDNG := false
	nIFD := 0
	for len(queue) > 0 && nIFD < 64 {
		io := queue[0]
		queue = queue[1:]
		if io < 8 || io >= max || visited[io] {
			continue
		}
		visited[io] = true
		cb := s.at(off+io, 2)
		if cb == nil {
			if nIFD == 0 {
				return res, false
			}
			continue
		}
		cnt := int(bo.Uint16(cb))
		if cnt == 0 || cnt > 2000 {
			if nIFD == 0 {
				return res, false
			}
			continue
		}
		eb := s.copyAt(off+io+2, cnt*12+4)
		if eb == nil {
			if nIFD == 0 {
				return res, false
			}
			continue
		}
		nIFD++
		grow(io + 2 + int64(cnt)*12 + 4)
		var offs, lens []int64
		var jOff, jLen int64
		bad := 0
		for i := 0; i < cnt; i++ {
			e := eb[i*12 : i*12+12]
			tag := bo.Uint16(e)
			typ := bo.Uint16(e[2:])
			n := int64(bo.Uint32(e[4:]))
			if typ == 0 || typ > 13 {
				bad++
				continue
			}
			if sz := tiffTypeSize[typ] * n; sz > 4 {
				vo := int64(bo.Uint32(e[8:]))
				if vo+sz <= max {
					grow(vo + sz)
				}
			}
			switch tag {
			case 273, 324:
				offs = append(offs, readVals(e, 1<<20)...)
			case 279, 325:
				lens = append(lens, readVals(e, 1<<20)...)
			case 513:
				if v := readVals(e, 1); v != nil {
					jOff = v[0]
				}
			case 514:
				if v := readVals(e, 1); v != nil {
					jLen = v[0]
				}
			case 330, 34665, 34853, 40965:
				queue = append(queue, readVals(e, 64)...)
			case 271:
				if n > 0 && n < 64 {
					if n <= 4 {
						maker = string(e[8 : 8+n])
					} else if mb := s.at(off+int64(bo.Uint32(e[8:])), int(n)); mb != nil {
						maker = string(mb)
					}
				}
			case 50706:
				isDNG = true
			}
		}
		if nIFD == 1 && bad*2 > cnt {
			return res, false
		}
		for i := 0; i < len(offs) && i < len(lens); i++ {
			grow(offs[i] + lens[i])
		}
		if jOff > 0 && jLen > 0 {
			grow(jOff + jLen)
		}
		queue = append(queue, int64(bo.Uint32(eb[cnt*12:])))
	}
	if nIFD == 0 || maxEnd < 512 || maxEnd > max {
		return res, false
	}
	if res.ext == "tif" {
		mk := bytes.ToUpper([]byte(maker))
		switch {
		case isDNG:
			res.ext = "dng"
		case isCR2:
			res.ext = "cr2"
		case bytes.HasPrefix(mk, []byte("NIKON")):
			res.ext = "nef"
		case bytes.HasPrefix(mk, []byte("SONY")):
			res.ext = "arw"
		case bytes.HasPrefix(mk, []byte("PENTAX")):
			res.ext = "pef"
		}
	}
	res.size = maxEnd
	return res, true
}

// ---------------------------------------------------------------- PSD

func carvePSD(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "psd"}
	b := s.copyAt(off, 26)
	if b == nil || !allZero(b[6:12]) {
		return res, false
	}
	ver := be16(b[4:])
	ch := int64(be16(b[12:]))
	h := int64(be32(b[14:]))
	w := int64(be32(b[18:]))
	depth := int64(be16(b[22:]))
	if ch < 1 || ch > 56 || h < 1 || w < 1 || h > 300000 || w > 300000 ||
		(depth != 1 && depth != 8 && depth != 16 && depth != 32) {
		return res, false
	}
	if ver == 2 {
		res.ext = "psb"
	}
	p := off + 26
	for i := 0; i < 3; i++ {
		var l int64
		if i == 2 && ver == 2 {
			x := s.at(p, 8)
			if x == nil {
				return res, false
			}
			l = int64(be64(x))
			p += 8
		} else {
			x := s.at(p, 4)
			if x == nil {
				return res, false
			}
			l = int64(be32(x))
			p += 4
		}
		if l < 0 || p+l-off > max {
			return res, false
		}
		p += l
	}
	cb := s.at(p, 2)
	if cb == nil {
		return res, false
	}
	p += 2
	switch be16(cb) {
	case 0:
		p += (w*depth + 7) / 8 * h * ch
	case 1:
		esz := int64(2)
		if ver == 2 {
			esz = 4
		}
		n := h * ch
		tbl := s.at(p, int(n*esz))
		if tbl == nil {
			return res, false
		}
		var sum int64
		for i := int64(0); i < n; i++ {
			if esz == 2 {
				sum += int64(be16(tbl[2*i:]))
			} else {
				sum += int64(be32(tbl[4*i:]))
			}
		}
		p += n*esz + sum
	default:
		return res, false
	}
	if p-off > max {
		return res, false
	}
	res.size = p - off
	return res, true
}

// ---------------------------------------------------------------- PDF

var pdfEOF = []byte("%%EOF")

func carvePDF(s *src, off, max int64) (carveResult, bool) {
	if max > 512<<20 {
		max = 512 << 20
	}
	res := carveResult{ext: "pdf"}
	// PDF "linealizado": la cabecera declara la longitud total.
	if h := s.upTo(off, 1024); h != nil {
		if i := bytes.Index(h, []byte("/Linearized")); i >= 0 {
			if j := bytes.Index(h[i:], []byte("/L ")); j >= 0 {
				var l int64
				for _, c := range h[i+j+3:] {
					if c < '0' || c > '9' {
						break
					}
					l = l*10 + int64(c-'0')
				}
				if l > 1024 && l <= max {
					if tail := s.at(off+l-1024, 1024); tail != nil && bytes.Contains(tail, pdfEOF) {
						res.size = l
						return res, true
					}
				}
			}
		}
	}
	// Si no, se busca la marca de fin "%%EOF" seguida de relleno de ceros
	// hasta el final del sector (así termina el último sector del archivo).
	const win = 1 << 20
	first := int64(-1)
	p := off
	for p-off < max {
		w := s.upTo(p, win)
		if len(w) < 5 {
			break
		}
		i := 0
		redo := false
		for {
			j := bytes.Index(w[i:], pdfEOF)
			if j < 0 {
				break
			}
			at := i + j
			e := at + 5
			for k := 0; k < 2 && e < len(w) && (w[e] == '\r' || w[e] == '\n'); k++ {
				e++
			}
			size := p + int64(e) - off
			pad := int((512 - size%512) % 512)
			if e+pad > len(w) && len(w) == win && at > 0 {
				p += int64(at) // repetir con la marca al principio de la ventana
				redo = true
				break
			}
			if first < 0 {
				first = size
			}
			if e+pad <= len(w) && allZero(w[e:e+pad]) {
				res.size = size
				res.walked = true
				return res, true
			}
			i = at + 5
		}
		if redo {
			continue
		}
		if first > 0 && p-off > first+32<<20 {
			break
		}
		if len(w) < win {
			break
		}
		p += int64(len(w)) - 4
	}
	if first > 0 {
		res.size = first
		return res, true
	}
	return res, false
}

// ---------------------------------------------------------------- RTF

func carveRTF(s *src, off, max int64) (carveResult, bool) {
	if max > 128<<20 {
		max = 128 << 20
	}
	res := carveResult{ext: "rtf", walked: true}
	depth := 0
	p := off
	esc := false
	for p-off < max {
		w := s.upTo(p, 256<<10)
		if len(w) == 0 {
			break
		}
		for i, c := range w {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					res.size = p + int64(i) + 1 - off
					return res, res.size > 16
				}
			case 0:
				return res, false // un RTF es texto: un byte nulo indica que ya no es el archivo
			}
		}
		p += int64(len(w))
	}
	return res, false
}

// ---------------------------------------------------------------- MPEG-TS

func carveTS(pkt, sync int, ext string) carveFn {
	return func(s *src, off, max int64) (carveResult, bool) {
		res := carveResult{ext: ext, walked: true}
		p := off
		for p-off < max {
			w := s.upTo(p, 1<<20)
			n := len(w) / pkt
			if n == 0 {
				break
			}
			i := 0
			for i < n && w[i*pkt+sync] == 0x47 {
				i++
			}
			p += int64(i * pkt)
			if i < n {
				break
			}
		}
		res.size = p - off
		return res, res.size >= int64(1000*pkt)
	}
}

// ---------------------------------------------------------------- MP3

var mp3BR = [2][3][16]int{
	{ // MPEG-1: capa I, II, III
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	},
	{ // MPEG-2 y 2.5
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}

var mp3SR = [4][3]int{{11025, 12000, 8000}, {0, 0, 0}, {22050, 24000, 16000}, {44100, 48000, 32000}}

// mp3Frame devuelve la longitud de la trama MPEG de audio cuya cabecera
// está en h (0 si no es válida) y una clave de versión/capa/frecuencia.
func mp3Frame(h []byte) (length int, key uint32) {
	if h[0] != 0xFF || h[1]&0xE0 != 0xE0 {
		return 0, 0
	}
	ver := int(h[1]>>3) & 3
	layer := int(h[1]>>1) & 3
	bri := int(h[2] >> 4)
	sri := int(h[2]>>2) & 3
	pad := int(h[2]>>1) & 1
	if ver == 1 || layer == 0 || bri == 0 || bri == 15 || sri == 3 {
		return 0, 0
	}
	v := 1
	if ver == 3 {
		v = 0
	}
	br := mp3BR[v][3-layer][bri] * 1000
	sr := mp3SR[ver][sri]
	switch layer {
	case 3: // capa I
		length = (12*br/sr + pad) * 4
	case 2: // capa II
		length = 144*br/sr + pad
	default: // capa III
		if ver == 3 {
			length = 144*br/sr + pad
		} else {
			length = 72*br/sr + pad
		}
	}
	if length < 24 {
		return 0, 0
	}
	return length, uint32(ver)<<8 | uint32(layer)<<4 | uint32(sri)
}

// mp3Sniff exige tres tramas consecutivas coherentes.
func mp3Sniff(b []byte) bool {
	p := 0
	var key uint32
	for i := 0; i < 3; i++ {
		if p+4 > len(b) {
			return false
		}
		l, k := mp3Frame(b[p:])
		if l == 0 || (i > 0 && k != key) {
			return false
		}
		key = k
		p += l
	}
	return true
}

func carveMP3(s *src, off, max int64) (carveResult, bool) {
	if max > 1<<30 {
		max = 1 << 30
	}
	res := carveResult{ext: "mp3", walked: true}
	p := off
	if b := s.at(p, 10); b != nil && string(b[:3]) == "ID3" {
		p += 10 + int64(b[6])<<21 + int64(b[7])<<14 + int64(b[8])<<7 + int64(b[9])
		if b[5]&0x10 != 0 {
			p += 10
		}
		// Algunas etiquetas dejan relleno antes de la primera trama.
		w := s.upTo(p, 8192)
		found := false
		for i := 0; i+4 <= len(w) && i < 4096; i++ {
			if w[i] == 0xFF && w[i+1]&0xE0 == 0xE0 && mp3Sniff(w[i:]) {
				p += int64(i)
				found = true
				break
			}
		}
		if !found {
			return res, false
		}
	}
	frames := 0
	var key uint32
	for p-off < max {
		h := s.at(p, 4)
		if h == nil {
			break
		}
		l, k := mp3Frame(h)
		if l == 0 || (frames > 0 && k != key) {
			break
		}
		key = k
		p += int64(l)
		frames++
	}
	if frames < 40 {
		return res, false
	}
	if t := s.at(p, 128); t != nil && string(t[:3]) == "TAG" {
		p += 128
	}
	res.size = p - off
	return res, true
}

// ---------------------------------------------------------------- OGG

func carveOGG(s *src, off, max int64) (carveResult, bool) {
	if max > 4<<30 {
		max = 4 << 30
	}
	res := carveResult{ext: "ogg", walked: true}
	p := off
	pages := 0
	for p-off < max {
		b := s.upTo(p, 27+255)
		if len(b) < 27 || string(b[:4]) != "OggS" || b[4] != 0 {
			break
		}
		nseg := int(b[26])
		if len(b) < 27+nseg {
			break
		}
		body := 0
		for _, c := range b[27 : 27+nseg] {
			body += int(c)
		}
		if pages == 0 {
			if pl := s.upTo(p+27+int64(nseg), 16); len(pl) >= 8 {
				switch {
				case string(pl[:8]) == "OpusHead":
					res.ext = "opus"
				case string(pl[1:7]) == "theora":
					res.ext = "ogv"
				}
			}
		}
		p += int64(27 + nseg + body)
		pages++
	}
	res.size = p - off
	return res, pages >= 3 && res.size >= 4096 && res.size <= max
}
