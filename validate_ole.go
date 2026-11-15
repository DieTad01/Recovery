package main

// Comprobación a fondo de documentos de Office antiguos (doc, xls, ppt):
// son un «archivo compuesto» (OLE) con su propia tabla de sectores. Se
// comprueba esa tabla, el directorio y, dentro de cada documento, la cadena
// de registros (Excel y PowerPoint) o el texto (Word).

import (
	"bytes"
	"io"
	"strconv"
	"unicode/utf16"
)

type oleFile struct {
	data    []byte
	ss      int64 // tamaño de sector
	nsec    uint32
	fat     []uint32
	minifat []uint32
	mini    []byte // flujo con los flujos pequeños
	cutoff  int64
	dir     []oleEntry
}

type oleEntry struct {
	name  string
	typ   byte
	start uint32
	size  int64
}

const (
	oleFree = 0xFFFFFFFF
	oleEnd  = 0xFFFFFFFE
	oleFAT  = 0xFFFFFFFD
	oleDIF  = 0xFFFFFFFC
)

func (o *oleFile) sector(n uint32) []byte {
	off := (int64(n) + 1) * o.ss
	if off+o.ss > int64(len(o.data)) {
		return nil
	}
	return o.data[off : off+o.ss]
}

// chain sigue una cadena de sectores; nil si está rota.
func (o *oleFile) chain(start uint32, table []uint32, limit int) []uint32 {
	var out []uint32
	seen := map[uint32]bool{}
	for n := start; n != oleEnd; {
		if int(n) >= len(table) || seen[n] || len(out) > limit {
			return nil
		}
		seen[n] = true
		out = append(out, n)
		n = table[n]
	}
	return out
}

// readStream lee un flujo; ok=false si su cadena está rota.
func (o *oleFile) readStream(e oleEntry) ([]byte, bool) {
	if e.size == 0 {
		return nil, true
	}
	if e.size < o.cutoff && e.typ == 2 {
		ch := o.chain(e.start, o.minifat, int(e.size/64)+2)
		if ch == nil || int64(len(ch))*64 < e.size {
			return nil, false
		}
		out := make([]byte, 0, e.size)
		for _, n := range ch {
			off := int64(n) * 64
			if off+64 > int64(len(o.mini)) {
				return nil, false
			}
			out = append(out, o.mini[off:off+64]...)
		}
		return out[:e.size], true
	}
	ch := o.chain(e.start, o.fat, int(e.size/o.ss)+2)
	if ch == nil || int64(len(ch))*o.ss < e.size {
		return nil, false
	}
	out := make([]byte, 0, int64(len(ch))*o.ss)
	for _, n := range ch {
		b := o.sector(n)
		if b == nil {
			return nil, false
		}
		out = append(out, b...)
	}
	return out[:e.size], true
}

func parseOLE(data []byte) (*oleFile, string) {
	if len(data) < 1536 || string(data[:8]) != "\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1" {
		return nil, "no empieza como un documento de Office"
	}
	shift := le16(data[30:])
	if shift != 9 && shift != 12 {
		return nil, "cabecera dañada"
	}
	o := &oleFile{data: data, ss: 1 << shift, cutoff: int64(le32(data[56:]))}
	if o.cutoff == 0 {
		o.cutoff = 4096
	}
	o.nsec = uint32((int64(len(data)) - o.ss) / o.ss)
	// Sectores de la tabla (los 109 primeros en la cabecera, el resto en la DIFAT).
	var fatSecs []uint32
	nFat := int(le32(data[44:]))
	for i := 0; i < 109 && len(fatSecs) < nFat; i++ {
		fatSecs = append(fatSecs, le32(data[76+4*i:]))
	}
	for next, guard := le32(data[68:]), 0; len(fatSecs) < nFat && next < oleDIF && guard < 1<<16; guard++ {
		b := o.sector(next)
		if b == nil {
			return nil, "tabla de sectores dañada"
		}
		for i := 0; i < int(o.ss)/4-1 && len(fatSecs) < nFat; i++ {
			fatSecs = append(fatSecs, le32(b[4*i:]))
		}
		next = le32(b[o.ss-4:])
	}
	if len(fatSecs) != nFat {
		return nil, "tabla de sectores incompleta"
	}
	for _, s := range fatSecs {
		b := o.sector(s)
		if b == nil {
			return nil, "tabla de sectores fuera del archivo (incompleto)"
		}
		for i := 0; i < len(b); i += 4 {
			o.fat = append(o.fat, le32(b[i:]))
		}
	}
	// Toda entrada de la tabla debe apuntar dentro del archivo o ser especial.
	for i, v := range o.fat {
		if v < oleDIF && v >= o.nsec && uint32(i) < o.nsec {
			return nil, "tabla de sectores dañada (datos sobrescritos)"
		}
	}
	// Directorio.
	dch := o.chain(le32(data[48:]), o.fat, 1<<16)
	if dch == nil {
		return nil, "directorio dañado"
	}
	for _, n := range dch {
		b := o.sector(n)
		for i := 0; i+128 <= len(b); i += 128 {
			e := b[i : i+128]
			nl := int(le16(e[64:]))
			typ := e[66]
			if typ == 0 {
				o.dir = append(o.dir, oleEntry{})
				continue
			}
			if typ != 1 && typ != 2 && typ != 5 || nl < 2 || nl > 64 || nl%2 != 0 {
				return nil, "directorio dañado (datos sobrescritos)"
			}
			u := make([]uint16, nl/2-1)
			for k := range u {
				u[k] = le16(e[2*k:])
			}
			sz := int64(le32(e[120:]))
			if o.ss == 4096 {
				sz = int64(le64(e[120:]))
			}
			o.dir = append(o.dir, oleEntry{name: string(utf16.Decode(u)), typ: typ, start: le32(e[116:]), size: sz})
		}
	}
	if len(o.dir) == 0 || o.dir[0].typ != 5 {
		return nil, "directorio dañado"
	}
	// Flujo pequeño y su tabla.
	if mfs := le32(data[60:]); mfs < oleDIF {
		ch := o.chain(mfs, o.fat, 1<<20)
		if ch == nil {
			return nil, "tabla de flujos pequeños dañada"
		}
		for _, n := range ch {
			b := o.sector(n)
			for i := 0; i < len(b); i += 4 {
				o.minifat = append(o.minifat, le32(b[i:]))
			}
		}
	}
	root := o.dir[0]
	if root.size > 0 {
		root.typ = 5
		ch := o.chain(root.start, o.fat, int(root.size/o.ss)+2)
		if ch == nil {
			return nil, "directorio dañado"
		}
		for _, n := range ch {
			o.mini = append(o.mini, o.sector(n)...)
		}
	}
	return o, ""
}

func (o *oleFile) find(name string) (oleEntry, bool) {
	for _, e := range o.dir {
		if e.typ == 2 && e.name == name {
			return e, true
		}
	}
	return oleEntry{}, false
}

// checkOLE comprueba doc/xls/ppt/msg y demás documentos compuestos.
func checkOLE(r io.ReaderAt, size int64) (int, string) {
	if size > 512<<20 {
		return vStruct, ""
	}
	data := readRange(r, 0, int(size))
	o, why := parseOLE(data)
	if o == nil {
		return vBad, "documento dañado: " + why
	}
	// Todos los flujos deben poder leerse entero.
	for _, e := range o.dir {
		if e.typ == 2 {
			if _, ok := o.readStream(e); !ok {
				return vBad, "documento dañado: la parte «" + e.name + "» está rota"
			}
		}
	}
	if e, ok := o.find("EncryptionInfo"); ok && e.size > 0 {
		return vStruct, "estructura correcta (documento con contraseña)"
	}
	if e, ok := o.find("Workbook"); ok {
		return checkBIFF(o, e)
	}
	if e, ok := o.find("Book"); ok {
		return checkBIFF(o, e)
	}
	if e, ok := o.find("PowerPoint Document"); ok {
		b, _ := o.readStream(e)
		n, ok := walkPPT(b, 0)
		if !ok {
			return vBad, "presentación dañada: su contenido se rompe (datos sobrescritos)"
		}
		if pe, has := o.find("Pictures"); has && pe.size > 0 {
			pb, _ := o.readStream(pe)
			m, ok := walkPPT(pb, 0)
			if !ok {
				return vBad, "presentación dañada: sus imágenes están rotas (datos sobrescritos)"
			}
			n += m
		}
		return vOK, "presentación íntegra (" + strconv.Itoa(n) + " registros comprobados)"
	}
	if e, ok := o.find("WordDocument"); ok {
		return checkWord(o, e)
	}
	return vStruct, "estructura del documento correcta"
}

// checkBIFF recorre los registros de una hoja de Excel 97-2003.
func checkBIFF(o *oleFile, e oleEntry) (int, string) {
	b, _ := o.readStream(e)
	if len(b) < 8 || le16(b) != 0x0809 && le16(b) != 0x0209 && le16(b) != 0x0409 {
		return vBad, "libro de Excel dañado: no empieza como debe"
	}
	n := 0
	pos := 0
	for pos+4 <= len(b) {
		typ, l := le16(b[pos:]), int(le16(b[pos+2:]))
		if typ == 0 && l == 0 && allZero(b[pos:]) {
			break // relleno final
		}
		if l > 8224 || typ > 0x1200 || pos+4+l > len(b) {
			return vBad, "libro de Excel dañado en el " + strconv.Itoa(pos*100/len(b)) + " % (datos sobrescritos)"
		}
		pos += 4 + l
		n++
	}
	return vOK, "libro de Excel íntegro (" + strconv.Itoa(n) + " registros comprobados)"
}

// walkPPT recorre los registros de PowerPoint 97-2003.
func walkPPT(b []byte, depth int) (int, bool) {
	n := 0
	for pos := 0; pos+8 <= len(b); {
		ver := le16(b[pos:]) & 0xF
		typ := le16(b[pos+2:])
		l := int(le32(b[pos+4:]))
		if typ == 0 && l == 0 && allZero(b[pos:]) {
			break
		}
		if !(typ >= 0x03E8 && typ <= 0x1772 || typ >= 0xF000 && typ <= 0xF200) || l < 0 || pos+8+l > len(b) {
			return n, false
		}
		n++
		body := b[pos+8 : pos+8+l]
		switch {
		case typ == 0x0FA0 || typ == 0x0FA8 || typ == 0x0FBA: // textos
			for k := 0; k < len(body); k += 4096 {
				if !looksLikeText(body[k:min(k+4096, len(body))], false) {
					return n, false
				}
			}
		case typ == 0xF01E || typ == 0xF01D: // imagen PNG / JPG incrustada
			if !validBlip(body, typ) {
				return n, false
			}
		}
		if ver == 0xF && depth < 8 {
			m, ok := walkPPT(b[pos+8:pos+8+l], depth+1)
			if !ok {
				return n, false
			}
			n += m
		}
		pos += 8 + l
	}
	return n, true
}

// checkWord comprueba que el texto de un documento de Word 97-2003 sigue
// siendo texto (a partir de su tabla de fragmentos de texto).
func checkWord(o *oleFile, e oleEntry) (int, string) {
	w, _ := o.readStream(e)
	if len(w) < 0x1AA || le16(w) != 0xA5EC && le16(w) != 0xA5DC {
		return vBad, "documento de Word dañado: no empieza como debe"
	}
	flags := le16(w[0x0A:])
	if flags&0x0100 != 0 { // cifrado
		return vStruct, "estructura correcta (documento con contraseña)"
	}
	tname := "0Table"
	if flags&0x0200 != 0 {
		tname = "1Table"
	}
	te, ok := o.find(tname)
	if !ok {
		return vBad, "documento de Word dañado: falta su tabla"
	}
	tb, _ := o.readStream(te)
	fc, lcb := int(le32(w[0x01A2:])), int(le32(w[0x01A6:]))
	if fc < 0 || lcb <= 0 || fc+lcb > len(tb) {
		return vBad, "documento de Word dañado: su índice de texto está fuera de lugar"
	}
	clx := tb[fc : fc+lcb]
	p := 0
	for p < len(clx) && clx[p] == 1 {
		if p+3 > len(clx) {
			return vBad, "documento de Word dañado"
		}
		p += 3 + int(le16(clx[p+1:]))
	}
	if p+5 > len(clx) || clx[p] != 2 {
		return vBad, "documento de Word dañado: índice de texto ilegible"
	}
	l := int(le32(clx[p+1:]))
	plc := clx[p+5:]
	if l > len(plc) || (l-4)%12 != 0 {
		return vBad, "documento de Word dañado: índice de texto ilegible"
	}
	n := (l - 4) / 12
	var chars int
	for i := 0; i < n; i++ {
		cp0, cp1 := int(le32(plc[4*i:])), int(le32(plc[4*i+4:]))
		pcd := plc[4*(n+1)+8*i:]
		f := le32(pcd[2:])
		cnt := cp1 - cp0
		if cnt <= 0 {
			continue
		}
		var off, nb int
		if f&0x40000000 != 0 {
			off, nb = int(f&^0x40000000)/2, cnt
		} else {
			off, nb = int(f), cnt*2
		}
		if off < 0 || off+nb > len(w) {
			return vBad, "documento de Word incompleto: falta parte del texto"
		}
		txt := w[off : off+nb]
		for k := 0; k < len(txt); k += 4096 {
			blk := txt[k:min(k+4096, len(txt))]
			if !looksLikeText(blk, false) {
				return vBad, "documento de Word dañado: parte del texto fue sobrescrita"
			}
		}
		chars += cnt
	}
	return vStruct, "documento de Word correcto (" + strconv.Itoa(chars) + " caracteres de texto comprobados)"
}

// validBlip comprueba una imagen incrustada en PowerPoint/Word (OfficeArt).
func validBlip(body []byte, typ uint16) bool {
	for _, skip := range []int{17, 33} { // 1 o 2 identificadores de 16 bytes + 1 byte
		if len(body) <= skip {
			continue
		}
		img := body[skip:]
		var v int
		if typ == 0xF01E && bytes.HasPrefix(img, []byte("\x89PNG")) {
			v, _ = validateContent(bytes.NewReader(img), int64(len(img)), "png")
		} else if typ == 0xF01D && bytes.HasPrefix(img, []byte{0xFF, 0xD8}) {
			v, _ = validateContent(bytes.NewReader(img), int64(len(img)), "jpg")
		} else {
			continue
		}
		return v != vBad
	}
	return true
}
