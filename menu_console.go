package main

// Menú de la consola: las tres herramientas en un solo programa.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// consoleMenu pregunta qué hacer: 1 recuperar, 2 reparar, 3 respaldar,
// 4 estado de los discos; 0 = salir.
func consoleMenu() int {
	switch appMode {
	case "recuperar":
		return 1
	case "reparar":
		return 2
	case "respaldo":
		return 3
	}
	fmt.Println("==============================================================")
	fmt.Printf("  HERRAMIENTAS DELIO %s\n", version)
	fmt.Println("==============================================================")
	fmt.Println()
	fmt.Println("¿Qué quieres hacer?")
	fmt.Println("  [1] Recuperar archivos borrados, de un disco formateado o de un equipo restaurado")
	fmt.Println("  [2] Reparar archivos dañados (y buscar copias sanas de ellos)")
	fmt.Println("  [3] Respaldar una carpeta en otro disco, guardando las versiones anteriores")
	fmt.Println("  [4] Ver el estado de salud de los discos")
	fmt.Println("  [0] Salir")
	for {
		a := ask("\nOpción: ")
		if a == "" || a == "0" {
			return 0
		}
		if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= 4 {
			fmt.Println()
			return n
		}
		fmt.Println("Escribe un número del 0 al 4.")
	}
}

func askPath(prompt string, mustExist bool) string {
	for {
		p := strings.Trim(ask(prompt), `" `)
		if p == "" {
			return ""
		}
		if _, err := os.Stat(p); err != nil && mustExist {
			fmt.Println("  No se encuentra esa carpeta.")
			continue
		}
		return p
	}
}

func consoleRepair(cfg *Config) int {
	fmt.Println("REPARAR ARCHIVOS DAÑADOS")
	fmt.Println("Se revisan los archivos de una carpeta (por ejemplo, lo que recuperaste), se arreglan los")
	fmt.Println("dañados con lo que se puede salvar y se buscan copias sanas. Los originales no se tocan.")
	fmt.Println()
	cfg.Repair = askPath("Carpeta a revisar: ", true)
	if cfg.Repair == "" {
		return 1
	}
	cfg.RepairIn = strings.Trim(ask("Otras carpetas donde buscar copias sanas (opcional, separadas por ;): "), `" `)
	return runRepairCLI(cfg)
}

func consoleBackup() int {
	fmt.Println("RESPALDO EN OTRO DISCO")
	fmt.Println("Copia una carpeta (la de la empresa, la de OneDrive…) a un disco externo. Lo que cambie o se")
	fmt.Println("borre después se guarda en «Versiones», por si un archivo se daña o lo cifra un virus.")
	fmt.Println()
	src := askPath("Carpeta a respaldar: ", true)
	if src == "" {
		return 1
	}
	ej := `E:\Respaldos`
	if runtime.GOOS != "windows" {
		ej = "/media/usb/Respaldos"
	}
	dest := askPath("Carpeta de respaldo en el otro disco (por ejemplo "+ej+"): ", false)
	if dest == "" {
		return 1
	}
	days := 90
	if a := ask("Días que se guardan las versiones anteriores [90]: "); a != "" {
		if n, err := strconv.Atoi(a); err == nil && n >= 0 {
			days = n
		}
	}
	code := runBackupCLI(src, dest, days, false)
	if code == 0 && runtime.GOOS == "windows" && askYesNo("¿Repetirlo automáticamente cada día?", true) {
		h := ask("¿A qué hora? [13:00]: ")
		if h == "" {
			h = "13:00"
		}
		j := BackupJob{Name: backupName(src), Src: src, Dest: dest, KeepDays: days, Time: h}
		if err := scheduleBackup(j); err != nil {
			fmt.Println("  No se pudo programar:", err)
			return 1
		}
		upsertBackup(j)
		fmt.Printf("  Programado cada día a las %s (si el equipo está apagado, se hace al encenderlo).\n", h)
	}
	return code
}

// runBackupCLI hace un respaldo desde la consola.
func runBackupCLI(src, dest string, days int, cloud bool) int {
	if dest == "" {
		fmt.Println("ERROR: falta la carpeta de respaldo (-destino)")
		return 1
	}
	j := BackupJob{Name: backupName(src), Src: src, Dest: dest, KeepDays: days, CloudFiles: cloud}
	for _, old := range loadBackups() { // se conserva la configuración guardada
		if strings.EqualFold(old.Name, j.Name) && sameDir(old.Src, src) {
			j.Time = old.Time
		}
	}
	fmt.Printf("Respaldando %s\n  en %s\n\n", src, j.Folder())
	last := time.Time{}
	res, err := RunBackup(j, func(done int, cur string) {
		if time.Since(last) > 200*time.Millisecond {
			last = time.Now()
			statusLine(fmt.Sprintf("  %d archivos revisados · %s", done, shorten(cur, 50)))
		}
	}, stopRequested)
	statusLine("")
	j.LastRun = time.Now()
	if err != nil {
		j.LastResult = "No se hizo: " + err.Error()
		upsertBackup(j)
		fmt.Println("ERROR:", err)
		return 1
	}
	j.LastOK, j.LastResult = j.LastRun, res.Text()
	upsertBackup(j)
	fmt.Println(res.Text())
	if res.Errors > 0 {
		fmt.Printf("Detalle de los errores en %s\n", filepath.Join(j.Folder(), "registro.txt"))
	}
	return 0
}
