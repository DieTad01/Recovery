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
	Verified grp // comprobados a fondo: correctos (sumas de control)
	StructOK grp // comprobados a fondo: estructura correcta (su tipo no lleva sumas de control)
	Good     grp // sin señales de daño, pero sin comprobar a fondo (o sin forma de comprobarlo)
	Checked  int // cuántos se comprobaron a fondo
	Partial  grp // dañados en parte o con contenido que no corresponde
	Blank    grp // en blanco (el disco los vació)
	Lost     grp // no recuperables (sobrescritos)
	Named    int // con nombre y ubicación
	Loose    int // en carpetas sin ubicación
	Carved   int // sin nombre (por contenido)
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
		case esVerified:
			s.Verified.add(e.Size)
		case esIntact, esCarved:
			if e.Deep == vStruct {
				s.StructOK.add(e.Size)
			} else {
				s.Good.add(e.Size)
			}
		case esPartial, esCarvedDoubt, esCorrupt:
			s.Partial.add(e.Size)
		case esZeroed:
			s.Blank.add(e.Size)
		case esLost:
			s.Lost.add(e.Size)
		}
		if e.Deep != vNone {
			s.Checked++
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
	return s.Verified.N + s.StructOK.N + s.Good.N + s.Partial.N + s.Blank.N + s.Lost.N
}

func (s Summary) totalBytes() int64 {
	return s.Verified.Bytes + s.StructOK.Bytes + s.Good.Bytes + s.Partial.Bytes + s.Blank.Bytes + s.Lost.Bytes
}

func pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

// GoodPct es el porcentaje (por tamaño) sin señales de daño (comprobados + sin comprobar).
func (s Summary) GoodPct() float64 {
	return pct(s.Verified.Bytes+s.StructOK.Bytes+s.Good.Bytes, s.totalBytes())
}

// VerifiedPct es el porcentaje (por tamaño) comprobado a fondo como correcto.
func (s Summary) VerifiedPct() float64 { return pct(s.Verified.Bytes+s.StructOK.Bytes, s.totalBytes()) }

// Headline es la frase corta del estado para la barra superior.
func (s Summary) Headline() string {
	if s.totalN() == 0 {
		return ""
	}
	if s.Checked > 0 {
		return fmt.Sprintf("Comprobado correcto: %.0f %% · sin forma de comprobar: %.0f %% · dañado o perdido: %.0f %% (de %s)",
			s.VerifiedPct(), pct(s.Good.Bytes, s.totalBytes()), 100-s.GoodPct(), humanSize(s.totalBytes()))
	}
	return fmt.Sprintf("Sin señales de daño: %.0f %% (%s de %s) · sin verificar a fondo",
		s.GoodPct(), humanSize(s.Verified.Bytes+s.Good.Bytes), humanSize(s.totalBytes()))
}

// Text es el resumen completo, legible, para mostrar o guardar.
func (s Summary) Text() string {
	tb, tn := s.totalBytes(), s.totalN()
	var b strings.Builder
	fmt.Fprintf(&b, "Archivos encontrados: %s  (%s)\r\n\r\n", groupDigits(int64(tn)), humanSize(tb))
	row := func(name string, g grp) {
		if g.N == 0 {
			return
		}
		fmt.Fprintf(&b, "  •  %s:  %s archivos · %s · %.1f %%\r\n", name, groupDigits(int64(g.N)), humanSize(g.Bytes), pct(g.Bytes, tb))
	}
	row("Verificados (sumas de control)", s.Verified)
	row("Estructura comprobada", s.StructOK)
	if s.Checked > 0 {
		row("Sin comprobar o no verificables", s.Good)
	} else {
		row("Buenos (sin verificar a fondo)", s.Good)
	}
	row("Dañados", s.Partial)
	row("En blanco (vaciados por el disco)", s.Blank)
	row("No recuperables", s.Lost)
	fmt.Fprintf(&b, "  (el %% es sobre el tamaño total)\r\n\r\n")
	if s.Checked > 0 {
		fmt.Fprintf(&b, "COMPROBADO que están correctos: %.0f %% del tamaño.\r\n", s.VerifiedPct())
		fmt.Fprintf(&b, "Sin señales de daño (incluye lo no comprobable): %.0f %%.\r\n", s.GoodPct())
	} else {
		fmt.Fprintf(&b, "Sin señales de daño: %.0f %% del tamaño (aún sin verificar a fondo).\r\n", s.GoodPct())
	}
	fmt.Fprintf(&b, "\r\nQué significa cada estado:\r\n"+
		"  · Verificado: se leyó entero y sus sumas de control son correctas.\r\n"+
		"  · Estructura OK: se recorrió entero y todo encaja (su tipo no lleva sumas de control).\r\n"+
		"  · Bueno: ni el mapa del disco ni las muestras leídas muestran daño, pero no es una "+
		"garantía: tras reinstalar Windows, datos temporales pueden haberlo pisado sin dejar "+
		"rastro. «Verificar a fondo» (o recuperar, que comprueba al guardar) lo aclara.\r\n"+
		"  · No verificable: tipos sin estructura que comprobar (.bin, .dat, .dll, formatos "+
		"propios de programas): su estado real no se puede saber.\r\n"+
		"  · Dañado: parte del contenido fue sobrescrito; se puede guardar, pero quizá no abra.\r\n")
	if s.Blank.N > 0 {
		fmt.Fprintf(&b, "\r\nLos «en blanco» los vació el propio disco (TRIM en SSD o «limpiar la unidad»): ningún "+
			"programa puede recuperarlos; la copia tendría que venir de la nube u otro respaldo.\r\n")
	}
	if s.Lost.N > 0 {
		fmt.Fprintf(&b, "Los «no recuperables» fueron sobrescritos por archivos nuevos.\r\n")
	}
	fmt.Fprintf(&b, "\r\nDesglose: con su ubicación %s · carpetas sin ubicación %s · sin nombre %s.\r\n",
		groupDigits(int64(s.Named)), groupDigits(int64(s.Loose)), groupDigits(int64(s.Carved)))
	fmt.Fprintf(&b, "Los porcentajes son sobre lo que el programa pudo LOCALIZAR en el disco, no sobre lo "+
		"que había originalmente (eso no hay forma de saberlo).")
	return b.String()
}
