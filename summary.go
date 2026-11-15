package main

import (
	"fmt"
	"strings"
)

// Summary resume el estado de todo lo encontrado, para estimar qué
// porcentaje se puede recuperar en buen estado.
type grp struct {
	N     int
	Bytes int64
}

type Summary struct {
	Good    grp // buenos (completos)
	Partial grp // dañados en parte (abren a medias)
	Blank   grp // en blanco (el disco los vació)
	Lost    grp // no recuperables (sobrescritos)
	Named   int // con nombre y ubicación
	Loose   int // en carpetas sin ubicación
	Carved  int // sin nombre (por contenido)
}

func (g *grp) add(size int64) {
	g.N++
	g.Bytes += size
}

// summarize cuenta las entradas por estado.
func summarize(entries []*Entry) Summary {
	var s Summary
	for _, e := range entries {
		switch e.State {
		case esIntact, esCarved:
			s.Good.add(e.Size)
		case esPartial, esCarvedDoubt:
			s.Partial.add(e.Size)
		case esZeroed:
			s.Blank.add(e.Size)
		case esLost:
			s.Lost.add(e.Size)
		}
		switch e.Group {
		case grpCarved:
			s.Carved++
		case grpLoose:
			s.Loose++
		case grpPath:
			s.Named++
		}
	}
	return s
}

func (s Summary) totalN() int {
	return s.Good.N + s.Partial.N + s.Blank.N + s.Lost.N
}

func (s Summary) totalBytes() int64 {
	return s.Good.Bytes + s.Partial.Bytes + s.Blank.Bytes + s.Lost.Bytes
}

func pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

// GoodPct es el porcentaje (por tamaño) que está en buen estado.
func (s Summary) GoodPct() float64 { return pct(s.Good.Bytes, s.totalBytes()) }

// Headline es la frase corta del estado para la barra superior.
func (s Summary) Headline() string {
	if s.totalN() == 0 {
		return ""
	}
	return fmt.Sprintf("En buen estado: %.0f %% (%s de %s)", s.GoodPct(), humanSize(s.Good.Bytes), humanSize(s.totalBytes()))
}

// Text es el resumen completo, legible, para mostrar o guardar.
func (s Summary) Text() string {
	tb, tn := s.totalBytes(), s.totalN()
	var b strings.Builder
	fmt.Fprintf(&b, "Archivos encontrados: %s  (%s)\r\n", groupDigits(int64(tn)), humanSize(tb))
	fmt.Fprintf(&b, "%s\r\n", strings.Repeat("-", 56))
	row := func(name string, g grp) {
		fmt.Fprintf(&b, "  %-26s %10s  %10s  %5.1f %%\r\n", name, groupDigits(int64(g.N)), humanSize(g.Bytes), pct(g.Bytes, tb))
	}
	row("En buen estado", s.Good)
	row("Dañados en parte", s.Partial)
	row("En blanco (vaciados)", s.Blank)
	row("No recuperables", s.Lost)
	fmt.Fprintf(&b, "%s\r\n", strings.Repeat("-", 56))
	recup := s.Good.Bytes + s.Partial.Bytes
	fmt.Fprintf(&b, "\r\nSe pueden recuperar (buenos + dañados): %s de %s  (%.0f %%).\r\n",
		humanSize(recup), humanSize(tb), pct(recup, tb))
	fmt.Fprintf(&b, "De lo que se recupera, en BUEN estado: %.0f %%.\r\n", s.GoodPct())
	if s.Blank.N > 0 {
		fmt.Fprintf(&b, "\r\nLos «en blanco» (%s) los vació el propio disco (TRIM en SSD o «limpiar la unidad»);\r\n"+
			"no hay forma de recuperarlos: la copia tendría que venir de la nube u otro respaldo.\r\n", groupDigits(int64(s.Blank.N)))
	}
	if s.Lost.N > 0 {
		fmt.Fprintf(&b, "Los «no recuperables» (%s) fueron sobrescritos por archivos nuevos.\r\n", groupDigits(int64(s.Lost.N)))
	}
	fmt.Fprintf(&b, "\r\nDesglose: con su ubicación %s · carpetas sin ubicación %s · sin nombre %s.\r\n",
		groupDigits(int64(s.Named)), groupDigits(int64(s.Loose)), groupDigits(int64(s.Carved)))
	fmt.Fprintf(&b, "\r\nNota: los porcentajes son sobre lo que el programa pudo LOCALIZAR en el disco, "+
		"no sobre lo que había originalmente (eso no hay forma de saberlo).")
	return b.String()
}
