package main

import (
	"bytes"
	"hash/crc32"
	"strings"
	"time"
)

// ---------------------------------------------------------------- ZIP y derivados (docx, xlsx, pptx, odt...)

// zipNext busca, a partir de p, el final de una entrada cuyo tamaño no
// figura en su cabecera (se escribió "en flujo" con descriptor de datos).
func zipNext(s *src, data, max int64) (next int64, ok bool) {
	const win = 1 << 20
	p := data
	for p-data < max {
		w := s.upTo(p, win)
		if len(w) < 4 {
			return 0, false
		}
		i := 0
		for {
			j := bytes.Index(w[i:], []byte("PK"))
			if j < 0 || i+j+16 > len(w) {
				break
			}
			at := i + j
			q := p + int64(at)
			switch le32(w[at:]) {
			case 0x08074b50: // descriptor con firma
				if int64(le32(w[at+8:])) == q-data {
					return q + 16, true
				}
				if at+24 <= len(w) && int64(le64(w[at+8:])) == q-data {
					return q + 24, true
				}
			case 0x04034b50, 0x02014b50: // siguiente entrada: descriptor sin firma justo antes
				if at >= 12 && int64(le32(w[at-8:])) == q-12-data {
					return q, true
				}
			}
			i = at + 2
		}
		if len(w) < win {
			return 0, false
		}
		p += int64(len(w)) - 32
	}
	return 0, false
}

func carveZIP(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "zip", walked: true}
	p := off
	entries, cdir := 0, 0
	var word, xl, ppt, visio, content, manifest, android bool
	mime := ""
	for p-off < max {
		b := s.upTo(p, 46)
		if len(b) < 22 {
			return res, false
		}
		switch le32(b) {
		case 0x04034b50:
			if len(b) < 30 {
				return res, false
			}
			flags := le16(b[6:])
			method := le16(b[8:])
			csize := int64(le32(b[18:]))
			usize := int64(le32(b[22:]))
			n := int(le16(b[26:]))
			m := int(le16(b[28:]))
			if n == 0 || n > 4096 || le16(b[4:]) > 100 {
				return res, false
			}
			nb := s.at(p+30, n+m)
			if nb == nil {
				return res, false
			}
			name := string(nb[:n])
			if csize == 0xFFFFFFFF || usize == 0xFFFFFFFF { // ZIP64: tamaños en el campo extra
				ex := nb[n:]
				for len(ex) >= 4 {
					id, l := le16(ex), int(le16(ex[2:]))
					if 4+l > len(ex) {
						break
					}
					if id == 1 {
						f := ex[4 : 4+l]
						if usize == 0xFFFFFFFF && len(f) >= 8 {
							f = f[8:]
						}
						if csize == 0xFFFFFFFF && len(f) >= 8 {
							csize = int64(le64(f))
						}
						break
					}
					ex = ex[4+l:]
				}
				if csize == 0xFFFFFFFF {
					return res, false
				}
			}
			data := p + 30 + int64(n+m)
			switch {
			case name == "[Content_Types].xml":
				content = true
			case strings.HasPrefix(name, "word/"):
				word = true
			case strings.HasPrefix(name, "xl/"):
				xl = true
			case strings.HasPrefix(name, "ppt/"):
				ppt = true
			case strings.HasPrefix(name, "visio/"):
				visio = true
			case name == "META-INF/MANIFEST.MF":
				manifest = true
			case name == "AndroidManifest.xml":
				android = true
			case name == "mimetype" && entries == 0 && method == 0 && csize > 0 && csize < 128:
				if mb := s.at(data, int(csize)); mb != nil {
					mime = string(mb)
				}
			}
			entries++
			if flags&8 != 0 && csize == 0 {
				next, ok := zipNext(s, data, max-(data-off))
				if !ok {
					return res, false
				}
				p = next
			} else {
				p = data + csize
			}
		case 0x02014b50:
			if len(b) < 46 {
				return res, false
			}
			p += 46 + int64(le16(b[28:])) + int64(le16(b[30:])) + int64(le16(b[32:]))
			cdir++
		case 0x06064b50:
			p += 12 + int64(le64(b[4:]))
		case 0x07064b50:
			p += 20
		case 0x08074b50:
			p += 16
		case 0x05054b50:
			p += 6 + int64(le16(b[4:]))
		case 0x06054b50:
			if entries == 0 || cdir == 0 {
				return res, false
			}
			res.size = p + 22 + int64(le16(b[20:])) - off
			switch {
			case content && word:
				res.ext = "docx"
			case content && xl:
				res.ext = "xlsx"
			case content && ppt:
				res.ext = "pptx"
			case content && visio:
				res.ext = "vsdx"
			case strings.HasPrefix(mime, "application/vnd.oasis.opendocument.text"):
				res.ext = "odt"
			case strings.HasPrefix(mime, "application/vnd.oasis.opendocument.spreadsheet"):
				res.ext = "ods"
			case strings.HasPrefix(mime, "application/vnd.oasis.opendocument.presentation"):
				res.ext = "odp"
			case strings.HasPrefix(mime, "application/epub"):
				res.ext = "epub"
			case android:
				res.ext = "apk"
			case manifest:
				res.ext = "jar"
			}
			return res, res.size <= max
		default:
			return res, false
		}
	}
	return res, false
}

// ---------------------------------------------------------------- OLE2 (doc, xls, ppt antiguos)

func carveOLE(s *src, off, max int64) (carveResult, bool) {
	if max > 2<<30 {
		max = 2 << 30
	}
	res := carveResult{ext: "doc"}
	h := s.copyAt(off, 512)
	if h == nil || le16(h[28:]) != 0xFFFE {
		return res, false
	}
	shift := le16(h[30:])
	if shift != 9 && shift != 12 {
		return res, false
	}
	ss := int64(1) << shift
	nFat := int(le32(h[44:]))
	dirStart := le32(h[48:])
	next := le32(h[68:])
	if nFat <= 0 || nFat > 1<<16 {
		return res, false
	}
	secOff := func(n uint32) int64 { return off + (int64(n)+1)*ss }
	fat := make([]uint32, 0, nFat)
	for i := 0; i < 109 && len(fat) < nFat; i++ {
		v := le32(h[76+4*i:])
		if v >= 0xFFFFFFFA {
			break
		}
		fat = append(fat, v)
	}
	for guard := 0; len(fat) < nFat && next < 0xFFFFFFFA && guard < 4096; guard++ {
		b := s.copyAt(secOff(next), int(ss))
		if b == nil {
			return res, false
		}
		for i := 0; i < int(ss)/4-1 && len(fat) < nFat; i++ {
			v := le32(b[4*i:])
			if v >= 0xFFFFFFFA {
				break
			}
			fat = append(fat, v)
		}
		next = le32(b[ss-4:])
	}
	if len(fat) != nFat {
		return res, false
	}
	per := int(ss) / 4
	maxSec := int64(-1)
	for i := nFat - 1; i >= 0 && maxSec < 0; i-- {
		b := s.at(secOff(fat[i]), int(ss))
		if b == nil {
			return res, false
		}
		for j := per - 1; j >= 0; j-- {
			if le32(b[4*j:]) != 0xFFFFFFFF {
				maxSec = int64(i*per + j)
				break
			}
		}
	}
	if maxSec < 0 {
		return res, false
	}
	size := (maxSec + 2) * ss
	if size > max || size < 3*ss {
		return res, false
	}
	// Tipo de documento: nombres de los flujos del directorio.
	fatEntry := func(n uint32) uint32 {
		idx := int(n) / per
		if idx >= len(fat) {
			return 0xFFFFFFFE
		}
		b := s.at(secOff(fat[idx])+int64(int(n)%per)*4, 4)
		if b == nil {
			return 0xFFFFFFFE
		}
		return le32(b)
	}
	sec := dirStart
	names := map[string]bool{}
	for i := 0; i < 32 && sec < 0xFFFFFFFA; i++ {
		b := s.copyAt(secOff(sec), int(ss))
		if b == nil {
			break
		}
		for e := 0; e+128 <= len(b); e += 128 {
			nl := int(le16(b[e+64:]))
			if nl >= 2 && nl <= 64 && b[e+66] != 0 {
				names[utf16String(b[e:e+nl-2])] = true
			}
		}
		sec = fatEntry(sec)
	}
	if !names["Root Entry"] && len(names) == 0 {
		return res, false
	}
	switch {
	case names["WordDocument"]:
		res.ext = "doc"
	case names["Workbook"] || names["Book"]:
		res.ext = "xls"
	case names["PowerPoint Document"]:
		res.ext = "ppt"
	case names["VisioDocument"]:
		res.ext = "vsd"
	case names["__properties_version1.0"]:
		res.ext = "msg"
	case names["Quill"]:
		res.ext = "pub"
	case names["Catalog"]:
		return res, false // thumbs.db: miniaturas del sistema, sin interés
	default:
		res.ext = "ole"
	}
	res.size = size
	return res, true
}

// ---------------------------------------------------------------- MP4 / MOV / M4A / 3GP / HEIC

var bmffTop = map[string]bool{
	"ftyp": true, "moov": true, "mdat": true, "free": true, "skip": true, "wide": true, "pnot": true,
	"uuid": true, "meta": true, "moof": true, "mfra": true, "sidx": true, "styp": true, "pdin": true,
	"udta": true, "emsg": true, "prft": true, "ssix": true,
}

func bmffExt(brand string) string {
	switch brand {
	case "qt  ":
		return "mov"
	case "M4A ", "M4B ", "M4P ":
		return "m4a"
	case "M4V ", "M4VH", "M4VP":
		return "m4v"
	case "heic", "heix", "hevc", "heim", "heis", "hevm", "hevs", "mif1", "msf1":
		return "heic"
	case "avif", "avis":
		return "avif"
	case "crx ":
		return "cr3"
	}
	if strings.HasPrefix(brand, "3gp") || strings.HasPrefix(brand, "3g2") {
		return "3gp"
	}
	return "mp4"
}

func carveBMFF(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "mov"}
	p := off
	var moov, mdat, meta bool
	n := 0
	for p-off < max {
		b := s.upTo(p, 16)
		if len(b) < 8 {
			break
		}
		size := int64(be32(b))
		typ := string(b[4:8])
		if !bmffTop[typ] {
			break
		}
		hdr := int64(8)
		if size == 1 {
			if len(b) < 16 {
				break
			}
			size = int64(be64(b[8:]))
			hdr = 16
		}
		if size < hdr || size > max-(p-off) {
			break // tamaño desconocido o que se sale del disco
		}
		switch typ {
		case "ftyp":
			if n == 0 && len(b) >= 12 {
				res.ext = bmffExt(string(b[8:12]))
			}
		case "moov":
			moov = true
			// Fecha de creación (mvhd) para el nombre del archivo.
			if mv := s.upTo(p+hdr, 32); len(mv) >= 24 && string(mv[4:8]) == "mvhd" {
				var ct int64
				if mv[8] == 1 && len(mv) >= 28 {
					ct = int64(be64(mv[12:]))
				} else {
					ct = int64(be32(mv[12:]))
				}
				if t := time.Unix(ct-2082844800, 0); t.Year() >= 2000 && t.Year() <= 2100 {
					res.mtime = t
					res.stamp = t.Local().Format("20060102_150405")
				}
			}
		case "moof":
			moov = true
		case "mdat":
			mdat = true
		case "meta":
			meta = true
		}
		p += size
		n++
	}
	if !mdat || !(moov || meta) {
		return res, false
	}
	res.size = p - off
	return res, res.size >= 4096
}

// ---------------------------------------------------------------- RIFF (AVI, WAV, WEBP)

func riffExt(form string) string {
	switch form {
	case "AVI ":
		return "avi"
	case "WAVE":
		return "wav"
	case "WEBP":
		return "webp"
	case "ACON":
		return "ani"
	case "RMID":
		return "mid"
	}
	if strings.HasPrefix(form, "CDR") {
		return "cdr"
	}
	return ""
}

func carveRIFF(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{}
	b := s.at(off, 16)
	if b == nil {
		return res, false
	}
	res.ext = riffExt(string(b[8:12]))
	total := int64(le32(b[4:])) + 8
	if res.ext == "" || total < 20 || total > max || !isAlpha4Loose(b[12:16]) {
		return res, false
	}
	if res.ext == "avi" { // AVI de más de 1 GB: varios bloques RIFF seguidos
		for {
			x := s.at(off+total, 12)
			if x == nil || string(x[:4]) != "RIFF" || string(x[8:12]) != "AVIX" {
				break
			}
			total += int64(le32(x[4:])) + 8
		}
		if total > max {
			return res, false
		}
	}
	res.size = total
	return res, true
}

func isAlpha4Loose(b []byte) bool {
	for _, c := range b[:4] {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- Matroska (MKV, WebM)

// ebmlSize lee un tamaño EBML de longitud variable.
func ebmlSize(b []byte) (v int64, n int, unknown, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false, false
	}
	n = 1
	mask := byte(0x80)
	for b[0]&mask == 0 {
		n++
		mask >>= 1
	}
	if n > len(b) {
		return 0, 0, false, false
	}
	u := uint64(b[0] & (mask - 1))
	ones := u == uint64(mask-1)
	for i := 1; i < n; i++ {
		u = u<<8 | uint64(b[i])
		if b[i] != 0xFF {
			ones = false
		}
	}
	return int64(u), n, ones, true
}

var mkvLevel1 = [][]byte{
	{0x11, 0x4D, 0x9B, 0x74}, {0x15, 0x49, 0xA9, 0x66}, {0x16, 0x54, 0xAE, 0x6B}, {0x1F, 0x43, 0xB6, 0x75},
	{0x1C, 0x53, 0xBB, 0x6B}, {0x19, 0x41, 0xA4, 0x69}, {0x10, 0x43, 0xA7, 0x70}, {0x12, 0x54, 0xC3, 0x67},
	{0xEC}, {0xBF},
}

func carveMKV(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "mkv"}
	b := s.upTo(off, 12)
	if len(b) < 12 {
		return res, false
	}
	hsz, n, unk, ok := ebmlSize(b[4:])
	if !ok || unk || hsz > 512 {
		return res, false
	}
	hdrEnd := off + 4 + int64(n) + hsz
	hb := s.at(off+4+int64(n), int(hsz))
	if hb == nil {
		return res, false
	}
	if bytes.Contains(hb, []byte("webm")) {
		res.ext = "webm"
	} else if !bytes.Contains(hb, []byte("matroska")) {
		return res, false
	}
	sb := s.upTo(hdrEnd, 12)
	if len(sb) < 12 || sb[0] != 0x18 || sb[1] != 0x53 || sb[2] != 0x80 || sb[3] != 0x67 {
		return res, false
	}
	ssz, sn, unk, ok := ebmlSize(sb[4:])
	if !ok {
		return res, false
	}
	dataStart := hdrEnd + 4 + int64(sn)
	if !unk {
		res.size = dataStart + ssz - off
		return res, res.size <= max && res.size > 1024
	}
	// Tamaño no declarado (grabación en directo): recorrer los elementos.
	p := dataStart
	for p-off < max {
		eb := s.upTo(p, 16)
		if len(eb) < 6 {
			break
		}
		idLen := 0
		for _, id := range mkvLevel1 {
			if bytes.HasPrefix(eb, id) {
				idLen = len(id)
				break
			}
		}
		if idLen == 0 {
			break
		}
		esz, en, unk, ok := ebmlSize(eb[idLen:])
		if !ok || unk || esz > max-(p-off) {
			break
		}
		p += int64(idLen+en) + esz
	}
	res.size = p - off
	res.walked = true
	return res, p > dataStart+1024
}

// ---------------------------------------------------------------- ASF (WMV, WMA)

var (
	asfFileProps   = []byte("\xA1\xDC\xAB\x8C\x47\xA9\xCF\x11\x8E\xE4\x00\xC0\x0C\x20\x53\x65")
	asfStreamProps = []byte("\x91\x07\xDC\xB7\xB7\xA9\xCF\x11\x8E\xE6\x00\xC0\x0C\x20\x53\x65")
	asfVideo       = []byte("\xC0\xEF\x19\xBC\x4D\x5B\xCF\x11\xA8\xFD\x00\x80\x5F\x5C\x44\x2B")
)

func carveASF(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "wma"}
	b := s.at(off, 30)
	if b == nil {
		return res, false
	}
	hsize := int64(le64(b[16:]))
	nobj := int(le32(b[24:]))
	if hsize < 30 || hsize > 16<<20 || hsize > max {
		return res, false
	}
	hb := s.at(off, int(hsize))
	if hb == nil {
		return res, false
	}
	var fileSize int64
	p := 30
	for i := 0; i < nobj && p+24 <= len(hb); i++ {
		osz := int(le64(hb[p+16:]))
		if osz < 24 || p+osz > len(hb) {
			break
		}
		switch {
		case bytes.Equal(hb[p:p+16], asfFileProps) && osz >= 48:
			fileSize = int64(le64(hb[p+40:]))
		case bytes.Equal(hb[p:p+16], asfStreamProps) && osz >= 40:
			if bytes.Equal(hb[p+24:p+40], asfVideo) {
				res.ext = "wmv"
			}
		}
		p += osz
	}
	if fileSize < hsize || fileSize > max {
		return res, false
	}
	res.size = fileSize
	return res, true
}

// ---------------------------------------------------------------- RAR

func rarVint(b []byte) (v uint64, n int, ok bool) {
	for n < len(b) && n < 10 {
		c := b[n]
		v |= uint64(c&0x7F) << uint(7*n)
		n++
		if c&0x80 == 0 {
			return v, n, true
		}
	}
	return 0, 0, false
}

func carveRAR(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "rar", walked: true}
	b := s.at(off, 8)
	if b == nil {
		return res, false
	}
	files := 0
	var p int64
	switch {
	case b[6] == 0: // RAR 1.5 - 4.x
		p = off + 7
		for p-off < max {
			h := s.upTo(p, 40)
			if len(h) < 11 {
				break
			}
			typ := h[2]
			flags := le16(h[3:])
			hsize := int64(le16(h[5:]))
			if typ < 0x72 || typ > 0x7B || hsize < 7 {
				break
			}
			var add int64
			if typ == 0x74 || typ == 0x7A {
				add = int64(le32(h[7:]))
				if flags&0x100 != 0 && len(h) >= 36 {
					add |= int64(le32(h[32:])) << 32
				}
				if typ == 0x74 {
					files++
				}
			} else if flags&0x8000 != 0 {
				add = int64(le32(h[7:]))
			}
			if hsize+add > max-(p-off) {
				return res, false
			}
			p += hsize + add
			if typ == 0x7B {
				break
			}
		}
	case b[6] == 1 && b[7] == 0: // RAR 5
		p = off + 8
		for p-off < max {
			h := s.upTo(p, 64)
			if len(h) < 8 {
				break
			}
			hs, n1, ok := rarVint(h[4:])
			if !ok || hs == 0 || hs > 2<<20 {
				break
			}
			rest := h[4+n1:]
			typ, n2, ok1 := rarVint(rest)
			if !ok1 {
				break
			}
			flags, n3, ok2 := rarVint(rest[n2:])
			if !ok2 || typ < 1 || typ > 5 {
				break
			}
			q := n2 + n3
			if flags&1 != 0 {
				_, n4, ok := rarVint(rest[q:])
				if !ok {
					break
				}
				q += n4
			}
			var dsz uint64
			if flags&2 != 0 {
				var ok bool
				if dsz, _, ok = rarVint(rest[q:]); !ok {
					break
				}
			}
			tot := int64(4+n1) + int64(hs) + int64(dsz)
			if dsz > 1<<50 || tot > max-(p-off) {
				return res, false
			}
			p += tot
			if typ == 2 {
				files++
			}
			if typ == 5 {
				break
			}
		}
	default:
		return res, false
	}
	if files == 0 {
		return res, false
	}
	res.size = p - off
	return res, true
}

// ---------------------------------------------------------------- 7z / SQLite / PST

func carve7Z(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "7z"}
	b := s.at(off, 32)
	if b == nil || b[6] != 0 || crc32.ChecksumIEEE(b[12:32]) != le32(b[8:]) {
		return res, false
	}
	nho := int64(le64(b[12:]))
	nhs := int64(le64(b[20:]))
	if nho < 0 || nhs < 0 || nho > max || nhs > max {
		return res, false
	}
	res.size = 32 + nho + nhs
	return res, res.size <= max && res.size > 32
}

func carveSQLite(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "sqlite"}
	b := s.at(off, 100)
	if b == nil {
		return res, false
	}
	ps := int64(be16(b[16:]))
	if ps == 1 {
		ps = 65536
	}
	pages := int64(be32(b[28:]))
	if ps < 512 || ps&(ps-1) != 0 || pages == 0 || be32(b[24:]) != be32(b[92:]) {
		return res, false
	}
	res.size = ps * pages
	return res, res.size <= max
}

func carvePST(s *src, off, max int64) (carveResult, bool) {
	res := carveResult{ext: "pst"}
	b := s.at(off, 192)
	if b == nil {
		return res, false
	}
	if b[9] == 'O' {
		res.ext = "ost"
	}
	ver := le16(b[10:])
	switch {
	case ver >= 23: // Unicode
		res.size = int64(le64(b[184:]))
	case ver == 14 || ver == 15: // ANSI
		res.size = int64(le32(b[168:]))
	default:
		return res, false
	}
	return res, res.size >= 4096 && res.size <= max
}
