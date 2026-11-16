package main

// Rescate de datos de documentos de Office 97-2003 (xls, doc, ppt).
//
// Estos formatos no se pueden rehacer pieza a pieza como los modernos, pero
// sus datos se pueden leer directamente: las celdas de cada hoja de Excel
// (registros con fila, columna y valor), el texto de Word y el de las
// diapositivas de PowerPoint. Se guardan en un Excel nuevo (solo valores) o
// en un archivo de texto. Si la tabla interna del documento está dañada, se
// supone que sus partes están seguidas en el disco (lo habitual).

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// oleStream lee un flujo del documento; si su cadena está rota, lo lee
// suponiendo que sus sectores van seguidos.
func oleStream(o *oleFile, name string) []byte {
	e, ok := o.find(name)
	if !ok {
		return nil
	}
	if b, ok := o.readStream(e); ok {
		return b
	}
	if e.size >= o.cutoff {
		off := (int64(e.start) + 1) * o.ss
		if off < int64(len(o.data)) {
			end := min(off+e.size, int64(len(o.data)))
			return o.data[off:end]
		}
	}
	return nil
}

// findStream busca el principio de un flujo por su firma, en límites de
// sector, cuando ni siquiera la cabecera del documento se puede leer.
func findStream(data []byte, sig []byte) []byte {
	for off := 512; off+len(sig) <= len(data); off += 512 {
		if bytes.Equal(data[off:off+len(sig)], sig) {
			return data[off:]
		}
	}
	return nil
}

func repairOLE(data []byte, ext string) *repairOut {
	out := &repairOut{}
	o, _ := parseOLE(data)
	switch ext {
	case "xls", "xlt":
		var wb []byte
		if o != nil {
			if wb = oleStream(o, "Workbook"); wb == nil {
				wb = oleStream(o, "Book")
			}
		}
		if wb == nil {
			wb = findStream(data, []byte{0x09, 0x08, 0x10, 0x00, 0x00, 0x06, 0x05, 0x00})
		}
		if wb == nil {
			wb = data // se buscan directamente los principios de hoja
		}
		sheets, notes := biffValues(wb)
		out.Notes = notes
		cells := 0
		for _, s := range sheets {
			for _, r := range s.Rows {
				cells += len(r.Cells)
			}
		}
		if cells > 0 {
			out.Rescue, out.RescueExt, out.RescueTag = buildValuesXLSX(sheets), "xlsx", "datos rescatados"
			out.Notes = append(out.Notes, fmt.Sprintf("%s rescatadas de %s", plural(cells, "celda", "celdas"), plural(len(sheets), "hoja", "hojas")))
		} else {
			out.Notes = append(out.Notes, "no quedan datos legibles en el libro")
		}
	case "doc", "dot":
		var w, t0, t1 []byte
		if o != nil {
			w, t0, t1 = oleStream(o, "WordDocument"), oleStream(o, "0Table"), oleStream(o, "1Table")
		}
		if w == nil {
			w = findStream(data, []byte{0xEC, 0xA5})
		}
		txt, how := wordText(w, t0, t1)
		if strings.TrimSpace(txt) != "" {
			out.Rescue, out.RescueExt, out.RescueTag = utf8BOM(txt), "txt", "texto rescatado"
			out.Notes = append(out.Notes, how)
		} else {
			out.Notes = append(out.Notes, "no queda texto legible en el documento")
		}
	case "ppt", "pps":
		var p []byte
		if o != nil {
			p = oleStream(o, "PowerPoint Document")
		}
		if p == nil {
			p = data
		}
		if txt := pptText(p); strings.TrimSpace(txt) != "" {
			out.Rescue, out.RescueExt, out.RescueTag = utf8BOM(txt), "txt", "texto rescatado"
			out.Notes = append(out.Notes, "se rescató el texto de las diapositivas")
		} else {
			out.Notes = append(out.Notes, "no queda texto legible en la presentación")
		}
	default:
		out.Notes = append(out.Notes, "este tipo de documento no se puede reparar")
	}
	return out
}

// ---------------------------------------------------------------- Excel 97-2003

type biffSheet struct {
	name string
	pos  int
}

// biffValues lee las celdas de un libro de Excel 97-2003.
func biffValues(wb []byte) ([]valSheet, []string) {
	var notes []string
	if len(wb) < 8 {
		return nil, notes
	}
	// Bloque global: lista de hojas y textos compartidos.
	var sheets []biffSheet
	var sst []string
	pos := 0
	var sstRecs [][]byte
	inSST := false
	for pos+4 <= len(wb) {
		typ, l := le16(wb[pos:]), int(le16(wb[pos+2:]))
		if pos+4+l > len(wb) || l > 8224 {
			notes = append(notes, "la parte general del libro está dañada")
			break
		}
		body := wb[pos+4 : pos+4+l]
		switch {
		case typ == 0x0085 && l >= 8: // BOUNDSHEET
			off := int(le32(body))
			name := biffShortString(body[6:])
			if body[5] == 0 { // hoja de cálculo (no gráfico ni macro)
				sheets = append(sheets, biffSheet{name, off})
			}
		case typ == 0x00FC:
			sstRecs, inSST = [][]byte{body}, true
		case typ == 0x003C && inSST:
			sstRecs = append(sstRecs, body)
		default:
			inSST = false
		}
		pos += 4 + l
		if typ == 0x000A { // EOF del bloque global
			break
		}
	}
	if sstRecs != nil {
		sst = parseSST(sstRecs)
	}
	var out []valSheet
	if len(sheets) == 0 {
		// Sin lista de hojas: se buscan los principios de hoja (BOF).
		for i := 0; i+8 <= len(wb); i++ {
			if wb[i] == 0x09 && wb[i+1] == 0x08 && le16(wb[i+4:]) == 0x0600 && le16(wb[i+6:]) == 0x0010 {
				sheets = append(sheets, biffSheet{"", i})
			}
		}
	}
	for i, sh := range sheets {
		if sh.pos < 0 || sh.pos >= len(wb) {
			continue
		}
		vs := valSheet{Name: sh.name}
		if vs.Name == "" {
			vs.Name = fmt.Sprintf("Hoja%d", i+1)
		}
		rows, ok := biffSheetValues(wb[sh.pos:], sst)
		vs.Rows = rows
		if !ok {
			notes = append(notes, "la hoja «"+vs.Name+"» está dañada: se rescató hasta el punto del daño")
		}
		out = append(out, vs)
	}
	return out, notes
}

func biffShortString(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	n, flag := int(b[0]), b[1]
	b = b[2:]
	if flag&1 != 0 {
		if len(b) < 2*n {
			n = len(b) / 2
		}
		u := make([]uint16, n)
		for i := range u {
			u[i] = le16(b[2*i:])
		}
		return string(utf16.Decode(u))
	}
	if len(b) < n {
		n = len(b)
	}
	return latin1(b[:n])
}

func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(cp1252[c])
	}
	return string(r)
}

// cp1252 convierte los bytes de Windows-1252 (el español en Office antiguo).
var cp1252 = func() [256]uint16 {
	var t [256]uint16
	for i := range t {
		t[i] = uint16(i)
	}
	hi := []uint16{0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
		0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178}
	copy(t[0x80:], hi)
	return t
}()

// parseSST lee la tabla de textos compartidos (con sus continuaciones).
func parseSST(recs [][]byte) []string {
	if len(recs[0]) < 8 {
		return nil
	}
	total := int(le32(recs[0][4:]))
	var out []string
	ri, p := 0, 8
	cur := recs[0]
	next := func() bool {
		ri++
		if ri >= len(recs) {
			return false
		}
		cur, p = recs[ri], 0
		return true
	}
	for len(out) < total {
		if p >= len(cur) && !next() {
			break
		}
		if p+3 > len(cur) {
			break
		}
		n := int(le16(cur[p:]))
		flags := cur[p+2]
		p += 3
		runs, ext := 0, 0
		if flags&8 != 0 {
			if p+2 > len(cur) {
				break
			}
			runs = int(le16(cur[p:]))
			p += 2
		}
		if flags&4 != 0 {
			if p+4 > len(cur) {
				break
			}
			ext = int(le32(cur[p:]))
			p += 4
		}
		wide := flags&1 != 0
		var sb []rune
		for len(sb) < n {
			if p >= len(cur) {
				if !next() || len(cur) == 0 {
					return out
				}
				wide = cur[0]&1 != 0 // cada continuación indica de nuevo el ancho
				p = 1
			}
			if wide {
				if p+2 > len(cur) {
					p = len(cur)
					continue
				}
				sb = append(sb, rune(le16(cur[p:])))
				p += 2
			} else {
				sb = append(sb, rune(cp1252[cur[p]]))
				p++
			}
		}
		out = append(out, string(utf16.Decode(runesTo16(sb))))
		skip := runs*4 + ext
		for skip > 0 {
			if p >= len(cur) && !next() {
				return out
			}
			k := min(skip, len(cur)-p)
			p += k
			skip -= k
		}
	}
	return out
}

func runesTo16(r []rune) []uint16 {
	u := make([]uint16, len(r))
	for i, c := range r {
		u[i] = uint16(c)
	}
	return u
}

// biffSheetValues lee las celdas de una hoja (desde su BOF hasta su EOF).
func biffSheetValues(b []byte, sst []string) ([]valRow, bool) {
	rows := map[int][]valCell{}
	var recent []int // filas de las últimas celdas añadidas
	put := func(r, c int, text string, num bool) {
		if text != "" && r >= 0 && r < 65536 && c >= 0 && c < 256 {
			rows[r] = append(rows[r], valCell{Col: c, Text: text, Num: num})
			recent = append(recent, r)
			if len(recent) > 8 {
				recent = recent[1:]
			}
		}
	}
	// Las dos últimas celdas antes del daño pueden estar a medio sobrescribir.
	dropRecent := func() {
		for k := 0; k < 2 && len(recent) > 0; k++ {
			r := recent[len(recent)-1]
			recent = recent[:len(recent)-1]
			if cs := rows[r]; len(cs) > 0 {
				rows[r] = cs[:len(cs)-1]
			}
		}
	}
	ok := true
	pending := [2]int{-1, -1} // celda con fórmula de texto: el valor viene en el registro STRING
	lastRow := 0
	for pos := 0; pos+4 <= len(b); {
		typ, l := le16(b[pos:]), int(le16(b[pos+2:]))
		if pos+4+l > len(b) || l > 8224 || typ > 0x1200 || pos == 0 && typ != 0x0809 {
			ok = false
			dropRecent()
			// Datos dañados: se busca dónde vuelven a empezar registros de
			// celdas sanos (los datos de Excel 97 no van comprimidos, así que
			// lo que hay después del daño se puede leer entero).
			next := biffResync(b, pos+1, lastRow)
			if next < 0 {
				break
			}
			pos = next
			continue
		}
		d := b[pos+4 : pos+4+l]
		r, c := 0, 0
		if l >= 4 {
			r, c = int(le16(d)), int(le16(d[2:]))
			if biffCell[typ] && r > lastRow {
				lastRow = r
			}
		}
		switch typ {
		case 0x00FD: // LABELSST
			if l >= 10 {
				if i := int(le32(d[6:])); i < len(sst) {
					put(r, c, sst[i], false)
				} else {
					put(r, c, "[texto perdido]", false)
				}
			}
		case 0x0203: // NUMBER
			if l >= 14 {
				put(r, c, fmtNum(math.Float64frombits(binary.LittleEndian.Uint64(d[6:]))), true)
			}
		case 0x027E: // RK
			if l >= 10 {
				put(r, c, fmtNum(rkValue(le32(d[6:]))), true)
			}
		case 0x00BD: // MULRK
			for i := 0; 4+6*i+6 <= l-2; i++ {
				put(r, c+i, fmtNum(rkValue(le32(d[4+6*i+2:]))), true)
			}
		case 0x0204: // LABEL
			if l >= 9 {
				put(r, c, biffLongString(d[6:]), false)
			}
		case 0x0006: // FORMULA: se toma el valor calculado
			if l >= 14 {
				v := d[6:14]
				if le16(v[6:]) == 0xFFFF {
					switch v[0] {
					case 0:
						pending = [2]int{r, c}
					case 1:
						put(r, c, map[bool]string{true: "VERDADERO", false: "FALSO"}[v[2] != 0], false)
					}
				} else {
					put(r, c, fmtNum(math.Float64frombits(binary.LittleEndian.Uint64(v))), true)
				}
			}
		case 0x0207: // STRING (resultado de la fórmula anterior)
			if pending[0] >= 0 && l >= 3 {
				put(pending[0], pending[1], biffLongString(d), false)
			}
			pending = [2]int{-1, -1}
		case 0x0205: // BOOLERR
			if l >= 8 && d[7] == 0 {
				put(r, c, map[bool]string{true: "VERDADERO", false: "FALSO"}[d[6] != 0], false)
			}
		}
		pos += 4 + l
		if typ == 0x000A {
			break
		}
	}
	keys := make([]int, 0, len(rows))
	for r := range rows {
		keys = append(keys, r)
	}
	sortInts(keys)
	out := make([]valRow, 0, len(keys))
	for _, r := range keys {
		cs := rows[r]
		sortCells(cs)
		out = append(out, valRow{Row: r + 1, Cells: cs})
	}
	return out, ok
}

// Registros de celdas y filas (los que van seguidos en la parte de datos de una hoja).
var biffCell = map[uint16]bool{0x00FD: true, 0x0203: true, 0x027E: true, 0x00BD: true, 0x0204: true,
	0x0006: true, 0x0205: true, 0x0201: true, 0x00BE: true}

// biffResync busca, a partir de from, una cadena de al menos 6 registros de
// celdas o filas válidos y coherentes (filas que no retroceden).
func biffResync(b []byte, from, lastRow int) int {
	valid := func(p int) (int, bool) {
		if p+4 > len(b) {
			return 0, false
		}
		typ, l := le16(b[p:]), int(le16(b[p+2:]))
		if p+4+l > len(b) {
			return 0, false
		}
		switch {
		case biffCell[typ]:
			if l < 6 || typ == 0x00FD && l != 10 || typ == 0x0203 && l != 14 || typ == 0x027E && l != 10 {
				return 0, false
			}
			r, c := int(le16(b[p+4:])), int(le16(b[p+6:]))
			if r < lastRow || r >= 65536 || c >= 256 {
				return 0, false
			}
		case typ == 0x0208: // ROW
			if l != 16 || int(le16(b[p+4:])) < lastRow {
				return 0, false
			}
		case typ == 0x0207 || typ == 0x00D7: // STRING, DBCELL
			if l > 8224 {
				return 0, false
			}
		default:
			return 0, false
		}
		return p + 4 + l, true
	}
	for p := from; p+4 <= len(b); p++ {
		q, n := p, 0
		for n < 6 {
			nq, ok := valid(q)
			if !ok {
				break
			}
			q, n = nq, n+1
		}
		if n >= 6 {
			return p
		}
	}
	return -1
}

func biffLongString(b []byte) string {
	if len(b) < 3 {
		return ""
	}
	n, flag := int(le16(b)), b[2]
	b = b[3:]
	if flag&1 != 0 {
		n = min(n, len(b)/2)
		u := make([]uint16, n)
		for i := range u {
			u[i] = le16(b[2*i:])
		}
		return string(utf16.Decode(u))
	}
	return latin1(b[:min(n, len(b))])
}

func rkValue(rk uint32) float64 {
	var v float64
	if rk&2 != 0 {
		v = float64(int32(rk) >> 2)
	} else {
		v = math.Float64frombits(uint64(rk&0xFFFFFFFC) << 32)
	}
	if rk&1 != 0 {
		v /= 100
	}
	return v
}

func fmtNum(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func sortCells(a []valCell) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j].Col < a[j-1].Col; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// ---------------------------------------------------------------- Word 97-2003

// wordText saca el texto de un Word 97-2003: con su tabla de fragmentos si
// está sana; si no, buscando los tramos de texto legible.
func wordText(w, t0, t1 []byte) (string, string) {
	if len(w) >= 0x1AA && (le16(w) == 0xA5EC || le16(w) == 0xA5DC) {
		flags := le16(w[0x0A:])
		tb := t0
		if flags&0x0200 != 0 {
			tb = t1
		}
		if txt := wordPieces(w, tb); strings.TrimSpace(txt) != "" {
			return txt, "se rescató el texto completo del documento (sin formato)"
		}
	}
	if txt := scanText(w); strings.TrimSpace(txt) != "" {
		return txt, "se rescataron los trozos de texto legibles (el índice del documento estaba dañado)"
	}
	return "", ""
}

func wordPieces(w, tb []byte) string {
	fc, lcb := int(le32(w[0x01A2:])), int(le32(w[0x01A6:]))
	if tb == nil || fc < 0 || lcb <= 0 || fc+lcb > len(tb) {
		return ""
	}
	clx := tb[fc : fc+lcb]
	p := 0
	for p < len(clx) && clx[p] == 1 {
		if p+3 > len(clx) {
			return ""
		}
		p += 3 + int(le16(clx[p+1:]))
	}
	if p+5 > len(clx) || clx[p] != 2 {
		return ""
	}
	l := int(le32(clx[p+1:]))
	plc := clx[p+5:]
	if l > len(plc) || (l-4)%12 != 0 {
		return ""
	}
	n := (l - 4) / 12
	var sb strings.Builder
	for i := 0; i < n; i++ {
		cp0, cp1 := int(le32(plc[4*i:])), int(le32(plc[4*i+4:]))
		f := le32(plc[4*(n+1)+8*i+2:])
		cnt := cp1 - cp0
		if cnt <= 0 {
			continue
		}
		if f&0x40000000 != 0 {
			off := int(f&^0x40000000) / 2
			if off < 0 || off+cnt > len(w) {
				return sb.String()
			}
			sb.WriteString(latin1(w[off : off+cnt]))
		} else {
			off := int(f)
			if off < 0 || off+2*cnt > len(w) {
				return sb.String()
			}
			u := make([]uint16, cnt)
			for k := range u {
				u[k] = le16(w[off+2*k:])
			}
			sb.WriteString(string(utf16.Decode(u)))
		}
	}
	return dropGarbage(cleanWordText(sb.String()))
}

// dropGarbage quita del texto los tramos que salieron de datos sobrescritos.
// Un texto real usa casi siempre los mismos alfabetos (en español: el latino
// básico y el de los acentos); unos datos ajenos leídos como texto salen
// repartidos por cientos de alfabetos distintos.
func dropGarbage(s string) string {
	rs := []rune(s)
	if len(rs) == 0 {
		return s
	}
	blocks := map[rune]int{}
	for _, r := range rs {
		blocks[r>>7]++
	}
	common := func(r rune) bool {
		return blocks[r>>7]*50 >= len(rs) || r < 0x80 && r >= 0x20 || r == '\r' || r == '\n' || r == '\t'
	}
	var b strings.Builder
	const win = 128
	gap := false
	for i := 0; i < len(rs); i += win {
		w := rs[i:min(i+win, len(rs))]
		odd := 0
		for _, r := range w {
			if !common(r) {
				odd++
			}
		}
		if odd*5 > len(w) {
			if !gap {
				b.WriteString("\r\n[... aquí falta un trozo ...]\r\n")
				gap = true
			}
			continue
		}
		gap = false
		b.WriteString(string(w))
	}
	return b.String()
}

// cleanWordText pasa los saltos de Word a saltos normales y quita los
// caracteres de control de campos e imágenes.
func cleanWordText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\r' || r == 0x0B || r == 0x0C:
			b.WriteString("\r\n")
		case r == 0x07:
			b.WriteString("\t")
		case r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0xFFFD:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// scanText busca tramos de texto legible (UTF-16 o de 8 bits) en unos datos.
func scanText(b []byte) string {
	var out strings.Builder
	isChar := func(c uint16) bool {
		return c >= 0x20 && c < 0x7F || c >= 0xA0 && c <= 0x17F || c == '\r' || c == '\t' || c == 0x2019 || c == 0x201C || c == 0x201D || c == 0x2013
	}
	// UTF-16
	run := []uint16{}
	flush := func() {
		if len(run) >= 20 && !oleName(string(utf16.Decode(run))) {
			out.WriteString(cleanWordText(string(utf16.Decode(run))))
			out.WriteString("\r\n")
		}
		run = run[:0]
	}
	for i := 0; i+1 < len(b); i += 2 {
		if c := le16(b[i:]); isChar(c) {
			run = append(run, c)
		} else {
			flush()
		}
	}
	flush()
	if out.Len() > 0 {
		return out.String()
	}
	// 8 bits
	var r8 []byte
	for _, c := range b {
		if c >= 0x20 && c < 0x7F || c >= 0xC0 || c == '\r' || c == '\t' {
			r8 = append(r8, c)
			continue
		}
		if len(r8) >= 20 && !oleName(latin1(r8)) {
			out.WriteString(cleanWordText(latin1(r8)))
			out.WriteString("\r\n")
		}
		r8 = r8[:0]
	}
	return out.String()
}

// oleName reconoce los nombres internos de las partes de un documento.
func oleName(s string) bool {
	s = strings.TrimSpace(s)
	for _, n := range []string{"Root Entry", "WordDocument", "SummaryInformation", "DocumentSummaryInformation",
		"0Table", "1Table", "CompObj", "ObjectPool", "Microsoft Word", "Word.Document"} {
		if strings.Contains(s, n) && len(s) < len(n)+12 {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- PowerPoint 97-2003

// pptText busca los registros de texto de las diapositivas (aunque el resto
// de la presentación esté dañado).
func pptText(b []byte) string {
	var out strings.Builder
	for i := 0; i+8 <= len(b); i++ {
		if b[i]&0x0F != 0 || b[i+1] != 0 {
			continue
		}
		typ, l := le16(b[i+2:]), int(le32(b[i+4:]))
		if typ != 0x0FA0 && typ != 0x0FA8 || l <= 0 || l > 1<<20 || i+8+l > len(b) {
			continue
		}
		body := b[i+8 : i+8+l]
		var s string
		if typ == 0x0FA0 {
			if l%2 != 0 {
				continue
			}
			u := make([]uint16, l/2)
			for k := range u {
				u[k] = le16(body[2*k:])
			}
			s = string(utf16.Decode(u))
		} else {
			s = latin1(body)
		}
		if !plausibleText(s) || masterText(s) {
			continue
		}
		out.WriteString(strings.ReplaceAll(cleanWordText(s), "\r\n", "\r\n"))
		out.WriteString("\r\n\r\n")
		i += 7 + l
	}
	return out.String()
}

// masterText reconoce los textos de ejemplo de los patrones de diapositivas.
func masterText(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"click to edit", "click to add", "haga clic para", "haga clic aquí", "pulse para",
		"second level", "third level", "fourth level", "fifth level", "segundo nivel", "tercer nivel", "cuarto nivel", "quinto nivel"} {
		if strings.HasPrefix(l, p) || strings.Contains(l, "\r"+p) {
			return true
		}
	}
	return false
}

func plausibleText(s string) bool {
	n, bad := 0, 0
	for _, r := range s {
		n++
		if r < 0x20 && r != '\r' && r != '\n' && r != '\t' && r != 0x0B || r == 0xFFFD || r >= 0xE000 && r < 0xF900 {
			bad++
		}
	}
	return n > 0 && bad*20 <= n
}
