package main

// Imagen de disco: copia la unidad completa a un archivo, tolerando
// sectores dañados y pudiendo continuar si se interrumpe. Después se
// recupera desde la imagen, sin volver a tocar el disco del cliente.
//
// Funciona como GNU ddrescue y usa su formato de «mapa» (.map):
//   1. Copia en bloques grandes; si una zona falla, la salta y sigue.
//   2. Vuelve a las zonas saltadas, en bloques más pequeños.
//   3. Lee sector a sector los bloques que fallaron.
// Las zonas ilegibles quedan a cero en la imagen y marcadas en el mapa.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Estados del mapa (los mismos caracteres que GNU ddrescue).
const (
	mapNonTried   = '?' // aún sin leer
	mapNonScraped = '*' // falló al leer en bloque: falta leer sector a sector
	mapBad        = '-' // sector ilegible
	mapFinished   = '+' // copiado
)

const (
	imgChunk     = 1 << 20  // fase 1
	imgChunk2    = 64 << 10 // fase 2
	imgMaxSkip   = 64 << 20 // salto máximo tras errores seguidos
	imgSaveEvery = 5 * time.Second
)

type mblock struct {
	pos, size int64
	st        byte
}

// ImageProgress describe el avance de la imagen.
type ImageProgress struct {
	Phase   int // 1 copiando, 2 reintentando, 3 sector a sector, 4 terminado
	Pos     int64
	Size    int64
	Done    int64 // bytes copiados
	Bad     int64 // bytes ilegibles
	Pending int64 // bytes aún por leer o reintentar
	Rate    float64
	ETA     time.Duration
	Stopped bool
}

// ImageJob es una imagen en curso (o por continuar).
type ImageJob struct {
	Dev      *Device
	ImgPath  string
	MapPath  string
	Size     int64
	Resumed  bool // continúa una imagen anterior
	sector   int64
	blocks   []mblock
	out      *os.File
	lastSave time.Time
	moved    int64 // bytes leídos en esta sesión (para la velocidad)
	start    time.Time
	prog     func(ImageProgress)
	lastProg time.Time
	phase    int
	pos      int64
	inRun    bool // hay un tramo copiado aún sin anotar en el mapa
	runStart int64
}

// flushRun anota en el mapa lo copiado del tramo actual.
func (j *ImageJob) flushRun() {
	if j.inRun && j.pos > j.runStart {
		j.setStatus(j.runStart, j.pos-j.runStart, mapFinished)
		j.runStart = j.pos
	}
}

var errImgStopped = errors.New("imagen detenida")

// imageBaseName propone el nombre del archivo de imagen para una unidad.
func imageBaseName(label string) string {
	return sanitizeName("Imagen - " + label)
}

// NewImageJob prepara la imagen de dev en dir. Si ya existe un mapa de
// una imagen anterior de la misma unidad, la continúa.
func NewImageJob(dev *Device, dir, base string) (*ImageJob, error) {
	j := &ImageJob{Dev: dev, Size: dev.Size,
		ImgPath: filepath.Join(dir, base+".img"), MapPath: filepath.Join(dir, base+".map")}
	j.sector = int64(dev.Align)
	if j.sector < 512 {
		j.sector = 512
	}
	if _, err := os.Stat(j.MapPath); err == nil {
		if err := j.loadMap(); err != nil {
			return nil, fmt.Errorf("el mapa %s no es válido: %v", j.MapPath, err)
		}
		if _, err := os.Stat(j.ImgPath); err == nil {
			j.Resumed = true
		} else {
			j.blocks = []mblock{{0, j.Size, mapNonTried}}
		}
	} else {
		j.blocks = []mblock{{0, j.Size, mapNonTried}}
	}
	return j, nil
}

// Complete indica si ya no queda nada por leer.
func (j *ImageJob) Complete() bool {
	for _, b := range j.blocks {
		if b.st == mapNonTried || b.st == mapNonScraped {
			return false
		}
	}
	return true
}

// Totals devuelve bytes copiados, ilegibles y pendientes.
func (j *ImageJob) Totals() (done, bad, pending int64) {
	for _, b := range j.blocks {
		switch b.st {
		case mapFinished:
			done += b.size
		case mapBad:
			bad += b.size
		default:
			pending += b.size
		}
	}
	return
}

// BadRanges devuelve las zonas ilegibles (para informar al usuario).
func (j *ImageJob) BadRanges() []mblock {
	var out []mblock
	for _, b := range j.blocks {
		if b.st == mapBad {
			out = append(out, b)
		}
	}
	return out
}

// Run hace (o continúa) la imagen. Se puede detener con stopFlag y
// continuar más tarde con otro NewImageJob sobre la misma carpeta.
func (j *ImageJob) Run(prog func(ImageProgress)) error {
	j.prog = prog
	j.start = time.Now()
	f, err := os.OpenFile(j.ImgPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	j.out = f
	defer f.Close()
	if !j.Resumed {
		setSparse(f) // las zonas vacías no ocuparán espacio
	}
	if err := j.saveMap(); err != nil {
		return err
	}
	buf := make([]byte, imgChunk)
	steps := []func([]byte) error{j.phaseCopy, j.phaseRetry, j.phaseScrape}
	for i, step := range steps {
		j.phase = i + 1
		if err := step(buf); err != nil {
			j.saveMap()
			if err == errImgStopped {
				j.report(true)
				return nil
			}
			return err
		}
	}
	j.phase = 4
	if err := f.Truncate(j.Size); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	j.report(true)
	return j.saveMap()
}

// ranges devuelve una copia de las zonas con ese estado.
func (j *ImageJob) ranges(st byte) [][2]int64 {
	var out [][2]int64
	for _, b := range j.blocks {
		if b.st == st {
			out = append(out, [2]int64{b.pos, b.size})
		}
	}
	return out
}

// readAt lee exactamente len(p) bytes del disco; error si no puede.
func (j *ImageJob) readAt(p []byte, off int64) error {
	n, err := j.Dev.r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// store escribe los datos leídos (las zonas a cero no se escriben: el
// archivo de imagen es disperso y ya las contiene como ceros).
func (j *ImageJob) store(p []byte, off int64) error {
	j.moved += int64(len(p))
	if allZero(p) {
		return nil
	}
	if _, err := j.out.WriteAt(p, off); err != nil {
		return fmt.Errorf("no se pudo escribir la imagen: %w", err)
	}
	return nil
}

// tick guarda el mapa de vez en cuando e informa del avance.
func (j *ImageJob) tick() error {
	j.flushRun()
	if stopRequested() {
		return errImgStopped
	}
	if time.Since(j.lastSave) > imgSaveEvery {
		if err := j.saveMap(); err != nil {
			return err
		}
	}
	j.report(false)
	return nil
}

func (j *ImageJob) report(force bool) {
	if j.prog == nil || (!force && time.Since(j.lastProg) < 250*time.Millisecond) {
		return
	}
	j.lastProg = time.Now()
	done, bad, pend := j.Totals()
	p := ImageProgress{Phase: j.phase, Pos: j.pos, Size: j.Size, Done: done, Bad: bad, Pending: pend,
		Stopped: stopRequested()}
	if el := time.Since(j.start).Seconds(); el > 1 {
		p.Rate = float64(j.moved) / el
		if p.Rate > 0 {
			p.ETA = time.Duration(float64(pend)/p.Rate) * time.Second
		}
	}
	j.prog(p)
}

// phaseCopy: lectura rápida en bloques grandes; ante un error salta
// hacia delante (cada vez más) para no castigar una zona dañada.
func (j *ImageJob) phaseCopy(buf []byte) error {
	defer func() { j.flushRun(); j.inRun = false }()
	for _, r := range j.ranges(mapNonTried) {
		pos, end := r[0], r[0]+r[1]
		j.pos, j.runStart, j.inRun = pos, pos, true
		skip := int64(imgChunk)
		for pos < end {
			n := min64(imgChunk, end-pos)
			if err := j.readAt(buf[:n], pos); err != nil {
				j.flushRun()
				j.setStatus(pos, n, mapNonScraped)
				next := min64(pos+n+skip, end) // la zona saltada queda «?» para la fase 2
				skip = min64(skip*2, imgMaxSkip)
				pos = next
				j.pos, j.runStart = pos, pos
			} else {
				if err := j.store(buf[:n], pos); err != nil {
					return err
				}
				pos += n
				j.pos = pos
				skip = imgChunk
			}
			if err := j.tick(); err != nil {
				return err
			}
		}
		j.flushRun()
	}
	return nil
}

// phaseRetry: las zonas que se saltaron, en bloques pequeños y sin saltar.
func (j *ImageJob) phaseRetry(buf []byte) error {
	for _, r := range j.ranges(mapNonTried) {
		for pos, end := r[0], r[0]+r[1]; pos < end; {
			n := min64(imgChunk2, end-pos)
			j.pos = pos
			if err := j.readAt(buf[:n], pos); err != nil {
				j.setStatus(pos, n, mapNonScraped)
			} else {
				if err := j.store(buf[:n], pos); err != nil {
					return err
				}
				j.setStatus(pos, n, mapFinished)
			}
			pos += n
			if err := j.tick(); err != nil {
				return err
			}
		}
	}
	return nil
}

// phaseScrape: sector a sector dentro de los bloques que fallaron.
func (j *ImageJob) phaseScrape(buf []byte) error {
	for _, r := range j.ranges(mapNonScraped) {
		for pos, end := r[0], r[0]+r[1]; pos < end; {
			n := min64(j.sector, end-pos)
			j.pos = pos
			if err := j.readAt(buf[:n], pos); err != nil {
				j.setStatus(pos, n, mapBad)
			} else {
				if err := j.store(buf[:n], pos); err != nil {
					return err
				}
				j.setStatus(pos, n, mapFinished)
			}
			pos += n
			if err := j.tick(); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- mapa

// find devuelve el índice del bloque que contiene p.
func (j *ImageJob) find(p int64) int {
	return sort.Search(len(j.blocks), func(i int) bool { return j.blocks[i].pos+j.blocks[i].size > p })
}

func (j *ImageJob) splitAt(p int64) {
	i := j.find(p)
	if i >= len(j.blocks) || j.blocks[i].pos == p {
		return
	}
	b := j.blocks[i]
	left := mblock{b.pos, p - b.pos, b.st}
	right := mblock{p, b.pos + b.size - p, b.st}
	j.blocks = append(j.blocks, mblock{})
	copy(j.blocks[i+2:], j.blocks[i+1:])
	j.blocks[i], j.blocks[i+1] = left, right
}

// setStatus marca [pos, pos+size) con el estado st y une los bloques
// vecinos que queden con el mismo estado.
func (j *ImageJob) setStatus(pos, size int64, st byte) {
	if size <= 0 {
		return
	}
	end := pos + size
	j.splitAt(pos)
	j.splitAt(end)
	i0 := j.find(pos)
	i1 := i0
	for i1 < len(j.blocks) && j.blocks[i1].pos < end {
		j.blocks[i1].st = st
		i1++
	}
	lo, hi := max(i0-1, 0), min(i1+1, len(j.blocks))
	merged := make([]mblock, 0, hi-lo)
	for _, b := range j.blocks[lo:hi] {
		if n := len(merged); n > 0 && merged[n-1].st == b.st {
			merged[n-1].size += b.size
		} else {
			merged = append(merged, b)
		}
	}
	j.blocks = append(j.blocks[:lo], append(merged, j.blocks[hi:]...)...)
}

// saveMap escribe el mapa en el formato de GNU ddrescue.
func (j *ImageJob) saveMap() error {
	j.lastSave = time.Now()
	tmp := j.MapPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "# Mapfile. Created by Recovery %s\n", version)
	fmt.Fprintf(w, "# Origen: %s\n", j.Dev.Path)
	fmt.Fprintf(w, "# current_pos  current_status  current_pass\n")
	st := byte('?')
	if j.phase == 4 {
		st = '+'
	}
	fmt.Fprintf(w, "0x%08X     %c               %d\n", j.pos, st, max(j.phase, 1))
	fmt.Fprintf(w, "#      pos        size  status\n")
	for _, b := range j.blocks {
		fmt.Fprintf(w, "0x%08X  0x%08X  %c\n", b.pos, b.size, b.st)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	os.Remove(j.MapPath)
	return os.Rename(tmp, j.MapPath)
}

// loadMap lee un mapa (propio o de GNU ddrescue).
func (j *ImageJob) loadMap() error {
	data, err := os.ReadFile(j.MapPath)
	if err != nil {
		return err
	}
	var blocks []mblock
	first := true
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if first { // línea de estado actual
			first = false
			continue
		}
		if len(f) < 3 || len(f[2]) != 1 {
			return fmt.Errorf("línea no válida: %q", line)
		}
		pos, err1 := strconv.ParseInt(f[0], 0, 64)
		size, err2 := strconv.ParseInt(f[1], 0, 64)
		if err1 != nil || err2 != nil || size <= 0 {
			return fmt.Errorf("línea no válida: %q", line)
		}
		st := f[2][0]
		if st == '/' { // «sin raspar» de ddrescue: equivale a «*»
			st = mapNonScraped
		}
		if !strings.ContainsRune("?*-+", rune(st)) {
			return fmt.Errorf("estado desconocido %q", f[2])
		}
		blocks = append(blocks, mblock{pos, size, st})
	}
	var pos int64
	for _, b := range blocks {
		if b.pos != pos {
			return fmt.Errorf("el mapa tiene huecos o solapes en 0x%X", b.pos)
		}
		pos += b.size
	}
	if pos != j.Size {
		return fmt.Errorf("el mapa es de una unidad de otro tamaño (%s, esta mide %s)", humanSize(pos), humanSize(j.Size))
	}
	j.blocks = blocks
	return nil
}
