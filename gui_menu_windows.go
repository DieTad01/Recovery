//go:build windows

package main

// Menú de Herramientas Delio (Recuperar · Reparar · Respaldo) y pantalla
// «Respaldo automático».

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// ---------------------------------------------------------------- menú

var menu struct {
	ctls                   []syscall.Handle
	hTitle, hSub, hStatus  syscall.Handle
	hRecover, hRepair, hBk syscall.Handle
	hDisk, hProgs, hNight  syscall.Handle
	hVer                   syscall.Handle
}

func buildMenuPage() {
	menu.hTitle = ctl("STATIC", "Herramientas", 0, 0, 0, fTitle)
	menu.hSub = ctl("STATIC", "Recupera, repara y protege los archivos de un equipo. Los discos que se analizan solo se leen: nunca se escribe en ellos.", 0, 0, 0, fSub)
	app.muted[menu.hSub] = true
	menu.hRecover = button("Recuperar archivos\nEncuentra archivos borrados, de discos formateados o de equipos restaurados, con su nombre y su carpeta. Ves lo encontrado antes de recuperarlo.",
		wsTabstop, idMenuRecover, btnCard, fBody)
	menu.hRepair = button("Reparar archivos dañados\nRevisa lo recuperado, arregla los archivos dañados con todo lo que se puede salvar y busca copias sanas en otras carpetas.",
		wsTabstop, idMenuRepair, btnCard, fBody)
	menu.hBk = button("Respaldo automático\nCopia cada día una carpeta (la de la empresa, la de OneDrive…) a un disco externo, guardando las versiones anteriores.",
		wsTabstop, idMenuBackup, btnCard, fBody)
	menu.hStatus = ctl("STATIC", "", 0x2000, 0, 0, fBody)
	app.muted[menu.hStatus] = true
	menu.hDisk = button("Estado de los discos", wsTabstop, idDisk, btnLink, fBold)
	menu.hProgs = button("Programas que más ocupan", wsTabstop, idProgs, btnLink, fBold)
	menu.hNight = button("Noche", wsTabstop, idNight, btnFrame, fBold)
	menu.hVer = ctl("STATIC", "Herramientas Delio "+version+" · delioph.tech", 0x2, 0, 0, fBody)
	app.muted[menu.hVer] = true
	menu.ctls = []syscall.Handle{menu.hTitle, menu.hSub, menu.hRecover, menu.hRepair, menu.hBk, menu.hStatus,
		menu.hDisk, menu.hProgs, menu.hNight, menu.hVer}
}

func layoutMenuPage(W, H int) {
	m := sc(32)
	lx := m + logoWidth(sc(homeLogoH)) + sc(36)
	moveWin(menu.hTitle, lx, sc(26), sc(300), sc(40))
	moveWin(menu.hProgs, W-m-sc(214), sc(28), sc(214), sc(36))
	moveWin(menu.hDisk, W-m-sc(214)-sc(14)-sc(170), sc(28), sc(170), sc(36))
	moveWin(menu.hSub, m, sc(86), W-2*m, sc(22))
	// (la onda va entre sc(118) y sc(158))
	gap := sc(20)
	y := sc(184)
	ch := H - y - sc(150)
	if ch > sc(230) {
		ch = sc(230)
	}
	cw := (W - 2*m - 2*gap) / 3
	for i, h := range []syscall.Handle{menu.hRecover, menu.hRepair, menu.hBk} {
		moveWin(h, m+i*(cw+gap), y, cw, ch)
	}
	moveWin(menu.hStatus, m, y+ch+sc(18), W-2*m, sc(44))
	moveWin(menu.hNight, W-m-sc(96), H-sc(62), sc(96), sc(40))
	moveWin(menu.hVer, W-m-sc(96)-sc(16)-sc(320), H-sc(52), sc(320), sc(20))
	invalidate(app.hwnd)
}

// updateMenuStatus muestra el estado de los respaldos configurados.
func updateMenuStatus() {
	jobs := loadBackups()
	if len(jobs) == 0 {
		setText(menu.hStatus, "Consejo: configura un respaldo automático de la carpeta de la empresa en un disco externo. "+
			"OneDrive por sí solo no basta: sincroniza, y copiaría un borrado o un virus también a la nube.")
		return
	}
	var parts []string
	for _, j := range jobs {
		parts = append(parts, "Respaldo «"+j.Name+"»: "+lastBackupText(j))
	}
	setText(menu.hStatus, strings.Join(parts, "\r\n"))
}

func lastBackupText(j BackupJob) string {
	if j.LastOK.IsZero() {
		if j.LastResult != "" {
			return j.LastResult
		}
		return "todavía no se ha hecho"
	}
	s := "último correcto " + agoText(j.LastOK)
	if j.LastRun.After(j.LastOK) && strings.HasPrefix(j.LastResult, "No se hizo") {
		s += " · el último intento falló: " + strings.TrimPrefix(j.LastResult, "No se hizo: ")
	}
	if time.Since(j.LastOK) > 3*24*time.Hour {
		s = "⚠ " + s + " (hace días que no se respalda: ¿está conectado el disco?)"
	}
	return s
}

func agoText(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "hace un momento"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case t.YearDay() == time.Now().YearDay() && t.Year() == time.Now().Year():
		return "hoy a las " + t.Format("15:04")
	case d < 48*time.Hour:
		return "ayer a las " + t.Format("15:04")
	}
	return "el " + t.Format("02/01/2006 15:04")
}

// menuOpen abre una herramienta desde el menú.
func menuOpen(id int) {
	switch id {
	case idMenuRecover:
		if !isAdmin() && !app.autotest {
			// Leer los discos necesita permisos de administrador: se vuelve a
			// abrir el programa con ellos, directamente en Recuperar.
			if err := relaunchElevated("-pantalla", "recuperar"); err == nil {
				pPostQuitMessage.Call(0)
				return
			}
			msgBox(app.hwnd, "Sin permisos de administrador no se pueden leer los discos.", "Recuperar archivos", mbIconWarning)
		}
		fillDrives()
		showPage(pageHome)
	case idMenuRepair:
		showPage(pageRepair)
	case idMenuBackup:
		bkRefresh()
		showPage(pageBackup)
	case idMenuBack:
		updateMenuStatus()
		showPage(pageMenu)
	}
}

// inMenu indica si el programa tiene menú (y por tanto botón «‹ Menú»).
func inMenu() bool { return appMode == "" }

// ---------------------------------------------------------------- respaldo

var bk struct {
	ctls                   []syscall.Handle
	hTitle, hSub           syscall.Handle
	hL1, hSrc, hSrcBtn     syscall.Handle
	hL2, hDest, hDestBtn   syscall.Handle
	hL3, hTime, hL4, hDays syscall.Handle
	hL5, hCloud            syscall.Handle
	hRun, hSched, hStop    syscall.Handle
	hProg, hStatus         syscall.Handle
	hLList                 syscall.Handle
	list                   *vlist
	hOpen, hRemove, hBack  syscall.Handle
	hNote                  syscall.Handle
	jobs                   []BackupJob
	mu                     sync.Mutex
	done                   int
	cur                    string
	res                    BackupResult
	err                    error
	job                    BackupJob
}

func buildBackupPage() {
	bk.hTitle = ctl("STATIC", "Respaldo automático", 0, 0, 0, fTitle)
	bk.hSub = ctl("STATIC", "Copia una carpeta a un disco externo y guarda las versiones anteriores de lo que cambie o se borre: "+
		"si el equipo se restaura o un virus cifra los archivos, la copia buena sigue ahí.", ssEndEllip, 0, 0, fBody)
	app.muted[bk.hSub] = true
	label := func(t string) syscall.Handle { return ctl("STATIC", t, 0, 0, 0, fBold) }
	field := func(id int, cue string) syscall.Handle {
		h := ctl("EDIT", "", wsTabstop|esAutoHScrol, 0, id, fBody)
		sendMsg(h, emSetCueBanner, 1, uintptr(unsafe.Pointer(wstr(cue))))
		return h
	}
	bk.hL1 = label("1.  Carpeta a respaldar")
	bk.hSrc = field(idBkSrcEdit, "La carpeta de la empresa, la de OneDrive, Documentos…")
	bk.hSrcBtn = button("Elegir…", wsTabstop, idBkSrc, btnFrame, fBold)
	bk.hL2 = label("2.  Guardar el respaldo en")
	bk.hDest = field(idBkDestEdit, "Una carpeta en un disco externo (no en el mismo disco)")
	bk.hDestBtn = button("Elegir…", wsTabstop, idBkDest, btnFrame, fBold)
	bk.hL3 = label("3.  Cada día a las")
	bk.hTime = field(idBkTimeEdit, "13:00")
	setText(bk.hTime, "13:00")
	bk.hL4 = label("guardar versiones")
	bk.hDays = field(idBkDaysEdit, "90")
	setText(bk.hDays, "90")
	bk.hL5 = label("días")
	bk.hCloud = button("Incluir los de OneDrive que están solo en la nube (se descargan)",
		wsTabstop, idBkCloud, btnCheck, fBody)
	bk.hRun = button("Respaldar ahora", wsTabstop, idBkRun, btnFill, fButton)
	bk.hSched = button("Programar cada día", wsTabstop, idBkSchedule, btnFrame, fBold)
	bk.hStop = button("Detener", wsTabstop, idBkStop, btnFrame, fBold)
	bk.hProg = newBar(0)
	bk.hStatus = ctl("STATIC", "", ssEndEllip, 0, 0, fBody)
	app.muted[bk.hStatus] = true
	bk.hLList = label("Respaldos configurados")
	bk.list = newVList(app.hwnd, idBkList, []vcol{
		{"Nombre", 140, false}, {"Carpeta", 260, false}, {"Se guarda en", 220, false},
		{"Cada día", 80, false}, {"Último respaldo", 420, false},
	}, false, sysSmallList(), true)
	sendMsg(bk.list.hwnd, wmSetFont, uintptr(app.fonts[fBody]), 0)
	bk.list.text = bkRowText
	bk.list.image = func(int) int32 { return fileIcon("/") }
	bk.list.onSelect = bkLoadRow
	bk.list.onActivate = func(row int) {
		if row < len(bk.jobs) {
			openFolder(bk.jobs[row].Folder())
		}
	}
	bk.hOpen = button("Abrir respaldo", wsTabstop, idBkOpen, btnFrame, fBold)
	bk.hRemove = button("Quitar", wsTabstop, idBkRemove, btnFrame, fBold)
	bk.hBack = button("‹  Menú", wsTabstop, idMenuBack, btnFrame, fBold)
	bk.hNote = ctl("STATIC", "Para restaurar: abre el respaldo; en «Actual» está la copia al día y en «Versiones», por fecha, "+
		"lo que se cambió o se borró ese día. Deja el disco externo conectado a la hora del respaldo (si el equipo "+
		"está apagado, se hace al encenderlo).", 0x2000, 0, 0, fBody)
	app.muted[bk.hNote] = true
	bk.ctls = []syscall.Handle{bk.hTitle, bk.hSub, bk.hL1, bk.hSrc, bk.hSrcBtn, bk.hL2, bk.hDest, bk.hDestBtn,
		bk.hL3, bk.hTime, bk.hL4, bk.hDays, bk.hL5, bk.hCloud, bk.hRun, bk.hSched, bk.hStop, bk.hProg, bk.hStatus,
		bk.hLList, bk.list.hwnd, bk.hOpen, bk.hRemove, bk.hBack, bk.hNote}
	enable(bk.hStop, false)
}

func layoutBackupPage(W, H int) {
	m := sc(32)
	lx := m + logoWidth(sc(repLogoH)) + sc(30)
	moveWin(bk.hTitle, lx, sc(14), W-lx-m, sc(40))
	moveWin(bk.hSub, m, sc(66), W-2*m, sc(20))
	lw0 := sc(232)
	bw := sc(130)
	fx := m + lw0
	fw := W - m - bw - sc(10) - fx
	for i, r := range [][3]syscall.Handle{{bk.hL1, bk.hSrc, bk.hSrcBtn}, {bk.hL2, bk.hDest, bk.hDestBtn}} {
		y := sc(114) + i*sc(44)
		moveWin(r[0], m, y+sc(8), lw0-sc(8), sc(22))
		moveField(app.hwnd, r[1], fx, y, fw, sc(34))
		moveWin(r[2], W-m-bw, y, bw, sc(34))
	}
	y := sc(202)
	moveWin(bk.hL3, m, y+sc(8), lw0-sc(8), sc(22))
	moveField(app.hwnd, bk.hTime, fx, y, sc(80), sc(34))
	moveWin(bk.hL4, fx+sc(96), y+sc(8), sc(140), sc(22))
	moveField(app.hwnd, bk.hDays, fx+sc(240), y, sc(64), sc(34))
	moveWin(bk.hL5, fx+sc(314), y+sc(8), sc(50), sc(22))
	moveWin(bk.hCloud, fx+sc(370), y+sc(5), W-m-fx-sc(370), sc(26))
	y = sc(254)
	moveWin(bk.hRun, m, y, sc(200), sc(46))
	moveWin(bk.hSched, m+sc(212), y+sc(6), sc(190), sc(36))
	px := m + sc(416)
	moveWin(bk.hProg, px, y+sc(4), W-m-px-sc(122), sc(16))
	moveWin(bk.hStop, W-m-sc(110), y, sc(110), sc(34))
	moveWin(bk.hStatus, px, y+sc(26), W-m-px-sc(122), sc(20))
	ly := sc(318)
	moveWin(bk.hLList, m, ly, W-2*m, sc(22))
	lh := H - ly - sc(26) - sc(130)
	moveFramed(app.hwnd, bk.list.hwnd, m, ly+sc(26), W-2*m, lh)
	by := ly + sc(26) + lh + sc(12)
	moveWin(bk.hOpen, m, by, sc(160), sc(36))
	moveWin(bk.hRemove, m+sc(172), by, sc(110), sc(36))
	moveWin(bk.hNote, m+sc(296), by, W-2*m-sc(296), sc(54))
	show(bk.hBack, inMenu() && app.page == pageBackup)
	moveWin(bk.hBack, m, H-sc(56), sc(110), sc(40))
	invalidate(app.hwnd)
}

func bkRowText(row, col int) string {
	if row >= len(bk.jobs) {
		return ""
	}
	j := bk.jobs[row]
	switch col {
	case 0:
		return j.Name
	case 1:
		return j.Src
	case 2:
		return j.Folder()
	case 3:
		if j.Time != "" {
			return j.Time
		}
		return "—"
	case 4:
		return lastBackupText(j)
	}
	return ""
}

func bkRefresh() {
	bk.jobs = loadBackups()
	bk.list.setCount(len(bk.jobs))
	bk.list.refresh()
}

func bkLoadRow(row int) {
	if row < 0 || row >= len(bk.jobs) || app.busy != busyNone {
		return
	}
	j := bk.jobs[row]
	setText(bk.hSrc, j.Src)
	setText(bk.hDest, j.Dest)
	if j.Time != "" {
		setText(bk.hTime, j.Time)
	}
	setText(bk.hDays, strconv.Itoa(j.KeepDays))
	setChecked(bk.hCloud, j.CloudFiles)
}

// bkFromFields arma el respaldo con lo escrito en la pantalla.
func bkFromFields() (BackupJob, bool) {
	src := strings.Trim(strings.TrimSpace(getText(bk.hSrc)), `"`)
	dest := strings.Trim(strings.TrimSpace(getText(bk.hDest)), `"`)
	if src == "" || dest == "" {
		msgBox(app.hwnd, "Elige la carpeta a respaldar y dónde guardar el respaldo.", "Respaldo", mbIconInfo)
		return BackupJob{}, false
	}
	if strings.EqualFold(filepath.VolumeName(src), filepath.VolumeName(dest)) && !app.autotest {
		if msgBox(app.hwnd, "El respaldo quedaría en el MISMO disco que los archivos: si ese disco falla o se "+
			"formatea, se pierden los dos. Lo recomendable es un disco externo.\n\n¿Guardarlo ahí de todos modos?",
			"Respaldo", mbYesNo|mbIconWarning|mbDefButton2) != idYes {
			return BackupJob{}, false
		}
	}
	days, err := strconv.Atoi(strings.TrimSpace(getText(bk.hDays)))
	if err != nil || days < 0 {
		days = 90
	}
	j := BackupJob{Name: backupName(src), Src: src, Dest: dest, KeepDays: days, CloudFiles: isChecked(bk.hCloud)}
	for _, old := range loadBackups() {
		if sameDir(old.Src, src) && sameDir(old.Dest, dest) {
			j.Name, j.Time, j.LastRun, j.LastOK, j.LastResult = old.Name, old.Time, old.LastRun, old.LastOK, old.LastResult
		}
	}
	return j, true
}

func backupCommand(id, code int) {
	switch id {
	case idBkSrc:
		if d := browseFolder("Elige la carpeta a respaldar (por ejemplo, la de la empresa o la de OneDrive)."); d != "" {
			setText(bk.hSrc, d)
		}
	case idBkDest:
		if d := browseFolder("Elige dónde guardar el respaldo: una carpeta en un disco externo."); d != "" {
			setText(bk.hDest, d)
		}
	case idBkRun:
		if j, ok := bkFromFields(); ok {
			startBackup(j)
		}
	case idBkSchedule:
		j, ok := bkFromFields()
		if !ok {
			return
		}
		if _, _, ok := parseHour(getText(bk.hTime)); !ok {
			msgBox(app.hwnd, "Escribe la hora así: 13:30.", "Respaldo", mbIconInfo)
			return
		}
		j.Time = strings.TrimSpace(getText(bk.hTime))
		if err := scheduleBackup(j); err != nil {
			msgBox(app.hwnd, "No se pudo programar el respaldo:\n\n"+err.Error(), "Respaldo", mbIconError)
			return
		}
		upsertBackup(j)
		bkRefresh()
		setText(bk.hStatus, "Programado cada día a las "+j.Time+". Si el equipo está apagado a esa hora, se hace al encenderlo.")
		if !app.autotest && j.LastOK.IsZero() &&
			msgBox(app.hwnd, "Programado cada día a las "+j.Time+".\n\n¿Hacer ahora el primer respaldo?", "Respaldo", mbYesNo|mbIconQuest) == idYes {
			startBackup(j)
		}
	case idBkStop:
		atomic.StoreInt32(&stopFlag, 1)
		enable(bk.hStop, false)
	case idBkOpen:
		if row := bk.list.focused(); row >= 0 && row < len(bk.jobs) {
			openFolder(bk.jobs[row].Folder())
		} else if j, ok := bkFromFields(); ok {
			openFolder(j.Folder())
		}
	case idBkRemove:
		row := bk.list.focused()
		if row < 0 || row >= len(bk.jobs) {
			msgBox(app.hwnd, "Elige en la lista el respaldo que quieres quitar.", "Respaldo", mbIconInfo)
			return
		}
		j := bk.jobs[row]
		if !app.autotest && msgBox(app.hwnd, "Se deja de respaldar «"+j.Name+"» automáticamente. Lo ya copiado en "+
			j.Folder()+" no se borra.\n\n¿Quitarlo?", "Respaldo", mbYesNo|mbIconQuest) != idYes {
			return
		}
		unscheduleBackup(j.Name)
		removeBackup(j.Name)
		bkRefresh()
	}
}

func startBackup(j BackupJob) {
	if app.busy != busyNone {
		return
	}
	bk.job = j
	app.busy = busyBackup
	atomic.StoreInt32(&stopFlag, 0)
	for _, h := range []syscall.Handle{bk.hRun, bk.hSched, bk.hRemove, bk.hBack} {
		enable(h, false)
	}
	enable(bk.hStop, true)
	setBar(bk.hProg, 0)
	setText(bk.hStatus, "Respaldando…")
	total := countFiles(j.Src)
	go func() {
		last := time.Time{}
		res, err := RunBackup(j, func(done int, cur string) {
			bk.mu.Lock()
			bk.done, bk.cur = done, cur
			bk.mu.Unlock()
			if time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				if atomic.CompareAndSwapInt32(&app.progPnd, 0, 1) {
					postMsg(app.hwnd, wmAppBkProg, uintptr(total), 0)
				}
			}
		}, stopRequested)
		bk.mu.Lock()
		bk.res, bk.err = res, err
		bk.mu.Unlock()
		postMsg(app.hwnd, wmAppBkDone, 0, 0)
	}()
}

func backupProgress(total int) {
	atomic.StoreInt32(&app.progPnd, 0)
	bk.mu.Lock()
	done, cur := bk.done, bk.cur
	bk.mu.Unlock()
	if total > 0 {
		setBar(bk.hProg, done*1000/total)
	}
	setText(bk.hStatus, fmt.Sprintf("%s de %s archivos · %s", groupDigits(int64(done)), groupDigits(int64(total)), cur))
}

func backupDone() {
	app.busy = busyNone
	atomic.StoreInt32(&stopFlag, 0)
	for _, h := range []syscall.Handle{bk.hRun, bk.hSched, bk.hRemove, bk.hBack} {
		enable(h, true)
	}
	enable(bk.hStop, false)
	bk.mu.Lock()
	res, err := bk.res, bk.err
	bk.mu.Unlock()
	j := bk.job
	j.LastRun = time.Now()
	if err != nil {
		j.LastResult = "No se hizo: " + err.Error()
		setText(bk.hStatus, j.LastResult)
	} else {
		j.LastOK, j.LastResult = j.LastRun, res.Text()
		setBar(bk.hProg, 1000)
		setText(bk.hStatus, "Hecho: "+res.Text())
	}
	upsertBackup(j)
	bkRefresh()
	if app.autotest {
		if f := os.Getenv("RECOVERY_GUI_ROWS_OUT"); f != "" {
			var b strings.Builder
			b.WriteString("status\t" + getText(bk.hStatus) + "\n")
			for i := range bk.jobs {
				var cols []string
				for c := 0; c < 5; c++ {
					cols = append(cols, bkRowText(i, c))
				}
				b.WriteString("row\t" + strings.Join(cols, "\t") + "\n")
			}
			os.WriteFile(f, []byte(b.String()), 0o644)
		}
		if os.Getenv("RECOVERY_GUI_KEEP") == "" {
			pPostQuitMessage.Call(0)
		}
		return
	}
	if err != nil {
		msgBox(app.hwnd, "No se hizo el respaldo:\n\n"+err.Error(), "Respaldo", mbIconWarning)
	}
}

// autotestBackup: pruebas automáticas de la pantalla de respaldo.
func autotestBackup() bool {
	src := os.Getenv("RECOVERY_GUI_BACKUP")
	if src == "" {
		return false
	}
	showPage(pageBackup)
	bkRefresh()
	setText(bk.hSrc, src)
	setText(bk.hDest, os.Getenv("RECOVERY_GUI_BACKUP_DEST"))
	if j, ok := bkFromFields(); ok {
		startBackup(j)
	}
	return true
}
