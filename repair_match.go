package main

// Búsqueda de copias sanas de un archivo dañado.
//
// Muchas veces el mismo documento existe en otro sitio: una versión anterior
// que quedó en el espacio libre del disco (Recovery la encuentra «sin
// nombre, por su contenido»), un respaldo en una memoria USB, una copia en
// el escritorio... Para reconocerlo sin equivocarse se usa, de más a menos
// seguro:
//
//   1. copia idéntica: mismo tamaño y mismos primeros 64 KB;
//   2. mismo documento: Office guarda dentro la fecha y hora exactas en que
//      se creó el documento (no cambia al guardar otra versión), los PDF un
//      identificador propio y las fotos la fecha y la cámara con que se
//      tomaron; además se comprueba que los textos o valores que quedan del
//      dañado aparecen en la copia;
//   3. mismo contenido: si comparte la mayoría de sus textos o valores;
//   4. mismo nombre (lo menos seguro: se indica que hay que revisarla).

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"hash/fnv"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

type docKey struct {
	path  string
	name  string // nombre sin extensión, normalizado
	ext   string
	size  int64
	mod   time.Time
	head  [20]byte
	ident string // identidad del documento ("" si no se sabe)
	fp    map[uint64]bool
	fpOK  bool
	valid int // vNone = sin comprobar
}

// matchGroup agrupa extensiones que son el mismo tipo de documento.
func matchGroup(ext string) string {
	switch ext {
	case "docx", "docm", "dotx":
		return "docx"
	case "xlsx", "xlsm", "xltx":
		return "xlsx"
	case "pptx", "pptm", "ppsx":
		return "pptx"
	case "jpg", "jpeg", "jpe", "jfif":
		return "jpg"
	case "doc", "dot":
		return "doc"
	case "xls", "xlt":
		return "xls"
	case "ppt", "pps":
		return "ppt"
	}
	return ext
}

var reCopySuffix = regexp.MustCompile(`(?i)\s*(\(\d+\)|\(reg \d+(-\d+)?\)|- copia( \(\d+\))?|- copy( \(\d+\))?|copia de |copy of )\s*`)

// normName quita del nombre lo que suelen añadir las copias («(2)», «- copia»).
func normName(name string) string {
	base := strings.TrimSuffix(name, pathExt(name))
	return strings.TrimSpace(fold(reCopySuffix.ReplaceAllString(base, " ")))
}

func pathExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}

func newDocKey(path string, size int64, mod time.Time) *docKey {
	base := path[strings.LastIndexAny(path, `/\`)+1:]
	k := &docKey{path: path, name: normName(base), ext: strings.ToLower(strings.TrimPrefix(pathExt(base), ".")), size: size, mod: mod}
	f, err := os.Open(path)
	if err != nil {
		return k
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	k.head = sha1.Sum(head[:n])
	switch matchGroup(k.ext) {
	case "docx", "xlsx", "pptx":
		if size <= 256<<20 {
			if data, err := os.ReadFile(path); err == nil {
				k.ident = ooxmlIdent(data)
			}
		}
	case "pdf":
		tail := make([]byte, min(size, 16<<10))
		f.ReadAt(tail, size-int64(len(tail)))
		k.ident = pdfIdent(append(head[:n], tail...))
	case "jpg":
		k.ident = exifIdent(head[:n])
	}
	return k
}

// fingerprint calcula (una vez) los textos o valores del documento.
func (k *docKey) fingerprint() map[uint64]bool {
	if !k.fpOK {
		k.fpOK = true
		if k.size <= 256<<20 {
			if data, err := os.ReadFile(k.path); err == nil {
				k.fp = contentPrint(data, k.ext)
			}
		}
	}
	return k.fp
}

// ---------------------------------------------------------------- identidad

var reCreated = regexp.MustCompile(`<dcterms:created\b[^>]*>([^<]+)</dcterms:created>`)
var reCreator = regexp.MustCompile(`<dc:creator>([^<]*)</dc:creator>`)

// ooxmlIdent: fecha de creación y autor que Office guarda en docProps/core.xml.
func ooxmlIdent(data []byte) string {
	for _, p := range salvageZip(data) {
		if p.Name != "docProps/core.xml" {
			continue
		}
		m := reCreated.FindSubmatch(p.Data)
		if m == nil {
			return ""
		}
		id := "ooxml:" + strings.TrimSpace(string(m[1]))
		if c := reCreator.FindSubmatch(p.Data); c != nil {
			id += "|" + strings.TrimSpace(string(c[1]))
		}
		return id
	}
	return ""
}

var rePDFID = regexp.MustCompile(`/ID\s*\[\s*<([0-9A-Fa-f]{16,64})>`)

func pdfIdent(b []byte) string {
	if m := rePDFID.FindSubmatch(b); m != nil {
		return "pdf:" + strings.ToUpper(string(m[1]))
	}
	return ""
}

// exifIdent: fecha y hora de la toma (con fracción de segundo) y la cámara.
func exifIdent(b []byte) string {
	i := bytes.Index(b, []byte("Exif\x00\x00"))
	if i < 0 || i+14 > len(b) {
		return ""
	}
	t := b[i+6:]
	bo := tiffOrder(t)
	var date, sub, model string
	var walk func(off, depth int)
	walk = func(off, depth int) {
		if depth > 2 || off+2 > len(t) {
			return
		}
		n := int(bo.Uint16(t[off:]))
		for k := 0; k < n && off+2+12*k+12 <= len(t); k++ {
			e := t[off+2+12*k:]
			tag, cnt := bo.Uint16(e), int(bo.Uint32(e[4:]))
			str := func() string {
				if cnt <= 4 {
					return strings.TrimRight(string(e[8:8+cnt]), "\x00 ")
				}
				o := int(bo.Uint32(e[8:]))
				if o+cnt > len(t) {
					return ""
				}
				return strings.TrimRight(string(t[o:o+cnt]), "\x00 ")
			}
			switch tag {
			case 0x0110:
				model = str()
			case 0x9003:
				date = str()
			case 0x9291:
				sub = str()
			case 0x8769:
				walk(int(bo.Uint32(e[8:])), depth+1)
			}
		}
	}
	if len(t) >= 8 {
		walk(int(bo.Uint32(t[4:])), 0)
	}
	if date == "" {
		return ""
	}
	return "exif:" + date + "." + sub + "|" + model
}

var _ = binary.LittleEndian

// ---------------------------------------------------------------- contenido

// contentPrint devuelve los textos o valores de un documento (aunque esté
// dañado), resumidos para compararlos con los de otro.
func contentPrint(data []byte, ext string) map[uint64]bool {
	set := map[uint64]bool{}
	add := func(s string) {
		s = strings.TrimSpace(fold(s))
		if len([]rune(s)) < 4 || strings.Contains(s, "texto perdido") || strings.Contains(s, "falta un trozo") {
			return
		}
		h := fnv.New64a()
		h.Write([]byte(s))
		set[h.Sum64()] = true
	}
	lines := func(t string) {
		for _, l := range strings.FieldsFunc(t, func(r rune) bool { return r == '\r' || r == '\n' }) {
			add(l)
		}
	}
	switch matchGroup(ext) {
	case "docx", "xlsx", "pptx":
		parts := salvageZip(data)
		byName := map[string][]byte{}
		for _, p := range parts {
			byName[p.Name] = p.Data
		}
		switch matchGroup(ext) {
		case "docx":
			if b := byName["word/document.xml"]; b != nil {
				if s := salvageXML(b, "body"); s.Data != nil {
					lines(docxText(s.Data))
				}
			}
		case "xlsx":
			strs := sharedStrings(byName["xl/sharedStrings.xml"])
			for _, s := range strs {
				add(s)
			}
			for n, b := range byName {
				if strings.HasPrefix(n, "xl/worksheets/sheet") {
					if s := salvageXML(b, "sheetData"); s.Data != nil {
						for _, r := range sheetValues(s.Data, strs) {
							for _, c := range r.Cells {
								if c.Num {
									add(c.Text)
								}
							}
						}
					}
				}
			}
		case "pptx":
			pk := &opcPkg{parts: map[string]*opcPart{}}
			for _, p := range parts {
				if s := salvageXML(p.Data, "spTree"); s.Data != nil {
					pk.order = append(pk.order, p.Name)
					pk.parts[p.Name] = &opcPart{data: s.Data}
				}
			}
			lines(pk.pptxText())
		}
	case "doc", "xls", "ppt":
		r := repairOLE(data, ext)
		switch r.RescueExt {
		case "txt":
			lines(string(r.Rescue))
		case "xlsx":
			o, _ := parseOLE(data)
			var wb []byte
			if o != nil {
				wb = oleStream(o, "Workbook")
			}
			if wb == nil {
				wb = data
			}
			sheets, _ := biffValues(wb)
			for _, s := range sheets {
				for _, row := range s.Rows {
					for _, c := range row.Cells {
						add(c.Text)
					}
				}
			}
		}
	default:
		if plainExts[ext] {
			lines(string(data))
		}
	}
	return set
}

// overlap es la parte de a que está también en b.
func overlap(a, b map[uint64]bool) float64 {
	if len(a) == 0 {
		return 0
	}
	n := 0
	for h := range a {
		if b[h] {
			n++
		}
	}
	return float64(n) / float64(len(a))
}

// ---------------------------------------------------------------- búsqueda

// copyMatch es la copia sana elegida para un archivo dañado.
type copyMatch struct {
	key   *docKey
	level int // mtIdentical … mtWeak
	why   string
}

const (
	mtWeak = 1 + iota // misma identidad interna, sin texto con que confirmarlo
	mtName
	mtContent
	mtDocument
	mtIdentical
)

// copyIndex guarda los posibles candidatos por tipo de documento.
type copyIndex struct {
	byGroup map[string][]*docKey
}

func (ix *copyIndex) add(k *docKey) {
	if ix.byGroup == nil {
		ix.byGroup = map[string][]*docKey{}
	}
	g := matchGroup(k.ext)
	ix.byGroup[g] = append(ix.byGroup[g], k)
}

// find busca la mejor copia sana de d.
func (ix *copyIndex) find(d *docKey) *copyMatch {
	var best *copyMatch
	consider := func(c *docKey, level int, why string) {
		if c.path == d.path || best != nil && level < best.level ||
			best != nil && level == best.level && !c.mod.After(best.key.mod) {
			return
		}
		if c.valid == vNone {
			if f, err := os.Open(c.path); err == nil {
				c.valid, _ = validateContent(f, c.size, c.ext)
				f.Close()
			} else {
				c.valid = vBad
			}
		}
		if c.valid == vBad {
			return
		}
		best = &copyMatch{key: c, level: level, why: why}
	}
	cands := ix.byGroup[matchGroup(d.ext)]
	for _, c := range cands {
		if c.path != d.path && c.size == d.size && c.head == d.head {
			consider(c, mtIdentical, "copia idéntica")
		}
	}
	if best != nil {
		return best
	}
	// Textos o valores propios del documento: los que se repiten en muchos
	// archivos (los encabezados de una plantilla de factura, por ejemplo) no
	// sirven para saber si es el mismo.
	dfp := d.fingerprint()
	own := map[uint64]bool{}
	if len(dfp) > 0 && len(cands) <= 3000 {
		freq := map[uint64]int{}
		for _, c := range cands {
			if c.path == d.path {
				continue
			}
			for h := range c.fingerprint() {
				if dfp[h] {
					freq[h]++
				}
			}
		}
		for h := range dfp {
			if freq[h] <= max(3, len(cands)/5) {
				own[h] = true
			}
		}
	}
	for _, c := range cands {
		if c.path == d.path {
			continue
		}
		if d.ident != "" && c.ident == d.ident {
			// Mismo identificador interno. Ojo: «Guardar como» también lo
			// copia (la factura de este mes hecha a partir de la del mes
			// pasado), así que tiene que coincidir además el contenido propio.
			switch {
			case len(own) >= 5 && overlap(own, c.fingerprint()) >= 0.5:
				consider(c, mtDocument, "el mismo documento (otra versión o copia)")
			case len(own) < 5:
				consider(c, mtWeak, "parece una versión del mismo documento, pero no queda texto suficiente para confirmarlo: revísala")
			}
		}
		if len(own) >= 5 && c.size <= 64*d.size+(1<<20) {
			switch o := overlap(own, c.fingerprint()); {
			case o >= 0.9:
				consider(c, mtDocument, "contiene todo lo que quedó legible del dañado")
			case o >= 0.6:
				consider(c, mtContent, "comparte la mayor parte del contenido (revisa que sea el mismo)")
			}
		}
		if d.name != "" && c.name == d.name {
			consider(c, mtName, "se llama igual (revisa que sea el mismo)")
		}
	}
	return best
}
