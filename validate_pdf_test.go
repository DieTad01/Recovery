package main

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"encoding/hex"
	"fmt"
	"testing"
)

// pdfConFiltro arma un PDF de una página cuyo contenido va comprimido con Flate y,
// según filtro, además codificado en ASCII85 o hexadecimal (como hacen ReportLab
// y muchos programas de facturación).
func pdfConFiltro(filtro string, romper bool) []byte {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	for i := 0; i < 40; i++ {
		fmt.Fprintf(w, "BT /F1 12 Tf 72 %d Td (Linea %d de la factura) Tj ET\n", 720-i*14, i)
	}
	w.Close()
	flate := z.Bytes()
	if romper {
		flate = append([]byte{}, flate...)
		for i := len(flate) / 3; i < len(flate)/3+12; i++ {
			flate[i] ^= 0x5A
		}
	}
	var body []byte
	switch filtro {
	case "[ /ASCII85Decode /FlateDecode ]":
		enc := make([]byte, ascii85.MaxEncodedLen(len(flate)))
		body = append(enc[:ascii85.Encode(enc, flate)], "~>"...)
	case "[/AHx /Fl]":
		body = append([]byte(hex.EncodeToString(flate)), '>')
	default:
		body = flate
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>\nendobj\n")
	fmt.Fprintf(&b, "4 0 obj\n<< /Filter %s /Length %d >>\nstream\n", filtro, len(body))
	b.Write(body)
	b.WriteString("\nendstream\nendobj\n")
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 5\ntrailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

func TestValidatePDFFiltros(t *testing.T) {
	for _, f := range []string{"/FlateDecode", "[ /ASCII85Decode /FlateDecode ]", "[/AHx /Fl]"} {
		sano := pdfConFiltro(f, false)
		if v, why := validateContent(bytes.NewReader(sano), int64(len(sano)), "pdf"); v == vBad {
			t.Errorf("%s: PDF sano marcado como dañado: %s", f, why)
		}
		roto := pdfConFiltro(f, true)
		if v, _ := validateContent(bytes.NewReader(roto), int64(len(roto)), "pdf"); v != vBad {
			t.Errorf("%s: PDF con el contenido roto no se detecta (veredicto %d)", f, v)
		}
	}
	if got := pdfFilters([]byte("<< /Length 9 /Filter/FlateDecode/DecodeParms<<>> >>")); len(got) != 1 || got[0] != "FlateDecode" {
		t.Errorf("pdfFilters con nombre suelto: %q", got)
	}
}
