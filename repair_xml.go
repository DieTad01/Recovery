package main

// Rescate de XML incompletos: el texto de un Word (word/document.xml), las
// filas de una hoja de Excel o los cuadros de una diapositiva se guardan en
// XML. Si el final se perdió, se conservan los elementos completos que hay
// antes del daño y se cierran las etiquetas que quedaron abiertas.

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// xmlSalvage es el resultado del rescate de un XML.
type xmlSalvage struct {
	Data     []byte // XML bien formado (nil si no se pudo rescatar nada útil)
	Kept     int    // elementos completos conservados dentro del contenedor
	Complete bool   // el XML estaba entero: no se cambió nada
}

// salvageXML conserva, de un XML posiblemente dañado, los hijos completos
// del primer elemento llamado container (nombre local, sin prefijo).
func salvageXML(b []byte, container string) xmlSalvage {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = true
	var stack []string // nombres tal como están escritos (prefijo:nombre)
	cDepth := -1       // profundidad del contenedor (len(stack) con él dentro)
	var ancestors []string
	lastChild, startEnd, cEnd := int64(-1), int64(-1), int64(-1)
	kept := 0
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			if len(stack) == 0 && cDepth >= 0 {
				return xmlSalvage{Data: b, Kept: kept, Complete: true}
			}
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, qname(t.Name))
			if cDepth < 0 && t.Name.Local == container {
				cDepth = len(stack)
				ancestors = append([]string(nil), stack[:len(stack)-1]...)
				startEnd = d.InputOffset()
			}
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1] != qname(t.Name) {
				goto cut // etiquetas que no casan: aquí empieza el daño
			}
			stack = stack[:len(stack)-1]
			if cDepth >= 0 && cEnd < 0 {
				switch len(stack) {
				case cDepth: // se cerró un hijo directo del contenedor
					lastChild = d.InputOffset()
					kept++
				case cDepth - 1: // se cerró el contenedor
					cEnd = d.InputOffset()
				}
			}
		case xml.CharData:
			if !utf8ValidXML(t) {
				goto cut
			}
		}
	}
cut:
	if cDepth < 0 {
		return xmlSalvage{}
	}
	var out bytes.Buffer
	switch {
	case cEnd >= 0:
		out.Write(b[:cEnd])
	case lastChild >= 0:
		out.Write(b[:lastChild])
		out.WriteString("</" + nameAt(b, startEnd, container) + ">")
	default:
		out.Write(b[:startEnd])
		out.WriteString("</" + nameAt(b, startEnd, container) + ">")
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		out.WriteString("</" + ancestors[i] + ">")
	}
	res := xmlSalvage{Data: out.Bytes(), Kept: kept}
	if !wellFormed(res.Data) {
		return xmlSalvage{}
	}
	return res
}

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// nameAt devuelve el nombre con prefijo de la etiqueta de apertura que
// termina en end (p. ej. «w:body»).
func nameAt(b []byte, end int64, local string) string {
	i := bytes.LastIndexByte(b[:end], '<')
	if i < 0 {
		return local
	}
	tag := b[i+1 : end]
	j := bytes.IndexAny(tag, " \t\r\n/>")
	if j < 0 {
		return local
	}
	return string(tag[:j])
}

func utf8ValidXML(b []byte) bool {
	for _, r := range string(b) {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// wellFormed indica si el XML se lee entero sin errores.
func wellFormed(b []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = true
	depth := 0
	roots := 0
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			return depth == 0 && roots == 1
		}
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
		case xml.EndElement:
			depth--
			if depth < 0 {
				return false
			}
		case xml.CharData:
			if depth > 0 && !utf8ValidXML(t) {
				return false
			}
		}
	}
}

// xmlEscape escapa texto para meterlo en un XML.
func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(cleanXMLText(s)))
	return b.String()
}

// cleanXMLText quita los caracteres que un XML no admite.
func cleanXMLText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0xFFFD || r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		return r
	}, s)
}
