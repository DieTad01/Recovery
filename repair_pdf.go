package main

// Reparación de PDF.
//
// Un PDF es una lista de «objetos» numerados (páginas, textos, imágenes,
// fuentes) más un índice al final que dice dónde está cada uno y cuál es
// la página de inicio. Al recuperar un PDF dañado, lo que suele faltar es
// justo el final (el índice) o algún objeto suelto. Aquí se buscan todos
// los objetos que quedan sanos, también los que van comprimidos dentro de
// otros («flujos de objetos»), se rehace el índice y, si hace falta, la
// lista de páginas, de modo que se vean todas las páginas que sobreviven.

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type pdfObj struct {
	num, gen int
	body     []byte // lo que va entre «obj» y «endobj»
	pos      int    // posición en el archivo (para saber cuál es la última versión)
}

var reObjHdr = regexp.MustCompile(`(\d{1,7})\s+(\d{1,5})\s+obj\b`)

// pdfObjects encuentra los objetos completos del PDF.
func pdfObjects(data []byte) map[int]*pdfObj {
	objs := map[int]*pdfObj{}
	locs := reObjHdr.FindAllSubmatchIndex(data, -1)
	for k, m := range locs {
		if m[0] > 0 && !isPDFSpace(data[m[0]-1]) {
			continue
		}
		num, _ := strconv.Atoi(string(data[m[2]:m[3]]))
		gen, _ := strconv.Atoi(string(data[m[4]:m[5]]))
		start := m[1]
		limit := len(data)
		if k+1 < len(locs) {
			limit = locs[k+1][0]
		}
		body, ok := objBody(data, start, limit)
		if !ok {
			continue
		}
		if prev := objs[num]; prev == nil || prev.pos < m[0] {
			objs[num] = &pdfObj{num: num, gen: gen, body: body, pos: m[0]}
		}
	}
	// Objetos guardados dentro de flujos de objetos (PDF 1.5 y posteriores).
	for _, o := range snapshot(objs) {
		d := pdfDict(o.body)
		if pdfName(d, "Type") != "ObjStm" {
			continue
		}
		raw, ok := streamData(o.body)
		if !ok {
			continue
		}
		dec, err := pdfInflate(raw, d)
		if err != nil && len(dec) == 0 {
			continue
		}
		n, _ := strconv.Atoi(pdfValue(d, "N"))
		first, _ := strconv.Atoi(pdfValue(d, "First"))
		if first <= 0 || first > len(dec) {
			continue
		}
		f := strings.Fields(string(dec[:first]))
		for i := 0; i+1 < len(f) && i/2 < n; i += 2 {
			num, e1 := strconv.Atoi(f[i])
			off, e2 := strconv.Atoi(f[i+1])
			if e1 != nil || e2 != nil || first+off > len(dec) {
				continue
			}
			end := len(dec)
			if i+3 < len(f) {
				if next, err := strconv.Atoi(f[i+3]); err == nil && first+next <= len(dec) && next >= off {
					end = first + next
				}
			}
			body := bytes.TrimSpace(dec[first+off : end])
			if len(body) == 0 {
				continue
			}
			if prev := objs[num]; prev == nil || prev.pos < o.pos {
				objs[num] = &pdfObj{num: num, gen: 0, body: body, pos: o.pos}
			}
		}
	}
	return objs
}

func snapshot(m map[int]*pdfObj) []*pdfObj {
	out := make([]*pdfObj, 0, len(m))
	for _, o := range m {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].num < out[j].num })
	return out
}

func isPDFSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

var reLength = regexp.MustCompile(`/Length\s+(\d+)(\s+\d+\s+R)?`)

// objBody devuelve el contenido de un objeto si está completo.
func objBody(data []byte, start, limit int) ([]byte, bool) {
	b := data[start:limit]
	s := bytes.Index(b, []byte("stream"))
	e := bytes.Index(b, []byte("endobj"))
	if s >= 0 && (e < 0 || s < e) && !bytes.HasPrefix(bytes.TrimSpace(b[:s]), []byte("endstream")) {
		// Objeto con flujo de datos: se respeta /Length si cuadra.
		ds := s + 6
		if ds < len(b) && b[ds] == '\r' {
			ds++
		}
		if ds < len(b) && b[ds] == '\n' {
			ds++
		}
		var es int = -1
		if m := reLength.FindSubmatch(b[:s]); m != nil && m[2] == nil {
			if l, _ := strconv.Atoi(string(m[1])); ds+l <= len(b) {
				rest := bytes.TrimLeft(b[ds+l:], " \r\n\t")
				if bytes.HasPrefix(rest, []byte("endstream")) {
					es = ds + l
				}
			}
		}
		if es < 0 {
			es = bytes.Index(b[ds:], []byte("endstream"))
			if es < 0 {
				return nil, false
			}
			es += ds
		}
		tok := bytes.Index(b[es:], []byte("endstream"))
		if tok < 0 {
			return nil, false
		}
		after := es + tok + len("endstream")
		k := bytes.Index(b[after:], []byte("endobj"))
		if k < 0 || len(bytes.TrimSpace(b[after:after+k])) > 0 {
			return nil, false
		}
		dict := b[:s]
		if !looksLikeText(dict, false) {
			return nil, false
		}
		data := b[ds:es]
		if es-ds > 0 && tok > 0 { // fin de línea antes de «endstream» (no es parte de los datos si /Length no lo incluía)
			data = bytes.TrimRight(data, "\r\n")
		}
		return []byte(fmt.Sprintf("%s\nstream\n%s\nendstream", bytes.TrimSpace(fixLength(dict, len(data))), data)), true
	}
	if e < 0 {
		return nil, false
	}
	body := bytes.TrimSpace(b[:e])
	if len(body) == 0 || !looksLikeText(body, false) || bytes.IndexByte(body, 0) >= 0 {
		return nil, false
	}
	return body, true
}

// fixLength pone /Length con el tamaño real del flujo.
func fixLength(dict []byte, n int) []byte {
	if reLength.Match(dict) {
		return reLength.ReplaceAll(dict, []byte("/Length "+strconv.Itoa(n)))
	}
	i := bytes.LastIndex(dict, []byte(">>"))
	if i < 0 {
		return dict
	}
	return append(append(append([]byte(nil), dict[:i]...), []byte(" /Length "+strconv.Itoa(n)+" ")...), dict[i:]...)
}

// streamData devuelve los datos de un objeto con flujo.
func streamData(body []byte) ([]byte, bool) {
	s := bytes.Index(body, []byte("\nstream\n"))
	e := bytes.LastIndex(body, []byte("\nendstream"))
	if s < 0 || e < s+8 {
		return nil, false
	}
	return body[s+8 : e], true
}

func pdfInflate(raw []byte, dict []byte) ([]byte, error) {
	if !bytes.Contains(dict, []byte("/FlateDecode")) {
		return raw, nil
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(zr, 256<<20))
	return out, err
}

// ---------------------------------------------------------------- diccionarios

// pdfDict devuelve el diccionario principal de un objeto («<< … >>»).
func pdfDict(body []byte) []byte {
	i := bytes.Index(body, []byte("<<"))
	if i < 0 {
		return nil
	}
	end := matchDict(body, i)
	if end < 0 {
		return nil
	}
	return body[i:end]
}

// matchDict devuelve dónde termina el diccionario que empieza en i.
func matchDict(b []byte, i int) int {
	depth := 0
	for p := i; p < len(b); p++ {
		switch c := b[p]; {
		case c == '(':
			p = skipString(b, p)
		case c == '<' && p+1 < len(b) && b[p+1] == '<':
			depth++
			p++
		case c == '>' && p+1 < len(b) && b[p+1] == '>':
			depth--
			p++
			if depth == 0 {
				return p + 1
			}
		case c == '<':
			for p < len(b) && b[p] != '>' {
				p++
			}
		case c == '%':
			for p < len(b) && b[p] != '\n' && b[p] != '\r' {
				p++
			}
		}
	}
	return -1
}

func skipString(b []byte, p int) int {
	depth := 0
	for ; p < len(b); p++ {
		switch b[p] {
		case '\\':
			p++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return p
			}
		}
	}
	return p
}

// pdfEntries devuelve las claves del primer nivel de un diccionario y dónde
// está el valor de cada una.
func pdfEntries(d []byte) map[string][2]int {
	out := map[string][2]int{}
	if len(d) < 4 {
		return out
	}
	p := 2
	for p < len(d)-2 {
		for p < len(d) && isPDFSpace(d[p]) {
			p++
		}
		if p >= len(d)-2 || d[p] != '/' {
			break
		}
		k := p + 1
		for k < len(d) && !isPDFSpace(d[k]) && !strings.ContainsRune("/<>[]()", rune(d[k])) {
			k++
		}
		key := string(d[p+1 : k])
		for k < len(d) && isPDFSpace(d[k]) {
			k++
		}
		v := k
		end := valueEnd(d, v)
		if end <= v {
			break
		}
		out[key] = [2]int{v, end}
		p = end
	}
	return out
}

var reRef = regexp.MustCompile(`^(\d+)\s+(\d+)\s+R`)

func valueEnd(d []byte, v int) int {
	if v >= len(d) {
		return v
	}
	switch c := d[v]; {
	case c == '<' && v+1 < len(d) && d[v+1] == '<':
		return matchDict(d, v)
	case c == '<':
		i := bytes.IndexByte(d[v:], '>')
		if i < 0 {
			return -1
		}
		return v + i + 1
	case c == '(':
		return skipString(d, v) + 1
	case c == '[':
		depth := 0
		for p := v; p < len(d); p++ {
			switch d[p] {
			case '(':
				p = skipString(d, p)
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					return p + 1
				}
			}
		}
		return -1
	case c == '/':
		p := v + 1
		for p < len(d) && !isPDFSpace(d[p]) && !strings.ContainsRune("/<>[]()", rune(d[p])) {
			p++
		}
		return p
	}
	if m := reRef.FindIndex(d[v:]); m != nil {
		return v + m[1]
	}
	p := v
	for p < len(d) && !isPDFSpace(d[p]) && !strings.ContainsRune("/<>[]()", rune(d[p])) {
		p++
	}
	return p
}

func pdfValue(d []byte, key string) string {
	if e, ok := pdfEntries(d)[key]; ok {
		return strings.TrimSpace(string(d[e[0]:e[1]]))
	}
	return ""
}

func pdfName(d []byte, key string) string { return strings.TrimPrefix(pdfValue(d, key), "/") }

func pdfRef(v string) (int, bool) {
	m := reRef.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

var reRefAll = regexp.MustCompile(`(\d+)\s+(\d+)\s+R\b`)

// setEntry cambia (o añade) una clave del diccionario principal de un objeto.
func setEntry(body []byte, key, value string) []byte {
	i := bytes.Index(body, []byte("<<"))
	if i < 0 {
		return body
	}
	end := matchDict(body, i)
	if end < 0 {
		return body
	}
	d := body[i:end]
	var nd []byte
	if e, ok := pdfEntries(d)[key]; ok {
		nd = append(append(append([]byte(nil), d[:e[0]]...), value...), d[e[1]:]...)
	} else {
		nd = append(append([]byte(nil), d[:len(d)-2]...), " /"+key+" "+value+" >>"...)
	}
	return append(append(append([]byte(nil), body[:i]...), nd...), body[end:]...)
}

// ---------------------------------------------------------------- reparación

// repairPDF rehace un PDF con los objetos que quedan sanos.
func repairPDF(data []byte) *repairOut {
	out := &repairOut{}
	if bytes.Contains(data, []byte("/Encrypt")) {
		out.Notes = append(out.Notes, "PDF protegido con contraseña: no se puede reparar sin ella")
		return out
	}
	objs := pdfObjects(data)
	if len(objs) == 0 {
		out.Notes = append(out.Notes, "no queda ningún objeto legible del PDF")
		return out
	}
	// Fuera los índices antiguos y los flujos de objetos (se escriben sueltos).
	maxNum := 0
	for n, o := range objs {
		d := pdfDict(o.body)
		switch pdfName(d, "Type") {
		case "XRef", "ObjStm":
			delete(objs, n)
			continue
		}
		if pdfValue(d, "Linearized") != "" {
			delete(objs, n)
			continue
		}
		if n > maxNum {
			maxNum = n
		}
	}
	// Flujos comprimidos dañados: se conserva lo que se puede descomprimir.
	broken := 0
	for _, o := range objs {
		d := pdfDict(o.body)
		raw, ok := streamData(o.body)
		if !ok || !bytes.Contains(d, []byte("/FlateDecode")) || bytes.Contains(d, []byte("/DecodeParms")) {
			continue
		}
		dec, err := pdfInflate(raw, d)
		if err == nil {
			continue
		}
		broken++
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		w.Write(dec)
		w.Close()
		o.body = []byte(fmt.Sprintf("%s\nstream\n%s\nendstream", fixLength(d, z.Len()), z.Bytes()))
	}
	// Catálogo y páginas.
	var catalog *pdfObj
	var pages []*pdfObj
	for _, o := range snapshot(objs) {
		switch pdfName(pdfDict(o.body), "Type") {
		case "Catalog":
			if catalog == nil || o.pos > catalog.pos {
				catalog = o
			}
		case "Page":
			pages = append(pages, o)
		}
	}
	if len(pages) == 0 {
		out.Notes = append(out.Notes, "no queda ninguna página legible")
		return out
	}
	ordered, complete := pdfPageTree(objs, catalog)
	seen := map[int]bool{}
	for _, p := range ordered {
		seen[p.num] = true
	}
	orphans := 0
	for _, p := range pages {
		if !seen[p.num] {
			ordered = append(ordered, p)
			orphans++
		}
	}
	notes := []string{}
	if catalog == nil || !complete || orphans > 0 {
		// Lista de páginas nueva, con los atributos que heredaban.
		maxNum++
		pagesNum := maxNum
		var kids []string
		for _, p := range ordered {
			p.body = inheritPageAttrs(objs, p)
			p.body = setEntry(p.body, "Parent", fmt.Sprintf("%d 0 R", pagesNum))
			kids = append(kids, fmt.Sprintf("%d %d R", p.num, p.gen))
		}
		objs[pagesNum] = &pdfObj{num: pagesNum, body: []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids)))}
		if catalog == nil {
			maxNum++
			catalog = &pdfObj{num: maxNum, body: []byte(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum))}
			objs[maxNum] = catalog
		} else {
			catalog.body = setEntry(catalog.body, "Pages", fmt.Sprintf("%d 0 R", pagesNum))
		}
		notes = append(notes, "se rehízo la lista de páginas")
	}
	// Referencias a objetos que ya no existen: se anulan.
	missing := 0
	for _, o := range objs {
		if !bytes.Contains(o.body, []byte(" R")) {
			continue
		}
		head, tail := o.body, []byte(nil)
		if i := bytes.Index(o.body, []byte("\nstream\n")); i >= 0 {
			head, tail = o.body[:i], o.body[i:]
		}
		nh := reRefAll.ReplaceAllFunc(head, func(m []byte) []byte {
			n, _ := pdfRef(string(m))
			if objs[n] == nil {
				missing++
				return []byte("null")
			}
			return m
		})
		o.body = append(nh, tail...)
	}
	// Información del documento e identificador del índice original.
	trailer := pdfTrailer(data)
	info := ""
	if n, ok := pdfRef(pdfValue(trailer, "Info")); ok && objs[n] != nil {
		info = fmt.Sprintf(" /Info %d 0 R", n)
	}
	id := pdfValue(trailer, "ID")

	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := map[int]int{}
	nums := make([]int, 0, len(objs))
	for n := range objs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for _, n := range nums {
		o := objs[n]
		offsets[n] = b.Len()
		fmt.Fprintf(&b, "%d %d obj\n", n, o.gen)
		b.Write(o.body)
		b.WriteString("\nendobj\n")
	}
	xref := b.Len()
	size := nums[len(nums)-1] + 1
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f\r\n", size)
	for n := 1; n < size; n++ {
		if off, ok := offsets[n]; ok {
			fmt.Fprintf(&b, "%010d %05d n\r\n", off, objs[n].gen)
		} else {
			b.WriteString("0000000000 00000 f\r\n")
		}
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %d %d R%s", size, catalog.num, catalog.gen, info)
	if id != "" && !strings.Contains(id, "R") {
		fmt.Fprintf(&b, " /ID %s", id)
	}
	fmt.Fprintf(&b, " >>\nstartxref\n%d\n%%%%EOF\n", xref)

	out.Repaired, out.RepairedExt = b.Bytes(), "pdf"
	out.Complete = broken == 0 && orphans == 0 && missing == 0 && complete
	out.Notes = append(notes, fmt.Sprintf("%s recuperadas", plural(len(ordered), "página", "páginas")))
	if broken > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%s con datos dañados (puede faltar parte de su contenido)", plural(broken, "bloque", "bloques")))
	}
	if missing > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("faltan %s (imágenes, fuentes o textos sobrescritos)", plural(missing, "parte", "partes")))
	}
	return out
}

// pdfPageTree recorre la lista de páginas desde el catálogo; complete indica
// si se pudo recorrer entera.
func pdfPageTree(objs map[int]*pdfObj, catalog *pdfObj) ([]*pdfObj, bool) {
	if catalog == nil {
		return nil, false
	}
	root, ok := pdfRef(pdfValue(pdfDict(catalog.body), "Pages"))
	if !ok {
		return nil, false
	}
	var out []*pdfObj
	complete := true
	visited := map[int]bool{}
	var walk func(n, depth int)
	walk = func(n, depth int) {
		o := objs[n]
		if o == nil || visited[n] || depth > 64 {
			complete = false
			return
		}
		visited[n] = true
		d := pdfDict(o.body)
		switch pdfName(d, "Type") {
		case "Page":
			out = append(out, o)
		case "Pages":
			kids := pdfValue(d, "Kids")
			if kids == "" {
				complete = false
			}
			for _, m := range reRefAll.FindAllStringSubmatch(kids, -1) {
				k, _ := strconv.Atoi(m[1])
				walk(k, depth+1)
			}
		default:
			complete = false
		}
	}
	walk(root, 0)
	return out, complete
}

// inheritPageAttrs copia en la página los atributos que heredaba de su lista
// de páginas original (tamaño, recursos, giro), que va a desaparecer.
func inheritPageAttrs(objs map[int]*pdfObj, p *pdfObj) []byte {
	body := p.body
	d := pdfDict(body)
	need := map[string]bool{}
	for _, k := range []string{"Resources", "MediaBox", "CropBox", "Rotate"} {
		if pdfValue(d, k) == "" {
			need[k] = true
		}
	}
	parent, ok := pdfRef(pdfValue(d, "Parent"))
	for depth := 0; ok && len(need) > 0 && depth < 32; depth++ {
		po := objs[parent]
		if po == nil {
			break
		}
		pd := pdfDict(po.body)
		for k := range need {
			if v := pdfValue(pd, k); v != "" {
				body = setEntry(body, k, v)
				delete(need, k)
			}
		}
		parent, ok = pdfRef(pdfValue(pd, "Parent"))
	}
	if need["MediaBox"] {
		body = setEntry(body, "MediaBox", "[0 0 612 792]")
	}
	if need["Resources"] {
		body = setEntry(body, "Resources", "<< >>")
	}
	return body
}

// pdfTrailer devuelve el último diccionario del índice original que se pueda leer.
func pdfTrailer(data []byte) []byte {
	if i := bytes.LastIndex(data, []byte("trailer")); i >= 0 {
		if d := pdfDict(data[i:]); d != nil {
			return d
		}
	}
	// PDF 1.5+: el índice es un objeto /Type /XRef.
	for _, m := range regexp.MustCompile(`/Type\s*/XRef`).FindAllIndex(data, -1) {
		k := bytes.LastIndex(data[:m[0]], []byte("<<"))
		if k >= 0 {
			if d := pdfDict(data[k:]); d != nil {
				return d
			}
		}
	}
	return nil
}
