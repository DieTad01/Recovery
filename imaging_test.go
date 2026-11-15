package main

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// faultyDisk simula un disco con sectores ilegibles: como un disco real,
// devuelve lo que pudo leer hasta el primer sector malo y un error.
type faultyDisk struct {
	data      []byte
	bad       [][2]int64 // zonas [inicio, fin)
	reads     int
	stopAfter int // simula pulsar «Detener» tras esta lectura
}

func (d *faultyDisk) ReadAt(p []byte, off int64) (int, error) {
	d.reads++
	if d.stopAfter > 0 && d.reads == d.stopAfter {
		atomic.StoreInt32(&stopFlag, 1)
	}
	end := off + int64(len(p))
	firstBad := end
	for _, b := range d.bad {
		if b[0] < end && b[1] > off && b[0] < firstBad {
			firstBad = max(b[0], off)
		}
	}
	n := copy(p[:firstBad-off], d.data[off:])
	if firstBad < end {
		return n, errors.New("error de E/S (sector dañado)")
	}
	return n, nil
}

func (d *faultyDisk) Close() error { return nil }

func newFaultyDevice(size int64, bad [][2]int64) (*Device, *faultyDisk) {
	rnd := rand.New(rand.NewSource(7))
	data := make([]byte, size)
	for i := int64(0); i < size; i += 4096 {
		if (i/4096)%5 != 0 { // uno de cada cinco bloques a cero (como un disco real)
			rnd.Read(data[i : i+4096])
		}
	}
	fd := &faultyDisk{data: data, bad: bad}
	return &Device{Path: "disco-simulado", Size: size, Align: 512, r: fd}, fd
}

func checkImage(t *testing.T, j *ImageJob, fd *faultyDisk) {
	t.Helper()
	img, err := os.ReadFile(j.ImgPath)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(img)) != j.Size {
		t.Fatalf("la imagen mide %d, el disco %d", len(img), j.Size)
	}
	isBad := func(p int64) bool {
		for _, b := range fd.bad {
			if p >= b[0] && p < b[1] {
				return true
			}
		}
		return false
	}
	for s := int64(0); s < j.Size; s += 512 {
		got := img[s : s+512]
		if isBad(s) {
			if !allZero(got) {
				t.Fatalf("sector dañado %d no está a cero en la imagen", s/512)
			}
		} else if !bytes.Equal(got, fd.data[s:s+512]) {
			t.Fatalf("sector %d distinto en la imagen", s/512)
		}
	}
	// El mapa debe marcar exactamente los sectores dañados.
	var badBytes int64
	for _, b := range fd.bad {
		badBytes += b[1] - b[0]
	}
	done, bad, pend := j.Totals()
	if bad != badBytes || pend != 0 || done+bad != j.Size {
		t.Errorf("mapa: copiado=%d ilegible=%d (esperado %d) pendiente=%d", done, bad, badBytes, pend)
	}
	for _, r := range j.BadRanges() {
		for p := r.pos; p < r.pos+r.size; p += 512 {
			if !isBad(p) {
				t.Errorf("el mapa marca como dañado el sector bueno %d", p/512)
			}
		}
	}
}

func TestImageBadSectors(t *testing.T) {
	const size = 24 << 20
	bad := [][2]int64{
		{5*512 + 3<<20, 6*512 + 3<<20},         // un sector suelto
		{10 << 20, 10<<20 + 2<<20},             // zona dañada de 2 MB
		{17<<20 + 4096, 17<<20 + 4096 + 3*512}, // tres sectores seguidos
		{size - 512, size},                     // el último sector
	}
	dev, fd := newFaultyDevice(size, bad)
	dir := t.TempDir()
	j, err := NewImageJob(dev, dir, "prueba")
	if err != nil {
		t.Fatal(err)
	}
	phases := map[int]bool{}
	if err := j.Run(func(p ImageProgress) { phases[p.Phase] = true }); err != nil {
		t.Fatal(err)
	}
	checkImage(t, j, fd)
	t.Logf("lecturas: %d, fases: %v, bloques en el mapa: %d", fd.reads, phases, len(j.blocks))
	// Disperso: los bloques a cero no deben ocupar espacio.
	if used := usedBytes(j.ImgPath); used >= size {
		t.Errorf("la imagen no es dispersa: ocupa %d bytes", used)
	}
	// El mapa guardado se puede volver a leer y dice que está completa.
	j2, err := NewImageJob(dev, dir, "prueba")
	if err != nil || !j2.Resumed || !j2.Complete() {
		t.Fatalf("al reabrir: err=%v reanudada=%v completa=%v", err, j2.Resumed, j2.Complete())
	}
}

func TestImageResume(t *testing.T) {
	const size = 16 << 20
	bad := [][2]int64{{3 << 20, 3<<20 + 1024}, {12 << 20, 12<<20 + 512}}
	dev, fd := newFaultyDevice(size, bad)
	dir := t.TempDir()
	j, _ := NewImageJob(dev, dir, "reanudar")
	fd.stopAfter = 7 // «Detener» a mitad de la copia
	err := j.Run(nil)
	atomic.StoreInt32(&stopFlag, 0)
	fd.stopAfter = 0
	if err != nil {
		t.Fatal(err)
	}
	if j.Complete() {
		t.Fatal("la imagen no debería estar completa tras detenerla")
	}
	_, _, pend := j.Totals()
	j2, err := NewImageJob(dev, dir, "reanudar")
	if err != nil || !j2.Resumed {
		t.Fatalf("no se reanudó: %v", err)
	}
	if _, _, p2 := j2.Totals(); p2 != pend {
		t.Errorf("el mapa reabierto no coincide: pendiente %d vs %d", p2, pend)
	}
	if err := j2.Run(nil); err != nil {
		t.Fatal(err)
	}
	checkImage(t, j2, fd)
}

func TestDdrescueMap(t *testing.T) {
	// Mapa escrito por GNU ddrescue (con un bloque «/» sin raspar).
	m := `# Mapfile. Created by GNU ddrescue version 1.27
# Command line: ddrescue /dev/sdb disco.img disco.map
# Start time:   2026-11-10 10:00:00
# current_pos  current_status  current_pass
0x00100000     ?               1
#      pos        size  status
0x00000000  0x00100000  +
0x00100000  0x00000200  -
0x00100200  0x00000E00  /
0x00101000  0x002FF000  ?
`
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "d.map"), []byte(m), 0o644)
	os.WriteFile(filepath.Join(dir, "d.img"), nil, 0o644)
	dev, _ := newFaultyDevice(4<<20, nil)
	j, err := NewImageJob(dev, dir, "d")
	if err != nil {
		t.Fatal(err)
	}
	done, bad, pend := j.Totals()
	if done != 1<<20 || bad != 512 || pend != 4<<20-1<<20-512 || !j.Resumed {
		t.Errorf("mapa de ddrescue mal leído: %d %d %d", done, bad, pend)
	}
	// Un mapa de otra unidad se rechaza.
	small, _ := newFaultyDevice(2<<20, nil)
	if _, err := NewImageJob(small, dir, "d"); err == nil {
		t.Error("debería rechazar un mapa de otro tamaño")
	}
}
