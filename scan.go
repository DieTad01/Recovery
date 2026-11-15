package main

import (
	"sort"
	"sync/atomic"
)

// stopFlag se activa con Ctrl+C para detener el escaneo de forma ordenada.
var stopFlag int32

func stopRequested() bool { return atomic.LoadInt32(&stopFlag) != 0 }

// Cand es un posible inicio de archivo detectado por su firma.
type Cand struct {
	Off  int64
	Kind uint8
}

// BootHit es un sector de arranque NTFS encontrado durante el escaneo
// (pista sobre dónde empezaba un volumen antiguo).
type BootHit struct {
	Off  int64
	Info BootInfo
}

// Volume es un sistema de archivos NTFS actualmente válido en el dispositivo.
type Volume struct {
	Cont      int
	Start     int64
	Size      int64
	CS        int64
	RecSize   int
	nClusters int64
	mftSegs   []DataSeg
	mftInit   int64
	bitmap    []byte
	Existing  int64 // archivos existentes (en uso): no se tocan
}

// isAlloc indica si el cluster c está ocupado en el sistema de archivos actual.
func (v *Volume) isAlloc(c int64) bool {
	if c < 0 || c>>3 >= int64(len(v.bitmap)) {
		return false
	}
	return v.bitmap[c>>3]&(1<<uint(c&7)) != 0
}

// allAlloc indica si todos los clusters de [c0, c1) están ocupados.
func (v *Volume) allAlloc(c0, c1 int64) bool {
	if c0 < 0 || c1 > v.nClusters || (c1+7)>>3 > int64(len(v.bitmap)) {
		return false
	}
	for c0 < c1 && c0&7 != 0 {
		if !v.isAlloc(c0) {
			return false
		}
		c0++
	}
	for c0+8 <= c1 {
		if v.bitmap[c0>>3] != 0xFF {
			return false
		}
		c0 += 8
	}
	for c0 < c1 {
		if !v.isAlloc(c0) {
			return false
		}
		c0++
	}
	return true
}

// countAlloc cuenta los clusters ocupados en [c0, c1).
func (v *Volume) countAlloc(c0, c1 int64) int64 {
	var n int64
	for c := c0; c < c1; c++ {
		if c&7 == 0 && c+8 <= c1 && c>>3 < int64(len(v.bitmap)) {
			b := v.bitmap[c>>3]
			if b == 0 {
				c += 7
				continue
			}
			if b == 0xFF {
				n += 8
				c += 7
				continue
			}
		}
		if v.isAlloc(c) {
			n++
		}
	}
	return n
}

func (v *Volume) setAlloc(c0, c1 int64) {
	for c := c0; c < c1; c++ {
		if c >= 0 && c>>3 < int64(len(v.bitmap)) {
			v.bitmap[c>>3] |= 1 << uint(c&7)
		}
	}
}

// ScanOpts son las opciones del escaneo.
type ScanOpts struct {
	Quick     bool // solo la MFT actual (archivos borrados recientemente)
	NoCarve   bool // no buscar por firmas
	NoMFT     bool // no buscar registros NTFS antiguos
	IgnoreMap bool // no fiarse del mapa de clusters ocupados
}

// Scan contiene todo lo encontrado en el dispositivo.
type Scan struct {
	Dev         *Device
	Opts        ScanOpts
	Conts       []Container
	Vols        []*Volume
	Recs        []*FileRec
	Cands       []Cand
	Boots       []BootHit
	OldMFT      []*FileRec // registros $MFT de sistemas de archivos antiguos
	Notes       []string
	ScannedTo   int64
	Interrupted bool

	geoms   []Geometry
	lastVol *Volume
	byKind  map[uint8][]int64
}

func (s *Scan) contOf(off int64) int16 {
	for i, c := range s.Conts {
		if c.Start >= 0 && off >= c.Start && off < c.Start+c.Size {
			return int16(i)
		}
	}
	return int16(len(s.Conts) - 1)
}

// volAt devuelve el volumen NTFS actual que contiene la posición off.
func (s *Scan) volAt(off int64) *Volume {
	if v := s.lastVol; v != nil && off >= v.Start && off < v.Start+v.Size {
		return v
	}
	for _, v := range s.Vols {
		if off >= v.Start && off < v.Start+v.Size {
			s.lastVol = v
			return v
		}
	}
	return nil
}

func (s *Scan) volOfCont(c int) *Volume {
	for _, v := range s.Vols {
		if v.Cont == c {
			return v
		}
	}
	return nil
}

// allocatedAt indica si la posición pertenece a un cluster ocupado por el
// sistema de archivos actual (es decir, datos que NO están perdidos).
func (s *Scan) allocatedAt(off int64) bool {
	v := s.volAt(off)
	if v == nil {
		return false
	}
	return v.isAlloc((off - v.Start) / v.CS)
}

func (s *Scan) fullyAllocated(off, n int64) bool {
	v := s.volAt(off)
	if v == nil || off+n > v.Start+v.Size {
		return false
	}
	c0 := (off - v.Start) / v.CS
	c1 := (off - v.Start + n + v.CS - 1) / v.CS
	return v.allAlloc(c0, c1)
}

// loadVolume comprueba si en el contenedor hay un NTFS válido y carga su MFT.
func (s *Scan) loadVolume(ci int) {
	c := s.Conts[ci]
	if c.Start < 0 {
		return
	}
	sec := make([]byte, 512)
	if n, _ := s.Dev.ReadAt(sec, c.Start); n < 512 {
		return
	}
	if string(sec[3:11]) == "-FVE-FS-" {
		s.Notes = append(s.Notes, "La "+c.Desc+" está cifrada con BitLocker. Leída así solo se ven datos cifrados: "+
			"para recuperar de ella hay que desbloquearla en Windows y escanearla por su letra de unidad (por ejemplo C:).")
		return
	}
	bi, ok := parseBoot(sec)
	if !ok {
		return
	}
	v := &Volume{Cont: ci, Start: c.Start, CS: bi.ClusterSize, RecSize: bi.RecSize}
	v.Size = bi.TotalSectors * int64(bi.BytesPerSector)
	if v.Start+v.Size > s.Dev.Size {
		v.Size = s.Dev.Size - v.Start
	}
	v.nClusters = v.Size / v.CS
	for _, lcn := range []int64{bi.MFTLCN, bi.MirrLCN} {
		rec := readRecord(s.Dev, v.Start+lcn*v.CS, v.RecSize)
		if rec == nil {
			continue
		}
		fr := parseMFTRecord(rec, v.Start+lcn*v.CS)
		if fr == nil || len(fr.Segs) == 0 || fr.Segs[0].StartVCN != 0 || fr.firstLCN() != bi.MFTLCN {
			continue
		}
		v.mftSegs = fr.Segs
		v.mftInit = fr.InitSize
		break
	}
	if v.mftSegs == nil {
		s.Notes = append(s.Notes, "La "+c.Desc+" tiene un NTFS dañado (no se pudo leer su MFT): se analizará como espacio sin formato.")
		return
	}
	s.Vols = append(s.Vols, v)
}

// scanLiveMFT recorre la MFT del sistema de archivos actual: localiza los
// archivos borrados (registros libres que aún conservan sus datos), las
// carpetas (para reconstruir rutas) y el mapa de clusters ocupados.
func (s *Scan) scanLiveMFT(v *Volume, progress func()) {
	queue := append([]DataSeg(nil), v.mftSegs...)
	seen := map[int64]bool{}
	var bmp *FileRec
	rec := make([]byte, v.RecSize)
	buf := make([]byte, 4<<20-(4<<20)%v.RecSize)
	type ext struct{ off, n int64 }
	var mftExt []ext
	for len(queue) > 0 && !stopRequested() {
		sg := queue[0]
		queue = queue[1:]
		if seen[sg.StartVCN] {
			continue
		}
		seen[sg.StartVCN] = true
		vcn := sg.StartVCN
		for _, ru := range sg.Runs {
			if ru.LCN < 0 {
				vcn += ru.Len
				continue
			}
			runOff := v.Start + ru.LCN*v.CS
			runLen := ru.Len * v.CS
			streamOff := vcn * v.CS
			vcn += ru.Len
			mftExt = append(mftExt, ext{ru.LCN, ru.Len})
			for done := int64(0); done < runLen; {
				n := int(min64(int64(len(buf)), runLen-done))
				m, _ := s.Dev.ReadAt(buf[:n], runOff+done)
				for o := 0; o+v.RecSize <= m; o += v.RecSize {
					if !isFileSig(buf[o:]) {
						continue
					}
					copy(rec, buf[o:o+v.RecSize])
					if !applyFixup(rec) {
						continue
					}
					abs := runOff + done + int64(o)
					so := streamOff + done + int64(o)
					fr := parseMFTRecord(rec, abs)
					if fr == nil {
						continue
					}
					if so+int64(v.RecSize) > v.mftInit {
						// Más allá de lo inicializado: restos de una MFT anterior.
						s.addOrphan(fr)
						continue
					}
					fr.RecNo = uint32(so / int64(v.RecSize))
					fr.Flags |= fLive
					fr.Group = int16(v.Cont)
					if fr.has(fInUse) {
						switch {
						case fr.has(fExt) && fr.BaseRec == 0:
							queue = append(queue, fr.Segs...) // más tramos de la propia MFT
						case fr.has(fExt) && fr.BaseRec == 6:
							if bmp != nil {
								bmp.Segs = append(bmp.Segs, fr.Segs...)
							}
						case fr.RecNo == 6:
							bmp = fr
						case fr.has(fDir):
							s.Recs = append(s.Recs, fr)
						case fr.has(fExt):
							if fr.Name != "" && len(fr.Segs) == 0 {
								s.Recs = append(s.Recs, fr) // nombre de una carpeta guardado en una extensión
							}
						default:
							v.Existing++
						}
						continue
					}
					// Registro libre: archivo o carpeta borrados.
					if fr.RecNo < 24 {
						continue
					}
					if fr.has(fDir) || fr.has(fExt) || fr.Size > 0 || fr.has(fAttrList) {
						s.Recs = append(s.Recs, fr)
					}
				}
				done += int64(n)
				if progress != nil {
					progress()
				}
			}
		}
	}
	// Mapa de clusters ocupados ($Bitmap).
	if bmp != nil && !s.Opts.IgnoreMap {
		v.bitmap = s.readBitmap(v, bmp)
	}
	if v.bitmap == nil {
		if !s.Opts.IgnoreMap {
			s.Notes = append(s.Notes, "No se pudo leer el mapa de clusters de "+s.Conts[v.Cont].Desc+
				": se analizará todo el espacio (puede aparecer algún archivo que no estaba perdido).")
		}
		v.bitmap = make([]byte, v.nClusters/8+1)
	}
	// La propia MFT siempre cuenta como ocupada.
	for _, e := range mftExt {
		v.setAlloc(e.off, e.off+e.n)
	}
}

func (s *Scan) readBitmap(v *Volume, r *FileRec) []byte {
	if r.has(fResident) || r.Size <= 0 || r.Size > 1<<32 {
		return nil
	}
	runs, gaps := r.flatten()
	if gaps {
		return nil
	}
	out := make([]byte, r.Size)
	var pos int64
	for _, ru := range runs {
		if pos >= r.Size {
			break
		}
		n := min64(ru.Len*v.CS, r.Size-pos)
		if ru.LCN >= 0 {
			if m, _ := s.Dev.ReadAt(out[pos:pos+n], v.Start+ru.LCN*v.CS); int64(m) < n {
				return nil
			}
		}
		pos += n
	}
	if pos < r.Size {
		return nil
	}
	return out
}

// addOrphan guarda un registro MFT que no pertenece al sistema de archivos
// actual (resto de un formateo o reinstalación anterior).
func (s *Scan) addOrphan(fr *FileRec) {
	fr.Group = s.contOf(fr.Off)
	if fr.RecNo < 24 && fr.RecNo != 5 {
		if fr.RecNo == 0 && fr.Name == "$MFT" && len(fr.Segs) > 0 {
			s.OldMFT = append(s.OldMFT, fr)
		}
		return
	}
	if !fr.has(fDir) && !fr.has(fExt) && fr.Size == 0 && !fr.has(fAttrList) {
		return
	}
	s.Recs = append(s.Recs, fr)
}

// Prepare localiza particiones y sistemas de archivos actuales y lee su MFT.
func (s *Scan) Prepare(progress func()) {
	s.Conts = readContainers(s.Dev)
	for i := range s.Conts {
		s.loadVolume(i)
	}
	for _, v := range s.Vols {
		s.scanLiveMFT(v, progress)
	}
}

const scanChunk = 8 << 20

// ScanDevice recorre el dispositivo de principio a fin buscando registros
// NTFS huérfanos, sectores de arranque antiguos y firmas de archivos.
// Los clusters ocupados por el sistema de archivos actual se saltan.
func (s *Scan) ScanDevice(progress func(pos int64)) {
	const over = 8192
	buf := make([]byte, scanChunk+over)
	rec := make([]byte, 4096)
	size := s.Dev.Size
	pos := s.ScannedTo - s.ScannedTo%scanChunk
	var tsKind uint8
	var tsBase, tsLast int64
	for pos < size {
		if stopRequested() {
			s.Interrupted = true
			break
		}
		span := min64(scanChunk, size-pos)
		if s.fullyAllocated(pos, span) {
			pos += span
			s.ScannedTo = pos
			if progress != nil {
				progress(pos)
			}
			continue
		}
		n, _ := s.Dev.ReadAt(buf[:min64(scanChunk+over, size-pos)], pos)
		data := buf[:n]
		for o := 0; o+512 <= int(span) && o+512 <= n; o += 512 {
			sec := data[o:]
			abs := pos + int64(o)
			if s.allocatedAt(abs) {
				continue
			}
			if isFileSig(sec) {
				if s.Opts.NoMFT {
					continue
				}
				rs := int(le32(sec[0x1C:]))
				if (rs == 1024 || rs == 2048 || rs == 4096) && len(sec) >= rs {
					copy(rec[:rs], sec[:rs])
					if applyFixup(rec[:rs]) {
						if fr := parseMFTRecord(rec[:rs], abs); fr != nil {
							s.addOrphan(fr)
						}
						o += rs - 512
					}
				}
				continue
			}
			if sec[510] == 0x55 && sec[511] == 0xAA && sec[3] == 'N' {
				if bi, ok := parseBoot(sec[:512]); ok {
					s.Boots = append(s.Boots, BootHit{abs, bi})
					continue
				}
			}
			if !s.Opts.NoCarve {
				if k := sniff(sec); k != 0 {
					if k == kTS || k == kM2TS {
						// Un video MPEG-TS vuelve a coincidir con el inicio de un
						// sector cada pocos paquetes: solo interesa el primero.
						pkt := int64(188)
						if k == kM2TS {
							pkt = 192
						}
						cont := k == tsKind && (abs-tsBase)%pkt == 0 && abs-tsLast <= 64<<10
						tsLast = abs
						if cont {
							continue
						}
						tsKind, tsBase = k, abs
					}
					s.Cands = append(s.Cands, Cand{abs, k})
				}
			}
		}
		pos += span
		s.ScannedTo = pos
		if progress != nil {
			progress(pos)
		}
	}
}

// --- índice ordenado de registros -------------------------------------

func recKey(live bool, group int16, recno uint32) uint64 {
	k := uint64(uint16(group))<<32 | uint64(recno)
	if live {
		k |= 1 << 48
	}
	return k
}

func (r *FileRec) key() uint64 { return recKey(r.has(fLive), r.Group, r.RecNo) }

// baseOff es la posición donde empezaría la MFT a la que pertenece el
// registro si fuera contigua; sirve para distinguir restos de MFT distintas.
func (r *FileRec) baseOff() int64 { return r.Off - int64(r.RecNo)*1024 }

// lookup devuelve los registros base con esa clave (s.Recs debe estar ordenado).
func (s *Scan) lookup(live bool, group int16, recno uint32) []*FileRec {
	k := recKey(live, group, recno)
	i := sort.Search(len(s.Recs), func(i int) bool { return s.Recs[i].key() >= k })
	j := i
	for j < len(s.Recs) && s.Recs[j].key() == k {
		j++
	}
	return s.Recs[i:j]
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// attachExtensions une los registros de extensión a su registro base,
// ordena el índice y elimina duplicados.
func (s *Scan) attachExtensions() {
	var exts, bases []*FileRec
	for _, r := range s.Recs {
		if r.has(fExt) {
			exts = append(exts, r)
		} else {
			bases = append(bases, r)
		}
	}
	sort.SliceStable(bases, func(i, j int) bool {
		a, b := bases[i], bases[j]
		if ka, kb := a.key(), b.key(); ka != kb {
			return ka < kb
		}
		return a.Off < b.Off
	})
	s.Recs = bases
	// Duplicados (copias del mismo registro en distintos lugares).
	out := bases[:0]
	for i, r := range bases {
		dup := false
		for j := i - 1; j >= 0 && bases[j].key() == r.key() && i-j < 8; j-- {
			p := bases[j]
			if p.Seq == r.Seq && p.Name == r.Name && p.Size == r.Size && p.firstLCN() == r.firstLCN() && p.Flags == r.Flags {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	s.Recs = out
	for _, e := range exts {
		var best *FileRec
		var bestD int64
		for _, c := range s.lookup(e.has(fLive), e.Group, e.BaseRec) {
			if c.Seq != e.BaseSeq && c.Seq != e.BaseSeq+1 {
				continue
			}
			d := absDiff(c.baseOff(), e.baseOff())
			if best == nil || d < bestD {
				best, bestD = c, d
			}
		}
		if best == nil {
			continue
		}
		if e.Name != "" && best.Name == "" {
			best.Name, best.ParentRec, best.ParentSeq = e.Name, e.ParentRec, e.ParentSeq
			if best.MTime == 0 {
				best.MTime = e.MTime
			}
		}
		if best.has(fDir) || len(e.Segs) == 0 {
			continue
		}
		for _, sg := range e.Segs {
			dup := false
			for _, have := range best.Segs {
				if have.StartVCN == sg.StartVCN {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			best.Segs = append(best.Segs, sg)
			if sg.StartVCN == 0 && best.Size == 0 {
				best.Size, best.InitSize, best.CompUnit = e.Size, e.InitSize, e.CompUnit
				best.Flags |= e.Flags & (fCompressed | fEncrypted)
			}
		}
		best.Flags |= fHasData
		best.Flags &^= fResident
	}
}

// findParent localiza la carpeta que contenía a r.
func (s *Scan) findParent(r *FileRec) *FileRec {
	if r.ParentRec == noRec {
		return nil
	}
	var best *FileRec
	var bestD int64
	for _, c := range s.lookup(r.has(fLive), r.Group, r.ParentRec) {
		if !c.has(fDir) || c == r {
			continue
		}
		ok := c.Seq == r.ParentSeq || (!c.has(fInUse) && c.Seq == r.ParentSeq+1)
		if !ok {
			continue
		}
		d := absDiff(c.baseOff(), r.baseOff())
		if best == nil || d < bestD {
			best, bestD = c, d
		}
	}
	return best
}
