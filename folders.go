package main

// Búsqueda de una carpeta concreta: la que el cliente necesita recuperar
// entera (p. ej. «Empresa» o «Contabilidad»), esté donde esté.
//
// Se buscan las carpetas por su nombre en las tres partes del árbol que
// conservan carpetas: con su ubicación original, carpetas sin ubicación y
// no recuperables. Si el nombre de la carpeta se perdió (aparece como
// «carpeta_123»), se encuentra por los archivos que tenía dentro: las
// carpetas que contienen archivos cuyo nombre coincide se muestran después.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// FolderHit es una carpeta encontrada.
type FolderHit struct {
	Node    *Node
	Score   int   // fsExact..fsSub por su nombre; fsNone = por los archivos que contiene
	Matches int   // (si Score == fsNone) archivos de la carpeta (sin subcarpetas) cuyo nombre coincide, incluidos los no recuperables
	Lost    *Node // la misma carpeta entre los no recuperables (archivos que ya no están)
}

// Límites para que una búsqueda muy general no llene la lista.
const (
	maxFolderHits  = 2000
	maxContentHits = 300
)

// relPath es la ruta de la carpeta dentro de su raíz ("" para la raíz).
func (n *Node) relPath() string {
	r := n
	for r.Parent != nil {
		r = r.Parent
	}
	if n == r {
		return ""
	}
	return strings.TrimPrefix(n.Path, r.Path+"/")
}

// Location es la ubicación de la carpeta, como la muestra la columna
// «Ubicación» de los archivos: \Users\Ana\Documents.
func (n *Node) Location() string {
	rel := n.relPath()
	if rel == "" {
		return ""
	}
	dir := ""
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		dir = rel[:i]
	}
	d := strings.ReplaceAll(dir, "/", `\`)
	switch n.Root {
	case rootLoose:
		return `(sin ubicación)\` + d
	case rootLost:
		return `(no recuperable)\` + d
	}
	return `\` + d
}

// Twin devuelve la misma carpeta entre los no recuperables, si la hay.
func (t *Tree) Twin(n *Node) *Node {
	if n.Root != rootPath && n.Root != rootLoose {
		return nil
	}
	rel := n.relPath()
	if rel == "" {
		return nil
	}
	base := t.Roots[rootLost].Path + "/"
	if tw := t.Find(base + rel); tw != nil {
		return tw
	}
	// Con varias particiones, la ruta lleva delante «Particion_N/».
	if i := strings.IndexByte(rel, '/'); i > 0 && strings.HasPrefix(rel, "Particion_") {
		return t.Find(base + rel[i+1:])
	}
	return nil
}

// liveTwin es lo contrario de Twin: para una carpeta de los no
// recuperables, la misma carpeta con los archivos que sí se recuperan.
func (t *Tree) liveTwin(n *Node) *Node {
	if n.Root != rootLost {
		return nil
	}
	rel := n.relPath()
	if rel == "" {
		return nil
	}
	for _, ri := range []int{rootPath, rootLoose} {
		if lt := t.Find(t.Roots[ri].Path + "/" + rel); lt != nil {
			return lt
		}
	}
	return nil
}

// walkNodes recorre la carpeta y todas sus subcarpetas.
func walkNodes(n *Node, f func(*Node)) {
	f(n)
	for _, k := range n.Kids {
		walkNodes(k, f)
	}
}

// FolderEntries devuelve los archivos de la carpeta y sus subcarpetas.
func FolderEntries(n *Node) []*Entry {
	var out []*Entry
	walkNodes(n, func(c *Node) { out = append(out, c.Files...) })
	return out
}

// FindFolders busca carpetas por su nombre y, después, las que contienen
// archivos cuyo nombre coincide (para las carpetas que perdieron el suyo).
func (t *Tree) FindFolders(m *Matcher) []FolderHit {
	var hits []FolderHit
	found := map[*Node]bool{} // carpetas ya incluidas (y sus gemelas no recuperables)
	for _, ri := range []int{rootPath, rootLoose, rootLost} {
		root := t.Roots[ri]
		for _, k := range root.Kids {
			walkNodes(k, func(n *Node) {
				if found[n] || syntheticDir(n.Name) {
					return
				}
				sc := m.FolderScore(n.relPath(), n.Name)
				if sc == fsNone {
					return
				}
				h := FolderHit{Node: n, Score: sc}
				if tw := t.Twin(n); tw != nil {
					h.Lost = tw
					found[tw] = true
				}
				found[n] = true
				hits = append(hits, h)
			})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if (a.Node.Root == rootLost) != (b.Node.Root == rootLost) {
			return b.Node.Root == rootLost // las recuperables primero
		}
		if a.Node.Count != b.Node.Count {
			return a.Node.Count > b.Node.Count
		}
		return fold(a.Node.Path) < fold(b.Node.Path)
	})
	if len(hits) > maxFolderHits {
		hits = hits[:maxFolderHits]
	}

	// Carpetas con archivos que coinciden, salvo las que ya están dentro
	// de una carpeta encontrada por su nombre.
	inside := func(n *Node) bool {
		for p := n; p != nil; p = p.Parent {
			if found[p] {
				return true
			}
		}
		return false
	}
	count := map[*Node]int{}
	var order []*Node
	for _, e := range t.Entries {
		if e.Group == grpCarved || !m.Match("", e.Name) {
			continue
		}
		var n *Node
		for _, c := range e.nodes {
			if c.Root == rootPath || c.Root == rootLoose || c.Root == rootLost {
				n = c
				break
			}
		}
		if lt := t.liveTwin(n); lt != nil {
			n = lt // se cuenta en la misma carpeta, junto a lo que sí se recupera
		}
		if n == nil || n.Parent == nil || inside(n) {
			continue
		}
		if count[n] == 0 {
			order = append(order, n)
		}
		count[n]++
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if count[a] != count[b] {
			return count[a] > count[b]
		}
		if (a.Root == rootLost) != (b.Root == rootLost) {
			return b.Root == rootLost
		}
		return fold(a.Path) < fold(b.Path)
	})
	if len(order) > maxContentHits {
		order = order[:maxContentHits]
	}
	for _, n := range order {
		h := FolderHit{Node: n, Matches: count[n]}
		if tw := t.Twin(n); tw != nil && !found[tw] {
			h.Lost = tw
		}
		hits = append(hits, h)
	}
	return hits
}

// FolderStats cuenta los archivos de una carpeta encontrada.
type FolderStats struct {
	Sum     Summary // todos sus archivos, incluidos los no recuperables
	Subdirs int
	Newest  time.Time
}

// Stats resume la carpeta (con sus subcarpetas) y su gemela no recuperable.
func (t *Tree) Stats(n *Node, lost *Node) FolderStats {
	var fs FolderStats
	es := FolderEntries(n)
	if lost != nil && lost != n {
		es = append(es, FolderEntries(lost)...)
	}
	fs.Sum = summarize(es)
	for _, e := range es {
		if e.MTime.After(fs.Newest) {
			fs.Newest = e.MTime
		}
	}
	walkNodes(n, func(c *Node) { fs.Subdirs++ })
	fs.Subdirs--
	return fs
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return groupDigits(int64(n)) + " " + many
}

// FolderReport describe una carpeta para el panel lateral.
func (t *Tree) FolderReport(n *Node, lost *Node) string {
	if lost == nil {
		lost = t.Twin(n)
	}
	st := t.Stats(n, lost)
	s := st.Sum
	var b strings.Builder
	loc := n.Location()
	if loc == "" {
		loc = n.Name
	} else {
		loc = strings.TrimSuffix(loc, `\`) + `\` + n.Name
	}
	fmt.Fprintf(&b, "Carpeta: %s\r\n", loc)
	ok := s.totalN() - s.Lost.N
	if n.Root == rootLost {
		fmt.Fprintf(&b, "%s archivos · %s\r\n", groupDigits(int64(s.Lost.N)), humanSize(s.Lost.Bytes))
		if st.Subdirs > 0 {
			b.WriteString(plural(st.Subdirs, "subcarpeta", "subcarpetas") + "\r\n")
		}
		b.WriteString("\r\nNinguno de estos archivos se puede recuperar: su espacio lo ocupan ya otros datos.")
		return b.String()
	}
	fmt.Fprintf(&b, "%s · %s", plural(ok, "archivo", "archivos"), humanSize(s.totalBytes()-s.Lost.Bytes))
	if st.Subdirs > 0 {
		b.WriteString(" · " + plural(st.Subdirs, "subcarpeta", "subcarpetas"))
	}
	b.WriteString("\r\n")
	if !st.Newest.IsZero() {
		fmt.Fprintf(&b, "Último cambio: %s\r\n", st.Newest.Local().Format("02/01/2006 15:04"))
	}
	b.WriteString("\r\n")
	if s.Checked > 0 {
		fmt.Fprintf(&b, "Comprobado correcto: %.0f %%\r\n", s.VerifiedPct())
		fmt.Fprintf(&b, "Sin señales de daño (con lo no comprobable): %.0f %%\r\n", s.GoodPct())
	} else {
		fmt.Fprintf(&b, "Sin señales de daño: %.0f %% de su tamaño (sin verificar a fondo)\r\n", s.GoodPct())
	}
	part := func(name string, g grp) {
		if g.N > 0 {
			fmt.Fprintf(&b, "  •  %s: %s (%s)\r\n", name, groupDigits(int64(g.N)), humanSize(g.Bytes))
		}
	}
	part("Dañados", s.Partial)
	part("En blanco", s.Blank)
	part("No recuperables", s.Lost)
	return strings.TrimSuffix(b.String(), "\r\n")
}

// CheckedFolders devuelve las carpetas marcadas enteras (las más altas),
// con su ubicación original o sin ella.
func (t *Tree) CheckedFolders() []*Node {
	var out []*Node
	for _, ri := range []int{rootPath, rootLoose} {
		for _, k := range t.Roots[ri].Kids {
			walkTop(k, &out)
		}
	}
	return out
}

func walkTop(n *Node, out *[]*Node) {
	if n.AllChecked() {
		// Una carpeta que solo contiene otra se describe por la de dentro
		// (si se marcó «Empresa» y su carpeta superior no tiene nada más).
		for len(n.Files) == 0 && len(n.Kids) == 1 {
			n = n.Kids[0]
		}
		*out = append(*out, n)
		return
	}
	if n.Checked == 0 {
		return
	}
	for _, k := range n.Kids {
		walkTop(k, out)
	}
}

// FoldersText describe las carpetas marcadas enteras (para el resumen y
// para resumen.txt); "" si no hay ninguna o son más de max.
func (t *Tree) FoldersText(title string, max int) string {
	fs := t.CheckedFolders()
	if len(fs) == 0 || len(fs) > max {
		return ""
	}
	var b strings.Builder
	b.WriteString(title + "\r\n\r\n")
	for _, n := range fs {
		b.WriteString(t.FolderReport(n, nil))
		b.WriteString("\r\n\r\n")
	}
	b.WriteString("(Los porcentajes de cada carpeta incluyen sus archivos no recuperables.)\r\n\r\n")
	return b.String()
}

// planDir devuelve la carpeta original de un archivo del plan (sin las
// carpetas que añade el programa al guardar) y si está «sin ubicación».
func planDir(rel string) (string, bool) {
	d, loose := dirOf(rel), false
	for _, top := range []string{dirFiles, dirLoose} {
		if d == top || strings.HasPrefix(d, top+"/") {
			loose = top == dirLoose
			d = strings.TrimPrefix(strings.TrimPrefix(d, top), "/")
			break
		}
	}
	if strings.HasPrefix(d, "Particion_") {
		if i := strings.IndexByte(d, '/'); i > 0 {
			d = d[i+1:]
		} else {
			d = ""
		}
	}
	return d, loose
}

// FolderCount resume lo encontrado de una carpeta buscada (línea de órdenes).
type FolderCount struct {
	Path                 string // como se muestra: \Users\Ana\Empresa
	Files, Partial, Lost int64
	Bytes, LostBytes     int64
	Newest               time.Time
}

// PlanFolders agrupa el plan por la carpeta buscada en la que está cada archivo.
func PlanFolders(p *Plan, m *Matcher) []FolderCount {
	idx := map[string]*FolderCount{}
	var out []*FolderCount
	get := func(dir string, loose bool) *FolderCount {
		f := m.FolderOf(dir)
		if f == "" {
			return nil
		}
		show := `\` + strings.ReplaceAll(f, "/", `\`)
		if loose || strings.HasPrefix(f, lostPrefix) {
			show = `(sin ubicación)` + show
		}
		k := strings.ToLower(show)
		if idx[k] == nil {
			idx[k] = &FolderCount{Path: show}
			out = append(out, idx[k])
		}
		return idx[k]
	}
	for _, it := range p.Items {
		dir, loose := planDir(it.Rel)
		if fc := get(dir, loose); fc != nil {
			fc.Files++
			fc.Bytes += it.Rec.Size
			if it.Overw > 0 {
				fc.Partial++
			}
			if t := ftToTime(it.Rec.MTime); t.After(fc.Newest) {
				fc.Newest = t
			}
		}
	}
	for _, sk := range p.Skipped {
		if fc := get(dirOf(sk.Rel), false); fc != nil {
			fc.Lost++
			fc.LostBytes += sk.Size
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Files > out[j].Files })
	res := make([]FolderCount, len(out))
	for i, f := range out {
		res[i] = *f
	}
	return res
}
