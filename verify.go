package main

// Comprobación del contenido de los archivos encontrados, en dos niveles:
//
//   - rápida (durante el análisis): unas muestras de 4 KB de cada archivo.
//     Detecta el contenido en blanco (TRIM), un principio que no es del tipo
//     de archivo que dice su nombre, o texto con datos ajenos dentro.
//   - a fondo («Verificar a fondo» y al recuperar): se lee el archivo entero
//     y se comprueba con validateContent (sumas de control, decodificación,
//     estructura).
//
// Sin esto, «Bueno» solo significaría «su espacio no lo ocupa ningún archivo
// actual», que no garantiza que el contenido siga ahí: una reinstalación
// escribe y borra muchos temporales que dejan el espacio libre otra vez.

import (
	"bytes"
	"io"
	"os"
	"sort"
	"strconv"
)

type qres struct {
	zero bool
	bad  string
}

// vres es el resultado de la comprobación a fondo.
type vres struct {
	v   int
	why string
}

// quickCheck lee unas muestras del archivo.
// En los archivos de sistema (muchísimos) solo se mira el principio.
func (ss *Session) quickCheck(r *FileRec, g Geometry, buf []byte, system bool) qres {
	runs, _ := r.flatten()
	limit := r.Size
	if r.InitSize >= 0 && r.InitSize < limit {
		limit = r.InitSize
	}
	if limit <= 0 {
		return qres{}
	}
	samples := []int64{0, limit / 2, max64(0, limit-4096)}
	if limit > 256<<10 {
		samples = []int64{0, limit / 4, limit / 2, limit * 3 / 4, max64(0, limit-4096)}
	}
	if system {
		samples = samples[:1]
	}
	ext := extOf(r.Name)
	text := plainExts[ext]
	utf16 := false
	read, zeros := 0, 0
	zeroAt := int64(-1)
	for i, lo := range samples {
		lo -= lo % g.CS
		var vcnOff int64
		for _, ru := range runs {
			l := ru.Len * g.CS
			if lo >= vcnOff && lo < vcnOff+l {
				if ru.LCN >= 0 {
					off := g.Delta + ru.LCN*g.CS + (lo - vcnOff)
					n := int(min64(int64(len(buf)), limit-lo))
					m, _ := ss.Dev.ReadAt(buf[:n], off)
					b := buf[:m]
					read++
					if allZero(b) {
						zeros++
						if zeroAt < 0 && i > 0 && lo+4096 < limit {
							zeroAt = lo
						}
						break
					}
					if i == 0 {
						if !r.has(fEncrypted) && checkSig(ext, b) < 0 {
							return qres{bad: "el principio del archivo no es de un ." + ext + ": lo ocupan otros datos"}
						}
						utf16 = len(b) >= 2 && (b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF)
					}
					if text && !r.has(fEncrypted) && !looksLikeText(b, utf16) {
						return qres{bad: "contiene datos que no son texto: lo ocupan otros datos"}
					}
				}
				break
			}
			vcnOff += l
		}
	}
	if read > 0 && zeros == read {
		return qres{zero: true}
	}
	if zeroAt >= 0 && (quickZeroBad[ext] || text && ext != "log") && !(jpegExts[ext] && zeroAt < 128<<10) {
		// (en JPG el principio puede llevar relleno de ceros en sus metadatos)
		return qres{bad: "tiene zonas en blanco: parte del contenido ya no existe"}
	}
	return qres{}
}

// Formatos en los que una zona de 4 KB en blanco dentro del archivo es
// señal segura de daño (sus datos van comprimidos).
var quickZeroBad = setOf("jpg jpeg jpe jfif gif 7z rar docx xlsx pptx png")

var jpegExts = setOf("jpg jpeg jpe jfif")

// ---------------------------------------------------------------- a fondo

func (ss *Session) deepKey(e *Entry) string {
	if e.Item != nil {
		return e.Key + "@" + strconv.FormatInt(ss.Scan.geomFor(e.Item.Rec).Delta, 10)
	}
	return e.Key
}

// VerifyResult resume una comprobación a fondo.
type VerifyResult struct {
	Checked, OK, Struct, Bad, Unknown int
	Stopped                           bool
}

// DeepVerify comprueba a fondo el contenido de las entradas indicadas y
// actualiza su estado (las ya comprobadas antes no se repiten).
func (ss *Session) DeepVerify(es []*Entry, prog func(done, total int, bytes int64)) VerifyResult {
	if ss.deep == nil {
		ss.deep = map[string]vres{}
	}
	type job struct {
		e    *Entry
		key  string
		phys int64
	}
	var jobs []job
	var cached []int
	ss.mu.RLock()
	for _, e := range es {
		if !e.Checkable() || e.Size == 0 {
			continue
		}
		k := ss.deepKey(e)
		ss.dmu.Lock()
		prev, done := ss.deep[k]
		ss.dmu.Unlock()
		if done {
			cached = append(cached, prev.v)
			continue
		}
		j := job{e: e, key: k}
		if e.Item != nil {
			j.phys = e.Item.Phys
		} else if e.Carve != nil {
			j.phys = e.Carve.Off
		}
		jobs = append(jobs, j)
	}
	ss.mu.RUnlock()
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].phys < jobs[b].phys })
	var bytesDone int64
	var res VerifyResult
	tally := func(v int) {
		res.Checked++
		switch v {
		case vOK:
			res.OK++
		case vStruct:
			res.Struct++
		case vBad:
			res.Bad++
		default:
			res.Unknown++
		}
	}
	for _, v := range cached {
		tally(v)
	}
	for i, j := range jobs {
		if stopRequested() {
			res.Stopped = true
			break
		}
		if prog != nil {
			prog(i, len(jobs), bytesDone)
		}
		r, size := ss.entryReader(j.e)
		if r == nil {
			continue
		}
		v, why := validateContent(r, size, j.e.Ext)
		ss.dmu.Lock()
		ss.deep[j.key] = vres{v, why}
		ss.dmu.Unlock()
		bytesDone += j.e.Size
		tally(v)
	}
	if prog != nil {
		prog(len(jobs), len(jobs), bytesDone)
	}
	ss.publish(nil)
	return res
}

// applyDeep ajusta el estado de una entrada con el resultado a fondo.
func applyDeep(e *Entry, r vres) {
	e.Deep = r.v
	switch r.v {
	case vOK:
		e.State, e.Why = esVerified, r.why
	case vStruct:
		if e.State == esIntact || e.State == esCarved {
			e.Why = r.why
		}
	case vBad:
		e.State, e.Why = esCorrupt, r.why
	}
}

// entryReader da acceso al contenido completo de una entrada.
func (ss *Session) entryReader(e *Entry) (io.ReaderAt, int64) {
	switch {
	case e.Carve != nil:
		return io.NewSectionReader(ss.Dev, e.Carve.Off, e.Carve.Size), e.Carve.Size
	case e.Item != nil:
		r := e.Item.Rec
		if r.has(fResident) || r.has(fCompressed) || r.Size <= 32<<20 {
			if r.Size > 1<<30 {
				return nil, 0
			}
			data, _ := ss.ReadEntry(e, r.Size)
			return bytes.NewReader(data), int64(len(data))
		}
		ss.mu.RLock()
		runs, _ := r.flatten()
		g := ss.Scan.geomFor(r)
		ss.mu.RUnlock()
		init := r.InitSize
		if init < 0 || init > r.Size {
			init = r.Size
		}
		return &runReader{dev: ss.Dev, runs: runs, g: g, size: r.Size, init: init}, r.Size
	}
	return nil, 0
}

// runReader lee un archivo NTFS sin comprimir directamente de sus tramos.
type runReader struct {
	dev        *Device
	runs       []Run
	g          Geometry
	size, init int64
}

func (rr *runReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= rr.size {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > rr.size-off {
		want = int(rr.size - off)
	}
	done := 0
	var vcn int64
	for _, ru := range rr.runs {
		l := ru.Len * rr.g.CS
		for done < want && off+int64(done) >= vcn && off+int64(done) < vcn+l {
			pos := off + int64(done)
			n := int(min64(int64(want-done), vcn+l-pos))
			dst := p[done : done+n]
			if ru.LCN < 0 || pos >= rr.init {
				clear(dst)
			} else {
				dev := rr.g.Delta + ru.LCN*rr.g.CS + (pos - vcn)
				if dev < 0 || dev+int64(n) > rr.dev.Size {
					clear(dst)
				} else {
					rr.dev.ReadAt(dst, dev)
				}
				if pos+int64(n) > rr.init { // más allá de los datos válidos: ceros
					clear(dst[rr.init-pos:])
				}
			}
			done += n
		}
		vcn += l
		if done >= want {
			break
		}
	}
	if done < want {
		clear(p[done:want]) // faltan tramos
	}
	if want < len(p) {
		return want, io.EOF
	}
	return want, nil
}

// ---------------------------------------------------------------- al recuperar

// validateFile comprueba un archivo ya recuperado en el destino.
func validateFile(path, ext string) (int, string) {
	f, err := os.Open(path)
	if err != nil {
		return vUnknown, ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return vUnknown, ""
	}
	return validateContent(f, st.Size(), ext)
}
