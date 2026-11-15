package main

import (
	"sort"
)

// Geometry indica dónde empezaba y qué tamaño de cluster tenía el volumen
// al que pertenecen unos registros MFT huérfanos. Tras formatear o
// reinstalar suele coincidir con el volumen actual, pero si se cambiaron
// las particiones hay que deducirlo.
type Geometry struct {
	Delta     int64 // posición en el dispositivo del inicio del volumen antiguo
	CS        int64 // tamaño de cluster
	Confirmed bool  // comprobado leyendo cabeceras de archivos conocidos
	Samples   int
	Match     int
	Zero      int
}

func (s *Scan) geomFor(r *FileRec) Geometry {
	if r.has(fLive) {
		if v := s.volOfCont(int(r.Group)); v != nil {
			return Geometry{Delta: v.Start, CS: v.CS, Confirmed: true}
		}
	}
	if int(r.Group) < len(s.geoms) {
		return s.geoms[r.Group]
	}
	return Geometry{CS: 4096}
}

// geomSamples elige archivos huérfanos de tipo conocido para comprobar la geometría.
func (s *Scan) geomSamples(g int) []*FileRec {
	var all []*FileRec
	for _, r := range s.Recs {
		if r.has(fLive) || int(r.Group) != g || r.has(fDir|fResident|fCompressed|fEncrypted) ||
			!r.has(fHasData) || !r.has(fInUse) || r.Size < 4096 || r.firstLCN() < 0 {
			continue
		}
		if sigKnown(extOf(r.Name)) {
			all = append(all, r)
		}
	}
	const want = 160
	if len(all) <= want {
		return all
	}
	out := make([]*FileRec, 0, want)
	for i := 0; i < want; i++ {
		out = append(out, all[i*len(all)/want])
	}
	return out
}

// evalGeom cuenta en cuántas muestras el contenido coincide con la firma.
func (s *Scan) evalGeom(delta, cs int64, samples []*FileRec) (match, zero, total int) {
	head := make([]byte, 16)
	for _, r := range samples {
		off := delta + r.firstLCN()*cs
		if off < 0 || off+16 > s.Dev.Size {
			total++
			continue
		}
		if s.allocatedAt(off) {
			continue // sobrescrito con seguridad: no dice nada sobre la geometría
		}
		total++
		if n, _ := s.Dev.ReadAt(head, off); n < 16 {
			continue
		}
		if allZero(head) {
			zero++
			continue
		}
		if checkSig(extOf(r.Name), head) == 1 {
			match++
		}
	}
	return
}

func geomOK(match, total int) bool { return match >= 3 && match*10 >= total }

// detectGeometries deduce la geometría de cada grupo de registros huérfanos.
func (s *Scan) detectGeometries(logf func(string, ...interface{})) {
	s.geoms = make([]Geometry, len(s.Conts))
	for g, c := range s.Conts {
		def := Geometry{Delta: c.Start, CS: 4096}
		if def.Delta < 0 {
			def.Delta = 0
		}
		if v := s.volOfCont(g); v != nil {
			def.CS = v.CS
		}
		s.geoms[g] = def
		samples := s.geomSamples(g)
		if len(samples) == 0 {
			continue
		}
		m, z, t := s.evalGeom(def.Delta, def.CS, samples)
		def.Samples, def.Match, def.Zero = t, m, z
		if geomOK(m, t) {
			def.Confirmed = true
			s.geoms[g] = def
			continue
		}
		s.geoms[g] = def
		if t > 0 && z*10 >= t*9 {
			continue // casi todo ceros: datos vaciados (TRIM), no hay nada que buscar
		}
		if logf != nil {
			logf("  Los archivos antiguos no están donde indica el volumen actual; buscando el volumen original...")
		}
		if best, ok := s.searchGeometry(g, def, samples); ok {
			s.geoms[g] = best
			if logf != nil {
				logf("  Volumen original localizado: inicio en %s, clusters de %d bytes (%d de %d comprobaciones correctas).",
					humanSize(best.Delta), best.CS, best.Match, best.Samples)
			}
		}
	}
}

type geomCand struct{ delta, cs int64 }

func (s *Scan) searchGeometry(g int, def Geometry, samples []*FileRec) (Geometry, bool) {
	csList := []int64{def.CS, 4096, 512, 1024, 2048, 8192, 16384, 32768, 65536}
	seen := map[geomCand]bool{{def.Delta, def.CS}: true}
	var cands []geomCand
	add := func(delta, cs int64) {
		k := geomCand{delta, cs}
		if delta%512 != 0 || delta <= -s.Dev.Size || delta >= s.Dev.Size || seen[k] {
			return
		}
		seen[k] = true
		cands = append(cands, k)
	}
	for _, c := range s.Conts {
		if c.Start >= 0 {
			for _, cs := range csList {
				add(c.Start, cs)
			}
		}
	}
	for _, cs := range csList {
		add(0, cs)
	}
	for i, b := range s.Boots {
		if i >= 200 {
			break
		}
		add(b.Off, b.Info.ClusterSize)                                                  // sector de arranque principal
		add(b.Off-b.Info.TotalSectors*int64(b.Info.BytesPerSector), b.Info.ClusterSize) // copia de respaldo (al final)
	}
	for i, m := range s.OldMFT {
		if i >= 50 {
			break
		}
		for _, cs := range csList {
			add(m.Off-m.firstLCN()*cs, cs)
		}
	}
	small := samples
	if len(small) > 40 {
		small = make([]*FileRec, 0, 40)
		for i := 0; i < 40; i++ {
			small = append(small, samples[i*len(samples)/40])
		}
	}
	best := def
	bestM := 0
	try := func(c geomCand) {
		if m, _, _ := s.evalGeom(c.delta, c.cs, small); m > bestM {
			bestM = m
			best = Geometry{Delta: c.delta, CS: c.cs}
		}
	}
	for _, c := range cands {
		if stopRequested() {
			break
		}
		try(c)
	}
	if bestM < 3 {
		// Último recurso: "votación" entre las posiciones de las firmas
		// detectadas en el disco y los clusters que indican los registros.
		for _, cs := range csList[1:] {
			for _, d := range s.voteDelta(samples, cs) {
				k := geomCand{d, cs}
				if !seen[k] {
					seen[k] = true
					try(k)
				}
			}
			if bestM >= 3 {
				break
			}
		}
	}
	if bestM < 3 {
		return def, false
	}
	m, z, t := s.evalGeom(best.Delta, best.CS, samples)
	if !geomOK(m, t) {
		return def, false
	}
	best.Confirmed, best.Samples, best.Match, best.Zero = true, t, m, z
	return best, true
}

// voteDelta propone inicios de volumen: para cada archivo huérfano de tipo
// conocido y cada firma del mismo tipo vista en el disco calcula el inicio
// que las haría coincidir, y devuelve los valores más repetidos.
func (s *Scan) voteDelta(samples []*FileRec, cs int64) []int64 {
	if s.byKind == nil {
		s.byKind = map[uint8][]int64{}
		for _, c := range s.Cands {
			s.byKind[c.Kind] = append(s.byKind[c.Kind], c.Off)
		}
	}
	votes := map[int64]int{}
	perKind := map[uint8]int{}
	for _, r := range samples {
		k := kindByExt[extOf(r.Name)]
		offs := s.byKind[k]
		if k == 0 || len(offs) == 0 || len(offs) > 200000 || perKind[k] >= 24 {
			continue
		}
		perKind[k]++
		base := r.firstLCN() * cs
		for _, o := range offs {
			votes[o-base]++
		}
		if len(votes) > 6000000 {
			break
		}
	}
	type dv struct {
		d int64
		n int
	}
	var top []dv
	for d, n := range votes {
		if n >= 3 && d%512 == 0 {
			top = append(top, dv{d, n})
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].n != top[j].n {
			return top[i].n > top[j].n
		}
		return top[i].d < top[j].d
	})
	var out []int64
	for i := 0; i < len(top) && i < 3; i++ {
		out = append(out, top[i].d)
	}
	return out
}
