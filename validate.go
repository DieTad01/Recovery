package main

// Comprobación real del contenido de un archivo recuperado: no basta con
// que su espacio en el disco esté libre; hay que mirar si los datos son
// válidos. Según el formato se usan sumas de control (ZIP/Office, PNG, OGG),
// la decodificación completa (JPG, GIF), o el recorrido de su estructura
// (PDF, MP3, MP4/MOV fotograma a fotograma, RIFF, 7z...).

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Veredictos de la comprobación.
const (
	vNone    = iota // aún no comprobado
	vUnknown        // no hay forma de comprobar este tipo de archivo
	vStruct         // su estructura es correcta (el contenido no lleva sumas de control)
	vOK             // contenido comprobado: correcto
	vBad            // dañado
)

var (
	zipExts   = setOf("zip docx xlsx pptx docm xlsm pptm dotx xltx potx ppsx odt ods odp odg jar apk epub vsdx kmz cbz xpi ipa")
	plainExts = setOf("txt log csv ini xml json html htm md cfg bat cmd ps1 py js css sql srt vbs reg yml yaml rtf tex java c cpp h cs php go sh svg")
	oleExts   = setOf("doc dot xls xlt ppt pps msg vsd pub mpp")
	bmffExts  = setOf("mp4 m4v m4a m4b mov qt 3gp 3g2 heic heif avif f4v")
	// Formatos comprimidos en los que un bloque de 4 KB en blanco es imposible si el archivo está sano.
	zeroBadExts = setOf("gif webp 7z rar")
)

func setOf(s string) map[string]bool {
	m := map[string]bool{}
	for _, e := range strings.Fields(s) {
		m[e] = true
	}
	return m
}

// validateContent comprueba un archivo completo (r de tamaño size).
func validateContent(r io.ReaderAt, size int64, ext string) (int, string) {
	if size == 0 {
		return vUnknown, ""
	}
	sr := io.NewSectionReader(r, 0, size)
	// Archivos totalmente en blanco.
	if allBlank(r, size) {
		return vBad, "el contenido está en blanco"
	}
	switch {
	case zipExts[ext]:
		return checkZip(r, size)
	case ext == "png":
		if _, err := png.Decode(sr); err != nil {
			return vBad, "imagen PNG dañada (" + shortErr(err) + ")"
		}
		return vOK, "imagen PNG íntegra (sumas de control correctas)"
	case ext == "jpg" || ext == "jpeg" || ext == "jpe" || ext == "jfif":
		if _, err := jpeg.Decode(sr); err != nil {
			return vBad, "foto JPG dañada (" + shortErr(err) + ")"
		}
		if !endsWith(r, size, []byte{0xFF, 0xD9}) {
			return vBad, "foto JPG incompleta (le falta el final)"
		}
		if v, why := zeroBlocksFrom(r, jpegScanStart(r, size), size); v == vBad {
			return v, why
		}
		return vOK, "foto JPG íntegra (se decodifica completa)"
	case ext == "gif":
		if _, err := gif.DecodeAll(sr); err != nil {
			return vBad, "imagen GIF dañada (" + shortErr(err) + ")"
		}
		return vOK, "imagen GIF íntegra (se decodifica completa)"
	case ext == "pdf":
		return checkPDF(r, size)
	case bmffExts[ext]:
		return checkBMFF(r, size)
	case ext == "ogg" || ext == "oga" || ext == "opus" || ext == "ogv" || ext == "spx":
		return checkOgg(r, size)
	case ext == "mp3":
		return checkMP3(r, size)
	case ext == "sqlite" || ext == "sqlite3" || ext == "db" && checkSig("sqlite", readRange(r, 0, 16)) == 1:
		return checkSQLite(r, size)
	case ext == "7z":
		return check7z(r, size)
	case ext == "wma" || ext == "wmv" || ext == "asf":
		return checkASF(r, size)
	case ext == "avi" || ext == "wav" || ext == "webp":
		return checkRIFF(r, size)
	case ext == "mkv" || ext == "webm" || ext == "mka":
		return checkMKV(r, size)
	case oleExts[ext]:
		if checkSig("doc", readRange(r, 0, 16)) != 1 && ext == "pub" {
			return vUnknown, "" // .pub también son claves SSH
		}
		return checkOLE(r, size)
	case (ext == "ts" || ext == "mts" || ext == "m2ts") && looksLikeText(readRange(r, 0, 4096), false) &&
		checkSig("ts", readRange(r, 0, 4096)) != 1:
		return checkText(r, size, ext) // .ts de TypeScript (texto)
	case ext == "ts":
		return checkTS(r, size, 188, 0)
	case ext == "mts" || ext == "m2ts":
		return checkTS(r, size, 192, 4)
	case plainExts[ext]:
		return checkText(r, size, ext)
	}
	// Formatos con estructura que se puede recorrer (sin sumas de control).
	if fn := structCheck(ext); fn != nil {
		return fn(r, size)
	}
	return vUnknown, "este tipo de archivo no se puede comprobar"
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, ": "); i > 0 && i < 20 {
		s = s[i+2:]
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// readAll lee [off, off+n) del lector (lo que haya).
func readRange(r io.ReaderAt, off int64, n int) []byte {
	b := make([]byte, n)
	m, _ := r.ReadAt(b, off)
	return b[:m]
}

func endsWith(r io.ReaderAt, size int64, tail []byte) bool {
	n := int64(64 << 10)
	if n > size {
		n = size
	}
	b := readRange(r, size-n, int(n))
	b = bytes.TrimRight(b, "\x00")
	return bytes.HasSuffix(b, tail)
}

// eachBlock recorre el archivo en bloques de 4 KB (completo hasta 256 MB;
// en archivos mayores, una muestra repartida).
func eachBlock(r io.ReaderAt, size int64, fn func(off int64, b []byte) bool) {
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	visit := func(off int64) bool {
		n := int64(chunk)
		if off+n > size {
			n = size - off
		}
		m, _ := r.ReadAt(buf[:n], off)
		for i := 0; i+4096 <= m; i += 4096 {
			if !fn(off+int64(i), buf[i:i+4096]) {
				return false
			}
		}
		return true
	}
	if size <= 256<<20 {
		for off := int64(0); off < size; off += chunk {
			if !visit(off) {
				return
			}
		}
		return
	}
	const samples = 256
	for i := int64(0); i < samples; i++ {
		off := (size - chunk) / samples * i
		off -= off % 4096
		if !visit(off) {
			return
		}
	}
}

func allBlank(r io.ReaderAt, size int64) bool {
	blank := true
	eachBlock(r, size, func(_ int64, b []byte) bool {
		blank = allZero(b)
		return blank
	})
	if size < 4096 {
		blank = allZero(readRange(r, 0, int(size)))
	}
	return blank
}

// zeroBlocks busca bloques de 4 KB en blanco dentro del archivo (sin
// contar el final): en formatos comprimidos indican que se vació o borró.
func zeroBlocks(r io.ReaderAt, size int64) (int, string) { return zeroBlocksFrom(r, 0, size) }

func zeroBlocksFrom(r io.ReaderAt, from, size int64) (int, string) {
	bad := int64(-1)
	eachBlock(r, size, func(off int64, b []byte) bool {
		if off >= from && off+4096 < size-4096 && allZero(b) {
			bad = off
			return false
		}
		return true
	})
	if bad >= 0 {
		return vBad, "tiene zonas en blanco a partir del " + strconv.FormatInt(bad*100/size, 10) + " % del archivo"
	}
	return vOK, ""
}

// jpegScanStart recorre las cabeceras del JPG hasta el inicio de la imagen
// comprimida (marcador SOS); antes puede haber relleno legítimo.
func jpegScanStart(r io.ReaderAt, size int64) int64 {
	pos := int64(2)
	for pos+4 <= size {
		h := readRange(r, pos, 4)
		if len(h) < 4 || h[0] != 0xFF {
			return size // estructura rara: no se comprueba
		}
		if h[1] == 0xFF {
			pos++
			continue
		}
		l := int64(h[2])<<8 | int64(h[3])
		if h[1] == 0xDA {
			return pos + 2 + l
		}
		pos += 2 + l
	}
	return size
}

func checkZip(r io.ReaderAt, size int64) (int, string) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return vBad, "documento dañado: no se puede leer su índice interno"
	}
	checked := 0
	for _, f := range zr.File {
		if f.Flags&1 != 0 { // cifrado: no se puede comprobar sin la contraseña
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return vBad, "documento dañado: «" + f.Name + "» no se puede abrir"
		}
		_, err = io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			if errors.Is(err, zip.ErrChecksum) {
				return vBad, "documento dañado: error de suma de control en «" + f.Name + "»"
			}
			return vBad, "documento dañado: «" + f.Name + "» ilegible"
		}
		checked++
	}
	if checked == 0 {
		return vStruct, "estructura correcta (contenido cifrado)"
	}
	return vOK, "documento íntegro (todas las sumas de control CRC correctas)"
}

var reEOF = regexp.MustCompile(`%%EOF`)

// checkPDF: principio y final del PDF y, sobre todo, sus flujos comprimidos
// (FlateDecode llevan suma de control Adler-32): si alguno no se descomprime
// bien, esa parte del archivo se sobrescribió.
func checkPDF(r io.ReaderAt, size int64) (int, string) {
	if !bytes.Contains(readRange(r, 0, 1024), []byte("%PDF-")) {
		return vBad, "no empieza como un PDF (sobrescrito)"
	}
	n := int64(8192)
	if n > size {
		n = size
	}
	if !reEOF.Match(readRange(r, size-n, int(n))) {
		return vBad, "PDF incompleto (le falta el final)"
	}
	if size > 256<<20 {
		return vStruct, "principio y final del PDF correctos"
	}
	data := readRange(r, 0, int(size))
	if bytes.Contains(data, []byte("/Encrypt")) {
		return vStruct, "principio y final correctos (PDF protegido: su contenido va cifrado)"
	}
	var checked, covered int64
	gapStart := 0
	// Lo que hay entre los bloques de datos es la sintaxis del PDF: texto.
	gap := func(a, b int) bool {
		for k := a; k < b; k += 4096 {
			if !looksLikeText(data[k:min(k+4096, b)], false) {
				return false
			}
		}
		return true
	}
	for pos := 0; ; {
		i := bytes.Index(data[pos:], []byte("stream"))
		if i < 0 {
			break
		}
		i += pos
		pos = i + 6
		if i >= 3 && string(data[i-3:i]) == "end" {
			continue
		}
		j := pos
		if j < len(data) && data[j] == '\r' {
			j++
		}
		if j >= len(data) || data[j] != '\n' {
			continue
		}
		j++
		end := bytes.Index(data[j:], []byte("endstream"))
		if end < 0 {
			return vBad, "PDF dañado: un bloque de contenido no termina"
		}
		if !gap(gapStart, i) {
			return vBad, "PDF dañado: datos ajenos en el " + strconv.Itoa(gapStart*100/len(data)) + " % del archivo (sobrescrito)"
		}
		gapStart = j + end
		pos = j + end
		dict := data[max(0, i-1024):i]
		if k := bytes.LastIndex(dict, []byte("obj")); k >= 0 {
			dict = dict[k:]
		}
		if !bytes.Contains(dict, []byte("/FlateDecode")) {
			continue
		}
		zr, err := zlib.NewReader(bytes.NewReader(data[j : j+end]))
		if err == nil {
			_, err = io.Copy(io.Discard, zr)
		}
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return vBad, "PDF dañado: un bloque de contenido no se descomprime (" + shortErr(err) + ")"
		}
		checked++
		covered += int64(end)
	}
	if !gap(gapStart, len(bytes.TrimRight(data, "\x00"))) {
		return vBad, "PDF dañado: datos ajenos cerca del final (sobrescrito)"
	}
	if checked > 0 && covered*2 >= size {
		return vOK, "PDF íntegro (" + strconv.FormatInt(checked, 10) + " bloques con suma de control correcta)"
	}
	if checked > 0 {
		return vStruct, "estructura PDF correcta (" + strconv.FormatInt(checked, 10) + " bloques comprobados)"
	}
	return vStruct, "principio y final del PDF correctos"
}

// ---------------------------------------------------------------- texto

func checkText(r io.ReaderAt, size int64, ext string) (int, string) {
	head := readRange(r, 0, 4)
	utf16 := len(head) >= 2 && (head[0] == 0xFF && head[1] == 0xFE || head[0] == 0xFE && head[1] == 0xFF)
	bad, blank := int64(-1), int64(-1)
	eachBlock(r, size, func(off int64, b []byte) bool {
		if !looksLikeText(b, utf16) {
			bad = off
			return false
		}
		if blank < 0 && ext != "log" && allZero(b) {
			blank = off
		}
		return true
	})
	if bad < 0 && blank >= 0 {
		return vBad, "tiene zonas en blanco a partir del " + strconv.FormatInt(blank*100/size, 10) + " % (contenido perdido)"
	}
	if size < 4096 && !looksLikeText(readRange(r, 0, int(size)), utf16) {
		bad = 0
	}
	if bad >= 0 {
		return vBad, "contiene datos que no son texto a partir del " + strconv.FormatInt(bad*100/size, 10) + " % (sobrescrito)"
	}
	return vStruct, "todo el contenido es texto legible"
}

// looksLikeText distingue texto de datos binarios (lo que deja un archivo
// sobrescrito por otro): los datos comprimidos o aleatorios tienen una
// entropía que ningún texto alcanza, y los programas o datos binarios están
// llenos de bytes cero, que en texto solo aparecen en UTF-16/UTF-32 (en
// posiciones alternas). Bloques en blanco no se juzgan aquí.
func looksLikeText(b []byte, _ bool) bool {
	if len(b) < 256 {
		return true
	}
	var hist [256]int
	for _, c := range b {
		hist[c]++
	}
	if hist[0] == len(b) {
		return true
	}
	n := float64(len(b))
	e := 0.0
	for _, h := range hist {
		if h > 0 {
			p := float64(h) / n
			e -= p * math.Log2(p)
		}
	}
	if e > 7.2 {
		return false // comprimido o aleatorio
	}
	if hist[0]*100 > len(b)*3 {
		even := 0
		for i := 0; i < len(b); i += 2 {
			if b[i] == 0 {
				even++
			}
		}
		odd := hist[0] - even
		if even*10 >= hist[0]*9 || odd*10 >= hist[0]*9 || hist[0]*100 >= len(b)*60 {
			return true // UTF-16 / UTF-32
		}
		return false
	}
	return true
}

// ---------------------------------------------------------------- audio

// checkOgg comprueba la suma de control CRC de cada página Ogg.
func checkOgg(r io.ReaderAt, size int64) (int, string) {
	var pos int64
	pages := 0
	for pos < size {
		h := readRange(r, pos, 27)
		if len(h) < 27 || string(h[:4]) != "OggS" {
			if pages > 0 && tailPad(r, pos, size) {
				break // relleno final
			}
			return vBad, "audio dañado: página " + strconv.Itoa(pages+1) + " ilegible"
		}
		nseg := int(h[26])
		segs := readRange(r, pos+27, nseg)
		body := 0
		for _, s := range segs {
			body += int(s)
		}
		page := readRange(r, pos, 27+nseg+body)
		if len(page) < 27+nseg+body {
			return vBad, "audio incompleto"
		}
		want := binary.LittleEndian.Uint32(page[22:])
		copy(page[22:26], []byte{0, 0, 0, 0})
		if oggCRC(page) != want {
			return vBad, "audio dañado: error de suma de control en la página " + strconv.Itoa(pages+1)
		}
		pos += int64(len(page))
		pages++
	}
	return vOK, "audio íntegro (" + strconv.Itoa(pages) + " páginas con suma de control correcta)"
}

var oggTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		c := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

func oggCRC(b []byte) uint32 {
	var c uint32
	for _, x := range b {
		c = c<<8 ^ oggTable[byte(c>>24)^x]
	}
	return c
}

// checkMP3 recorre las tramas MPEG: en un MP3 sano van seguidas de
// principio a fin; si una zona se sobrescribió, la cadena se rompe.
func checkMP3(r io.ReaderAt, size int64) (int, string) {
	data := readRange(r, 0, int(min64(size, 64<<20)))
	pos := 0
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		pos = 10 + int(data[6])<<21 | int(data[7])<<14 | int(data[8])<<7 | int(data[9])
	}
	garbage, frames := 0, 0
	for pos+4 <= len(data) {
		if l, _ := mp3Frame(data[pos:]); l > 0 && pos+l <= len(data) {
			pos += l
			frames++
			continue
		}
		if len(data)-pos <= 128 && string(data[pos:min(pos+3, len(data))]) == "TAG" {
			break
		}
		if allZero(data[pos:min(pos+4096, len(data))]) && len(data)-pos <= 4096 {
			break // relleno final
		}
		pos++
		garbage++
	}
	if frames == 0 {
		return vBad, "no contiene audio MP3 válido"
	}
	if garbage*100 > len(data) {
		return vBad, "audio dañado: " + strconv.Itoa(garbage*100/len(data)) + " % del archivo no son datos de audio"
	}
	return vStruct, "audio MP3 completo (" + strconv.Itoa(frames) + " tramas seguidas)"
}

// check7z comprueba las sumas de control de la cabecera del 7z.
func check7z(r io.ReaderAt, size int64) (int, string) {
	h := readRange(r, 0, 32)
	if len(h) < 32 || !bytes.Equal(h[:6], sevenZMag) || crc32.ChecksumIEEE(h[12:32]) != le32(h[8:]) {
		return vBad, "comprimido 7z dañado (cabecera)"
	}
	nho, nhs := int64(le64(h[12:])), int64(le64(h[20:]))
	if 32+nho+nhs > size || nhs <= 0 || nhs > 64<<20 {
		return vBad, "comprimido 7z incompleto"
	}
	if crc32.ChecksumIEEE(readRange(r, 32+nho, int(nhs))) != le32(h[28:]) {
		return vBad, "comprimido 7z dañado (índice)"
	}
	return vStruct, "estructura 7z correcta (sumas de control de la cabecera)"
}

// ---------------------------------------------------------------- video MP4/MOV

type bmffBox struct {
	typ       string
	off, size int64 // incluye la cabecera
	hdr       int64
}

func readBoxes(r io.ReaderAt, start, end int64) ([]bmffBox, bool) {
	var out []bmffBox
	for pos := start; pos+8 <= end; {
		b := readRange(r, pos, 16)
		if len(b) < 8 {
			return out, false
		}
		sz, hdr := int64(be32(b)), int64(8)
		if sz == 1 && len(b) >= 16 {
			sz, hdr = int64(be64(b[8:])), 16
		} else if sz == 0 {
			sz = end - pos
		}
		if sz < hdr || pos+sz > end {
			return out, false
		}
		out = append(out, bmffBox{string(b[4:8]), pos, sz, hdr})
		pos += sz
	}
	return out, true
}

func findBox(bs []bmffBox, typ string) *bmffBox {
	for i := range bs {
		if bs[i].typ == typ {
			return &bs[i]
		}
	}
	return nil
}

// checkBMFF recorre la estructura y, en las pistas de video H.264/H.265,
// comprueba fotograma a fotograma que los datos tienen forma válida.
func checkBMFF(r io.ReaderAt, size int64) (int, string) {
	top, ok := readBoxes(r, 0, size)
	if !ok {
		return vBad, "video dañado: su estructura está rota"
	}
	moov, mdat := findBox(top, "moov"), findBox(top, "mdat")
	if moov == nil || mdat == nil {
		if findBox(top, "moof") != nil {
			return vStruct, "estructura correcta (video fragmentado)"
		}
		return vBad, "video incompleto (le falta el índice o los datos)"
	}
	traks, ok := readBoxes(r, moov.off+moov.hdr, moov.off+moov.size)
	if !ok {
		return vBad, "video dañado: índice ilegible"
	}
	checkedVideo := false
	for _, t := range traks {
		if t.typ != "trak" {
			continue
		}
		res, why := checkTrak(r, size, t)
		if res == vBad {
			return vBad, why
		}
		if res == vOK {
			checkedVideo = true
		}
	}
	if checkedVideo {
		return vOK, "video íntegro (fotogramas comprobados)"
	}
	return vStruct, "estructura del archivo correcta"
}

// path desciende por cajas anidadas.
func boxPath(r io.ReaderAt, b bmffBox, path ...string) *bmffBox {
	cur := b
	for _, p := range path {
		kids, ok := readBoxes(r, cur.off+cur.hdr, cur.off+cur.size)
		if !ok {
			return nil
		}
		k := findBox(kids, p)
		if k == nil {
			return nil
		}
		cur = *k
	}
	return &cur
}

func checkTrak(r io.ReaderAt, size int64, trak bmffBox) (int, string) {
	stbl := boxPath(r, trak, "mdia", "minf", "stbl")
	if stbl == nil {
		return vStruct, ""
	}
	body := func(b *bmffBox) []byte {
		if b == nil || b.size-b.hdr > 64<<20 {
			return nil
		}
		return readRange(r, b.off+b.hdr, int(b.size-b.hdr))
	}
	stsd := body(boxPath(r, *stbl, "stsd"))
	if len(stsd) < 16 {
		return vStruct, ""
	}
	format := string(stsd[12:16])
	lenSize := 0
	switch format {
	case "avc1", "avc3":
		if i := bytes.Index(stsd, []byte("avcC")); i > 0 && i+9 < len(stsd) {
			lenSize = int(stsd[i+8]&3) + 1
		}
	case "hvc1", "hev1":
		if i := bytes.Index(stsd, []byte("hvcC")); i > 0 && i+26 < len(stsd) {
			lenSize = int(stsd[i+25]&3) + 1
		}
	}
	stsz, stsc := body(boxPath(r, *stbl, "stsz")), body(boxPath(r, *stbl, "stsc"))
	stco := body(boxPath(r, *stbl, "stco"))
	co64 := stco == nil
	if co64 {
		stco = body(boxPath(r, *stbl, "co64"))
	}
	if len(stsz) < 12 || len(stsc) < 8 || len(stco) < 8 {
		return vBad, "video dañado: tablas del índice ilegibles"
	}
	// Tamaños de las muestras.
	fixed, nSamples := int64(be32(stsz[4:])), int(be32(stsz[8:]))
	sampleSize := func(i int) int64 {
		if fixed != 0 {
			return fixed
		}
		if 12+4*i+4 > len(stsz) {
			return -1
		}
		return int64(be32(stsz[12+4*i:]))
	}
	// Desplazamientos de los bloques.
	nChunks := int(be32(stco[4:]))
	chunkOff := func(i int) int64 {
		if co64 {
			if 8+8*i+8 > len(stco) {
				return -1
			}
			return int64(be64(stco[8+8*i:]))
		}
		if 8+4*i+4 > len(stco) {
			return -1
		}
		return int64(be32(stco[8+4*i:]))
	}
	// Muestras por bloque.
	nEnt := int(be32(stsc[4:]))
	type run struct{ first, per int }
	var runs []run
	for i := 0; i < nEnt && 8+12*i+12 <= len(stsc); i++ {
		runs = append(runs, run{int(be32(stsc[8+12*i:])), int(be32(stsc[12+12*i:]))})
	}
	if len(runs) == 0 || nSamples <= 0 {
		return vStruct, ""
	}
	// Recorrer todas las muestras: cada una debe caer dentro del archivo.
	step := 1
	if lenSize > 0 && nSamples > 600 {
		step = nSamples / 600 // en video se comprueban unas 600 muestras repartidas
	}
	sample, ri := 0, 0
	for c := 1; c <= nChunks && sample < nSamples; c++ {
		for ri+1 < len(runs) && runs[ri+1].first <= c {
			ri++
		}
		off := chunkOff(c - 1)
		if off < 0 {
			return vBad, "video dañado: índice incompleto"
		}
		for k := 0; k < runs[ri].per && sample < nSamples; k++ {
			sz := sampleSize(sample)
			if sz < 0 || off+sz > size {
				return vBad, "video incompleto: faltan datos al final"
			}
			if lenSize > 0 && sample%step == 0 && sz > 0 {
				if !validNALs(readRange(r, off, int(min64(sz, 8<<20))), lenSize, sz) {
					return vBad, "video dañado: el fotograma " + strconv.Itoa(sample+1) + " no es válido (datos sobrescritos)"
				}
			}
			off += sz
			sample++
		}
	}
	if lenSize > 0 {
		return vOK, ""
	}
	return vStruct, ""
}

// validNALs comprueba que una muestra de video H.264/H.265 es una
// secuencia de unidades NAL con su longitud, que encaja exactamente.
func validNALs(b []byte, lenSize int, total int64) bool {
	if int64(len(b)) < total {
		total = int64(len(b)) // muestras enormes: solo el principio
		if total < int64(lenSize)+1 {
			return false
		}
		return b[lenSize]&0x80 == 0
	}
	pos := 0
	for pos < len(b) {
		if pos+lenSize > len(b) {
			return false
		}
		var n int
		for i := 0; i < lenSize; i++ {
			n = n<<8 | int(b[pos+i])
		}
		pos += lenSize
		if n <= 0 || pos+n > len(b) || b[pos]&0x80 != 0 { // bit prohibido de la cabecera NAL
			return false
		}
		pos += n
	}
	return pos == len(b)
}

// ---------------------------------------------------------------- estructura

// structCheck reutiliza los analizadores de la búsqueda por firma: si la
// estructura recorrida coincide con el tamaño del archivo, es coherente.
func structCheck(ext string) func(io.ReaderAt, int64) (int, string) {
	var kind uint8
	switch ext {
	case "tif", "tiff", "dng", "nef", "cr2", "arw", "orf", "rw2", "pef":
		kind = kTIFF
	case "psd", "psb":
		kind = kPSD
	case "bmp":
		kind = kBMP
	case "avi", "wav", "webp":
		kind = kRIFF
	case "mkv", "webm":
		kind = kMKV
	case "wmv", "wma", "asf":
		kind = kASF
	case "rar":
		kind = kRAR
	default:
		return nil
	}
	return func(r io.ReaderAt, size int64) (int, string) {
		if size < 2048 {
			return vUnknown, "" // demasiado pequeño para juzgar su estructura
		}
		dev := &Device{Path: "archivo", Size: size, Align: 1, r: readerAtCloser{r}}
		res, ok := carvers[kind](newSrc(dev), 0, size)
		if !ok || res.partial {
			return vBad, "estructura interna rota (datos sobrescritos o incompletos)"
		}
		if res.size > size {
			return vBad, "archivo incompleto (le faltan datos)"
		}
		if zeroBadExts[ext] {
			if v, why := zeroBlocks(r, size); v == vBad {
				return v, why
			}
		}
		return vStruct, "estructura interna correcta"
	}
}

type readerAtCloser struct{ io.ReaderAt }

func (readerAtCloser) Close() error { return nil }

// tailPad indica si desde pos hasta el final solo hay ceros, y como mucho
// 64 KB (un relleno final normal; más que eso es contenido perdido).
func tailPad(r io.ReaderAt, pos, size int64) bool {
	if size-pos > 64<<10 {
		return false
	}
	return allZero(readRange(r, pos, int(size-pos)))
}
