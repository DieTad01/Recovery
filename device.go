package main

import (
	"io"
	"sync/atomic"
)

// rawReader es el acceso de bajo nivel a la unidad o al archivo de imagen.
type rawReader interface {
	ReadAt(p []byte, off int64) (int, error)
	Close() error
}

// Device es la fuente de lectura: una unidad (C:), un disco físico o un
// archivo de imagen. Solo se abre para LECTURA; nunca se escribe en ella.
type Device struct {
	Path     string
	Size     int64
	Align    int   // 1 en archivos de imagen; tamaño de sector en dispositivos
	BadBytes int64 // bytes ilegibles (sectores dañados), rellenados con ceros
	r        rawReader
}

// OpenDevice abre la fuente en modo de solo lectura.
func OpenDevice(path string) (*Device, error) {
	r, size, align, err := openRaw(path)
	if err != nil {
		return nil, err
	}
	if align < 1 {
		align = 1
	}
	if align > 1 {
		size -= size % int64(align)
	}
	return &Device{Path: path, Size: size, Align: align, r: r}, nil
}

func (d *Device) Close() error { return d.r.Close() }

// readAligned lee [off, off+len(p)) tolerando sectores dañados: lo que no
// se puede leer se rellena con ceros y se contabiliza en BadBytes.
func (d *Device) readAligned(p []byte, off int64) {
	n, err := d.r.ReadAt(p, off)
	if n == len(p) {
		return
	}
	_ = err
	a := d.Align
	if a < 512 {
		a = 512
	}
	if n < 0 {
		n = 0
	}
	pos := n / a * a
	const step = 64 << 10
	for pos < len(p) {
		end := pos + step
		if end > len(p) {
			end = len(p)
		}
		m, _ := d.r.ReadAt(p[pos:end], off+int64(pos))
		if m == end-pos {
			pos = end
			continue
		}
		for s := pos; s < end; s += a {
			e := s + a
			if e > end {
				e = end
			}
			m, _ := d.r.ReadAt(p[s:e], off+int64(s))
			if m != e-s {
				for i := s; i < e; i++ {
					p[i] = 0
				}
				atomic.AddInt64(&d.BadBytes, int64(e-s))
			}
		}
		pos = end
	}
}

// ReadAt lee desde cualquier posición (se encarga de alinear a sector
// cuando el dispositivo lo exige). Lo que queda fuera del dispositivo no
// se lee: devuelve los bytes disponibles e io.EOF.
func (d *Device) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= d.Size {
		return 0, io.EOF
	}
	n := len(p)
	var eof error
	if int64(n) > d.Size-off {
		n = int(d.Size - off)
		eof = io.EOF
	}
	a := int64(d.Align)
	if a <= 1 || (off%a == 0 && int64(n)%a == 0) {
		d.readAligned(p[:n], off)
		return n, eof
	}
	start := off / a * a
	end := (off + int64(n) + a - 1) / a * a
	if end > d.Size {
		end = d.Size
	}
	tmp := make([]byte, end-start)
	d.readAligned(tmp, start)
	copy(p[:n], tmp[off-start:])
	return n, eof
}

// src es una ventana deslizante de lectura sobre el dispositivo, usada por
// los analizadores de formatos. Los cortes devueltos por at() solo son
// válidos hasta la siguiente llamada.
type src struct {
	d    *Device
	buf  []byte
	base int64
	n    int
	win  int
}

const (
	srcMinWin = 64 << 10
	srcMaxWin = 2 << 20
)

func newSrc(d *Device) *src { return &src{d: d, base: -1, win: srcMinWin} }

// at devuelve exactamente n bytes en off, o nil si no existen.
func (s *src) at(off int64, n int) []byte {
	if off < 0 || n < 0 || off+int64(n) > s.d.Size {
		return nil
	}
	if s.base >= 0 && off >= s.base && off+int64(n) <= s.base+int64(s.n) {
		return s.buf[off-s.base : off-s.base+int64(n)]
	}
	// Lectura secuencial -> ventana cada vez mayor; salto -> ventana chica.
	if s.base >= 0 && off >= s.base && off <= s.base+int64(s.n)+int64(srcMinWin) {
		if s.win < srcMaxWin {
			s.win *= 2
		}
	} else {
		s.win = srcMinWin
	}
	base := off &^ 4095
	need := int(off-base) + n
	sz := s.win
	if need > sz {
		sz = (need + 4095) &^ 4095
	}
	if cap(s.buf) < sz {
		s.buf = make([]byte, sz)
	}
	s.buf = s.buf[:sz]
	m, _ := s.d.ReadAt(s.buf, base)
	s.base, s.n = base, m
	if need > m {
		return nil
	}
	return s.buf[off-base : off-base+int64(n)]
}

// upTo devuelve hasta n bytes en off (menos si se acaba el dispositivo).
func (s *src) upTo(off int64, n int) []byte {
	if off < 0 || off >= s.d.Size {
		return nil
	}
	if int64(n) > s.d.Size-off {
		n = int(s.d.Size - off)
	}
	return s.at(off, n)
}

// copyAt devuelve una copia propia de n bytes en off.
func (s *src) copyAt(off int64, n int) []byte {
	b := s.at(off, n)
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}
