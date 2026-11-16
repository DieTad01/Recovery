//go:build windows

package main

// Respaldo en Windows: archivos de OneDrive que están solo en la nube y
// programación diaria con el Programador de tareas.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

// isCloudFile indica si el archivo está solo en la nube (OneDrive «a
// petición»): leerlo obligaría a descargarlo.
func isCloudFile(fi os.FileInfo) bool {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		a := d.FileAttributes
		return a&0x400000 != 0 || a&0x40000 != 0 || a&0x1000 != 0 // RECALL_ON_DATA_ACCESS, RECALL_ON_OPEN, OFFLINE
	}
	return false
}

func backupTaskName(name string) string { return `Delio\Respaldo - ` + name }

// installedExe copia el programa a una carpeta fija (la tarea programada no
// puede depender de que siga en Descargas o en una memoria USB).
func installedExe() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	base, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		return "", err
	}
	dst := filepath.Join(base, "Delio", "Herramientas", "delio-respaldo.exe")
	if sameDir(self, dst) {
		return dst, nil
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if _, err := copyKeepTime(self, dst); err != nil {
		if _, statErr := os.Stat(dst); statErr == nil {
			return dst, nil // ya estaba (quizá en uso por otro respaldo)
		}
		return "", err
	}
	return dst, nil
}

func runHidden(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// scheduleBackup programa el respaldo cada día a la hora indicada. Si el
// equipo está apagado a esa hora, se hace en cuanto se encienda.
func scheduleBackup(j BackupJob) error {
	hh, mm, ok := parseHour(j.Time)
	if !ok {
		return fmt.Errorf("la hora debe ser como 13:30")
	}
	exe, err := installedExe()
	if err != nil {
		return fmt.Errorf("no se pudo instalar el programa para el respaldo: %v", err)
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, time.Local).Format("2006-01-02T15:04:05")
	user := os.Getenv("USERNAME")
	if d := os.Getenv("USERDOMAIN"); d != "" {
		user = d + `\` + user
	}
	x := backupTaskXML(j, exe, user, start)
	u := utf16.Encode([]rune(x))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	f := filepath.Join(os.TempDir(), "delio-respaldo.xml")
	if err := os.WriteFile(f, b, 0o644); err != nil {
		return err
	}
	defer os.Remove(f)
	if out, err := runHidden("schtasks", "/Create", "/TN", backupTaskName(j.Name), "/XML", f, "/F"); err != nil {
		return fmt.Errorf("el Programador de tareas no aceptó la tarea: %s", strings.TrimSpace(out))
	}
	return nil
}

func unscheduleBackup(name string) error {
	if out, err := runHidden("schtasks", "/Delete", "/TN", backupTaskName(name), "/F"); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(out))
	}
	return nil
}

// backupScheduled indica si la tarea existe en el Programador de tareas.
func backupScheduled(name string) bool {
	_, err := runHidden("schtasks", "/Query", "/TN", backupTaskName(name))
	return err == nil
}
