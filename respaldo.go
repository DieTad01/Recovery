package main

// Respaldo automático: copia una carpeta (la de la empresa, la de OneDrive…)
// a un disco externo, guardando las versiones anteriores.
//
// En el destino queda:
//
//	<destino>\<nombre>\Actual\…                la copia al día, tal cual (para restaurar basta copiarla)
//	<destino>\<nombre>\Versiones\<fecha>\…     lo que se cambió o se borró en cada respaldo
//	<destino>\<nombre>\registro.txt             una línea por respaldo
//
// Así, aunque un virus cifre los archivos o alguien borre la carpeta, la
// versión buena sigue en «Versiones» (durante los días que se indiquen).
// OneDrive no sirve para esto por sí solo: sincroniza, así que copiaría el
// borrado o el cifrado a la nube.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupJob es un respaldo configurado.
type BackupJob struct {
	Name       string    `json:"nombre"`
	Src        string    `json:"origen"`
	Dest       string    `json:"destino"`
	KeepDays   int       `json:"dias_versiones"`    // 0 = guardar siempre
	CloudFiles bool      `json:"incluir_solo_nube"` // descargar los de OneDrive que están solo en la nube
	Time       string    `json:"hora"`              // «HH:MM» si está programado cada día
	LastRun    time.Time `json:"ultimo,omitempty"`  // último intento
	LastOK     time.Time `json:"ultimo_correcto,omitempty"`
	LastResult string    `json:"resultado,omitempty"`
}

// BackupResult resume un respaldo.
type BackupResult struct {
	Start, End                         time.Time
	Files, New, Updated, Deleted, Same int
	CloudOnly, Errors, Pruned          int
	Bytes                              int64
	ErrList                            []string
	Stopped                            bool
}

// Folder es la carpeta del respaldo dentro del destino.
func (j BackupJob) Folder() string { return filepath.Join(j.Dest, j.Name) }

const (
	bkCurrent  = "Actual"
	bkVersions = "Versiones"
	bkStamp    = "2006-01-02 15.04.05"
)

// Text es el resumen corto de un respaldo.
func (r BackupResult) Text() string {
	s := fmt.Sprintf("%s archivos: %s nuevos, %s cambiados, %s borrados en el origen (guardados en Versiones), %s iguales · %s copiados",
		groupDigits(int64(r.Files)), groupDigits(int64(r.New)), groupDigits(int64(r.Updated)), groupDigits(int64(r.Deleted)),
		groupDigits(int64(r.Same)), humanSize(r.Bytes))
	if r.CloudOnly > 0 {
		s += fmt.Sprintf(" · %s solo en la nube (no respaldados)", groupDigits(int64(r.CloudOnly)))
	}
	if r.Errors > 0 {
		s += fmt.Sprintf(" · %s con error", groupDigits(int64(r.Errors)))
	}
	if r.Stopped {
		s += " · detenido antes de terminar"
	}
	return s
}

// RunBackup hace un respaldo. prog recibe el avance (archivos revisados y
// el archivo actual).
func RunBackup(j BackupJob, prog func(done int, current string), stopped func() bool) (BackupResult, error) {
	res := BackupResult{Start: time.Now()}
	if prog == nil {
		prog = func(int, string) {}
	}
	if stopped == nil {
		stopped = func() bool { return false }
	}
	st, err := os.Stat(j.Src)
	if err != nil || !st.IsDir() {
		return res, fmt.Errorf("no se encuentra la carpeta a respaldar: %s", j.Src)
	}
	if vol := filepath.VolumeName(j.Dest); vol != "" {
		if _, err := os.Stat(vol + string(filepath.Separator)); err != nil {
			return res, fmt.Errorf("el disco de respaldo (%s) no está conectado", vol)
		}
	}
	if sameDir(j.Src, j.Dest) || isInside(j.Dest, j.Src) {
		return res, fmt.Errorf("el respaldo no puede guardarse dentro de la carpeta que se respalda")
	}
	cur := filepath.Join(j.Folder(), bkCurrent)
	if err := os.MkdirAll(cur, 0o755); err != nil {
		return res, fmt.Errorf("no se puede escribir en el disco de respaldo: %v", err)
	}
	stamp := res.Start.Format(bkStamp)
	verDir := filepath.Join(j.Folder(), bkVersions, stamp)
	keepOld := func(rel string) error {
		dst := filepath.Join(verDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.Rename(filepath.Join(cur, rel), dst)
	}

	// 1. Lo que hay en el origen.
	present := map[string]bool{} // rutas (en minúsculas) que existen en el origen
	var srcFiles []string
	filepath.Walk(j.Src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			if p != j.Src {
				res.Errors++
				res.ErrList = append(res.ErrList, p+": "+err.Error())
			}
			return nil
		}
		if fi.IsDir() || !fi.Mode().IsRegular() && !isCloudFile(fi) {
			return nil
		}
		rel, _ := filepath.Rel(j.Src, p)
		present[strings.ToLower(rel)] = true
		srcFiles = append(srcFiles, rel)
		return nil
	})
	// Protección: si el origen está vacío pero el respaldo no, es que la
	// carpeta no está disponible (o se borró entera): no se toca nada.
	if len(srcFiles) == 0 {
		if n := countFiles(cur); n > 0 {
			return res, fmt.Errorf("la carpeta a respaldar está vacía y el respaldo tiene %d archivos: no se cambia nada (¿se borró o no está disponible?)", n)
		}
	}

	// 2. Copiar lo nuevo y lo cambiado.
	for i, rel := range srcFiles {
		if stopped() {
			res.Stopped = true
			break
		}
		prog(i, rel)
		res.Files++
		sp := filepath.Join(j.Src, rel)
		si, err := os.Lstat(sp)
		if err != nil {
			res.Errors++
			res.ErrList = append(res.ErrList, rel+": "+err.Error())
			continue
		}
		dp := filepath.Join(cur, rel)
		di, derr := os.Stat(dp)
		if derr == nil && di.Size() == si.Size() && absDur(di.ModTime().Sub(si.ModTime())) <= 2*time.Second {
			res.Same++
			continue
		}
		if isCloudFile(si) && !j.CloudFiles {
			res.CloudOnly++ // se queda la copia anterior, si la había
			continue
		}
		if derr == nil {
			if err := keepOld(rel); err != nil {
				res.Errors++
				res.ErrList = append(res.ErrList, rel+": no se pudo guardar la versión anterior: "+err.Error())
				continue
			}
		}
		n, err := copyKeepTime(sp, dp)
		if err != nil {
			res.Errors++
			res.ErrList = append(res.ErrList, rel+": "+err.Error())
			if derr == nil { // se devuelve la versión anterior a su sitio
				os.Rename(filepath.Join(verDir, rel), dp)
			}
			continue
		}
		res.Bytes += n
		if derr == nil {
			res.Updated++
		} else {
			res.New++
		}
	}

	// 3. Lo que se borró del origen pasa a Versiones.
	if !res.Stopped {
		var gone []string
		filepath.Walk(cur, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(cur, p)
			if !present[strings.ToLower(rel)] {
				gone = append(gone, rel)
			}
			return nil
		})
		for _, rel := range gone {
			if err := keepOld(rel); err == nil {
				res.Deleted++
			}
		}
		removeEmptyDirs(cur)
	}

	// 4. Versiones más antiguas que los días indicados.
	if j.KeepDays > 0 {
		res.Pruned = pruneVersions(filepath.Join(j.Folder(), bkVersions), res.Start.AddDate(0, 0, -j.KeepDays))
	}
	res.End = time.Now()
	writeBackupLog(j, res)
	return res, nil
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func isInside(p, dir string) bool {
	a, _ := filepath.Abs(p)
	b, _ := filepath.Abs(dir)
	rel, err := filepath.Rel(b, a)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

func countFiles(dir string) int {
	n := 0
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// copyKeepTime copia un archivo (a un temporal que luego se renombra, para no
// dejar copias a medias) y le pone la fecha del original.
func copyKeepTime(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp := dst + ".copiando"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != st.Size() {
		err = fmt.Errorf("copia incompleta (el archivo cambió mientras se copiaba)")
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	os.Chtimes(tmp, st.ModTime(), st.ModTime())
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

// pruneVersions borra las carpetas de versiones anteriores a before.
func pruneVersions(dir string, before time.Time) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		t, err := time.ParseInLocation(bkStamp, e.Name(), time.Local)
		if err != nil || !e.IsDir() || !t.Before(before) {
			continue
		}
		if os.RemoveAll(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}

// Versions devuelve las fechas de las versiones guardadas (la más reciente primero).
func (j BackupJob) Versions() []string {
	ents, _ := os.ReadDir(filepath.Join(j.Folder(), bkVersions))
	var out []string
	for _, e := range ents {
		if _, err := time.Parse(bkStamp, e.Name()); err == nil && e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func writeBackupLog(j BackupJob, r BackupResult) {
	f, err := os.OpenFile(filepath.Join(j.Folder(), "registro.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  (%s)  %s\r\n", r.Start.Format("02/01/2006 15:04"), humanDur(r.End.Sub(r.Start)), r.Text())
	for i, e := range r.ErrList {
		if i == 20 {
			fmt.Fprintf(f, "      … y %d errores más\r\n", len(r.ErrList)-i)
			break
		}
		fmt.Fprintf(f, "      error: %s\r\n", e)
	}
	leeme := filepath.Join(j.Folder(), "LEEME - cómo restaurar.txt")
	if _, err := os.Stat(leeme); os.IsNotExist(err) {
		os.WriteFile(leeme, []byte("\xEF\xBB\xBF"+
			"RESPALDO DE «"+j.Name+"»\r\n"+
			"Origen: "+j.Src+"\r\n\r\n"+
			"  Actual\\      la copia más reciente, tal cual. Para restaurar, copia de aquí lo que necesites.\r\n"+
			"  Versiones\\   en cada fecha, los archivos que se cambiaron o se borraron ese día (la versión\r\n"+
			"                anterior). Si un archivo se dañó o lo cifró un virus, búscalo aquí en una fecha\r\n"+
			"                anterior al problema.\r\n"+
			"  registro.txt  qué se hizo en cada respaldo.\r\n\r\n"+
			"Hecho con Herramientas Delio · delioph.tech\r\n"), 0o644)
	}
}

// ---------------------------------------------------------------- configuración

// backupConfigFile es donde se guardan los respaldos configurados.
func backupConfigFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Delio", "respaldos.json")
}

func loadBackups() []BackupJob {
	var jobs []BackupJob
	if b, err := os.ReadFile(backupConfigFile()); err == nil {
		json.Unmarshal(b, &jobs)
	}
	return jobs
}

func saveBackups(jobs []BackupJob) error {
	f := backupConfigFile()
	if f == "" {
		return fmt.Errorf("no hay carpeta de configuración")
	}
	os.MkdirAll(filepath.Dir(f), 0o755)
	b, _ := json.MarshalIndent(jobs, "", "  ")
	return os.WriteFile(f, b, 0o644)
}

// upsertBackup guarda (o actualiza, por nombre) un respaldo.
func upsertBackup(j BackupJob) error {
	jobs := loadBackups()
	for i := range jobs {
		if strings.EqualFold(jobs[i].Name, j.Name) {
			jobs[i] = j
			return saveBackups(jobs)
		}
	}
	return saveBackups(append(jobs, j))
}

func removeBackup(name string) error {
	jobs := loadBackups()
	var out []BackupJob
	for _, j := range jobs {
		if !strings.EqualFold(j.Name, name) {
			out = append(out, j)
		}
	}
	return saveBackups(out)
}

// backupName propone un nombre para el respaldo a partir de la carpeta.
func backupName(src string) string {
	n := filepath.Base(filepath.Clean(src))
	n = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, n)
	if n == "" || n == "." || strings.HasSuffix(n, ":") || n == string(filepath.Separator) {
		n = "Respaldo"
	}
	return n
}

// runScheduledBackup hace el respaldo programado (lo lanza el Programador de
// tareas de Windows) y guarda el resultado en la configuración.
func runScheduledBackup(name string) int {
	for _, j := range loadBackups() {
		if !strings.EqualFold(j.Name, name) {
			continue
		}
		res, err := RunBackup(j, nil, nil)
		j.LastRun = time.Now()
		if err != nil {
			j.LastResult = "No se hizo: " + err.Error()
		} else {
			j.LastOK = j.LastRun
			j.LastResult = res.Text()
		}
		upsertBackup(j)
		if err != nil {
			return 1
		}
		return 0
	}
	return 2
}

// parseHour lee una hora «HH:MM».
func parseHour(s string) (int, int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// taskXML es la tarea del Programador de tareas de Windows.
const taskXML = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Respaldo de «%s» a %s (Herramientas Delio)</Description></RegistrationInfo>
  <Triggers>
    <CalendarTrigger>
      <StartBoundary>%s</StartBoundary>
      <Enabled>true</Enabled>
      <ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay>
    </CalendarTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <ExecutionTimeLimit>PT12H</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>`

// backupTaskXML arma la tarea diaria de un respaldo.
func backupTaskXML(j BackupJob, exe, user, start string) string {
	return fmt.Sprintf(taskXML, xmlEscape(j.Name), xmlEscape(j.Dest), start, xmlEscape(user), xmlEscape(exe),
		xmlEscape(`-respaldo-programado "`+j.Name+`"`))
}
