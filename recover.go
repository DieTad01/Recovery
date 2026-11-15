package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// sink escribe un archivo recuperado y va observando su contenido.
type sink struct {
	w       io.Writer
	head    []byte
	n       int64
	limit   int64 // si > 0, se deja de escribir al llegar a este tamaño
	nonZero bool
	err     error
}

// errLimit indica que se alcanzó el límite del sink (vista previa).
var errLimit = errors.New("límite alcanzado")

func (k *sink) write(p []byte) {
	if k.err != nil || len(p) == 0 {
		return
	}
	if len(k.head) < 64 {
		m := 64 - len(k.head)
		if m > len(p) {
			m = len(p)
		}
		k.head = append(k.head, p[:m]...)
	}
	if k.limit > 0 && k.n+int64(len(p)) > k.limit {
		p = p[:k.limit-k.n]
		k.err = errLimit
	}
	if !k.nonZero && !allZero(p) {
		k.nonZero = true
	}
	if _, err := k.w.Write(p); err != nil {
		k.err = err
	}
	k.n += int64(len(p))
}

var zeroBuf = make([]byte, 1<<20)

func (k *sink) zeros(n int64) {
	for n > 0 && k.err == nil {
		m := int64(len(zeroBuf))
		if m > n {
			m = n
		}
		if len(k.head) < 64 {
			h := 64 - len(k.head)
			if int64(h) > m {
				h = int(m)
			}
			k.head = append(k.head, zeroBuf[:h]...)
		}
		if k.limit > 0 && k.n+m > k.limit {
			m = k.limit - k.n
			k.err = errLimit
		}
		if _, err := k.w.Write(zeroBuf[:m]); err != nil {
			k.err = err
		}
		k.n += m
		n -= m
	}
}

// Recoverer copia los archivos encontrados a la carpeta de destino.
type Recoverer struct {
	s        *Scan
	out      string
	rep      *Report
	flt      Filter
	MinCarve int64
	buf      []byte
	covered  []iv
	madeDir  map[string]bool

	NGood, NDoubt, NEmpty, NErr, NAlready int64
	NCarved, NCarvedDoubt                 int64
	Bytes                                 int64
	Full                                  bool // el destino se quedó sin espacio
	lastTick                              time.Time
}

func NewRecoverer(s *Scan, out string, rep *Report, flt Filter) *Recoverer {
	return &Recoverer{s: s, out: out, rep: rep, flt: flt, buf: make([]byte, 4<<20),
		madeDir: map[string]bool{}, MinCarve: 4096}
}

func (rc *Recoverer) abs(rel string) string {
	return filepath.Join(rc.out, filepath.FromSlash(rel))
}

func (rc *Recoverer) mkdirFor(path string) error {
	dir := filepath.Dir(path)
	if rc.madeDir[dir] {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rc.madeDir[dir] = true
	return nil
}

// copyRange copia n bytes del dispositivo al archivo de salida.
func (rc *Recoverer) copyRange(k *sink, off, n int64) {
	for n > 0 && k.err == nil {
		m := int64(len(rc.buf))
		if m > n {
			m = n
		}
		// Se lee redondeando al sector para usar la lectura directa.
		rd := m
		if a := int64(rc.s.Dev.Align); a > 1 && off%a == 0 && m%a != 0 {
			if r := (m + a - 1) / a * a; r <= int64(len(rc.buf)) && off+r <= rc.s.Dev.Size {
				rd = r
			}
		}
		got, _ := rc.s.Dev.ReadAt(rc.buf[:rd], off)
		if int64(got) > m {
			got = int(m)
		}
		k.write(rc.buf[:got])
		if int64(got) < m {
			k.zeros(m - int64(got))
		}
		off += m
		n -= m
	}
}

// writeData vuelca el contenido del archivo descrito por el registro r.
// Devuelve true si se sabe que el resultado está incompleto.
func (rc *Recoverer) writeData(r *FileRec, g Geometry, k *sink) (damaged bool) {
	dev := rc.s.Dev
	if r.has(fResident) {
		raw := make([]byte, 4096)
		n, _ := dev.ReadAt(raw, r.Off)
		if n < 1024 || !isFileSig(raw) {
			return true
		}
		rs := int(le32(raw[0x1C:]))
		if rs != 1024 && rs != 2048 && rs != 4096 || rs > n || !applyFixup(raw[:rs]) {
			return true
		}
		data := residentData(raw[:rs])
		if data == nil {
			return true
		}
		k.write(data)
		return false
	}
	runs, gaps := r.flatten()
	if r.has(fCompressed) && r.CompUnit > 0 && r.CompUnit <= 8 {
		return rc.writeCompressed(r, g, runs, k) || gaps
	}
	remaining, init := r.Size, r.InitSize
	for _, ru := range runs {
		if remaining <= 0 {
			break
		}
		n := min64(ru.Len*g.CS, remaining)
		valid := min64(n, init)
		if valid < 0 {
			valid = 0
		}
		if ru.LCN < 0 {
			k.zeros(n)
		} else {
			off := g.Delta + ru.LCN*g.CS
			if off < 0 || off+valid > dev.Size {
				k.zeros(n)
				damaged = true
			} else {
				rc.copyRange(k, off, valid)
				k.zeros(n - valid)
			}
		}
		remaining -= n
		init -= n
	}
	if remaining > 0 {
		damaged = true // faltan tramos: archivo truncado
	}
	return damaged || gaps
}

// writeCompressed vuelca un archivo con compresión NTFS (LZNT1).
func (rc *Recoverer) writeCompressed(r *FileRec, g Geometry, runs []Run, k *sink) (damaged bool) {
	dev := rc.s.Dev
	unit := int64(1) << r.CompUnit
	ub := unit * g.CS
	in := make([]byte, ub)
	out := make([]byte, ub)
	ri := 0
	var rpos int64
	remaining := r.Size
	for remaining > 0 && k.err == nil {
		var real, hole int64
		need := unit
		for need > 0 && ri < len(runs) {
			take := min64(runs[ri].Len-rpos, need)
			if runs[ri].LCN < 0 {
				hole += take
			} else {
				off := g.Delta + (runs[ri].LCN+rpos)*g.CS
				dst := in[real*g.CS : (real+take)*g.CS]
				if off < 0 || off+int64(len(dst)) > dev.Size {
					for i := range dst {
						dst[i] = 0
					}
					damaged = true
				} else {
					dev.ReadAt(dst, off)
				}
				real += take
			}
			rpos += take
			need -= take
			if rpos == runs[ri].Len {
				ri++
				rpos = 0
			}
		}
		n := min64(ub, remaining)
		switch {
		case real == 0:
			if need == unit {
				damaged = true // se acabó la lista de tramos
			}
			k.zeros(n)
		case hole == 0: // unidad sin comprimir
			m := min64(n, real*g.CS)
			k.write(in[:m])
			k.zeros(n - m)
		default: // unidad comprimida
			for i := range out {
				out[i] = 0
			}
			lznt1Decompress(in[:real*g.CS], out)
			k.write(out[:n])
		}
		remaining -= n
	}
	return damaged
}

func (rc *Recoverer) tick(format string, a ...interface{}) {
	if time.Since(rc.lastTick) < 400*time.Millisecond {
		return
	}
	rc.lastTick = time.Now()
	statusLine(fmt.Sprintf(format, a...))
}

func originOf(r *FileRec) string {
	if r.has(fLive) {
		return "NTFS actual (borrado)"
	}
	return "NTFS anterior"
}

// RecoverMFT recupera los archivos del plan (con nombre y carpeta).
func (rc *Recoverer) RecoverMFT(p *Plan) {
	rc.covered = append(rc.covered, p.covered...)
	for _, sk := range p.Skipped {
		rc.rep.Row("NO RECUPERABLE", "NTFS", sk.Rel, sk.Size, sk.MTime, sk.Reason)
	}
	start := time.Now()
	var doneBytes int64
	for i, it := range p.Items {
		if stopRequested() || rc.Full {
			break
		}
		rc.tick("  Recuperando archivos: %s de %s  ·  %s de %s  ·  %s",
			groupDigits(int64(i)), groupDigits(int64(len(p.Items))),
			humanSize(doneBytes), humanSize(p.TotalSize), humanDur(time.Since(start)))
		progress(float64(i) / float64(len(p.Items)+1))
		rc.recoverItem(it)
		doneBytes += it.Rec.Size
	}
	statusLine("")
}

func (rc *Recoverer) recoverItem(it *Item) {
	r := it.Rec
	g := rc.s.geomFor(r)
	final := rc.abs(it.Rel)
	doubt := rc.abs(dirDoubt + "/" + it.Rel)
	// Reanudación: si ya se recuperó en una ejecución anterior, se omite.
	for _, p := range []string{final, doubt} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() == r.Size {
			rc.NAlready++
			if p == final && it.Overw == 0 {
				rc.covered = appendRuns(rc.covered, r, g)
			}
			return
		}
	}
	if err := rc.mkdirFor(final); err != nil {
		rc.fail(it, err)
		return
	}
	f, err := os.Create(final)
	if err != nil {
		rc.fail(it, err)
		return
	}
	k := &sink{w: f}
	bad0 := atomic.LoadInt64(&rc.s.Dev.BadBytes)
	damaged := rc.writeData(r, g, k)
	cerr := f.Close()
	if k.err == nil {
		k.err = cerr
	}
	if k.err != nil {
		os.Remove(final)
		rc.fail(it, k.err)
		return
	}
	if !k.nonZero && !r.has(fResident) {
		os.Remove(final)
		rc.NEmpty++
		rc.rep.Row("VACÍO", originOf(r), it.Rel, r.Size, r.MTime,
			"el contenido en el disco es todo ceros (típico de SSD con TRIM): no hay nada que recuperar")
		return
	}
	if t := ftToTime(r.MTime); !t.IsZero() {
		os.Chtimes(final, t, t)
	}
	ext := extOf(r.Name)
	if r.Name == "" {
		// Sin nombre original: al menos se le pone la extensión que corresponde.
		if e := guessExt(k.head); e != "" {
			if os.Rename(final, final+"."+e) == nil {
				final += "." + e
				doubt += "." + e
				it.Rel += "." + e
				ext = e
			}
		}
	}
	var why []string
	if it.Overw > 0 {
		why = append(why, fmt.Sprintf("%d%% de sus datos fue sobrescrito por archivos nuevos", it.Overw*100/max64(it.Total, 1)))
	}
	if damaged {
		why = append(why, "faltan fragmentos del archivo")
	}
	if atomic.LoadInt64(&rc.s.Dev.BadBytes) > bad0 {
		why = append(why, "sectores ilegibles en el disco")
	}
	if !r.has(fEncrypted) && checkSig(ext, k.head) < 0 {
		why = append(why, "el contenido no corresponde a un archivo ."+ext+" (probablemente sobrescrito)")
	}
	if !g.Confirmed && g.Samples > 0 && !r.has(fResident) && len(why) == 0 && checkSig(ext, k.head) == 0 {
		why = append(why, "no se pudo confirmar la posición del volumen original")
	}
	if len(why) > 0 {
		if err := rc.mkdirFor(doubt); err == nil {
			os.Remove(doubt)
			if err := os.Rename(final, doubt); err == nil {
				rc.NDoubt++
				rc.rep.Row("DUDOSO", originOf(r), dirDoubt+"/"+it.Rel, r.Size, r.MTime, strings.Join(why, "; "))
				return
			}
		}
		rc.NDoubt++
		rc.rep.Row("DUDOSO", originOf(r), it.Rel, r.Size, r.MTime, strings.Join(why, "; "))
		return
	}
	rc.NGood++
	rc.Bytes += r.Size
	detail := ""
	if r.has(fEncrypted) {
		detail = "cifrado con EFS: solo se podrá abrir con el certificado del usuario original"
	}
	rc.rep.Row("RECUPERADO", originOf(r), it.Rel, r.Size, r.MTime, detail)
	if !r.has(fResident) {
		rc.covered = appendRuns(rc.covered, r, g)
	}
}

func (rc *Recoverer) fail(it *Item, err error) {
	if isDiskFull(err) {
		rc.Full = true
		return
	}
	rc.NErr++
	rc.rep.Row("ERROR", originOf(it.Rec), it.Rel, it.Rec.Size, it.Rec.MTime, err.Error())
}

// CarvedFile es un archivo localizado por su contenido (sin nombre).
type CarvedFile struct {
	Off, Size int64
	Ext       string
	Name      string
	MTime     time.Time
	Doubt     bool
	Why       string
	Rel       string // ruta relativa dentro de la carpeta de destino
}

// RecoverCarved extrae los archivos detectados por firma que no hayan
// sido ya recuperados con su nombre.
func (rc *Recoverer) RecoverCarved() {
	rc.WriteCarved(rc.AnalyzeCarved(nil))
}

// AnalyzeCarved recorre los candidatos detectados por firma, calcula el
// tamaño real de cada archivo y decide si es fiable. No escribe nada.
func (rc *Recoverer) AnalyzeCarved(progressFn func(done, total, found int)) []CarvedFile {
	s := rc.s
	rc.covered = mergeIvs(rc.covered)
	sr := newSrc(s.Dev)
	var suppress int64
	var out []CarvedFile
	start := time.Now()
	for i, c := range s.Cands {
		if stopRequested() || rc.Full {
			break
		}
		rc.tick("  Analizando archivos por firma: %s de %s candidatos  ·  %s válidos  ·  %s",
			groupDigits(int64(i)), groupDigits(int64(len(s.Cands))), groupDigits(int64(len(out))),
			humanDur(time.Since(start)))
		progress(float64(i) / float64(len(s.Cands)+1))
		if progressFn != nil && i%64 == 0 {
			progressFn(i, len(s.Cands), len(out))
		}
		if c.Off < suppress || ivContains(rc.covered, c.Off) || s.allocatedAt(c.Off) {
			continue
		}
		fn := carvers[c.Kind]
		if fn == nil {
			continue
		}
		res, ok := fn(sr, c.Off, s.Dev.Size-c.Off)
		if !ok || res.size <= 0 || c.Off+res.size > s.Dev.Size {
			continue
		}
		if res.walked {
			suppress = c.Off + res.size
		}
		if res.size < rc.MinCarve || (rc.flt.Exts != nil && !rc.flt.Exts[res.ext]) {
			continue
		}
		if rc.flt.Search != nil && !rc.flt.Search.MatchExt(res.ext) {
			continue
		}
		// ¿El tramo pisa datos que pertenecen a otros archivos? Entonces
		// el archivo estaba fragmentado y lo extraído no es fiable.
		cf := CarvedFile{Off: c.Off, Size: res.size, Ext: res.ext, MTime: res.mtime}
		if res.partial {
			cf.Doubt = true
			cf.Why = "archivo incompleto (fragmentado o parcialmente sobrescrito)"
		}
		if !res.walked && (ivOverlaps(rc.covered, c.Off, c.Off+res.size) || s.anyAllocated(c.Off, res.size)) {
			cf.Doubt = true
			cf.Why = "el archivo estaba fragmentado: el tramo incluye datos de otros archivos"
		}
		cf.Name = fmt.Sprintf("%s_%010d.%s", res.ext, c.Off/512, res.ext)
		if res.stamp != "" {
			cf.Name = fmt.Sprintf("%s_%010d.%s", res.stamp, c.Off/512, res.ext)
		}
		cf.Rel = dirCarved + "/" + res.ext + "/" + cf.Name
		if cf.Doubt {
			cf.Rel = dirDoubt + "/" + cf.Rel
		}
		out = append(out, cf)
	}
	if progressFn != nil {
		progressFn(len(s.Cands), len(s.Cands), len(out))
	}
	statusLine("")
	return out
}

// WriteCarved guarda en el destino los archivos localizados por firma.
func (rc *Recoverer) WriteCarved(list []CarvedFile) {
	start := time.Now()
	for i := range list {
		if stopRequested() || rc.Full {
			break
		}
		rc.tick("  Guardando archivos por firma: %s de %s  ·  %s",
			groupDigits(int64(i)), groupDigits(int64(len(list))), humanDur(time.Since(start)))
		rc.WriteCarvedOne(&list[i])
	}
	statusLine("")
}

// WriteCarvedOne guarda un archivo localizado por firma.
func (rc *Recoverer) WriteCarvedOne(cf *CarvedFile) {
	final := rc.abs(cf.Rel)
	if st, err := os.Stat(final); err == nil && st.Size() == cf.Size {
		rc.NAlready++
		return
	}
	if err := rc.mkdirFor(final); err != nil {
		rc.NErr++
		return
	}
	f, err := os.Create(final)
	if err != nil {
		rc.NErr++
		rc.rep.Row("ERROR", "firma", cf.Rel, cf.Size, 0, err.Error())
		return
	}
	k := &sink{w: f}
	rc.copyRange(k, cf.Off, cf.Size)
	cerr := f.Close()
	if k.err == nil {
		k.err = cerr
	}
	if k.err != nil {
		os.Remove(final)
		if isDiskFull(k.err) {
			rc.Full = true
			return
		}
		rc.NErr++
		rc.rep.Row("ERROR", "firma", cf.Rel, cf.Size, 0, k.err.Error())
		return
	}
	if !cf.MTime.IsZero() {
		os.Chtimes(final, cf.MTime, cf.MTime)
	}
	if cf.Doubt {
		rc.NCarvedDoubt++
		rc.rep.Row("DUDOSO", "firma", cf.Rel, cf.Size, 0, cf.Why)
	} else {
		rc.NCarved++
		rc.Bytes += cf.Size
		rc.rep.Row("RECUPERADO", "firma", cf.Rel, cf.Size, 0, "sin nombre original (recuperado por su contenido)")
	}
}

// anyAllocated indica si algún cluster de [off, off+n) está ocupado por el
// sistema de archivos actual.
func (s *Scan) anyAllocated(off, n int64) bool {
	v := s.volAt(off)
	if v == nil {
		return false
	}
	c0 := (off - v.Start) / v.CS
	c1 := (off - v.Start + n + v.CS - 1) / v.CS
	return v.countAlloc(c0, c1) > 0
}

// removeEmptyDirs borra las carpetas que hayan quedado vacías.
func removeEmptyDirs(root string) {
	var dirs []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // solo tiene efecto si está vacía
	}
}
