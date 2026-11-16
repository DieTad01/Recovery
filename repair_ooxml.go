package main

// Reparación de documentos de Office modernos (docx, xlsx, pptx).
//
// Se reconstruye un documento nuevo con las piezas sanas, lo que se puede
// leer de las dañadas (el texto o las filas hasta el punto del daño) y,
// donde falta algo imprescindible, una pieza nueva mínima: el índice de
// tipos, las relaciones, los estilos o el libro que lista las hojas. Las
// imágenes perdidas se sustituyen por un recuadro gris para que el resto
// del documento siga en su sitio. Además se guardan los datos rescatados
// en un archivo aparte y sencillo (un Excel solo con los valores, o el
// texto), que se abre siempre.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type opcPart struct {
	data []byte
	orig *zipPart // pieza original sana y sin cambios: se copia tal cual
}

type opcPkg struct {
	kind   string // docx | xlsx | pptx
	ext    string
	order  []string
	parts  map[string]*opcPart
	ctDef  map[string]string // tipos por extensión
	ctOver map[string]string // tipos por pieza (con «/» delante)
	notes  []string
	lost   bool // se perdió contenido del usuario
}

func (pk *opcPkg) has(name string) bool { return pk.parts[name] != nil }

func (pk *opcPkg) set(name string, data []byte) {
	if pk.parts[name] == nil {
		pk.order = append(pk.order, name)
	}
	pk.parts[name] = &opcPart{data: data}
}

func (pk *opcPkg) drop(name string) { delete(pk.parts, name) }

func (pk *opcPkg) note(format string, a ...any) {
	pk.notes = append(pk.notes, fmt.Sprintf(format, a...))
}

func ooxmlKind(ext string, parts []*zipPart) string {
	switch ext {
	case "docx", "docm", "dotx", "dotm":
		return "docx"
	case "xlsx", "xlsm", "xltx", "xltm":
		return "xlsx"
	case "pptx", "pptm", "ppsx", "potx", "ppsm":
		return "pptx"
	}
	for _, p := range parts {
		switch {
		case strings.HasPrefix(p.Name, "word/"):
			return "docx"
		case strings.HasPrefix(p.Name, "xl/"):
			return "xlsx"
		case strings.HasPrefix(p.Name, "ppt/"):
			return "pptx"
		}
	}
	return ""
}

// Contenedor cuyos hijos se conservan al rescatar cada pieza XML.
func salvageContainer(kind, name string) string {
	switch {
	case name == "word/document.xml":
		return "body"
	case regexp.MustCompile(`^word/header\d*\.xml$`).MatchString(name):
		return "hdr"
	case regexp.MustCompile(`^word/footer\d*\.xml$`).MatchString(name):
		return "ftr"
	case name == "word/footnotes.xml":
		return "footnotes"
	case name == "word/endnotes.xml":
		return "endnotes"
	case name == "word/comments.xml":
		return "comments"
	case regexp.MustCompile(`^xl/worksheets/sheet\d+\.xml$`).MatchString(name):
		return "sheetData"
	case name == "xl/sharedStrings.xml":
		return "sst"
	case name == "xl/workbook.xml":
		return "sheets"
	case name == "xl/styles.xml":
		return "styleSheet"
	case regexp.MustCompile(`^ppt/slides/slide\d+\.xml$`).MatchString(name):
		return "spTree"
	case strings.HasSuffix(name, ".rels"):
		return "Relationships"
	case name == "[Content_Types].xml":
		return "Types"
	}
	return ""
}

// Piezas cuya pérdida no quita nada al usuario.
func minorPart(name string) bool {
	return strings.HasPrefix(name, "docProps/") || strings.HasSuffix(name, "calcChain.xml") ||
		strings.Contains(name, "printerSettings") || strings.Contains(name, "thumbnail") ||
		strings.HasSuffix(name, "webSettings.xml") || strings.HasSuffix(name, "fontTable.xml") ||
		strings.Contains(name, "/theme/") || strings.HasSuffix(name, ".rels") || name == "[Content_Types].xml" ||
		strings.HasSuffix(name, "settings.xml") || strings.HasSuffix(name, "Props.xml") ||
		strings.HasSuffix(name, "tableStyles.xml") || strings.HasPrefix(name, "customXml/")
}

// repairOOXML repara un docx/xlsx/pptx dañado.
func repairOOXML(data []byte, ext string) *repairOut {
	out := &repairOut{}
	parts := salvageZip(data)
	if len(parts) == 0 {
		out.Notes = append(out.Notes, "no queda ninguna pieza legible del documento")
		return out
	}
	pk := &opcPkg{kind: ooxmlKind(ext, parts), ext: ext, parts: map[string]*opcPart{}}
	if pk.kind == "" {
		return repairZipArchive(parts, ext)
	}
	afterGap := false
	for _, p := range parts {
		pk.order = append(pk.order, p.Name)
		if p.AfterGap {
			afterGap = true
		}
		if p.OK {
			pk.parts[p.Name] = &opcPart{data: p.Data, orig: p}
			continue
		}
		c := salvageContainer(pk.kind, p.Name)
		if c != "" && len(p.Data) > 0 {
			if s := salvageXML(p.Data, c); s.Data != nil {
				if c == "sheetData" && !s.Complete {
					s.Data, s.Kept = saneRows(s.Data)
				}
				pk.parts[p.Name] = &opcPart{data: s.Data}
				if !s.Complete && !minorPart(p.Name) {
					pk.lost = true
					pk.note("«%s» estaba dañado: se rescataron %s", p.Name, describeKept(p.Name, s.Kept))
				}
				continue
			}
		}
		if !minorPart(p.Name) {
			pk.lost = true
			pk.note("«%s» se perdió (sobrescrito)", p.Name)
		}
	}
	pk.loadContentTypes()

	switch pk.kind {
	case "xlsx":
		pk.fixXLSX()
	case "docx":
		pk.fixDOCX()
	case "pptx":
		pk.fixPPTX()
	}
	pk.fixRelationships()
	pk.fixRootRels()

	// Datos rescatados en un formato sencillo.
	switch pk.kind {
	case "xlsx":
		if b, n := pk.valuesWorkbook(); b != nil && n > 0 {
			out.Rescue, out.RescueExt, out.RescueTag = b, "xlsx", "datos rescatados"
		}
	case "docx":
		if t := docxText(pk.part("word/document.xml")); strings.TrimSpace(t) != "" {
			out.Rescue, out.RescueExt, out.RescueTag = utf8BOM(t), "txt", "texto rescatado"
		}
	case "pptx":
		if t := pk.pptxText(); strings.TrimSpace(t) != "" {
			out.Rescue, out.RescueExt, out.RescueTag = utf8BOM(t), "txt", "texto rescatado"
		}
	}

	if pk.mainPart() == "" || !pk.has(pk.mainPart()) {
		out.Notes = append(pk.notes, "no se pudo rehacer el documento: su parte principal se perdió")
		return out
	}
	if !pk.hasContent() {
		out.Notes = append(pk.notes, "no queda contenido del documento que rescatar")
		return out
	}
	doc, err := pk.write()
	if err == nil {
		err = checkOPC(doc)
	}
	if err != nil {
		out.Notes = append(pk.notes, "no se pudo rehacer el documento ("+err.Error()+")")
		return out
	}
	out.Repaired, out.RepairedExt = doc, ext
	out.Complete = !pk.lost
	if afterGap {
		pk.note("algunas piezas aparecieron después de una zona dañada: revisa que el contenido sea el correcto")
	}
	out.Notes = pk.notes
	return out
}

// hasContent indica si al documento rehecho le queda algo del usuario.
func (pk *opcPkg) hasContent() bool {
	switch pk.kind {
	case "xlsx":
		wb := string(pk.part("xl/workbook.xml"))
		if !strings.Contains(wb, "<sheet ") {
			return false
		}
		for _, sh := range pk.sheetParts() {
			if strings.Contains(string(pk.part(sh)), "<c ") {
				return true
			}
		}
		return false
	case "docx":
		return strings.TrimSpace(docxText(pk.part("word/document.xml"))) != "" ||
			strings.Contains(string(pk.part("word/document.xml")), "<w:drawing")
	case "pptx":
		return strings.Contains(string(pk.part("ppt/presentation.xml")), "<p:sldId ")
	}
	return true
}

func describeKept(name string, n int) string {
	switch {
	case strings.HasPrefix(name, "xl/worksheets/"):
		return plural(n, "fila", "filas")
	case name == "xl/sharedStrings.xml":
		return plural(n, "texto", "textos")
	case name == "word/document.xml":
		return plural(n, "párrafo o tabla", "párrafos y tablas")
	case strings.HasPrefix(name, "ppt/slides/"):
		return plural(n, "elemento", "elementos")
	}
	return plural(n, "elemento", "elementos")
}

func (pk *opcPkg) part(name string) []byte {
	if p := pk.parts[name]; p != nil {
		return p.data
	}
	return nil
}

func (pk *opcPkg) mainPart() string {
	switch pk.kind {
	case "docx":
		return "word/document.xml"
	case "xlsx":
		return "xl/workbook.xml"
	case "pptx":
		return "ppt/presentation.xml"
	}
	return ""
}

// ---------------------------------------------------------------- tipos de contenido

var reCTDefault = regexp.MustCompile(`<Default\b[^>]*>`)
var reCTOverride = regexp.MustCompile(`<Override\b[^>]*>`)
var reAttr = regexp.MustCompile(`([A-Za-z_:][-A-Za-z0-9_:.]*)\s*=\s*"([^"]*)"`)

func attrs(tag string) map[string]string {
	m := map[string]string{}
	for _, a := range reAttr.FindAllStringSubmatch(tag, -1) {
		m[a[1]] = xmlUnescape(a[2])
	}
	return m
}

func xmlUnescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")
	return r.Replace(s)
}

func (pk *opcPkg) loadContentTypes() {
	pk.ctDef, pk.ctOver = map[string]string{}, map[string]string{}
	ct := string(pk.part("[Content_Types].xml"))
	for _, t := range reCTDefault.FindAllString(ct, -1) {
		a := attrs(t)
		pk.ctDef[strings.ToLower(a["Extension"])] = a["ContentType"]
	}
	for _, t := range reCTOverride.FindAllString(ct, -1) {
		a := attrs(t)
		pk.ctOver[a["PartName"]] = a["ContentType"]
	}
	pk.drop("[Content_Types].xml") // se rehace al final con las piezas que quedan
}

const (
	ctWML  = "application/vnd.openxmlformats-officedocument.wordprocessingml."
	ctSML  = "application/vnd.openxmlformats-officedocument.spreadsheetml."
	ctPML  = "application/vnd.openxmlformats-officedocument.presentationml."
	ctRels = "application/vnd.openxmlformats-package.relationships+xml"
)

var knownCT = []struct {
	re *regexp.Regexp
	ct string
}{
	{regexp.MustCompile(`^word/styles\.xml$`), ctWML + "styles+xml"},
	{regexp.MustCompile(`^word/settings\.xml$`), ctWML + "settings+xml"},
	{regexp.MustCompile(`^word/fontTable\.xml$`), ctWML + "fontTable+xml"},
	{regexp.MustCompile(`^word/webSettings\.xml$`), ctWML + "webSettings+xml"},
	{regexp.MustCompile(`^word/numbering\.xml$`), ctWML + "numbering+xml"},
	{regexp.MustCompile(`^word/footnotes\.xml$`), ctWML + "footnotes+xml"},
	{regexp.MustCompile(`^word/endnotes\.xml$`), ctWML + "endnotes+xml"},
	{regexp.MustCompile(`^word/comments\.xml$`), ctWML + "comments+xml"},
	{regexp.MustCompile(`^word/header\d*\.xml$`), ctWML + "header+xml"},
	{regexp.MustCompile(`^word/footer\d*\.xml$`), ctWML + "footer+xml"},
	{regexp.MustCompile(`/theme/theme\d*\.xml$`), "application/vnd.openxmlformats-officedocument.theme+xml"},
	{regexp.MustCompile(`^docProps/core\.xml$`), "application/vnd.openxmlformats-package.core-properties+xml"},
	{regexp.MustCompile(`^docProps/app\.xml$`), "application/vnd.openxmlformats-officedocument.extended-properties+xml"},
	{regexp.MustCompile(`^docProps/custom\.xml$`), "application/vnd.openxmlformats-officedocument.custom-properties+xml"},
	{regexp.MustCompile(`^xl/worksheets/sheet\d+\.xml$`), ctSML + "worksheet+xml"},
	{regexp.MustCompile(`^xl/styles\.xml$`), ctSML + "styles+xml"},
	{regexp.MustCompile(`^xl/sharedStrings\.xml$`), ctSML + "sharedStrings+xml"},
	{regexp.MustCompile(`^xl/calcChain\.xml$`), ctSML + "calcChain+xml"},
	{regexp.MustCompile(`^xl/tables/table\d+\.xml$`), ctSML + "table+xml"},
	{regexp.MustCompile(`^xl/comments\d+\.xml$`), ctSML + "comments+xml"},
	{regexp.MustCompile(`/drawings/drawing\d+\.xml$`), "application/vnd.openxmlformats-officedocument.drawing+xml"},
	{regexp.MustCompile(`/charts/chart\d+\.xml$`), "application/vnd.openxmlformats-officedocument.drawingml.chart+xml"},
	{regexp.MustCompile(`^ppt/slides/slide\d+\.xml$`), ctPML + "slide+xml"},
	{regexp.MustCompile(`^ppt/slideLayouts/slideLayout\d+\.xml$`), ctPML + "slideLayout+xml"},
	{regexp.MustCompile(`^ppt/slideMasters/slideMaster\d+\.xml$`), ctPML + "slideMaster+xml"},
	{regexp.MustCompile(`^ppt/notesSlides/notesSlide\d+\.xml$`), ctPML + "notesSlide+xml"},
	{regexp.MustCompile(`^ppt/notesMasters/notesMaster\d+\.xml$`), ctPML + "notesMaster+xml"},
	{regexp.MustCompile(`^ppt/handoutMasters/handoutMaster\d+\.xml$`), ctPML + "handoutMaster+xml"},
	{regexp.MustCompile(`^ppt/presProps\.xml$`), ctPML + "presProps+xml"},
	{regexp.MustCompile(`^ppt/viewProps\.xml$`), ctPML + "viewProps+xml"},
	{regexp.MustCompile(`^ppt/tableStyles\.xml$`), ctPML + "tableStyles+xml"},
}

var knownDefaults = map[string]string{
	"rels": ctRels, "xml": "application/xml", "png": "image/png", "jpeg": "image/jpeg", "jpg": "image/jpeg",
	"jpe": "image/jpeg", "jfif": "image/jpeg", "gif": "image/gif", "bmp": "image/bmp", "tif": "image/tiff",
	"tiff": "image/tiff", "emf": "image/x-emf", "wmf": "image/x-wmf", "svg": "image/svg+xml",
	"wdp": "image/vnd.ms-photo", "vml": "application/vnd.openxmlformats-officedocument.vmlDrawing",
}

func (pk *opcPkg) mainCT() string {
	switch pk.ext {
	case "docm":
		return "application/vnd.ms-word.document.macroEnabled.main+xml"
	case "dotx":
		return ctWML + "template.main+xml"
	case "dotm":
		return "application/vnd.ms-word.template.macroEnabledTemplate.main+xml"
	case "xlsm":
		return "application/vnd.ms-excel.sheet.macroEnabled.main+xml"
	case "xltx":
		return ctSML + "template.main+xml"
	case "xltm":
		return "application/vnd.ms-excel.template.macroEnabled.main+xml"
	case "pptm":
		return "application/vnd.ms-powerpoint.presentation.macroEnabled.main+xml"
	case "ppsx":
		return ctPML + "slideshow.main+xml"
	case "potx":
		return ctPML + "template.main+xml"
	}
	switch pk.kind {
	case "docx":
		return ctWML + "document.main+xml"
	case "xlsx":
		return ctSML + "sheet.main+xml"
	}
	return ctPML + "presentation.main+xml"
}

// contentType devuelve el tipo de una pieza ("" si no se sabe).
func (pk *opcPkg) contentType(name string) (ct string, override bool) {
	if c := pk.ctOver["/"+name]; c != "" {
		return c, true
	}
	if name == pk.mainPart() {
		return pk.mainCT(), true
	}
	for _, k := range knownCT {
		if k.re.MatchString(name) {
			return k.ct, true
		}
	}
	e := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	if c := pk.ctDef[e]; c != "" {
		return c, false
	}
	return knownDefaults[e], false
}

func (pk *opcPkg) contentTypesXML() []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	defs := map[string]string{"rels": ctRels, "xml": "application/xml"}
	var overrides []string
	for _, name := range pk.order {
		if !pk.has(name) {
			continue
		}
		ct, over := pk.contentType(name)
		e := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
		switch {
		case e == "rels" || ct == "":
		case over || defs[e] != "" && defs[e] != ct:
			overrides = append(overrides, fmt.Sprintf(`<Override PartName="/%s" ContentType="%s"/>`, xmlEscape(name), ct))
		default:
			defs[e] = ct
		}
	}
	keys := make([]string, 0, len(defs))
	for k := range defs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, `<Default Extension="%s" ContentType="%s"/>`, xmlEscape(k), defs[k])
	}
	for _, o := range overrides {
		b.WriteString(o)
	}
	b.WriteString(`</Types>`)
	return []byte(b.String())
}

// ---------------------------------------------------------------- relaciones

const relNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"

type rel struct {
	ID, Type, Target string
	External         bool
}

var reRel = regexp.MustCompile(`<Relationship\b[^>]*>`)

func parseRels(b []byte) []rel {
	var out []rel
	for _, t := range reRel.FindAllString(string(b), -1) {
		a := attrs(t)
		out = append(out, rel{ID: a["Id"], Type: a["Type"], Target: a["Target"], External: a["TargetMode"] == "External"})
	}
	return out
}

func relsXML(rs []rel) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for _, r := range rs {
		mode := ""
		if r.External {
			mode = ` TargetMode="External"`
		}
		fmt.Fprintf(&b, `<Relationship Id="%s" Type="%s" Target="%s"%s/>`, xmlEscape(r.ID), xmlEscape(r.Type), xmlEscape(r.Target), mode)
	}
	b.WriteString(`</Relationships>`)
	return []byte(b.String())
}

// relsOf devuelve el nombre del archivo de relaciones de una pieza.
func relsOf(part string) string {
	return path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
}

// sourceOf es lo contrario de relsOf.
func sourceOf(relsName string) string {
	dir := path.Dir(path.Dir(relsName))
	base := strings.TrimSuffix(path.Base(relsName), ".rels")
	if dir == "." {
		return base
	}
	return path.Join(dir, base)
}

func resolveTarget(source, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return strings.TrimPrefix(path.Clean(path.Join(path.Dir(source), target)), "/")
}

// fixRelationships quita las relaciones con piezas que ya no existen (las
// imágenes perdidas se sustituyen por un recuadro gris) y lo que las usaba.
func (pk *opcPkg) fixRelationships() {
	for pass := 0; pass < 3; pass++ {
		changed := false
		for _, name := range append([]string(nil), pk.order...) {
			if !pk.has(name) || !strings.HasSuffix(name, ".rels") || name == "_rels/.rels" {
				continue
			}
			src := sourceOf(name)
			if !pk.has(src) {
				pk.drop(name)
				continue
			}
			rs := parseRels(pk.part(name))
			var keep []rel
			removed := map[string]string{}
			for _, r := range rs {
				if r.External || r.ID == "" {
					keep = append(keep, r)
					continue
				}
				t := resolveTarget(src, r.Target)
				if pk.has(t) {
					keep = append(keep, r)
					continue
				}
				if strings.HasSuffix(r.Type, "/image") {
					if ph := placeholderImage(t); ph != nil {
						pk.set(t, ph)
						pk.note("una imagen se perdió: en su lugar queda un recuadro gris")
						pk.lost = true
						keep = append(keep, r)
						continue
					}
				}
				removed[r.ID] = r.Type
			}
			if len(removed) > 0 || len(keep) != len(rs) {
				pk.parts[name] = &opcPart{data: relsXML(keep)}
				pk.stripRefs(src, removed)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
}

// fixRootRels se asegura de que _rels/.rels apunta a la pieza principal.
func (pk *opcPkg) fixRootRels() {
	rs := parseRels(pk.part("_rels/.rels"))
	var keep []rel
	hasMain := false
	for _, r := range rs {
		if r.External || pk.has(resolveTarget("", r.Target)) {
			keep = append(keep, r)
			if resolveTarget("", r.Target) == pk.mainPart() {
				hasMain = true
			}
		}
	}
	if !hasMain {
		keep = append(keep, rel{ID: "rIdMain", Type: relNS + "officeDocument", Target: pk.mainPart()})
	}
	if pk.has("docProps/core.xml") && !relTo(keep, "docProps/core.xml") {
		keep = append(keep, rel{ID: "rIdCore", Type: "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties", Target: "docProps/core.xml"})
	}
	if pk.has("docProps/app.xml") && !relTo(keep, "docProps/app.xml") {
		keep = append(keep, rel{ID: "rIdApp", Type: relNS + "extended-properties", Target: "docProps/app.xml"})
	}
	if len(keep) != len(rs) || !pk.has("_rels/.rels") {
		pk.set("_rels/.rels", relsXML(keep))
	}
}

func relTo(rs []rel, target string) bool {
	for _, r := range rs {
		if resolveTarget("", r.Target) == target {
			return true
		}
	}
	return false
}

var reRelAttr = regexp.MustCompile(`\br:[A-Za-z]+="([^"]+)"`)

// stripRefs quita de una pieza lo que usaba relaciones eliminadas.
func (pk *opcPkg) stripRefs(src string, removed map[string]string) {
	if len(removed) == 0 || !strings.HasSuffix(src, ".xml") {
		return
	}
	s := string(pk.part(src))
	uses := func(block string) bool {
		for _, m := range reRelAttr.FindAllStringSubmatch(block, -1) {
			if _, ok := removed[m[1]]; ok {
				return true
			}
		}
		return false
	}
	for _, tag := range []string{"mc:AlternateContent", "w:drawing", "w:pict", "w:object", "p:graphicFrame", "p:pic",
		"xdr:twoCellAnchor", "xdr:oneCellAnchor", "xdr:absoluteAnchor", "oleObjects", "controls"} {
		s = removeBlocks(s, tag, uses)
	}
	s = unwrapBlocks(s, "w:hyperlink", uses)
	// Elementos sueltos (sin contenido) que apuntan a lo eliminado.
	s = regexp.MustCompile(`<[A-Za-z][\w:.-]*\b[^<>]*\br:[A-Za-z]+="[^"]*"[^<>]*/>`).ReplaceAllStringFunc(s, func(t string) string {
		if uses(t) {
			return ""
		}
		return t
	})
	types := map[string]bool{}
	for _, t := range removed {
		types[path.Base(t)] = true
	}
	if types["footnotes"] {
		s = regexp.MustCompile(`<w:footnoteReference\b[^>]*/>`).ReplaceAllString(s, "")
	}
	if types["endnotes"] {
		s = regexp.MustCompile(`<w:endnoteReference\b[^>]*/>`).ReplaceAllString(s, "")
	}
	if types["comments"] {
		s = regexp.MustCompile(`<w:comment(Reference|RangeStart|RangeEnd)\b[^>]*/>`).ReplaceAllString(s, "")
	}
	if types["numbering"] {
		s = removeBlocks(s, "w:numPr", func(string) bool { return true })
	}
	// Listas que quedaron vacías.
	s = regexp.MustCompile(`<(externalReferences|pivotCaches|p:notesMasterIdLst|p:handoutMasterIdLst|p:sldIdLst)>\s*</(externalReferences|pivotCaches|p:notesMasterIdLst|p:handoutMasterIdLst|p:sldIdLst)>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`<tableParts\b[^>]*>\s*</tableParts>`).ReplaceAllString(s, "")
	s = recount(s, "tableParts", "tablePart")
	if pk.kind == "xlsx" && src == "xl/workbook.xml" {
		for _, t := range removed {
			if strings.HasSuffix(t, "/worksheet") {
				s = removeBlocks(s, "definedNames", func(string) bool { return true })
				break
			}
		}
	}
	pk.parts[src] = &opcPart{data: []byte(s)}
}

// removeBlocks quita los elementos <tag>…</tag> (o <tag …/>) que cumplen pred,
// teniendo en cuenta los que van anidados dentro de otros iguales.
func removeBlocks(s, tag string, pred func(string) bool) string {
	open := "<" + tag
	closeT := "</" + tag + ">"
	var b strings.Builder
	pos := 0
	for {
		i := indexTag(s, open, pos)
		if i < 0 {
			break
		}
		end := blockEnd(s, i, open, closeT)
		if end < 0 {
			break
		}
		if pred(s[i:end]) {
			b.WriteString(s[pos:i])
		} else {
			b.WriteString(s[pos:end])
		}
		pos = end
	}
	b.WriteString(s[pos:])
	return b.String()
}

// unwrapBlocks quita la etiqueta de apertura y cierre de los elementos que
// cumplen pred, pero conserva su contenido (p. ej. el texto de un enlace).
func unwrapBlocks(s, tag string, pred func(string) bool) string {
	open := "<" + tag
	closeT := "</" + tag + ">"
	var b strings.Builder
	pos := 0
	for {
		i := indexTag(s, open, pos)
		if i < 0 {
			break
		}
		gt := strings.IndexByte(s[i:], '>')
		if gt < 0 {
			break
		}
		openTag := s[i : i+gt+1]
		end := blockEnd(s, i, open, closeT)
		if end < 0 || !pred(openTag) {
			b.WriteString(s[pos : i+gt+1])
			pos = i + gt + 1
			continue
		}
		b.WriteString(s[pos:i])
		if !strings.HasSuffix(openTag, "/>") {
			b.WriteString(s[i+gt+1 : end-len(closeT)])
		}
		pos = end
	}
	b.WriteString(s[pos:])
	return b.String()
}

func indexTag(s, open string, from int) int {
	for {
		i := strings.Index(s[from:], open)
		if i < 0 {
			return -1
		}
		i += from
		if j := i + len(open); j < len(s) && (s[j] == '>' || s[j] == ' ' || s[j] == '/' || s[j] == '\t' || s[j] == '\r' || s[j] == '\n') {
			return i
		}
		from = i + len(open)
	}
}

// blockEnd devuelve dónde termina el elemento que empieza en i.
func blockEnd(s string, i int, open, closeT string) int {
	gt := strings.IndexByte(s[i:], '>')
	if gt < 0 {
		return -1
	}
	if s[i+gt-1] == '/' {
		return i + gt + 1
	}
	depth := 1
	pos := i + gt + 1
	for depth > 0 {
		o := indexTag(s, open, pos)
		c := strings.Index(s[pos:], closeT)
		if c < 0 {
			return -1
		}
		c += pos
		if o >= 0 && o < c {
			g := strings.IndexByte(s[o:], '>')
			if g < 0 {
				return -1
			}
			if s[o+g-1] != '/' {
				depth++
			}
			pos = o + g + 1
			continue
		}
		depth--
		pos = c + len(closeT)
	}
	return pos
}

var reCount = regexp.MustCompile(`\scount="\d+"`)

// recount corrige el atributo count de una lista tras quitarle elementos.
func recount(s, list, item string) string {
	i := indexTag(s, "<"+list, 0)
	if i < 0 {
		return s
	}
	end := blockEnd(s, i, "<"+list, "</"+list+">")
	if end < 0 {
		return s
	}
	gt := strings.IndexByte(s[i:], '>') + i
	n := 0
	for p := gt; ; {
		k := indexTag(s[:end], "<"+item, p)
		if k < 0 {
			break
		}
		n++
		p = k + 1
	}
	tag := s[i : gt+1]
	nt := reCount.ReplaceAllString(tag, ` count="`+strconv.Itoa(n)+`"`)
	return s[:i] + nt + s[gt+1:]
}

// ---------------------------------------------------------------- Excel

var reSheetTag = regexp.MustCompile(`<sheet\b[^>]*>`)
var reCellS = regexp.MustCompile(`<c\b([^>]*?)\st="s"([^>]*)>\s*<v>(\d+)</v>\s*</c>`)
var reSAttr = regexp.MustCompile(`\ss="(\d+)"`)

var reRowTag = regexp.MustCompile(`<row\b[^>]*>`)
var reCellTag = regexp.MustCompile(`<c\b[^>]*\sr="([A-Za-z]+)(\d+)"`)

// saneRows corta una hoja rescatada en la primera fila incoherente: número
// de fila que no avanza o celdas que dicen ser de otra fila. Es la señal de
// que, al llegar al daño, el descompresor empezó a producir datos falsos.
func saneRows(b []byte) ([]byte, int) {
	s := string(b)
	start := strings.Index(s, "<sheetData")
	if start < 0 {
		return b, 0
	}
	last, kept := 0, 0
	cut := -1
	for pos := start; ; {
		i := indexTag(s, "<row", pos)
		if i < 0 {
			break
		}
		end := blockEnd(s, i, "<row", "</row>")
		if end < 0 {
			break
		}
		row := s[i:end]
		ok := true
		n := last + 1
		if m := regexp.MustCompile(`^<row\b[^>]*\sr="(\d+)"`).FindStringSubmatch(row); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
		if n <= last || n > 1048576 {
			ok = false
		}
		for _, c := range reCellTag.FindAllStringSubmatch(row, -1) {
			if r, _ := strconv.Atoi(c[2]); r != n {
				ok = false
			}
		}
		if !ok {
			cut = i
			break
		}
		last = n
		kept++
		pos = end
	}
	if cut < 0 {
		return b, kept
	}
	e := strings.LastIndex(s, "</sheetData>")
	if e < cut {
		return b, kept
	}
	return []byte(s[:cut] + s[e:]), kept
}

func (pk *opcPkg) sheetParts() []string {
	var out []string
	re := regexp.MustCompile(`^xl/worksheets/sheet(\d+)\.xml$`)
	for _, n := range pk.order {
		if pk.has(n) && re.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return natLess(out[i], out[j]) })
	return out
}

// natLess ordena «sheet2» antes que «sheet10».
func natLess(a, b string) bool {
	na, nb := lastNum(a), lastNum(b)
	if na != nb && na >= 0 && nb >= 0 {
		return na < nb
	}
	return a < b
}

func lastNum(s string) int {
	m := regexp.MustCompile(`(\d+)\D*$`).FindStringSubmatch(s)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func (pk *opcPkg) fixXLSX() {
	// Textos compartidos: los que se perdieron se marcan en las celdas.
	nStr := -1
	if p := pk.parts["xl/sharedStrings.xml"]; p != nil {
		if p.orig == nil {
			nStr = strings.Count(string(p.data), "</si>") + strings.Count(string(p.data), "<si/>")
			s := regexp.MustCompile(`<sst\b[^>]*>`).ReplaceAllStringFunc(string(p.data), func(t string) string {
				return regexp.MustCompile(`\s(count|uniqueCount)="\d+"`).ReplaceAllString(t, "")
			})
			p.data = []byte(s)
		}
	} else {
		nStr = 0
	}
	maxStyle := 0
	for _, sh := range pk.sheetParts() {
		s := string(pk.part(sh))
		if nStr >= 0 {
			lostCells := 0
			s = reCellS.ReplaceAllStringFunc(s, func(c string) string {
				m := reCellS.FindStringSubmatch(c)
				if i, _ := strconv.Atoi(m[3]); i < nStr {
					return c
				}
				lostCells++
				return `<c` + m[1] + ` t="inlineStr"` + m[2] + `><is><t>[texto perdido]</t></is></c>`
			})
			if lostCells > 0 {
				pk.note("%s con texto perdido en %s", path.Base(sh), plural(lostCells, "celda", "celdas"))
				pk.parts[sh] = &opcPart{data: []byte(s)}
			}
		}
		for _, m := range reSAttr.FindAllStringSubmatch(s, -1) {
			if n, _ := strconv.Atoi(m[1]); n > maxStyle {
				maxStyle = n
			}
		}
	}
	// Estilos: si se perdieron, unos mínimos (con tantos formatos como usan las celdas).
	styles := pk.parts["xl/styles.xml"]
	if styles == nil || styles.orig == nil && !stylesUsable(styles.data, maxStyle) {
		pk.set("xl/styles.xml", minimalXLStyles(maxStyle+1))
		for _, sh := range pk.sheetParts() {
			s := removeBlocks(string(pk.part(sh)), "conditionalFormatting", func(string) bool { return true })
			s = removeBlocks(s, "extLst", func(string) bool { return true })
			pk.parts[sh] = &opcPart{data: []byte(s)}
		}
		pk.note("los formatos de celda se perdieron: los valores están, pero las fechas pueden verse como números")
		pk.lost = true
	}
	// Libro (lista de hojas) y sus relaciones.
	wb := pk.part("xl/workbook.xml")
	wbRels := pk.part("xl/_rels/workbook.xml.rels")
	if wb == nil || wbRels == nil || !strings.Contains(string(wb), "<sheet ") {
		pk.rebuildWorkbook(wb, wbRels)
	}
}

func stylesUsable(b []byte, maxStyle int) bool {
	s := string(b)
	for _, need := range []string{"</fonts>", "</fills>", "</borders>", "</cellXfs>"} {
		if !strings.Contains(s, need) {
			return false
		}
	}
	i := strings.Index(s, "<cellXfs")
	j := strings.Index(s, "</cellXfs>")
	n := strings.Count(s[i:j], "<xf ") + strings.Count(s[i:j], "<xf>")
	return n > maxStyle
}

func minimalXLStyles(n int) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		`<fonts count="1"><font><sz val="11"/><name val="Calibri"/><family val="2"/></font></fonts>` +
		`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
		`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
		`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>`)
	fmt.Fprintf(&b, `<cellXfs count="%d">`, n)
	for i := 0; i < n; i++ {
		b.WriteString(`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>`)
	}
	b.WriteString(`</cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`)
	return []byte(b.String())
}

// sheetNames devuelve el nombre de cada hoja (por su archivo) según el libro.
func sheetNames(wb, wbRels []byte) map[string]string {
	targets := map[string]string{}
	for _, r := range parseRels(wbRels) {
		targets[r.ID] = resolveTarget("xl/workbook.xml", r.Target)
	}
	names := map[string]string{}
	for _, t := range reSheetTag.FindAllString(string(wb), -1) {
		a := attrs(t)
		if f := targets[a["r:id"]]; f != "" && a["name"] != "" {
			names[f] = a["name"]
		}
	}
	return names
}

func (pk *opcPkg) rebuildWorkbook(oldWB, oldRels []byte) {
	sheets := pk.sheetParts()
	if len(sheets) == 0 {
		pk.drop("xl/workbook.xml")
		return
	}
	names := sheetNames(oldWB, oldRels)
	var wb strings.Builder
	wb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	var rs []rel
	used := map[string]bool{}
	for i, sh := range sheets {
		name := names[sh]
		if name == "" || used[strings.ToLower(name)] {
			name = fmt.Sprintf("Hoja%d", i+1)
		}
		used[strings.ToLower(name)] = true
		id := fmt.Sprintf("rIdS%d", i+1)
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="%s"/>`, xmlEscape(name), i+1, id)
		rs = append(rs, rel{ID: id, Type: relNS + "worksheet", Target: strings.TrimPrefix(sh, "xl/")})
	}
	wb.WriteString(`</sheets></workbook>`)
	for _, x := range []struct{ part, typ string }{
		{"xl/styles.xml", "styles"}, {"xl/sharedStrings.xml", "sharedStrings"}, {"xl/theme/theme1.xml", "theme"},
	} {
		if pk.has(x.part) {
			rs = append(rs, rel{ID: "rId" + x.typ, Type: relNS + x.typ, Target: strings.TrimPrefix(x.part, "xl/")})
		}
	}
	pk.set("xl/workbook.xml", []byte(wb.String()))
	pk.set("xl/_rels/workbook.xml.rels", relsXML(rs))
	if len(names) == 0 {
		pk.note("la lista de hojas se perdió: se rehízo (las hojas se llaman Hoja1, Hoja2…)")
	} else {
		pk.note("se rehízo la lista de hojas del libro")
	}
}

// valuesWorkbook crea un Excel nuevo solo con los valores rescatados.
func (pk *opcPkg) valuesWorkbook() ([]byte, int) {
	strs := sharedStrings(pk.part("xl/sharedStrings.xml"))
	names := sheetNames(pk.part("xl/workbook.xml"), pk.part("xl/_rels/workbook.xml.rels"))
	var sheets []valSheet
	cells := 0
	for i, sh := range pk.sheetParts() {
		vs := valSheet{Name: names[sh]}
		if vs.Name == "" {
			vs.Name = fmt.Sprintf("Hoja%d", i+1)
		}
		vs.Rows = sheetValues(pk.part(sh), strs)
		for _, r := range vs.Rows {
			cells += len(r.Cells)
		}
		sheets = append(sheets, vs)
	}
	if cells == 0 {
		return nil, 0
	}
	return buildValuesXLSX(sheets), cells
}

func sharedStrings(b []byte) []string {
	var out []string
	d := xml.NewDecoder(bytes.NewReader(b))
	var cur strings.Builder
	inSI, inT, inRPh := false, false, false
	for {
		tok, err := d.RawToken()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = true
			case "rPh":
				inRPh = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
				inSI = false
			case "t":
				inT = false
			case "rPh":
				inRPh = false
			}
		case xml.CharData:
			if inSI && inT && !inRPh {
				cur.Write(t)
			}
		}
	}
	return out
}

type valCell struct {
	Col  int
	Text string
	Num  bool
}

type valRow struct {
	Row   int
	Cells []valCell
}

type valSheet struct {
	Name string
	Rows []valRow
}

// sheetValues lee los valores de una hoja (solo valores, sin formatos).
func sheetValues(b []byte, strs []string) []valRow {
	var rows []valRow
	d := xml.NewDecoder(bytes.NewReader(b))
	var row *valRow
	var cell valCell
	var typ, val string
	var txt strings.Builder
	inV, inT, inC, inRPh := false, false, false, false
	nextRow, nextCol := 1, 0
	for {
		tok, err := d.RawToken()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				r := nextRow
				for _, a := range t.Attr {
					if a.Name.Local == "r" {
						r, _ = strconv.Atoi(a.Value)
					}
				}
				rows = append(rows, valRow{Row: r})
				row = &rows[len(rows)-1]
				nextRow, nextCol = r+1, 0
			case "c":
				inC = true
				cell, typ, val = valCell{Col: nextCol}, "", ""
				txt.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						if c, _, ok := cellRef(a.Value); ok {
							cell.Col = c
						}
					case "t":
						typ = a.Value
					}
				}
			case "v":
				inV = true
			case "t":
				inT = true
			case "rPh":
				inRPh = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "rPh":
				inRPh = false
			case "c":
				inC = false
				nextCol = cell.Col + 1
				switch typ {
				case "s":
					if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && i >= 0 && i < len(strs) {
						cell.Text = strs[i]
					} else if val != "" {
						cell.Text = "[texto perdido]"
					}
				case "inlineStr":
					cell.Text = txt.String()
				case "str", "e":
					cell.Text = val
				case "b":
					cell.Text = map[bool]string{true: "VERDADERO", false: "FALSO"}[strings.TrimSpace(val) == "1"]
				default:
					cell.Text, cell.Num = strings.TrimSpace(val), true
					if _, err := strconv.ParseFloat(cell.Text, 64); err != nil {
						cell.Num = false
					}
				}
				if cell.Text != "" && row != nil {
					row.Cells = append(row.Cells, cell)
				}
			}
		case xml.CharData:
			if inC && inV {
				val += string(t)
			} else if inC && inT && !inRPh {
				txt.Write(t)
			}
		}
	}
	return rows
}

// cellRef convierte «B12» en columna 1 (desde 0) y fila 12.
func cellRef(s string) (col, row int, ok bool) {
	i := 0
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		c := s[i]
		if c >= 'a' {
			c -= 32
		}
		col = col*26 + int(c-'A'+1)
		i++
	}
	if i == 0 {
		return 0, 0, false
	}
	row, err := strconv.Atoi(s[i:])
	return col - 1, row, err == nil
}

func colName(c int) string {
	s := ""
	for c++; c > 0; c = (c - 1) / 26 {
		s = string(rune('A'+(c-1)%26)) + s
	}
	return s
}

var badSheetChars = strings.NewReplacer("[", "(", "]", ")", ":", "-", "*", "", "?", "", "/", "-", `\`, "-")

// buildValuesXLSX escribe un libro de Excel nuevo y sencillo con los valores.
func buildValuesXLSX(sheets []valSheet) []byte {
	z := newZipOut()
	var wb, rs, ct strings.Builder
	ct.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="` + ctRels + `"/><Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="` + ctSML + `sheet.main+xml"/>` +
		`<Override PartName="/xl/styles.xml" ContentType="` + ctSML + `styles+xml"/>`)
	wb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	var wrs []rel
	used := map[string]bool{}
	for i, sh := range sheets {
		name := strings.TrimSpace(badSheetChars.Replace(sh.Name))
		if r := []rune(name); len(r) > 31 {
			name = string(r[:31])
		}
		if name == "" || used[strings.ToLower(name)] {
			name = fmt.Sprintf("Hoja%d", i+1)
		}
		used[strings.ToLower(name)] = true
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEscape(name), i+1, i+1)
		wrs = append(wrs, rel{ID: fmt.Sprintf("rId%d", i+1), Type: relNS + "worksheet", Target: fmt.Sprintf("worksheets/sheet%d.xml", i+1)})
		fmt.Fprintf(&ct, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="%sworksheet+xml"/>`, i+1, ctSML)
		var sx strings.Builder
		sx.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
			`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
		lastRow := 0
		for _, r := range sh.Rows {
			if r.Row <= lastRow || len(r.Cells) == 0 {
				continue
			}
			lastRow = r.Row
			fmt.Fprintf(&sx, `<row r="%d">`, r.Row)
			lastCol := -1
			for _, c := range r.Cells {
				if c.Col <= lastCol {
					continue
				}
				lastCol = c.Col
				ref := colName(c.Col) + strconv.Itoa(r.Row)
				if c.Num {
					fmt.Fprintf(&sx, `<c r="%s"><v>%s</v></c>`, ref, c.Text)
				} else {
					fmt.Fprintf(&sx, `<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, xmlEscape(c.Text))
				}
			}
			sx.WriteString(`</row>`)
		}
		sx.WriteString(`</sheetData></worksheet>`)
		z.add(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), []byte(sx.String()), 8)
	}
	wb.WriteString(`</sheets></workbook>`)
	ct.WriteString(`</Types>`)
	wrs = append(wrs, rel{ID: "rIdStyles", Type: relNS + "styles", Target: "styles.xml"})
	rs.Write(relsXML([]rel{{ID: "rId1", Type: relNS + "officeDocument", Target: "xl/workbook.xml"}}))
	z.add("[Content_Types].xml", []byte(ct.String()), 8)
	z.add("_rels/.rels", []byte(rs.String()), 8)
	z.add("xl/workbook.xml", []byte(wb.String()), 8)
	z.add("xl/_rels/workbook.xml.rels", relsXML(wrs), 8)
	z.add("xl/styles.xml", minimalXLStyles(1), 8)
	b, _ := z.bytes()
	return b
}

// ---------------------------------------------------------------- Word

const minimalWordStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri"/><w:sz w:val="22"/></w:rPr></w:rPrDefault><w:pPrDefault/></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style></w:styles>`

func (pk *opcPkg) fixDOCX() {
	if !pk.has("word/document.xml") {
		return
	}
	relsName := "word/_rels/document.xml.rels"
	if !pk.has(relsName) {
		// Sin relaciones no se sabe a qué apuntaba cada referencia: se
		// quitan (imágenes, encabezados, enlaces) y se rehacen las básicas.
		var rs []rel
		for _, x := range []struct{ part, typ string }{
			{"word/styles.xml", "styles"}, {"word/settings.xml", "settings"}, {"word/fontTable.xml", "fontTable"},
			{"word/numbering.xml", "numbering"}, {"word/theme/theme1.xml", "theme"}, {"word/webSettings.xml", "webSettings"},
			{"word/footnotes.xml", "footnotes"}, {"word/endnotes.xml", "endnotes"},
		} {
			if pk.has(x.part) {
				rs = append(rs, rel{ID: "rId" + x.typ, Type: relNS + x.typ, Target: strings.TrimPrefix(x.part, "word/")})
			}
		}
		all := map[string]string{}
		for _, m := range reRelAttr.FindAllStringSubmatch(string(pk.part("word/document.xml")), -1) {
			all[m[1]] = "desconocido"
		}
		if !pk.has("word/footnotes.xml") {
			all["~footnotes"] = relNS + "footnotes"
		}
		if !pk.has("word/endnotes.xml") {
			all["~endnotes"] = relNS + "endnotes"
		}
		if !pk.has("word/numbering.xml") {
			all["~numbering"] = relNS + "numbering"
		}
		pk.set(relsName, relsXML(rs))
		pk.stripRefs("word/document.xml", all)
		if len(all) > 3 {
			pk.note("se perdieron las referencias del documento: las imágenes y encabezados no se pudieron recuperar")
			pk.lost = true
		}
	}
	// Estilos imprescindibles.
	for _, r := range parseRels(pk.part(relsName)) {
		if strings.HasSuffix(r.Type, "/styles") {
			if t := resolveTarget("word/document.xml", r.Target); !pk.has(t) {
				pk.set(t, []byte(minimalWordStyles))
				pk.note("los estilos se perdieron: el texto está, con un formato sencillo")
			}
		}
	}
}

// docxText saca el texto de word/document.xml (un párrafo por línea).
func docxText(b []byte) string {
	if b == nil {
		return ""
	}
	var out strings.Builder
	d := xml.NewDecoder(bytes.NewReader(b))
	inT := false
	for {
		tok, err := d.RawToken()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inT = t.Name.Space == "w" || t.Name.Space == ""
			case "tab":
				out.WriteString("\t")
			case "br", "cr":
				out.WriteString("\r\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				out.WriteString("\r\n")
			case "tc":
				out.WriteString("\t")
			}
		case xml.CharData:
			if inT {
				out.Write(t)
			}
		}
	}
	return out.String()
}

// ---------------------------------------------------------------- PowerPoint

func (pk *opcPkg) slideParts() []string {
	var out []string
	re := regexp.MustCompile(`^ppt/slides/slide\d+\.xml$`)
	for _, n := range pk.order {
		if pk.has(n) && re.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return natLess(out[i], out[j]) })
	return out
}

func (pk *opcPkg) fixPPTX() {
	// Una diapositiva necesita su diseño, el diseño su patrón y el patrón su tema.
	needs := func(part, typ string) bool {
		for _, r := range parseRels(pk.part(relsOf(part))) {
			if strings.HasSuffix(r.Type, "/"+typ) && pk.has(resolveTarget(part, r.Target)) {
				return true
			}
		}
		return false
	}
	re := regexp.MustCompile(`^ppt/(slideMasters/slideMaster|slideLayouts/slideLayout|slides/slide)\d+\.xml$`)
	for _, n := range append([]string(nil), pk.order...) {
		if !pk.has(n) || !re.MatchString(n) {
			continue
		}
		switch {
		case strings.HasPrefix(n, "ppt/slideMasters/") && !needs(n, "theme"):
			pk.drop(n)
		case strings.HasPrefix(n, "ppt/slideLayouts/") && !needs(n, "slideMaster"):
			pk.drop(n)
		}
	}
	for _, n := range pk.slideParts() {
		if !needs(n, "slideLayout") {
			pk.drop(n)
			pk.note("la diapositiva %s perdió su diseño y no se pudo conservar (su texto está en el archivo de texto rescatado)", path.Base(n))
			pk.lost = true
		}
	}
	pres := pk.part("ppt/presentation.xml")
	presRels := pk.part("ppt/_rels/presentation.xml.rels")
	if pres != nil && presRels != nil && pk.parts["ppt/presentation.xml"].orig != nil {
		return
	}
	// Se rehace la presentación con los patrones y diapositivas que quedan.
	var masters []string
	for _, n := range pk.order {
		if pk.has(n) && strings.HasPrefix(n, "ppt/slideMasters/") && strings.HasSuffix(n, ".xml") {
			masters = append(masters, n)
		}
	}
	if len(masters) == 0 {
		pk.drop("ppt/presentation.xml")
		return
	}
	size := `<p:sldSz cx="12192000" cy="6858000"/>`
	if m := regexp.MustCompile(`<p:sldSz\b[^>]*/>`).Find(pres); m != nil {
		size = string(m)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
		`xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldMasterIdLst>`)
	var rs []rel
	for i, m := range masters {
		id := fmt.Sprintf("rIdM%d", i+1)
		fmt.Fprintf(&b, `<p:sldMasterId id="%d" r:id="%s"/>`, 2147483648+i, id)
		rs = append(rs, rel{ID: id, Type: relNS + "slideMaster", Target: strings.TrimPrefix(m, "ppt/")})
	}
	b.WriteString(`</p:sldMasterIdLst>`)
	if slides := pk.slideParts(); len(slides) > 0 {
		b.WriteString(`<p:sldIdLst>`)
		for i, s := range slides {
			id := fmt.Sprintf("rIdS%d", i+1)
			fmt.Fprintf(&b, `<p:sldId id="%d" r:id="%s"/>`, 256+i, id)
			rs = append(rs, rel{ID: id, Type: relNS + "slide", Target: strings.TrimPrefix(s, "ppt/")})
		}
		b.WriteString(`</p:sldIdLst>`)
	}
	b.WriteString(size + `<p:notesSz cx="6858000" cy="9144000"/></p:presentation>`)
	for _, x := range []string{"presProps", "viewProps", "tableStyles"} {
		if pk.has("ppt/" + x + ".xml") {
			rs = append(rs, rel{ID: "rId" + x, Type: relNS + x, Target: x + ".xml"})
		}
	}
	if pk.has("ppt/theme/theme1.xml") {
		rs = append(rs, rel{ID: "rIdTheme", Type: relNS + "theme", Target: "theme/theme1.xml"})
	}
	pk.set("ppt/presentation.xml", []byte(b.String()))
	pk.set("ppt/_rels/presentation.xml.rels", relsXML(rs))
	pk.note("se rehízo el índice de la presentación con las diapositivas que quedan")
}

func (pk *opcPkg) pptxText() string {
	var out strings.Builder
	for i, s := range pk.slideParts() {
		fmt.Fprintf(&out, "--- Diapositiva %d ---\r\n", i+1)
		d := xml.NewDecoder(bytes.NewReader(pk.part(s)))
		inT := false
		for {
			tok, err := d.RawToken()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				inT = t.Name.Local == "t"
			case xml.EndElement:
				if t.Name.Local == "t" {
					inT = false
				}
				if t.Name.Local == "p" {
					out.WriteString("\r\n")
				}
			case xml.CharData:
				if inT {
					out.Write(t)
				}
			}
		}
		out.WriteString("\r\n")
	}
	return out.String()
}

// ---------------------------------------------------------------- escritura y comprobación

func (pk *opcPkg) write() ([]byte, error) {
	z := newZipOut()
	if err := z.add("[Content_Types].xml", pk.contentTypesXML(), 8); err != nil {
		return nil, err
	}
	written := map[string]bool{"[Content_Types].xml": true}
	names := append([]string{"_rels/.rels"}, pk.order...)
	for _, n := range names {
		p := pk.parts[n]
		if p == nil || written[n] {
			continue
		}
		written[n] = true
		if ct, _ := pk.contentType(n); ct == "" && !strings.HasSuffix(n, ".rels") {
			continue // sin tipo conocido: Office no la aceptaría
		}
		var err error
		if p.orig != nil && p.orig.Raw != nil {
			err = z.raw(p.orig)
		} else {
			err = z.add(n, p.data, 8)
		}
		if err != nil {
			return nil, err
		}
	}
	return z.bytes()
}

// checkOPC comprueba que el documento rehecho es coherente: todas las piezas
// se leen, los XML están bien formados y las relaciones apuntan a algo.
func checkOPC(b []byte) error {
	parts := zipFromCentral(b)
	if parts == nil {
		return fmt.Errorf("no se pudo escribir")
	}
	have := map[string]bool{}
	for _, p := range parts {
		if !p.OK {
			return fmt.Errorf("pieza ilegible %s", p.Name)
		}
		have[p.Name] = true
	}
	for _, p := range parts {
		if (strings.HasSuffix(p.Name, ".xml") || strings.HasSuffix(p.Name, ".rels")) && !wellFormed(p.Data) {
			return fmt.Errorf("XML mal formado en %s", p.Name)
		}
		if strings.HasSuffix(p.Name, ".rels") {
			src := sourceOf(p.Name)
			for _, r := range parseRels(p.Data) {
				if !r.External && !have[resolveTarget(src, r.Target)] {
					return fmt.Errorf("relación rota en %s", p.Name)
				}
			}
		}
	}
	if !have["[Content_Types].xml"] || !have["_rels/.rels"] {
		return fmt.Errorf("faltan piezas básicas")
	}
	return nil
}

// ---------------------------------------------------------------- imágenes de relleno

// placeholderImage crea un recuadro gris del formato que indica el nombre.
func placeholderImage(name string) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			c := color.RGBA{0xD9, 0xD9, 0xD9, 0xFF}
			if x == y+8 || x == 55-y || x == 0 || y == 0 || x == 63 || y == 47 {
				c = color.RGBA{0x99, 0x99, 0x99, 0xFF}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	var err error
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		err = png.Encode(&b, img)
	case ".jpg", ".jpeg", ".jpe", ".jfif":
		err = jpeg.Encode(&b, img, nil)
	case ".gif":
		err = gif.Encode(&b, img, nil)
	default:
		return nil
	}
	if err != nil {
		return nil
	}
	return b.Bytes()
}

func utf8BOM(s string) []byte { return append([]byte("\xEF\xBB\xBF"), s...) }

// repairZipArchive rehace un ZIP normal con las piezas sanas.
func repairZipArchive(parts []*zipPart, ext string) *repairOut {
	out := &repairOut{}
	z := newZipOut()
	good, bad := 0, 0
	for _, p := range parts {
		if p.OK && p.Raw != nil {
			if z.raw(p) == nil {
				good++
			}
		} else {
			bad++
			out.Notes = append(out.Notes, "«"+p.Name+"» se perdió")
		}
	}
	if good == 0 {
		out.Notes = append(out.Notes, "no queda ningún archivo sano dentro")
		return out
	}
	b, err := z.bytes()
	if err != nil {
		return out
	}
	out.Repaired, out.RepairedExt = b, ext
	out.Complete = bad == 0
	out.Notes = append([]string{fmt.Sprintf("%s sanos de %d", plural(good, "archivo", "archivos"), good+bad)}, out.Notes...)
	return out
}

var _ = io.EOF
