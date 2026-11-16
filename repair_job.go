package main

// Revisión de una carpeta completa: se comprueba cada archivo, se reparan
// los dañados, se buscan copias sanas y se escribe un informe. Los
// originales no se tocan: todo lo nuevo va a la carpeta de resultados.

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RepairJob es lo que hay que revisar.
type RepairJob struct {
	Src      string   // carpeta (o archivo) con los archivos a revisar
	Out      string   // carpeta donde se guarda lo reparado
	SearchIn []string // otras carpetas donde buscar copias sanas
	// OnRow, si no es nil, recibe cada archivo dañado en cuanto se revisa y
	// otra vez cuando se termina de tratar (para ir mostrándolos).
	OnRow func(RepairRow)
}

// Estados de un archivo revisado.
const (
	rsOK      = iota // sano (comprobado)
	rsUnknown        // no se puede comprobar
	rsDamaged        // dañado
	rsBlank          // en blanco: no queda nada
	rsError          // no se pudo leer
)

// RepairRow es una fila del informe.
type RepairRow struct {
	Rel      string
	Ext      string
	Size     int64
	State    int
	Problem  string
	Action   string   // qué se consiguió (resumen corto)
	Created  []string // archivos creados, relativos a la carpeta de resultados
	Complete bool     // se consiguió el archivo completo (reparado entero o copia sana)
	Notes    string
}

func (r RepairRow) StateText() string {
	switch r.State {
	case rsOK:
		return "Sano"
	case rsUnknown:
		return "No verificable"
	case rsDamaged:
		return "Dañado"
	case rsBlank:
		return "En blanco"
	}
	return "No se pudo leer"
}

// RepairSummary cuenta los resultados.
type RepairSummary struct {
	Files, OK, Unknown, Damaged, Blank, Errors int
	Complete, Partial, NoFix, Copies           int
	Stopped                                    bool
}

// RepairProgress informa del avance.
type RepairProgress struct {
	Phase       string
	Done, Total int
	Current     string
}

type srcFile struct {
	path, rel string
	size      int64
	mod       time.Time
}

// listFiles recorre una carpeta (sin entrar en skip).
func listFiles(root, skip string) []srcFile {
	var out []srcFile
	st, err := os.Stat(root)
	if err != nil {
		return nil
	}
	if !st.IsDir() {
		return []srcFile{{root, filepath.Base(root), st.Size(), st.ModTime()}}
	}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.IsDir() {
			if skip != "" && sameDir(p, skip) {
				return filepath.SkipDir
			}
			return nil
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, srcFile{p, rel, fi.Size(), fi.ModTime()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

func sameDir(a, b string) bool {
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// RunRepair revisa la carpeta y guarda los resultados.
func RunRepair(job RepairJob, prog func(RepairProgress), stopped func() bool) ([]RepairRow, RepairSummary, error) {
	var sum RepairSummary
	if err := os.MkdirAll(job.Out, 0o755); err != nil {
		return nil, sum, err
	}
	if prog == nil {
		prog = func(RepairProgress) {}
	}
	prog(RepairProgress{Phase: "Buscando archivos…"})
	files := listFiles(job.Src, job.Out)
	sum.Files = len(files)
	rows := make([]RepairRow, len(files))
	var damaged []int
	groups := map[string]bool{}

	// 1. Comprobar cada archivo.
	for i, f := range files {
		if stopped() {
			sum.Stopped = true
			rows = rows[:i]
			files = files[:i]
			break
		}
		prog(RepairProgress{Phase: "Revisando", Done: i, Total: len(files), Current: f.rel})
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.path), "."))
		r := RepairRow{Rel: f.rel, Ext: ext, Size: f.size}
		r.State, r.Problem = checkFile(f.path, f.size, ext)
		switch r.State {
		case rsDamaged, rsBlank:
			damaged = append(damaged, i)
			groups[matchGroup(ext)] = true
		}
		rows[i] = r
		if job.OnRow != nil && r.State != rsOK && r.State != rsUnknown {
			job.OnRow(r)
		}
	}

	// 2. Índice de posibles copias sanas (solo de los tipos dañados).
	ix := &copyIndex{}
	if len(damaged) > 0 && !sum.Stopped {
		prog(RepairProgress{Phase: "Buscando copias sanas…"})
		isDamaged := map[string]bool{}
		for _, i := range damaged {
			isDamaged[files[i].path] = true
		}
		seen := map[string]bool{}
		for _, dir := range append([]string{job.Src}, job.SearchIn...) {
			if strings.TrimSpace(dir) == "" {
				continue
			}
			for _, f := range listFiles(dir, job.Out) {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.path), "."))
				abs, _ := filepath.Abs(f.path)
				if !groups[matchGroup(ext)] || seen[abs] || stopped() {
					continue
				}
				seen[abs] = true
				k := newDocKey(f.path, f.size, f.mod)
				if isDamaged[f.path] {
					k.valid = vBad
				}
				ix.add(k)
			}
		}
	}

	// 3. Arreglar los dañados.
	for n, i := range damaged {
		if stopped() {
			sum.Stopped = true
			break
		}
		f := files[i]
		r := &rows[i]
		prog(RepairProgress{Phase: "Reparando", Done: n, Total: len(damaged), Current: f.rel})
		repairOne(job, f, r, ix)
		if job.OnRow != nil {
			job.OnRow(*r)
		}
	}

	for _, r := range rows {
		switch r.State {
		case rsOK:
			sum.OK++
		case rsUnknown:
			sum.Unknown++
		case rsDamaged:
			sum.Damaged++
		case rsBlank:
			sum.Blank++
		case rsError:
			sum.Errors++
		}
		if r.State == rsDamaged || r.State == rsBlank {
			switch {
			case r.Complete:
				sum.Complete++
			case len(r.Created) > 0:
				sum.Partial++
			default:
				sum.NoFix++
			}
			if strings.Contains(r.Action, "copia sana") {
				sum.Copies++
			}
		}
	}
	writeRepairReport(job, rows, sum)
	prog(RepairProgress{Phase: "Terminado", Done: len(damaged), Total: len(damaged)})
	return rows, sum, nil
}

// checkFile comprueba el contenido de un archivo.
func checkFile(path string, size int64, ext string) (int, string) {
	f, err := os.Open(path)
	if err != nil {
		return rsError, err.Error()
	}
	defer f.Close()
	if size == 0 {
		return rsUnknown, "archivo vacío"
	}
	if allBlank(f, size) {
		return rsBlank, "el contenido está en blanco (lo vació el disco): no queda nada que reparar"
	}
	if size > 2<<30 {
		return rsUnknown, "demasiado grande para comprobarlo"
	}
	v, why := validateContent(f, size, ext)
	switch v {
	case vOK, vStruct:
		return rsOK, why
	case vBad:
		return rsDamaged, why
	}
	return rsUnknown, why
}

// repairOne intenta arreglar un archivo y busca una copia sana.
func repairOne(job RepairJob, f srcFile, r *RepairRow, ix *copyIndex) {
	base := strings.TrimSuffix(filepath.Base(f.rel), filepath.Ext(f.rel))
	dir := filepath.Join(job.Out, filepath.Dir(f.rel))
	var notes, did []string
	save := func(tag, ext string, data []byte) {
		os.MkdirAll(dir, 0o755)
		p := uniquePath(filepath.Join(dir, base+" ("+tag+")."+ext))
		if err := os.WriteFile(p, data, 0o644); err != nil {
			notes = append(notes, "no se pudo guardar: "+err.Error())
			return
		}
		rel, _ := filepath.Rel(job.Out, p)
		r.Created = append(r.Created, rel)
	}
	// Copia sana en otro sitio.
	d := newDocKey(f.path, f.size, f.mod)
	if m := ix.find(d); m != nil {
		os.MkdirAll(dir, 0o755)
		ext := strings.TrimPrefix(filepath.Ext(m.key.path), ".")
		p := uniquePath(filepath.Join(dir, base+" (copia sana)."+ext))
		if err := copyFile(m.key.path, p); err == nil {
			rel, _ := filepath.Rel(job.Out, p)
			r.Created = append(r.Created, rel)
			did = append(did, "copia sana")
			notes = append(notes, fmt.Sprintf("copia sana en «%s»: %s, guardada el %s", m.key.path, m.why, m.key.mod.Local().Format("02/01/2006 15:04")))
			if m.level >= mtDocument {
				r.Complete = true
			}
		}
	}
	// Reparación.
	if r.State == rsDamaged && f.size <= 512<<20 {
		if data, err := os.ReadFile(f.path); err == nil {
			out := repairData(data, r.Ext)
			if out.Repaired != nil {
				save("reparado", out.RepairedExt, out.Repaired)
				if out.Complete {
					did = append(did, "reparado completo")
					r.Complete = true
				} else {
					did = append(did, "reparado en parte")
				}
			}
			if out.Rescue != nil {
				save(out.RescueTag, out.RescueExt, out.Rescue)
				did = append(did, out.RescueTag)
			}
			notes = append(notes, out.Notes...)
		}
	}
	if len(did) == 0 {
		r.Action = "sin arreglo posible"
	} else {
		r.Action = strings.Join(did, " · ")
	}
	r.Notes = strings.Join(notes, "; ")
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for n := 2; ; n++ {
		q := fmt.Sprintf("%s %d%s", base, n, ext)
		if _, err := os.Stat(q); os.IsNotExist(err) {
			return q
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if st, err := in.Stat(); err == nil {
		defer os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return out.Close()
}

// ---------------------------------------------------------------- informe

func writeRepairReport(job RepairJob, rows []RepairRow, sum RepairSummary) {
	f, err := os.Create(filepath.Join(job.Out, "informe_reparacion.csv"))
	if err == nil {
		f.WriteString("\xEF\xBB\xBF")
		w := csv.NewWriter(f)
		w.Comma = ';'
		w.UseCRLF = true
		w.Write([]string{"Archivo", "Estado", "Problema", "Qué se hizo", "Archivos creados", "Detalle"})
		for _, r := range rows {
			w.Write([]string{r.Rel, r.StateText(), r.Problem, r.Action, strings.Join(r.Created, " | "), r.Notes})
		}
		w.Flush()
		f.Close()
	}
	os.WriteFile(filepath.Join(job.Out, "resumen_reparacion.txt"), []byte("\xEF\xBB\xBF"+sum.Text(job)), 0o644)
}

// Text es el resumen para mostrar o guardar.
func (s RepairSummary) Text(job RepairJob) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REVISIÓN Y REPARACIÓN DE ARCHIVOS\r\nCarpeta revisada: %s\r\nResultados en: %s\r\n", job.Src, job.Out)
	if len(job.SearchIn) > 0 {
		fmt.Fprintf(&b, "Copias sanas buscadas también en: %s\r\n", strings.Join(job.SearchIn, "; "))
	}
	fmt.Fprintf(&b, "\r\nArchivos revisados: %s\r\n", groupDigits(int64(s.Files)))
	fmt.Fprintf(&b, "  •  Sanos: %s\r\n", groupDigits(int64(s.OK)))
	fmt.Fprintf(&b, "  •  Sin forma de comprobar: %s\r\n", groupDigits(int64(s.Unknown)))
	fmt.Fprintf(&b, "  •  Dañados: %s\r\n", groupDigits(int64(s.Damaged)))
	if s.Blank > 0 {
		fmt.Fprintf(&b, "  •  En blanco: %s\r\n", groupDigits(int64(s.Blank)))
	}
	if s.Errors > 0 {
		fmt.Fprintf(&b, "  •  No se pudieron leer: %s\r\n", groupDigits(int64(s.Errors)))
	}
	if s.Damaged+s.Blank > 0 {
		fmt.Fprintf(&b, "\r\nDe los dañados y en blanco:\r\n")
		fmt.Fprintf(&b, "  •  Recuperados completos (reparados enteros o con una copia sana): %s\r\n", groupDigits(int64(s.Complete)))
		fmt.Fprintf(&b, "  •  Recuperados en parte (lo que se salvó): %s\r\n", groupDigits(int64(s.Partial)))
		fmt.Fprintf(&b, "  •  Sin arreglo posible: %s\r\n", groupDigits(int64(s.NoFix)))
		fmt.Fprintf(&b, "  •  Copias sanas encontradas en otro sitio: %s\r\n", groupDigits(int64(s.Copies)))
	}
	if s.Stopped {
		b.WriteString("\r\nLa revisión se detuvo antes de terminar.\r\n")
	}
	b.WriteString("\r\nQué significa cada archivo creado:\r\n" +
		"  · «(reparado)»: el mismo documento rehecho con todo lo que se pudo salvar. Si le falta algo, el detalle\r\n" +
		"    del informe dice qué; las imágenes perdidas aparecen como un recuadro gris.\r\n" +
		"  · «(datos rescatados)» / «(texto rescatado)»: solo los valores o el texto, en un archivo sencillo que\r\n" +
		"    se abre siempre (por si el reparado no abriera en algún programa).\r\n" +
		"  · «(vista previa)»: la imagen pequeña que la cámara guardó dentro de la foto.\r\n" +
		"  · «(copia sana)»: el mismo archivo encontrado entero en otro sitio (otra versión o una copia).\r\n" +
		"    Compara su fecha: puede ser una versión anterior.\r\n" +
		"Los originales no se modificaron. El detalle de cada archivo está en informe_reparacion.csv.\r\n")
	return b.String()
}
