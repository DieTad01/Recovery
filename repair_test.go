package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testWorkbook crea un Excel con n filas: texto, número y fecha por fila.
func testWorkbook(n int) ([]byte, []valRow) {
	var rows []valRow
	rnd := rand.New(rand.NewSource(7))
	for i := 1; i <= n; i++ {
		rows = append(rows, valRow{Row: i, Cells: []valCell{
			{Col: 0, Text: fmt.Sprintf("Cliente %d %x", i, rnd.Int63())},
			{Col: 1, Text: strconv.FormatFloat(rnd.Float64()*10000, 'f', 2, 64), Num: true},
			{Col: 2, Text: strconv.Itoa(40000 + i), Num: true},
		}})
	}
	return buildValuesXLSX([]valSheet{{Name: "Nómina", Rows: rows}}), rows
}

// partRange devuelve dónde están los datos comprimidos de una pieza.
func partRange(t *testing.T, data []byte, name string) (int64, int64) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			off, _ := f.DataOffset()
			return off, int64(f.CompressedSize64)
		}
	}
	t.Fatalf("no está %s", name)
	return 0, 0
}

func valuesOf(t *testing.T, xlsx []byte) map[string]bool {
	out := map[string]bool{}
	for _, p := range salvageZip(xlsx) {
		if !p.OK {
			t.Fatalf("el resultado tiene una pieza ilegible: %s", p.Name)
		}
		if strings.HasPrefix(p.Name, "xl/worksheets/") {
			for _, r := range sheetValues(p.Data, nil) {
				for _, c := range r.Cells {
					out[c.Text] = true
				}
			}
		}
	}
	return out
}

func TestRepairXLSX(t *testing.T) {
	orig, rows := testWorkbook(3000)
	all := map[string]bool{}
	for _, r := range rows {
		for _, c := range r.Cells {
			all[c.Text] = true
		}
	}
	off, n := partRange(t, orig, "xl/worksheets/sheet1.xml")
	for name, damage := range map[string]func([]byte){
		"sobrescrito en medio": func(d []byte) { rand.New(rand.NewSource(1)).Read(d[off+n/2 : off+n/2+4096]) },
		"en blanco en medio":   func(d []byte) { clear(d[off+n/2 : off+n/2+8192]) },
		// El libro, los tipos y el índice van al final: se pierden y hay que rehacerlos.
		"final perdido": func(d []byte) { rand.New(rand.NewSource(2)).Read(d[off+n-200:]) },
	} {
		d := append([]byte(nil), orig...)
		damage(d)
		if v, _ := validateContent(bytes.NewReader(d), int64(len(d)), "xlsx"); v != vBad {
			t.Fatalf("%s: el dañado debería detectarse como dañado", name)
		}
		r := repairData(d, "xlsx")
		if r.Repaired == nil || r.Rescue == nil {
			t.Fatalf("%s: sin resultado: %v", name, r.Notes)
		}
		if err := checkOPC(r.Repaired); err != nil {
			t.Fatalf("%s: el reparado no es coherente: %v", name, err)
		}
		if v, _ := validateContent(bytes.NewReader(r.Repaired), int64(len(r.Repaired)), "xlsx"); v != vOK {
			t.Fatalf("%s: el reparado no pasa la comprobación", name)
		}
		got := valuesOf(t, r.Repaired)
		for v := range got {
			if !all[v] && v != "[texto perdido]" {
				t.Errorf("%s: valor que no estaba en el original: %q", name, v)
			}
		}
		if len(got) < len(all)/4 {
			t.Errorf("%s: se rescató muy poco (%d de %d valores)", name, len(got), len(all))
		}
		t.Logf("%s: %d de %d valores rescatados; %s", name, len(got), len(all), strings.Join(r.Notes, "; "))
	}
}

// testDocx crea un Word mínimo con n párrafos.
func testDocx(n int) []byte {
	z := newZipOut()
	z.add("[Content_Types].xml", []byte(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`+
		`<Default Extension="rels" ContentType="`+ctRels+`"/><Default Extension="xml" ContentType="application/xml"/>`+
		`<Override PartName="/word/document.xml" ContentType="`+ctWML+`document.main+xml"/>`+
		`<Override PartName="/word/styles.xml" ContentType="`+ctWML+`styles+xml"/>`+
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`), 8)
	z.add("_rels/.rels", relsXML([]rel{{ID: "rId1", Type: relNS + "officeDocument", Target: "word/document.xml"},
		{ID: "rId2", Type: "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties", Target: "docProps/core.xml"}}), 8)
	z.add("docProps/core.xml", []byte(`<?xml version="1.0"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" `+
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`+
		`<dc:creator>Ana</dc:creator><dcterms:created xsi:type="dcterms:W3CDTF">2024-03-02T10:11:12Z</dcterms:created></cp:coreProperties>`), 8)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>`)
	rnd := rand.New(rand.NewSource(3))
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<w:p><w:r><w:t>Párrafo %d del acta: acuerdo %x</w:t></w:r></w:p>`, i, rnd.Int63())
	}
	b.WriteString(`<w:sectPr/></w:body></w:document>`)
	z.add("word/document.xml", []byte(b.String()), 8)
	z.add("word/_rels/document.xml.rels", relsXML([]rel{{ID: "rId1", Type: relNS + "styles", Target: "styles.xml"}}), 8)
	z.add("word/styles.xml", []byte(minimalWordStyles), 8)
	out, _ := z.bytes()
	return out
}

func TestRepairDOCX(t *testing.T) {
	orig := testDocx(4000)
	off, n := partRange(t, orig, "word/document.xml")
	d := append([]byte(nil), orig...)
	rand.New(rand.NewSource(5)).Read(d[off+n/3 : off+n/3+4096])
	r := repairData(d, "docx")
	if r.Repaired == nil || r.Rescue == nil {
		t.Fatalf("sin resultado: %v", r.Notes)
	}
	if err := checkOPC(r.Repaired); err != nil {
		t.Fatal(err)
	}
	var doc []byte
	for _, p := range salvageZip(r.Repaired) {
		if p.Name == "word/document.xml" {
			doc = p.Data
		}
	}
	txt := docxText(doc)
	if !strings.Contains(txt, "Párrafo 0 del acta") || strings.Contains(txt, "Párrafo 3999 ") {
		t.Errorf("texto rescatado inesperado")
	}
	origTxt := docxText(salvageZip(orig)[3].Data)
	for _, l := range strings.Split(txt, "\r\n") {
		if l != "" && !strings.Contains(origTxt, l) {
			t.Errorf("línea que no estaba en el original: %q", l)
		}
	}
	// La identidad del documento sobrevive al daño (sirve para buscar copias).
	if id := ooxmlIdent(d); id != "ooxml:2024-03-02T10:11:12Z|Ana" {
		t.Errorf("identidad: %q", id)
	}
}

// testPDF crea un PDF sencillo de n páginas.
func testPDF(n int) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	off := map[int]int{}
	obj := func(num int, body string) {
		off[num] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	var kids []string
	for i := 0; i < n; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", 10+2*i))
	}
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R >> >> >>", strings.Join(kids, " "), n))
	obj(3, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("BT /F1 18 Tf 72 720 Td (Pagina %d del contrato) Tj ET", i+1)
		obj(10+2*i, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /Contents %d 0 R >>", 11+2*i))
		obj(11+2*i, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", 11+2*n)
	for i := 1; i < 11+2*n; i++ {
		if o, ok := off[i]; ok {
			fmt.Fprintf(&b, "%010d 00000 n \n", o)
		} else {
			b.WriteString("0000000000 00000 f \n")
		}
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", 11+2*n, x)
	return b.Bytes()
}

func TestRepairPDF(t *testing.T) {
	orig := testPDF(40)
	// Final perdido: sin índice ni las últimas páginas.
	d := orig[:len(orig)*6/10]
	if v, _ := validateContent(bytes.NewReader(d), int64(len(d)), "pdf"); v != vBad {
		t.Fatal("el PDF cortado debería estar dañado")
	}
	r := repairPDF(d)
	if r.Repaired == nil {
		t.Fatalf("sin reparar: %v", r.Notes)
	}
	if v, why := validateContent(bytes.NewReader(r.Repaired), int64(len(r.Repaired)), "pdf"); v == vBad {
		t.Fatalf("el reparado no es válido: %s", why)
	}
	objs := pdfObjects(r.Repaired)
	var cat *pdfObj
	for _, o := range objs {
		if pdfName(pdfDict(o.body), "Type") == "Catalog" {
			cat = o
		}
	}
	pages, complete := pdfPageTree(objs, cat)
	if !complete || len(pages) < 20 || len(pages) >= 40 {
		t.Errorf("páginas recuperadas: %d (completo=%v)", len(pages), complete)
	}
	// Las páginas conservan el tamaño y la fuente que heredaban.
	for _, p := range pages {
		d := pdfDict(p.body)
		if pdfValue(d, "MediaBox") == "" || !strings.Contains(pdfValue(d, "Resources"), "/F1") {
			t.Fatalf("página sin atributos heredados: %s", d)
		}
	}
	// Catálogo perdido (principio sobrescrito): se rehace.
	d2 := append([]byte(nil), orig...)
	copy(d2[9:], bytes.Repeat([]byte{'#'}, 40))
	r2 := repairPDF(d2)
	if r2.Repaired == nil {
		t.Fatalf("sin catálogo: %v", r2.Notes)
	}
	objs = pdfObjects(r2.Repaired)
	for _, o := range objs {
		if pdfName(pdfDict(o.body), "Type") == "Catalog" {
			cat = o
		}
	}
	if pages, _ := pdfPageTree(objs, cat); len(pages) != 40 {
		t.Errorf("con el catálogo rehecho deberían verse las 40 páginas: %d", len(pages))
	}
}

// biffRec escribe un registro de Excel 97.
func biffRec(b *bytes.Buffer, typ uint16, body []byte) {
	binary.Write(b, binary.LittleEndian, typ)
	binary.Write(b, binary.LittleEndian, uint16(len(body)))
	b.Write(body)
}

func TestBIFFResync(t *testing.T) {
	var b bytes.Buffer
	biffRec(&b, 0x0809, []byte{0, 6, 0x10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	for r := 0; r < 2000; r++ {
		body := make([]byte, 14)
		binary.LittleEndian.PutUint16(body, uint16(r))
		binary.LittleEndian.PutUint16(body[2:], 1)
		binary.LittleEndian.PutUint64(body[6:], uint64(0x4059000000000000)) // 100.0
		biffRec(&b, 0x0203, body)
	}
	biffRec(&b, 0x000A, nil)
	d := b.Bytes()
	rand.New(rand.NewSource(9)).Read(d[len(d)/2 : len(d)/2+4096])
	rows, ok := biffSheetValues(d, nil)
	if ok {
		t.Error("debería detectar el daño")
	}
	// Antes del daño ~1000 filas; después del daño se retoma: deben salir muchas más.
	if len(rows) < 1600 {
		t.Errorf("filas rescatadas: %d (no se retomó la lectura después del daño)", len(rows))
	}
	for _, r := range rows {
		for _, c := range r.Cells {
			if c.Text != "100" {
				t.Fatalf("valor inventado: %q en la fila %d", c.Text, r.Row)
			}
		}
	}
}

func TestRepairText(t *testing.T) {
	var b bytes.Buffer
	for b.Len() < 40000 {
		b.WriteString("Proveedor;Factura;Importe;1234.50\r\n")
	}
	d := b.Bytes()
	rand.New(rand.NewSource(4)).Read(d[8192:12288])
	r := repairData(d, "csv")
	if r.Repaired == nil || bytes.Contains(r.Repaired, d[8192:8300]) || !bytes.Contains(r.Repaired, []byte("aquí faltan datos")) {
		t.Fatalf("texto no limpiado: %v", r.Notes)
	}
}

func TestJPEGPreview(t *testing.T) {
	var thumb bytes.Buffer
	jpeg.Encode(&thumb, image.NewGray(image.Rect(0, 0, 160, 120)), nil)
	// EXIF con IFD0 vacío y la miniatura en IFD1.
	var tiff bytes.Buffer
	tiff.WriteString("II*\x00")
	binary.Write(&tiff, binary.LittleEndian, uint32(8))
	binary.Write(&tiff, binary.LittleEndian, uint16(0))  // IFD0 sin entradas
	binary.Write(&tiff, binary.LittleEndian, uint32(14)) // siguiente: IFD1
	binary.Write(&tiff, binary.LittleEndian, uint16(2))
	thumbOff := 14 + 2 + 2*12 + 4
	for _, e := range [][2]uint32{{0x0201, uint32(thumbOff)}, {0x0202, uint32(thumb.Len())}} {
		binary.Write(&tiff, binary.LittleEndian, uint16(e[0]))
		binary.Write(&tiff, binary.LittleEndian, uint16(4))
		binary.Write(&tiff, binary.LittleEndian, uint32(1))
		binary.Write(&tiff, binary.LittleEndian, e[1])
	}
	binary.Write(&tiff, binary.LittleEndian, uint32(0))
	tiff.Write(thumb.Bytes())
	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var f bytes.Buffer
	f.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	binary.Write(&f, binary.BigEndian, uint16(len(app1)+2))
	f.Write(app1)
	f.Write([]byte{0xFF, 0xDA, 0, 2}) // comienzo de la imagen (dañada)
	junk := make([]byte, 20000)
	rand.New(rand.NewSource(8)).Read(junk)
	f.Write(junk)
	r := repairData(f.Bytes(), "jpg")
	if r.Rescue == nil || !bytes.Equal(r.Rescue, thumb.Bytes()) {
		t.Fatalf("no se rescató la miniatura: %v", r.Notes)
	}
}

func TestRunRepairFindsCopies(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Recuperado")
	usb := filepath.Join(dir, "USB")
	os.MkdirAll(filepath.Join(src, "Por_firma"), 0o755)
	os.MkdirAll(usb, 0o755)
	doc := testDocx(3000)
	off, n := partRange(t, doc, "word/document.xml")
	bad := append([]byte(nil), doc...)
	rand.New(rand.NewSource(6)).Read(bad[off+n/2 : off+n/2+4096])
	os.WriteFile(filepath.Join(src, "Acta.docx"), bad, 0o644)
	// La misma acta, sana, sin nombre (como la deja la búsqueda por contenido).
	os.WriteFile(filepath.Join(src, "Por_firma", "docx_0000123456.docx"), doc, 0o644)
	xl, _ := testWorkbook(2000)
	xoff, xn := partRange(t, xl, "xl/worksheets/sheet1.xml")
	xbad := append([]byte(nil), xl...)
	clear(xbad[xoff+xn/2 : xoff+xn/2+8192])
	os.WriteFile(filepath.Join(src, "Nómina.xlsx"), xbad, 0o644)
	os.WriteFile(filepath.Join(usb, "Nómina - copia.xlsx"), xl, 0o644)
	os.WriteFile(filepath.Join(src, "Vacío.pdf"), make([]byte, 50000), 0o644)
	other, _ := testWorkbook(50) // otro libro distinto: no debe tomarse por copia
	os.WriteFile(filepath.Join(usb, "Otro.xlsx"), other, 0o644)

	out := filepath.Join(src, "Reparados")
	var seen []string
	rows, sum, err := RunRepair(RepairJob{Src: src, Out: out, SearchIn: []string{usb},
		OnRow: func(r RepairRow) { seen = append(seen, r.Rel) }}, nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]RepairRow{}
	for _, r := range rows {
		byName[filepath.Base(r.Rel)] = r
	}
	acta := byName["Acta.docx"]
	if acta.State != rsDamaged || !acta.Complete || !strings.Contains(acta.Action, "copia sana") {
		t.Errorf("Acta: %+v", acta)
	}
	got, _ := os.ReadFile(filepath.Join(out, "Acta (copia sana).docx"))
	if !bytes.Equal(got, doc) {
		t.Error("la copia sana del acta no es la buena")
	}
	nom := byName["Nómina.xlsx"]
	if !nom.Complete || !strings.Contains(nom.Notes, "Nómina - copia.xlsx") {
		t.Errorf("Nómina: %+v", nom)
	}
	if v := byName["Vacío.pdf"]; v.State != rsBlank || v.Action != "sin arreglo posible" {
		t.Errorf("Vacío: %+v", v)
	}
	if sum.Damaged != 2 || sum.Blank != 1 || sum.Complete != 2 || sum.NoFix != 1 {
		t.Errorf("resumen: %+v", sum)
	}
	if len(seen) < 3 {
		t.Errorf("OnRow no informó de los archivos: %v", seen)
	}
	for _, f := range []string{"informe_reparacion.csv", "resumen_reparacion.txt"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("falta %s", f)
		}
	}
	// Los originales no se tocan.
	if b, _ := os.ReadFile(filepath.Join(src, "Acta.docx")); !bytes.Equal(b, bad) {
		t.Error("se modificó un original")
	}
	_ = time.Now
}

// Facturas hechas con «Guardar como» a partir de la anterior: comparten la
// identidad interna y los encabezados. Una factura dañada no debe tomarse
// por otra.
func TestCopyMatchTemplates(t *testing.T) {
	dir := t.TempDir()
	mk := func(i int) []byte {
		z := newZipOut()
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
		for _, h := range []string{"FACTURA", "Distribuidora del Norte SA de CV", "RFC DNO010101AAA", "Concepto Cantidad Importe", "Condiciones de pago: 30 días", "Gracias por su compra"} {
			fmt.Fprintf(&b, `<w:p><w:r><w:t>%s</w:t></w:r></w:p>`, h)
		}
		rnd := rand.New(rand.NewSource(int64(i)))
		for k := 0; k < 400; k++ {
			fmt.Fprintf(&b, `<w:p><w:r><w:t>Partida %d de la factura %d: %x</w:t></w:r></w:p>`, k, i, rnd.Int63())
		}
		b.WriteString(`</w:body></w:document>`)
		z.add("word/document.xml", []byte(b.String()), 8)
		z.add("docProps/core.xml", []byte(`<cp:coreProperties xmlns:cp="x" xmlns:dc="y" xmlns:dcterms="z"><dc:creator>Ventas</dc:creator><dcterms:created>2023-01-05T09:00:00Z</dcterms:created></cp:coreProperties>`), 8)
		out, _ := z.bytes()
		return out
	}
	var keys []*docKey
	ix := &copyIndex{}
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, fmt.Sprintf("Factura %03d.docx", i))
		os.WriteFile(p, mk(i), 0o644)
		k := newDocKey(p, int64(len(mk(i))), time.Now())
		keys = append(keys, k)
		ix.add(k)
	}
	// La factura 3, dañada justo después de los encabezados (solo quedan ellos).
	d := mk(3)
	off, n := partRange(t, d, "word/document.xml")
	rand.New(rand.NewSource(1)).Read(d[off+300 : off+n])
	p := filepath.Join(dir, "dañada", "Factura 003.docx")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, d, 0o644)
	dk := newDocKey(p, int64(len(d)), time.Now())
	if dk.ident == "" {
		t.Fatal("la identidad interna debería leerse")
	}
	m := ix.find(dk)
	if m == nil || m.key.path != keys[3].path || m.level >= mtDocument {
		t.Fatalf("debería proponer solo la del mismo nombre, para revisar: %+v", m)
	}
	// Dañada más adelante: quedan partidas propias y se reconoce la buena.
	d2 := mk(5)
	off, n = partRange(t, d2, "word/document.xml")
	rand.New(rand.NewSource(2)).Read(d2[off+n/2 : off+n])
	p2 := filepath.Join(dir, "dañada", "sin nombre.docx")
	os.WriteFile(p2, d2, 0o644)
	m = ix.find(newDocKey(p2, int64(len(d2)), time.Now()))
	if m == nil || m.key.path != keys[5].path || m.level != mtDocument {
		t.Fatalf("debería encontrar la factura 5: %+v", m)
	}
}
