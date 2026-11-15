package main

// Session es el análisis de una unidad tal como lo usa el explorador de la
// interfaz gráfica: se ejecuta por etapas y va publicando resultados
// (primero los rápidos, luego los del análisis completo y por último los
// archivos sin nombre), que se muestran como un árbol de carpetas.

import (
	"bytes"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Etapas del análisis.
const (
	stagePrepare = iota // leyendo el sistema de archivos actual
	stageDeep           // análisis completo del disco
	stageAnalyze        // reconstruyendo carpetas
	stageVerify         // comprobando qué archivos conservan su contenido
	stageCarve          // analizando archivos sin nombre
	stageDone
)

// Eventos que la sesión comunica a la interfaz.
const (
	evProgress = iota
	evResults
	evDone
)

// SessionProgress describe el avance actual.
type SessionProgress struct {
	Stage       int
	Frac        float64 // 0..1 dentro de la etapa
	Pos, Total  int64
	Rate        float64 // bytes/s
	ETA         time.Duration
	Records     int // registros NTFS encontrados
	Cands       int // candidatos por firma
	Zeroed      int // archivos con el contenido en blanco
	Interrupted bool
}

// Session guarda todo lo encontrado en una unidad.
type Session struct {
	Source string
	Quick  bool
	Dev    *Device
	Scan   *Scan

	mu      sync.RWMutex // protege los registros mientras se reconstruye el árbol
	Plan    *Plan
	Carved  []CarvedFile
	Entries []*Entry
	Final   bool // ya terminó todo el análisis
	Err     error

	zcache map[zkey]bool // resultado de la comprobación de contenido
}

type zkey struct {
	rec   *FileRec
	delta int64
}

// NewSession abre la unidad (solo lectura).
func NewSession(source string, quick bool) (*Session, error) {
	dev, err := OpenDevice(source)
	if err != nil {
		return nil, err
	}
	return &Session{Source: source, Quick: quick, Dev: dev,
		Scan: &Scan{Dev: dev, Opts: ScanOpts{Quick: quick}}}, nil
}

func (ss *Session) Close() { ss.Dev.Close() }

// Run ejecuta todas las etapas. notify se llama desde este mismo
// goroutine; la interfaz se encarga de pasarlo a su hilo.
func (ss *Session) Run(notify func(ev int, p SessionProgress)) {
	s := ss.Scan
	p := SessionProgress{Stage: stagePrepare}
	notify(evProgress, p)
	last := time.Time{}
	s.Prepare(func() {
		if time.Since(last) > 300*time.Millisecond {
			last = time.Now()
			notify(evProgress, p)
		}
	})

	// Resultados rápidos: lo que dice el sistema de archivos actual.
	ss.mu.Lock()
	ss.analyze()
	ss.mu.Unlock()
	ss.publish(nil)
	notify(evResults, p)
	if ss.verify(&p, notify) {
		ss.publish(nil)
		notify(evResults, p)
	}

	if !ss.Quick && !stopRequested() {
		p.Stage = stageDeep
		p.Total = ss.Dev.Size
		start := time.Now()
		last = time.Time{}
		s.ScanDevice(func(pos int64) {
			if time.Since(last) < 250*time.Millisecond {
				return
			}
			last = time.Now()
			el := time.Since(start).Seconds()
			p.Pos = pos
			p.Frac = float64(pos) / float64(ss.Dev.Size)
			p.Rate = float64(pos) / (el + 0.001)
			p.ETA = time.Duration(float64(ss.Dev.Size-pos)/(p.Rate+1)) * time.Second
			p.Records = len(s.Recs)
			p.Cands = len(s.Cands)
			notify(evProgress, p)
		})
		if s.Interrupted {
			// «Detener»: se deja de leer el disco y se muestra lo encontrado.
			p.Interrupted = true
			atomic.StoreInt32(&stopFlag, 0)
		}
		p.Stage = stageAnalyze
		notify(evProgress, p)
		ss.mu.Lock()
		ss.analyze()
		ss.mu.Unlock()
		ss.publish(nil)
		notify(evResults, p)
		if ss.verify(&p, notify) {
			ss.publish(nil)
			notify(evResults, p)
		}

		// Archivos sin nombre (por su contenido).
		if len(s.Cands) > 0 {
			p.Stage = stageCarve
			p.Frac = 0
			notify(evProgress, p)
			rc := NewRecoverer(s, "", nil, Filter{Todo: true})
			rc.covered = ss.intactRuns()
			last = time.Time{}
			carved := rc.AnalyzeCarved(func(done, total, found int) {
				if time.Since(last) < 250*time.Millisecond && done < total {
					return
				}
				last = time.Now()
				p.Frac = float64(done) / float64(total+1)
				notify(evProgress, p)
			})
			if stopRequested() {
				p.Interrupted = true
				atomic.StoreInt32(&stopFlag, 0)
			}
			ss.publish(carved)
			notify(evResults, p)
		}
	}
	ss.Final = true
	p.Stage = stageDone
	p.Frac = 1
	notify(evDone, p)
}

// analyze reconstruye carpetas y estados con lo encontrado hasta ahora.
func (ss *Session) analyze() {
	s := ss.Scan
	s.attachExtensions()
	for _, r := range s.Recs { // las rutas pueden cambiar con registros nuevos
		r.resolved, r.path, r.rooted = false, "", false
	}
	s.detectGeometries(nil)
	ss.Plan = s.BuildPlan(Filter{Todo: true})
}

// verify comprueba, leyendo unas muestras de cada archivo con nombre, si
// su contenido sigue en el disco o está en blanco (lo que hacen los SSD
// con TRIM, o «limpiar la unidad» al restablecer Windows). Devuelve true
// si hubo cambios.
func (ss *Session) verify(p *SessionProgress, notify func(int, SessionProgress)) bool {
	if ss.zcache == nil {
		ss.zcache = map[zkey]bool{}
	}
	ss.mu.RLock()
	var todo []*Item
	for _, it := range ss.Plan.Items {
		r := it.Rec
		if it.System || r.has(fResident) || r.has(fCompressed) || r.Size == 0 || it.Overw >= it.Total {
			continue
		}
		todo = append(todo, it)
	}
	ss.mu.RUnlock()
	if len(todo) > 400000 {
		todo = todo[:400000]
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].Phys < todo[j].Phys })
	p.Stage, p.Frac = stageVerify, 0
	notify(evProgress, *p)
	buf := make([]byte, 4096)
	last := time.Now()
	changed := false
	zero := 0
	for i, it := range todo {
		if stopRequested() {
			break
		}
		g := ss.Scan.geomFor(it.Rec)
		k := zkey{it.Rec, g.Delta}
		z, ok := ss.zcache[k]
		if !ok {
			z = ss.isZeroed(it.Rec, g, buf)
			ss.zcache[k] = z
		}
		if it.Zeroed != z {
			it.Zeroed = z
			changed = true
		}
		if z {
			zero++
		}
		if time.Since(last) > 250*time.Millisecond {
			last = time.Now()
			p.Frac = float64(i) / float64(len(todo))
			p.Zeroed = zero
			notify(evProgress, *p)
		}
	}
	p.Zeroed = zero
	return changed
}

// isZeroed lee 4 KB al principio, a la mitad y al final del archivo: si
// las tres muestras están en blanco, el contenido ya no existe.
func (ss *Session) isZeroed(r *FileRec, g Geometry, buf []byte) bool {
	runs, _ := r.flatten()
	samples := []int64{0, r.Size / 2, max64(0, r.Size-4096)}
	read := 0
	for _, lo := range samples {
		lo -= lo % g.CS
		var vcnOff int64
		for _, ru := range runs {
			l := ru.Len * g.CS
			if lo >= vcnOff && lo < vcnOff+l {
				if ru.LCN >= 0 {
					off := g.Delta + ru.LCN*g.CS + (lo - vcnOff)
					n, _ := ss.Dev.ReadAt(buf, off)
					read++
					if n > 0 && !allZero(buf[:n]) {
						return false
					}
				}
				break
			}
			vcnOff += l
		}
	}
	return read > 0
}

// intactRuns devuelve las zonas ocupadas por archivos con nombre intactos,
// para no listarlos otra vez como archivos sin nombre.
func (ss *Session) intactRuns() []iv {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	var out []iv
	for _, it := range ss.Plan.Items {
		if it.Overw == 0 && !it.Rec.has(fResident) {
			out = appendRuns(out, it.Rec, ss.Scan.geomFor(it.Rec))
		}
	}
	return out
}

// publish crea la lista de entradas para el explorador (las casillas las
// conserva la interfaz por su clave). carved == nil mantiene los sin nombre
// anteriores.
func (ss *Session) publish(carved []CarvedFile) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if carved != nil {
		ss.Carved = carved
	}
	var list []*Entry
	for _, it := range ss.Plan.Items {
		e := &Entry{Item: it, Name: path.Base(it.Rel), Size: it.Rec.Size,
			MTime: ftToTime(it.Rec.MTime), System: it.System,
			Key: "n" + strconv.FormatInt(it.Rec.Off, 10)}
		rel := it.Rel
		switch {
		case strings.HasPrefix(rel, dirFiles+"/"):
			e.Group, rel = grpPath, strings.TrimPrefix(rel, dirFiles+"/")
		case strings.HasPrefix(rel, dirLoose+"/"):
			e.Group, rel = grpLoose, strings.TrimPrefix(rel, dirLoose+"/")
		}
		e.Dir = dirOf(rel)
		if it.Overw > 0 {
			e.State = esPartial
			e.Why = strconv.FormatInt(it.Overw*100/max64(it.Total, 1), 10) + "% sobrescrito"
		}
		if it.Zeroed {
			e.State, e.Why = esZeroed, "contenido en blanco"
		}
		list = append(list, e)
	}
	for i := range ss.Carved {
		cf := &ss.Carved[i]
		e := &Entry{Carve: cf, Name: cf.Name, Size: cf.Size, MTime: cf.MTime, Group: grpCarved,
			Dir: strings.ToUpper(cf.Ext), State: esCarved, Key: "c" + strconv.FormatInt(cf.Off, 10)}
		if cf.Doubt {
			e.State, e.Why = esCarvedDoubt, cf.Why
		}
		list = append(list, e)
	}
	for _, sk := range ss.Plan.Skipped {
		list = append(list, &Entry{Name: path.Base(sk.Rel), Dir: dirOf(sk.Rel), Size: sk.Size,
			MTime: ftToTime(sk.MTime), Group: grpLost, State: esLost, Why: sk.Reason,
			System: sk.System, Key: "l" + sk.Rel})
	}
	for _, e := range list {
		e.Ext = extOf(e.Name)
		e.Cat = category(e.Ext)
	}
	ss.Entries = list
}

func dirOf(rel string) string {
	d := path.Dir(rel)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// ReadEntry lee el contenido de una entrada (para la vista previa), como
// mucho limit bytes. truncated indica que el archivo es más grande.
func (ss *Session) ReadEntry(e *Entry, limit int64) (data []byte, truncated bool) {
	var buf bytes.Buffer
	k := &sink{w: &buf, limit: limit}
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	rc := &Recoverer{s: ss.Scan, buf: make([]byte, 1<<20)}
	switch {
	case e.Item != nil:
		rc.writeData(e.Item.Rec, ss.Scan.geomFor(e.Item.Rec), k)
	case e.Carve != nil:
		rc.copyRange(k, e.Carve.Off, min64(e.Carve.Size, limit))
	default:
		return nil, false
	}
	return buf.Bytes(), k.err == errLimit || (e.Carve != nil && e.Carve.Size > limit)
}

// RecoverResult resume una recuperación desde el explorador.
type RecoverResult struct {
	Good, Doubt, Empty, Errors, Already int64
	Bytes                               int64
	Full, Stopped                       bool
}

// Recover guarda las entradas elegidas en dest, respetando sus carpetas.
func (ss *Session) Recover(sel []*Entry, dest string, prog func(done, total int, bytes int64)) RecoverResult {
	rc := NewRecoverer(ss.Scan, dest, nil, Filter{Todo: true})
	var res RecoverResult
	var done int64
	for i, e := range sel {
		if stopRequested() || rc.Full {
			res.Stopped = stopRequested()
			break
		}
		if prog != nil {
			prog(i, len(sel), done)
		}
		ss.mu.RLock()
		switch {
		case e.Item != nil:
			rc.recoverItem(e.Item)
		case e.Carve != nil:
			rc.WriteCarvedOne(e.Carve)
		}
		ss.mu.RUnlock()
		done += e.Size
	}
	for _, d := range []string{dirFiles, dirLoose, dirDoubt, dirCarved} {
		removeEmptyDirs(dest + "/" + d)
	}
	res.Good = rc.NGood + rc.NCarved
	res.Doubt = rc.NDoubt + rc.NCarvedDoubt
	res.Empty, res.Errors, res.Already = rc.NEmpty, rc.NErr, rc.NAlready
	res.Bytes, res.Full = rc.Bytes, rc.Full
	if prog != nil {
		prog(len(sel), len(sel), done)
	}
	return res
}

// ---------------------------------------------------------------- entradas y árbol

// Grupos raíz del explorador.
const (
	grpPath   = iota // con su ruta original
	grpLoose         // carpetas cuya carpeta superior se perdió
	grpCarved        // sin nombre, encontrados por su contenido
	grpLost          // localizados pero sobrescritos
)

// Estados de una entrada.
const (
	esIntact      = iota // intacto
	esPartial            // parcialmente sobrescrito
	esCarved             // sin nombre, completo
	esCarvedDoubt        // sin nombre, quizá incompleto
	esZeroed             // el contenido está en blanco (lo vació el disco)
	esLost               // ya no se puede recuperar
)

// Entry es un archivo que se muestra en el explorador.
type Entry struct {
	Name    string
	Dir     string // carpeta dentro de su grupo
	Group   int
	Size    int64
	MTime   time.Time
	Ext     string
	Cat     string
	State   int
	Why     string
	System  bool
	Item    *Item
	Carve   *CarvedFile
	Key     string
	Checked bool
	nodes   []*Node
}

func (e *Entry) Checkable() bool { return e.State != esLost && e.State != esZeroed }

// StateText describe el estado para la columna «Estado».
func (e *Entry) StateText() string {
	switch e.State {
	case esIntact, esCarved:
		return "Bueno"
	case esPartial:
		return "Dañado en parte"
	case esCarvedDoubt:
		return "Puede estar incompleto"
	case esZeroed:
		return "En blanco"
	}
	return "No recuperable"
}

// StateDetail explica el estado con más detalle (panel de vista previa).
func (e *Entry) StateDetail() string {
	switch e.State {
	case esIntact:
		return "Bueno: se puede recuperar entero"
	case esCarved:
		return "Bueno (sin nombre original: encontrado por su contenido)"
	case esPartial:
		return "Dañado en parte: " + e.Why + " por archivos nuevos"
	case esCarvedDoubt:
		return "Puede estar incompleto: " + e.Why
	case esZeroed:
		return "En blanco: el disco ya vació su contenido (los SSD lo hacen con TRIM; también ocurre al " +
			"«limpiar la unidad» al restablecer Windows). Ningún programa puede recuperarlo."
	}
	return "No recuperable: " + e.Why
}

// Node es una carpeta del árbol.
type Node struct {
	Name     string
	Path     string
	Parent   *Node
	Kids     []*Node
	kid      map[string]*Node
	Files    []*Entry
	Count    int   // archivos (recursivo)
	Size     int64 // tamaño (recursivo)
	Sel      int   // archivos que se pueden marcar (recursivo)
	Checked  int   // archivos marcados (recursivo)
	Root     int
	Handle   uintptr // elemento del TreeView (lo usa la interfaz)
	Inserted bool    // sus subcarpetas ya están en el TreeView
}

// AllChecked indica si están marcados todos sus archivos.
func (n *Node) AllChecked() bool { return n.Sel > 0 && n.Checked == n.Sel }

func (n *Node) child(name string) *Node {
	if c := n.kid[strings.ToLower(name)]; c != nil {
		return c
	}
	if n.kid == nil {
		n.kid = map[string]*Node{}
	}
	c := &Node{Name: name, Parent: n, Root: n.Root}
	if n.Path == "" {
		c.Path = name
	} else {
		c.Path = n.Path + "/" + name
	}
	n.kid[strings.ToLower(name)] = c
	n.Kids = append(n.Kids, c)
	return c
}

// Tree organiza las entradas en carpetas.
type Tree struct {
	Roots       []*Node
	Entries     []*Entry // entradas visibles
	Checked     int
	CheckedSize int64
}

// Nombres de las raíces del árbol.
const (
	rootPath = iota
	rootLoose
	rootTypes
	rootCarved
	rootLost
	nRoots
)

var rootNames = [nRoots]string{
	"Con su ubicación original",
	"Carpetas sin ubicación",
	"Por tipo de archivo",
	"Sin nombre (por contenido)",
	"Encontrados pero no recuperables",
}

// BuildTree organiza las entradas. hideSystem oculta los archivos de
// Windows y de los programas.
func BuildTree(entries []*Entry, hideSystem bool) *Tree {
	t := &Tree{}
	for i := 0; i < nRoots; i++ {
		t.Roots = append(t.Roots, &Node{Name: rootNames[i], Path: rootNames[i], Root: i})
	}
	add := func(n *Node, e *Entry) {
		n.Files = append(n.Files, e)
		e.nodes = append(e.nodes, n)
		for p := n; p != nil; p = p.Parent {
			p.Count++
			p.Size += e.Size
			if e.Checkable() {
				p.Sel++
				if e.Checked {
					p.Checked++
				}
			}
		}
	}
	descend := func(n *Node, dir string) *Node {
		if dir == "" {
			return n
		}
		for _, c := range strings.Split(dir, "/") {
			n = n.child(c)
		}
		return n
	}
	for _, e := range entries {
		e.nodes = e.nodes[:0]
		if hideSystem && e.System {
			continue
		}
		t.Entries = append(t.Entries, e)
		if e.Checked {
			t.Checked++
			t.CheckedSize += e.Size
		}
		switch e.Group {
		case grpPath:
			add(descend(t.Roots[rootPath], e.Dir), e)
		case grpLoose:
			add(descend(t.Roots[rootLoose], e.Dir), e)
		case grpCarved:
			add(descend(t.Roots[rootCarved], e.Dir), e)
		case grpLost:
			add(descend(t.Roots[rootLost], e.Dir), e)
			continue // los no recuperables no van en «Por tipo»
		}
		ext := strings.ToUpper(e.Ext)
		if ext == "" {
			ext = "(sin extensión)"
		}
		add(t.Roots[rootTypes].child(e.Cat).child(ext), e)
	}
	for _, r := range t.Roots {
		sortNode(r)
	}
	return t
}

func sortNode(n *Node) {
	sort.Slice(n.Kids, func(i, j int) bool { return fold(n.Kids[i].Name) < fold(n.Kids[j].Name) })
	sort.Slice(n.Files, func(i, j int) bool { return fold(n.Files[i].Name) < fold(n.Files[j].Name) })
	for _, k := range n.Kids {
		sortNode(k)
	}
}

// SetChecked marca o desmarca un archivo y actualiza los contadores.
func (t *Tree) SetChecked(e *Entry, on bool) {
	if !e.Checkable() || e.Checked == on {
		return
	}
	e.Checked = on
	d := 1
	if !on {
		d = -1
	}
	t.Checked += d
	t.CheckedSize += int64(d) * e.Size
	for _, n := range e.nodes {
		for p := n; p != nil; p = p.Parent {
			p.Checked += d
		}
	}
}

// SetNodeChecked marca o desmarca todo lo que hay dentro de una carpeta.
func (t *Tree) SetNodeChecked(n *Node, on bool) {
	for _, e := range n.Files {
		t.SetChecked(e, on)
	}
	for _, k := range n.Kids {
		t.SetNodeChecked(k, on)
	}
}

// CheckedEntries devuelve los archivos marcados en orden de disco (más rápido).
func (t *Tree) CheckedEntries() []*Entry {
	var out []*Entry
	for _, e := range t.Entries {
		if e.Checked {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return entryPhys(out[i]) < entryPhys(out[j]) })
	return out
}

func entryPhys(e *Entry) int64 {
	switch {
	case e.Item != nil:
		return e.Item.Phys
	case e.Carve != nil:
		return e.Carve.Off
	}
	return 0
}

// Find localiza una carpeta por su ruta (la que guarda Node.Path).
func (t *Tree) Find(p string) *Node {
	for _, r := range t.Roots {
		if p == r.Path {
			return r
		}
		if !strings.HasPrefix(p, r.Path+"/") {
			continue
		}
		n := r
		for _, c := range strings.Split(strings.TrimPrefix(p, r.Path+"/"), "/") {
			if n = n.kid[strings.ToLower(c)]; n == nil {
				return nil
			}
		}
		return n
	}
	return nil
}

// Search devuelve las entradas visibles que coinciden con la búsqueda.
func (t *Tree) Search(m *Matcher) []*Entry {
	var out []*Entry
	for _, e := range t.Entries {
		if m.Match(e.Dir, e.Name) {
			out = append(out, e)
		}
	}
	return out
}
