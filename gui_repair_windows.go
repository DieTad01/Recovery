//go:build windows

package main

// Pantalla «Reparador»: revisa una carpeta (normalmente lo ya recuperado),
// arregla los archivos dañados y busca copias sanas. Es también la única
// pantalla de reparador.exe.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var rep struct {
	ctls                 []syscall.Handle
	hTitle, hSub         syscall.Handle
	hL1, hSrc, hSrcBtn   syscall.Handle
	hL2, hIn, hInBtn     syscall.Handle
	hL3, hOut, hOutBtn   syscall.Handle
	hRun, hStop, hProg   syscall.Handle
	hStatus, hSum, hBack syscall.Handle
	hOpen                syscall.Handle
	list                 *vlist
	rows                 []RepairRow
	mu                   sync.Mutex
	pending              []RepairRow
	prog                 RepairProgress
	sum                  RepairSummary
	job                  RepairJob
	finished             bool
	autoOut              string // destino puesto automáticamente (se cambia si cambia la carpeta)
}

func buildRepairPage() {
	rep.hTitle = ctl("STATIC", "Reparador", 0, 0, 0, fTitle)
	rep.hSub = ctl("STATIC", "Revisa archivos ya recuperados, arregla los dañados y busca copias sanas en otras carpetas. "+
		"Nunca modifica los originales.", 0, 0, 0, fBody)
	app.muted[rep.hSub] = true
	label := func(t string) syscall.Handle { return ctl("STATIC", t, 0, 0, 0, fBold) }
	field := func(id int, cue string) syscall.Handle {
		h := ctl("EDIT", "", wsTabstop|esAutoHScrol, 0, id, fBody)
		sendMsg(h, emSetCueBanner, 1, uintptr(unsafe.Pointer(wstr(cue))))
		return h
	}
	rep.hL1 = label("1.  Archivos a revisar")
	rep.hSrc = field(idRepSrcEdit, "La carpeta con lo recuperado (o cualquier carpeta con archivos dañados)")
	rep.hSrcBtn = button("Elegir…", wsTabstop, idRepSrc, btnFrame, fBold)
	rep.hL2 = label("2.  Buscar copias sanas en")
	rep.hIn = field(idRepInEdit, "Opcional: respaldos, memorias USB, otro equipo… (varias con «;»)")
	rep.hInBtn = button("Añadir…", wsTabstop, idRepIn, btnFrame, fBold)
	rep.hL3 = label("3.  Guardar resultados en")
	rep.hOut = field(idRepOutEdit, "Se crea una carpeta «Reparados» dentro de la revisada")
	rep.hOutBtn = button("Elegir…", wsTabstop, idRepOut, btnFrame, fBold)
	rep.hRun = button("Revisar y reparar", wsTabstop, idRepRun, btnFill, fButton)
	rep.hStop = button("Detener", wsTabstop, idRepStop, btnFrame, fBold)
	rep.hProg = newBar(0)
	rep.hStatus = ctl("STATIC", "", ssEndEllip, 0, 0, fBody)
	app.muted[rep.hStatus] = true
	rep.list = newVList(app.hwnd, idRepList, []vcol{
		{"Archivo", 250, false}, {"Estado", 90, false}, {"Qué se consiguió", 330, false},
		{"Carpeta", 200, false}, {"Detalle", 700, false},
	}, false, sysSmallList(), false)
	sendMsg(rep.list.hwnd, wmSetFont, uintptr(app.fonts[fBody]), 0)
	rep.list.text = repRowText
	rep.list.image = func(row int) int32 {
		if row < len(rep.rows) {
			return fileIcon(rep.rows[row].Ext)
		}
		return -1
	}
	rep.list.onActivate = repOpenRow
	rep.hSum = ctl("STATIC", "", 0x2000, 0, 0, fBody)
	backText := "‹  Inicio"
	if inMenu() {
		backText = "‹  Menú"
	}
	rep.hBack = button(backText, wsTabstop, idRepBack, btnFrame, fBold)
	rep.hOpen = button("Abrir resultados", wsTabstop, idRepOpen, btnFrame, fBold)
	rep.ctls = []syscall.Handle{rep.hTitle, rep.hSub, rep.hL1, rep.hSrc, rep.hSrcBtn, rep.hL2, rep.hIn, rep.hInBtn,
		rep.hL3, rep.hOut, rep.hOutBtn, rep.hRun, rep.hStop, rep.hProg, rep.hStatus, rep.list.hwnd, rep.hSum,
		rep.hBack, rep.hOpen}
	enable(rep.hStop, false)
	enable(rep.hOpen, false)
	setText(rep.hSum, "Los archivos dañados aparecerán aquí con lo que se consiguió de cada uno. "+
		"Doble clic: abrir el archivo reparado.")
}

func layoutRepairPage(W, H int) {
	m := sc(32)
	lx := m + logoWidth(sc(repLogoH)) + sc(30)
	moveWin(rep.hTitle, lx, sc(14), W-lx-m, sc(40))
	moveWin(rep.hSub, m, sc(66), W-2*m, sc(20))
	// (raya en sc(98))
	lw0 := sc(232)
	bw := sc(130)
	fx := m + lw0
	fw := W - m - bw - sc(10) - fx
	for i, r := range [][3]syscall.Handle{{rep.hL1, rep.hSrc, rep.hSrcBtn}, {rep.hL2, rep.hIn, rep.hInBtn}, {rep.hL3, rep.hOut, rep.hOutBtn}} {
		y := sc(114) + i*sc(44)
		moveWin(r[0], m, y+sc(8), lw0-sc(8), sc(22))
		moveField(app.hwnd, r[1], fx, y, fw, sc(34))
		moveWin(r[2], W-m-bw, y, bw, sc(34))
	}
	y := sc(252)
	moveWin(rep.hRun, m, y, sc(220), sc(46))
	moveWin(rep.hProg, m+sc(236), y+sc(4), W-2*m-sc(236)-sc(122), sc(16))
	moveWin(rep.hStop, W-m-sc(110), y, sc(110), sc(34))
	moveWin(rep.hStatus, m+sc(236), y+sc(26), W-2*m-sc(236)-sc(122), sc(20))
	ly := sc(314)
	moveFramed(app.hwnd, rep.list.hwnd, m, ly, W-2*m, H-ly-sc(76))
	bx := m
	if appMode != "reparar" {
		moveWin(rep.hBack, m, H-sc(60), sc(110), sc(40))
		bx = m + sc(124)
	}
	show(rep.hBack, appMode != "reparar" && app.page == pageRepair)
	moveWin(rep.hSum, bx, H-sc(62), W-bx-m-sc(214), sc(48))
	moveWin(rep.hOpen, W-m-sc(200), H-sc(60), sc(200), sc(40))
	invalidate(app.hwnd)
}

const repLogoH = 40

func repRowText(row, col int) string {
	if row >= len(rep.rows) {
		return ""
	}
	r := rep.rows[row]
	switch col {
	case 0:
		return filepath.Base(r.Rel)
	case 1:
		return r.StateText()
	case 2:
		if r.Action == "" && (r.State == rsDamaged || r.State == rsBlank) {
			return "en espera…"
		}
		return r.Action
	case 3:
		return filepath.Dir(r.Rel)
	case 4:
		if r.Notes != "" {
			return r.Problem + " — " + r.Notes
		}
		return r.Problem
	}
	return ""
}

// repOpenRow abre lo creado para ese archivo (o su carpeta).
func repOpenRow(row int) {
	if row >= len(rep.rows) {
		return
	}
	r := rep.rows[row]
	if len(r.Created) > 0 {
		openFolder(filepath.Join(rep.job.Out, r.Created[0]))
		return
	}
	openFolder(filepath.Dir(filepath.Join(rep.job.Src, r.Rel)))
}

func repairCommand(id, code int) {
	switch id {
	case idRepSrc:
		if d := browseFolder("Elige la carpeta con los archivos a revisar (por ejemplo, la de lo recuperado)."); d != "" {
			setText(rep.hSrc, d)
			repSuggestOut()
		}
	case idRepSrcEdit:
		if code == enChange {
			repSuggestOut()
		}
	case idRepIn:
		if d := browseFolder("Elige otra carpeta donde buscar copias sanas (un respaldo, una memoria USB…)."); d != "" {
			cur := strings.TrimSpace(getText(rep.hIn))
			if cur != "" {
				cur += "; "
			}
			setText(rep.hIn, cur+d)
		}
	case idRepOut:
		if d := browseFolder("Elige dónde guardar los archivos reparados."); d != "" {
			setText(rep.hOut, d)
			rep.autoOut = ""
		}
	case idRepRun:
		startRepair()
	case idRepStop:
		atomic.StoreInt32(&stopFlag, 1)
		enable(rep.hStop, false)
		setText(rep.hStatus, "Deteniendo…")
	case idRepOpen:
		if rep.job.Out != "" {
			openFolder(rep.job.Out)
		}
	case idRepBack:
		if app.busy == busyNone {
			if inMenu() {
				menuOpen(idMenuBack)
			} else {
				showPage(pageHome)
			}
		}
	}
}

// repSuggestOut propone «<carpeta>\Reparados» como destino.
func repSuggestOut() {
	src := strings.TrimSpace(getText(rep.hSrc))
	cur := strings.TrimSpace(getText(rep.hOut))
	if cur != "" && cur != rep.autoOut {
		return
	}
	out := ""
	if src != "" {
		out = filepath.Join(src, "Reparados")
		if st, err := os.Stat(src); err == nil && !st.IsDir() {
			out = filepath.Join(filepath.Dir(src), "Reparados")
		}
	}
	rep.autoOut = out
	setText(rep.hOut, out)
}

func startRepair() {
	if app.busy != busyNone {
		return
	}
	src := strings.Trim(strings.TrimSpace(getText(rep.hSrc)), `"`)
	if _, err := os.Stat(src); src == "" || err != nil {
		msgBox(app.hwnd, "Elige primero la carpeta con los archivos a revisar.", "Reparador", mbIconInfo)
		return
	}
	repSuggestOut()
	job := RepairJob{Src: src, Out: strings.Trim(strings.TrimSpace(getText(rep.hOut)), `"`)}
	for _, d := range strings.Split(getText(rep.hIn), ";") {
		if d = strings.Trim(strings.TrimSpace(d), `"`); d != "" {
			job.SearchIn = append(job.SearchIn, d)
		}
	}
	rep.job = job
	rep.rows = nil
	rep.pending = nil
	rep.finished = false
	rep.list.setCount(0)
	atomic.StoreInt32(&stopFlag, 0)
	app.busy = busyRepair
	enable(rep.hRun, false)
	enable(rep.hStop, true)
	enable(rep.hOpen, false)
	enable(rep.hBack, false)
	setBar(rep.hProg, 0)
	setText(rep.hSum, "")
	setText(rep.hStatus, "Empezando…")
	job.OnRow = func(r RepairRow) {
		rep.mu.Lock()
		rep.pending = append(rep.pending, r)
		rep.mu.Unlock()
	}
	go func() {
		last := time.Time{}
		_, sum, err := RunRepair(job, func(p RepairProgress) {
			rep.mu.Lock()
			rep.prog = p
			rep.mu.Unlock()
			if time.Since(last) > 150*time.Millisecond || p.Phase == "Terminado" {
				last = time.Now()
				if atomic.CompareAndSwapInt32(&app.progPnd, 0, 1) {
					postMsg(app.hwnd, wmAppRepProg, 0, 0)
				}
			}
		}, stopRequested)
		rep.mu.Lock()
		rep.sum = sum
		if err != nil {
			rep.sum.Stopped = true
			rep.prog.Current = err.Error()
		}
		rep.mu.Unlock()
		postMsg(app.hwnd, wmAppRepDone, 0, 0)
	}()
}

// repairProgress aplica el avance en el hilo de la ventana.
func repairProgress() {
	atomic.StoreInt32(&app.progPnd, 0)
	rep.mu.Lock()
	p := rep.prog
	newRows := rep.pending
	rep.pending = nil
	rep.mu.Unlock()
	for _, r := range newRows {
		updated := false
		for i := range rep.rows {
			if rep.rows[i].Rel == r.Rel {
				rep.rows[i] = r
				updated = true
				break
			}
		}
		if !updated {
			rep.rows = append(rep.rows, r)
		}
	}
	if len(newRows) > 0 {
		rep.list.setCount(len(rep.rows))
	}
	switch {
	case p.Total > 0:
		setText(rep.hStatus, fmt.Sprintf("%s %s de %s · %s", p.Phase, groupDigits(int64(p.Done)), groupDigits(int64(p.Total)), p.Current))
		phase := 0
		if p.Phase == "Reparando" {
			phase = 1
		}
		setBar(rep.hProg, phase*500+p.Done*500/p.Total)
	default:
		setText(rep.hStatus, p.Phase)
	}
}

func repairDone() {
	repairProgress()
	app.busy = busyNone
	atomic.StoreInt32(&stopFlag, 0)
	rep.finished = true
	enable(rep.hRun, true)
	enable(rep.hStop, false)
	enable(rep.hOpen, true)
	enable(rep.hBack, true)
	setBar(rep.hProg, 1000)
	s := rep.sum
	txt := fmt.Sprintf("%s revisados: %s sanos, %s dañados", groupDigits(int64(s.Files)), groupDigits(int64(s.OK)), groupDigits(int64(s.Damaged+s.Blank)))
	if s.Damaged+s.Blank > 0 {
		txt += fmt.Sprintf(" → %s recuperados completos, %s en parte, %s sin arreglo", groupDigits(int64(s.Complete)),
			groupDigits(int64(s.Partial)), groupDigits(int64(s.NoFix)))
	}
	if s.Stopped {
		txt += " (detenido antes de terminar)"
	}
	setText(rep.hSum, txt+". Resultados e informe en la carpeta «"+filepath.Base(rep.job.Out)+"».")
	setText(rep.hStatus, "Terminado.")
	if app.autotest {
		if f := os.Getenv("RECOVERY_GUI_ROWS_OUT"); f != "" {
			var b strings.Builder
			b.WriteString("sum\t" + getText(rep.hSum) + "\n")
			for i := range rep.rows {
				var cols []string
				for c := 0; c < 5; c++ {
					cols = append(cols, repRowText(i, c))
				}
				b.WriteString("row\t" + strings.Join(cols, "\t") + "\n")
			}
			os.WriteFile(f, []byte(b.String()), 0o644)
		}
		if os.Getenv("RECOVERY_GUI_KEEP") == "" {
			pPostQuitMessage.Call(0)
		}
	}
}

// autotestRepair arranca el reparador con variables de entorno (pruebas).
func autotestRepair() bool {
	src := os.Getenv("RECOVERY_GUI_REPAIR")
	if src == "" {
		return false
	}
	showPage(pageRepair)
	setText(rep.hSrc, src)
	repSuggestOut()
	if v := os.Getenv("RECOVERY_GUI_REPAIR_IN"); v != "" {
		setText(rep.hIn, v)
	}
	if v := os.Getenv("RECOVERY_GUI_REPAIR_OUT"); v != "" {
		setText(rep.hOut, v)
	}
	startRepair()
	return true
}
