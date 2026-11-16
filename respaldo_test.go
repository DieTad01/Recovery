package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupVersions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Empresa")
	dest := filepath.Join(dir, "DiscoExterno")
	write := func(rel, body string, age time.Duration) {
		p := filepath.Join(src, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		tm := time.Now().Add(-age)
		os.Chtimes(p, tm, tm)
	}
	write("Facturas/F001.xml", "factura 1", time.Hour)
	write("Facturas/F002.xml", "factura 2", time.Hour)
	write("Nómina 2025.xlsx", "nomina v1", time.Hour)
	j := BackupJob{Name: "Empresa", Src: src, Dest: dest, KeepDays: 30}
	r, err := RunBackup(j, nil, nil)
	if err != nil || r.New != 3 || r.Files != 3 {
		t.Fatalf("primer respaldo: %+v %v", r, err)
	}
	cur := filepath.Join(dest, "Empresa", "Actual")
	if b, _ := os.ReadFile(filepath.Join(cur, "Facturas", "F001.xml")); string(b) != "factura 1" {
		t.Fatal("no se copió")
	}
	// Segundo respaldo: un archivo cambia (p. ej. cifrado por un virus), otro se borra.
	write("Nómina 2025.xlsx", "CIFRADO!!", 0)
	os.Remove(filepath.Join(src, "Facturas", "F002.xml"))
	write("Facturas/F003.xml", "factura 3", 0)
	time.Sleep(1100 * time.Millisecond) // otra carpeta de versiones
	r, err = RunBackup(j, nil, nil)
	if err != nil || r.New != 1 || r.Updated != 1 || r.Deleted != 1 || r.Same != 1 {
		t.Fatalf("segundo respaldo: %+v %v", r, err)
	}
	if b, _ := os.ReadFile(filepath.Join(cur, "Nómina 2025.xlsx")); string(b) != "CIFRADO!!" {
		t.Error("Actual debe tener la versión nueva")
	}
	vs := j.Versions()
	if len(vs) != 1 {
		t.Fatalf("versiones: %v", vs)
	}
	vdir := filepath.Join(dest, "Empresa", "Versiones", vs[0])
	if b, _ := os.ReadFile(filepath.Join(vdir, "Nómina 2025.xlsx")); string(b) != "nomina v1" {
		t.Error("la versión buena (antes del cifrado) debe quedar en Versiones")
	}
	if b, _ := os.ReadFile(filepath.Join(vdir, "Facturas", "F002.xml")); string(b) != "factura 2" {
		t.Error("lo borrado debe quedar en Versiones")
	}
	if _, err := os.Stat(filepath.Join(cur, "Facturas", "F002.xml")); err == nil {
		t.Error("lo borrado no debe seguir en Actual")
	}
	// Sin cambios: no se copia nada.
	r, _ = RunBackup(j, nil, nil)
	if r.New+r.Updated+r.Deleted != 0 || r.Same != 3 {
		t.Errorf("tercer respaldo sin cambios: %+v", r)
	}
	log, _ := os.ReadFile(filepath.Join(dest, "Empresa", "registro.txt"))
	if strings.Count(string(log), "\r\n") < 3 {
		t.Errorf("registro: %s", log)
	}
}

func TestBackupSafety(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Empresa")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	j := BackupJob{Name: "Empresa", Src: src, Dest: filepath.Join(dir, "R")}
	if _, err := RunBackup(j, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Carpeta vaciada (o no disponible): no se toca el respaldo.
	os.Remove(filepath.Join(src, "a.txt"))
	if _, err := RunBackup(j, nil, nil); err == nil || !strings.Contains(err.Error(), "vacía") {
		t.Errorf("debería negarse con el origen vacío: %v", err)
	}
	if _, err := os.Stat(filepath.Join(j.Folder(), "Actual", "a.txt")); err != nil {
		t.Error("el respaldo se tocó")
	}
	// Destino dentro del origen: no.
	if _, err := RunBackup(BackupJob{Name: "x", Src: dir, Dest: filepath.Join(dir, "R")}, nil, nil); err == nil {
		t.Error("no debe permitir respaldar dentro de la misma carpeta")
	}
	if _, err := RunBackup(BackupJob{Name: "x", Src: filepath.Join(dir, "no existe"), Dest: dir}, nil, nil); err == nil {
		t.Error("origen inexistente")
	}
}

func TestBackupPrune(t *testing.T) {
	dir := t.TempDir()
	v := filepath.Join(dir, "Versiones")
	old := time.Now().AddDate(0, 0, -40).Format(bkStamp)
	recent := time.Now().AddDate(0, 0, -5).Format(bkStamp)
	for _, d := range []string{old, recent, "otra carpeta"} {
		os.MkdirAll(filepath.Join(v, d), 0o755)
	}
	if n := pruneVersions(v, time.Now().AddDate(0, 0, -30)); n != 1 {
		t.Errorf("borradas %d", n)
	}
	for _, d := range []string{recent, "otra carpeta"} {
		if _, err := os.Stat(filepath.Join(v, d)); err != nil {
			t.Errorf("no debía borrarse %s", d)
		}
	}
	if h, m, ok := parseHour("7:05"); !ok || h != 7 || m != 5 {
		t.Error("parseHour")
	}
	if _, _, ok := parseHour("25:00"); ok {
		t.Error("hora imposible")
	}
	if backupName(`C:\Users\Ana\Documents\Empresa`) == "" {
		t.Error("nombre")
	}
}

func TestBackupTaskXML(t *testing.T) {
	j := BackupJob{Name: "Empresa & Cía", Dest: `E:\Respaldos`, Time: "13:00"}
	x := backupTaskXML(j, `C:\Users\Ana\AppData\Local\Delio\Herramientas\delio-respaldo.exe`, `PC\Ana`, "2026-11-16T13:00:00")
	x = strings.Replace(x, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	if !wellFormed([]byte(x)) {
		t.Fatalf("XML mal formado:\n%s", x)
	}
	for _, s := range []string{"<StartWhenAvailable>true</StartWhenAvailable>", "<DaysInterval>1</DaysInterval>",
		"-respaldo-programado &#34;Empresa &amp; Cía&#34;", "<LogonType>InteractiveToken</LogonType>"} {
		if !strings.Contains(x, s) {
			t.Errorf("falta %q", s)
		}
	}
}
