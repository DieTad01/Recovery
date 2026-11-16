package main

import (
	"fmt"
	"sort"
	"strings"
)

// Item es un archivo perdido que se va a recuperar a partir de su registro NTFS.
type Item struct {
	Rec    *FileRec
	Rel    string // ruta relativa dentro de la carpeta de destino
	Phys   int64  // posición física (para leer el disco en orden)
	Total  int64  // bytes almacenados en clusters
	Overw  int64  // bytes en clusters ya reutilizados por el sistema actual
	Rooted bool
	System bool   // pertenece a Windows o a un programa instalado
	Zeroed bool   // su contenido en el disco está en blanco (SSD con TRIM)
	Bad    string // la comprobación rápida vio que el contenido no corresponde
}

// SkipRow es un archivo localizado que no se puede recuperar.
type SkipRow struct {
	Rel    string
	Size   int64
	MTime  int64
	Reason string
	System bool
}

// Filter decide qué archivos se recuperan.
type Filter struct {
	Todo   bool            // incluir también carpetas y archivos de sistema
	Exts   map[string]bool // nil = todas las extensiones
	Search *Matcher        // nil = sin búsqueda por nombre
	Folder *Matcher        // nil = cualquier carpeta; si no, solo lo que está dentro de esas carpetas
}

type catStat struct {
	N    int64
	Size int64
}

// Plan es la lista de archivos a recuperar por NTFS.
type Plan struct {
	Items     []*Item
	TotalSize int64
	ByCat     map[string]*catStat
	Skipped   []SkipRow
	Rooted    int64
	Loose     int64
	Intact    int64
	Partial   int64
	NSystem   int64 // omitidos por ser archivos de sistema
	NFiltered int64 // omitidos por el filtro de extensiones
	NNoMatch  int64 // no coinciden con la búsqueda
	NOverw    int64
	NNoData   int64
	covered   []iv // zonas de archivos intactos que no se recuperan (para no duplicarlos por firma)
}

const (
	dirFiles   = "Archivos"
	dirLoose   = "Carpetas_sueltas"
	dirDoubt   = "Dudosos"
	dirCarved  = "Por_firma"
	lostPrefix = "carpeta_"
)

// dirPath devuelve la ruta de una carpeta y si llega hasta la raíz.
func (s *Scan) dirPath(d *FileRec, depth int) (string, bool) {
	if d.resolved {
		return d.path, d.rooted
	}
	if d.RecNo == 5 {
		d.path, d.rooted, d.resolved = "", true, true
		return "", true
	}
	if d.Name == "" {
		// Carpeta cuyo nombre ya no existe: sus archivos se agrupan igualmente.
		d.path, d.rooted, d.resolved = fmt.Sprintf("%s%d", lostPrefix, d.RecNo), false, true
		return d.path, false
	}
	name := sanitizeName(d.Name)
	var base string
	var rooted bool
	switch {
	case d.ParentRec == 5:
		rooted = true
	case depth > 100:
		base = lostPrefix + "bucle"
	default:
		if p := s.findParent(d); p != nil {
			base, rooted = s.dirPath(p, depth+1)
		} else {
			base = fmt.Sprintf("%s%d", lostPrefix, d.ParentRec)
		}
	}
	if base != "" {
		name = base + "/" + name
	}
	d.path, d.rooted, d.resolved = name, rooted, true
	return name, rooted
}

// fileDir devuelve la carpeta donde estaba el archivo.
func (s *Scan) fileDir(r *FileRec) (string, bool) {
	switch {
	case r.Name == "" || r.ParentRec == noRec:
		return "sin_nombre", false
	case r.ParentRec == 5:
		return "", true
	}
	if p := s.findParent(r); p != nil {
		return s.dirPath(p, 0)
	}
	return fmt.Sprintf("%s%d", lostPrefix, r.ParentRec), false
}

var sysTop = map[string]bool{
	"windows": true, "program files": true, "program files (x86)": true, "programdata": true,
	"system volume information": true, "recovery": true, "$winreagent": true, "$windows.~bt": true,
	"$windows.~ws": true, "perflogs": true, "config.msi": true, "msocache": true, "$sysreset": true,
	"$getcurrent": true, "$extend": true,
}

var sysAny = map[string]bool{
	"appdata": true, "winsxs": true, "system32": true, "syswow64": true, "windowsapps": true,
	"driverstore": true, "softwaredistribution": true, "servicing": true, "microsoft.net": true,
	"systemapps": true, "installer": true,
}

var sysRootFiles = map[string]bool{
	"pagefile.sys": true, "hiberfil.sys": true, "swapfile.sys": true, "bootmgr": true, "bootnxt": true,
	"dumpstack.log.tmp": true, "bootsect.bak": true,
}

var sysExt = map[string]bool{}

func init() {
	for _, e := range strings.Fields("dll sys mui manifest cat mum pnf nls etl pf drv ocx cpl efi tlb winmd pri blf sdb msp msu admx adml mof ime rll regtrans-ms cdf-ms") {
		sysExt[e] = true
	}
}

// isSystemFile indica si el archivo pertenece a Windows o a programas
// instalados (no es un archivo personal).
func isSystemFile(dir string, rooted bool, name string) bool {
	comps := strings.Split(strings.ToLower(dir), "/")
	if rooted {
		if len(comps) > 0 && comps[0] == "windows.old" {
			comps = comps[1:]
		}
		if len(comps) == 0 || comps[0] == "" {
			return sysRootFiles[strings.ToLower(name)]
		}
		if sysTop[comps[0]] {
			return true
		}
	}
	for _, c := range comps {
		if sysAny[c] {
			return true
		}
	}
	return !rooted && sysExt[extOf(name)]
}

// measure calcula dónde están los datos del archivo y cuánto de ellos ha
// sido ya reutilizado por el sistema de archivos actual.
func (s *Scan) measure(r *FileRec, g Geometry, newer []iv) (total, overw, phys int64) {
	phys = -1
	runs, _ := r.flatten()
	remaining := r.Size
	comp := r.has(fCompressed)
	for _, ru := range runs {
		if remaining <= 0 && !comp {
			break
		}
		n := ru.Len * g.CS
		if !comp {
			if lim := (remaining + g.CS - 1) / g.CS * g.CS; n > lim {
				n = lim
			}
		}
		remaining -= ru.Len * g.CS
		if ru.LCN < 0 || n <= 0 {
			continue
		}
		off := g.Delta + ru.LCN*g.CS
		if phys < 0 {
			phys = off
		}
		total += n
		if off < 0 || off+n > s.Dev.Size {
			overw += n
			continue
		}
		var o int64
		if v := s.volAt(off); v != nil {
			c0 := (off - v.Start) / v.CS
			c1 := (off - v.Start + n + v.CS - 1) / v.CS
			o = v.countAlloc(c0, c1) * v.CS
		}
		// Un archivo del sistema anterior también está sobrescrito donde
		// después escribió un archivo del sistema actual (aunque ya se borrara).
		o += ivOverlapBytes(newer, off, off+n)
		if o > n {
			o = n
		}
		overw += o
	}
	return
}

// BuildPlan decide qué archivos perdidos se recuperan y con qué ruta.
func (s *Scan) BuildPlan(flt Filter) *Plan {
	p := &Plan{ByCat: map[string]*catStat{}}
	groupsUsed := map[int16]bool{}
	type pend struct {
		it   *Item
		dir  string
		name string
	}
	var pends []pend
	// Zonas escritas por archivos del sistema actual que ya fueron borrados
	// (p. ej. temporales de la instalación de Windows).
	var newer []iv
	for _, r := range s.Recs {
		if r.has(fLive) && !r.has(fInUse|fDir|fResident) && r.has(fHasData) && r.Size > 0 {
			newer = appendRuns(newer, r, s.geomFor(r))
		}
	}
	newer = mergeIvs(newer)
	for _, r := range s.Recs {
		if r.has(fDir) || !r.has(fHasData) || r.Size <= 0 || (r.has(fLive) && r.has(fInUse)) {
			continue
		}
		dir, rooted := s.fileDir(r)
		name := sanitizeName(r.Name)
		if r.Name == "" {
			name = fmt.Sprintf("sin_nombre_%d", r.RecNo)
		}
		g := s.geomFor(r)
		var total, overw, phys int64
		if r.has(fResident) {
			phys = r.Off
		} else {
			if r.has(fLive) {
				total, overw, phys = s.measure(r, g, nil)
			} else {
				total, overw, phys = s.measure(r, g, newer)
			}
		}
		intact := r.has(fResident) || (total > 0 && overw == 0)
		addCovered := func() {
			if !intact || r.has(fResident) || !r.has(fInUse) && !r.has(fLive) {
				return
			}
			p.covered = appendRuns(p.covered, r, g)
		}
		system := isSystemFile(dir, rooted, r.Name)
		if !flt.Todo && system {
			p.NSystem++
			addCovered()
			continue
		}
		ext := extOf(r.Name)
		if flt.Exts != nil && !flt.Exts[ext] {
			p.NFiltered++
			addCovered()
			continue
		}
		if flt.Search != nil && !flt.Search.Match(dir, r.Name) {
			p.NNoMatch++
			addCovered()
			continue
		}
		if flt.Folder != nil && !flt.Folder.InFolder(dir) {
			p.NNoMatch++
			addCovered()
			continue
		}
		rel := name
		if dir != "" {
			rel = dir + "/" + name
		}
		if !r.has(fResident) && total == 0 {
			p.NNoData++
			reason := "sin datos en el disco (archivo disperso)"
			if r.has(fReparse) {
				reason = "sin datos locales (archivo solo en la nube, p. ej. OneDrive)"
			}
			p.Skipped = append(p.Skipped, SkipRow{rel, r.Size, r.MTime, reason, system})
			continue
		}
		if !r.has(fResident) && overw >= total {
			p.NOverw++
			p.Skipped = append(p.Skipped, SkipRow{rel, r.Size, r.MTime, "sobrescrito por completo por archivos nuevos", system})
			continue
		}
		it := &Item{Rec: r, Phys: phys, Total: total, Overw: overw, Rooted: rooted, System: system}
		pends = append(pends, pend{it, dir, name})
		groupsUsed[r.Group] = true
	}
	// Rutas finales: carpeta superior, prefijo de partición y nombres únicos.
	multi := len(groupsUsed) > 1
	dirs := map[string]bool{}
	for i := range pends {
		pd := &pends[i]
		top := dirFiles
		if !pd.it.Rooted {
			top = dirLoose
		}
		if multi {
			top += fmt.Sprintf("/Particion_%d", pd.it.Rec.Group+1)
		}
		if pd.dir != "" {
			pd.dir = top + "/" + pd.dir
		} else {
			pd.dir = top
		}
		d := strings.ToLower(pd.dir)
		for d != "" && !dirs[d] {
			dirs[d] = true
			if j := strings.LastIndexByte(d, '/'); j >= 0 {
				d = d[:j]
			} else {
				d = ""
			}
		}
	}
	used := map[string]bool{}
	for _, pd := range pends {
		rel := pd.dir + "/" + pd.name
		k := strings.ToLower(rel)
		if used[k] || dirs[k] {
			base, ext := pd.name, ""
			if j := strings.LastIndexByte(pd.name, '.'); j > 0 {
				base, ext = pd.name[:j], pd.name[j:]
			}
			for n := 0; used[k] || dirs[k]; n++ {
				suffix := fmt.Sprintf(" (reg %d)", pd.it.Rec.RecNo)
				if n > 0 {
					suffix = fmt.Sprintf(" (reg %d-%d)", pd.it.Rec.RecNo, n)
				}
				rel = pd.dir + "/" + base + suffix + ext
				k = strings.ToLower(rel)
			}
		}
		used[k] = true
		it := pd.it
		it.Rel = rel
		p.Items = append(p.Items, it)
		p.TotalSize += it.Rec.Size
		if it.Rooted {
			p.Rooted++
		} else {
			p.Loose++
		}
		if it.Overw == 0 {
			p.Intact++
		} else {
			p.Partial++
		}
		cat := category(extOf(it.Rec.Name))
		cs := p.ByCat[cat]
		if cs == nil {
			cs = &catStat{}
			p.ByCat[cat] = cs
		}
		cs.N++
		cs.Size += it.Rec.Size
	}
	sort.SliceStable(p.Items, func(i, j int) bool { return p.Items[i].Phys < p.Items[j].Phys })
	return p
}

// --- intervalos cubiertos por archivos ya recuperados ------------------

type iv struct{ a, b int64 }

func appendRuns(dst []iv, r *FileRec, g Geometry) []iv {
	runs, _ := r.flatten()
	for _, ru := range runs {
		if ru.LCN >= 0 {
			off := g.Delta + ru.LCN*g.CS
			dst = append(dst, iv{off, off + ru.Len*g.CS})
		}
	}
	return dst
}

func mergeIvs(v []iv) []iv {
	if len(v) == 0 {
		return v
	}
	sort.Slice(v, func(i, j int) bool { return v[i].a < v[j].a })
	out := v[:1]
	for _, x := range v[1:] {
		last := &out[len(out)-1]
		if x.a <= last.b {
			if x.b > last.b {
				last.b = x.b
			}
		} else {
			out = append(out, x)
		}
	}
	return out
}

// ivContains indica si off cae dentro de algún intervalo (v ordenado y fusionado).
func ivContains(v []iv, off int64) bool {
	i := sort.Search(len(v), func(i int) bool { return v[i].b > off })
	return i < len(v) && v[i].a <= off
}

// ivOverlapBytes cuenta cuántos bytes de [a, b) caen dentro de los intervalos.
func ivOverlapBytes(v []iv, a, b int64) int64 {
	var n int64
	for i := sort.Search(len(v), func(i int) bool { return v[i].b > a }); i < len(v) && v[i].a < b; i++ {
		n += min64(v[i].b, b) - max64(v[i].a, a)
	}
	return n
}

// ivOverlaps indica si [a, b) toca algún intervalo.
func ivOverlaps(v []iv, a, b int64) bool {
	i := sort.Search(len(v), func(i int) bool { return v[i].b > a })
	return i < len(v) && v[i].a < b
}
