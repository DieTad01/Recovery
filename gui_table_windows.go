//go:build windows

package main

// Ventanas de herramientas con una tabla: «Estado de los discos» y
// «Programas que más ocupan».

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

type tableWin struct {
	hwnd    syscall.Handle
	list    *vlist
	hFoot   syscall.Handle
	hClose  syscall.Handle
	numeric []bool
	rows    [][]string
	sortCol int
	desc    bool

	mu      sync.Mutex
	pRows   [][]string
	pFoot   string
	pHasRow bool
}

var gTables = map[syscall.Handle]*tableWin{}

const idTableClose = 900

func registerTableClass(cursor, iconBig, iconSm syscall.Handle) {
	wc := wndclassex{
		cbSize: uint32(unsafe.Sizeof(wndclassex{})), style: 3,
		lpfnWndProc: syscall.NewCallback(tableProc), hInstance: syscall.Handle(gInst),
		hIcon: iconBig, hCursor: cursor,
		lpszClassName: wstr("RecoveryTable"), hIconSm: iconSm,
	}
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
}

func newTableWindow(title string, cols []vcol, numeric []bool, w, h int) *tableWin {
	t := &tableWin{numeric: numeric, sortCol: -1}
	r := rect{0, 0, int32(sc(w)), int32(sc(h))}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), wsOverlappedWin, 0, wsExControlPar)
	hw, _, _ := pCreateWindowExW.Call(wsExControlPar, uintptr(unsafe.Pointer(wstr("RecoveryTable"))),
		uintptr(unsafe.Pointer(wstr(title))), wsOverlappedWin|wsClipChildren,
		0x80000000, 0x80000000, uintptr(r.right-r.left), uintptr(r.bottom-r.top), // CW_USEDEFAULT
		uintptr(app.hwnd), 0, gInst, 0)
	t.hwnd = syscall.Handle(hw)
	gTables[t.hwnd] = t
	styleCaption(t.hwnd)
	t.list = newVList(t.hwnd, 1, cols, false, 0, false)
	sendMsg(t.list.hwnd, wmSetFont, uintptr(app.fonts[fBody]), 0)
	t.list.text = func(row, col int) string {
		if row < len(t.rows) && col < len(t.rows[row]) {
			return t.rows[row][col]
		}
		return ""
	}
	t.list.onSort = t.sortBy
	mk := func(class, text string, style uintptr, id int, font int) syscall.Handle {
		h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(wstr(class))), uintptr(unsafe.Pointer(wstr(text))),
			wsChild|wsVisible|style, 0, 0, 10, 10, uintptr(t.hwnd), uintptr(id), gInst, 0)
		sendMsg(syscall.Handle(h), wmSetFont, uintptr(app.fonts[font]), 0)
		return syscall.Handle(h)
	}
	t.hFoot = mk("STATIC", "Cargando…", 0x2000, 0, fBody)
	t.hClose = mk("BUTTON", "Cerrar", wsTabstop|bsOwnerDraw, idTableClose, fBold)
	btnKind[t.hClose] = btnFrame
	app.ctlFont[t.hClose] = fBold
	t.layout()
	pShowWindow.Call(hw, swShow)
	return t
}

func (t *tableWin) layout() {
	W, H := clientSize(t.hwnd)
	m := sc(14)
	moveFramed(t.hwnd, t.list.hwnd, m, m, W-2*m, H-2*m-sc(80))
	moveWin(t.hFoot, m, H-m-sc(66), W-2*m-sc(124), sc(66))
	moveWin(t.hClose, W-m-sc(104), H-m-sc(38), sc(104), sc(38))
	fitLastColumn(t.list)
	invalidate(t.hwnd)
}

// post entrega datos desde cualquier hilo; rows == nil solo cambia el pie.
func (t *tableWin) post(rows [][]string, foot string) {
	t.mu.Lock()
	if rows != nil {
		t.pRows, t.pHasRow = rows, true
	}
	t.pFoot = foot
	t.mu.Unlock()
	postMsg(app.hwnd, wmAppTable, uintptr(t.hwnd), 0)
}

// tableUpdate aplica los datos en el hilo de la interfaz.
func tableUpdate(h syscall.Handle) {
	t := gTables[h]
	if t == nil {
		return
	}
	t.mu.Lock()
	rows, has, foot := t.pRows, t.pHasRow, t.pFoot
	t.pHasRow = false
	t.mu.Unlock()
	if has {
		t.rows = rows
		t.list.setCount(len(rows))
	}
	setText(t.hFoot, foot)
}

func (t *tableWin) sortBy(col int) {
	if t.sortCol == col {
		t.desc = !t.desc
	} else {
		t.sortCol, t.desc = col, col < len(t.numeric) && t.numeric[col]
	}
	num := col < len(t.numeric) && t.numeric[col]
	sort.SliceStable(t.rows, func(i, j int) bool {
		a, b := t.rows[i][col], t.rows[j][col]
		var c int
		if num {
			c = cmp64(sizeKey(a), sizeKey(b))
		} else {
			c = strings.Compare(fold(a), fold(b))
		}
		if t.desc {
			return c > 0
		}
		return c < 0
	})
	t.list.refresh()
}

// sizeKey convierte «1,5 GB», «~300 MB», «42 °C» o «1.234 h» en un número para ordenar.
func sizeKey(s string) int64 {
	s = strings.TrimPrefix(strings.TrimSpace(s), "~")
	f := strings.Fields(s)
	if len(f) == 0 {
		return -1
	}
	num := strings.ReplaceAll(f[0], ".", "")
	num = strings.ReplaceAll(num, ",", ".")
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		v2, err2 := strconv.ParseFloat(f[0], 64)
		if err2 != nil {
			return -1
		}
		v = v2
	}
	mult := 1.0
	if len(f) > 1 {
		switch f[1] {
		case "KB":
			mult = 1 << 10
		case "MB":
			mult = 1 << 20
		case "GB":
			mult = 1 << 30
		case "TB":
			mult = 1 << 40
		}
	}
	return int64(v * mult)
}

func tableProc(hwnd syscall.Handle, msg uint32, wp, lp uintptr) uintptr {
	t := gTables[hwnd]
	switch msg {
	case wmSize:
		if t != nil {
			t.layout()
		}
		return 0
	case wmGetMinMax:
		wrI32(lp, 24, int32(sc(600)))
		wrI32(lp, 28, int32(sc(360)))
		return 0
	case wmEraseBkgnd:
		w, h := clientSize(hwnd)
		fillR(wp, rect{0, 0, int32(w), int32(h)}, app.brBg)
		return 1
	case wmPaint:
		var ps paintstruct
		hdc, _, _ := pBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		paintBoxes(hdc, hwnd)
		pEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmSetCursor:
		if onSetCursor(wp) {
			return 1
		}
	case wmDrawItem:
		drawButton(lp)
		return 1
	case wmCtlColorStat:
		pSetTextColor.Call(wp, uintptr(cText))
		pSetBkColor.Call(wp, uintptr(cBg))
		return uintptr(app.brBg)
	case wmNotify:
		if ok, r := handleListNotify(lp); ok {
			return r
		}
		return 0
	case wmCommand:
		if int(wp&0xFFFF) == idTableClose && wp>>16&0xFFFF == 0 {
			pDestroyWindow.Call(uintptr(hwnd))
		}
		return 0
	case wmDestroy:
		if t != nil {
			delete(gLists, t.list.hwnd)
			delete(boxes, t.list.hwnd)
			delete(btnKind, t.hClose)
			if btnHot == t.hClose {
				btnHot = 0
			}
		}
		delete(gTables, hwnd)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wp, lp)
	return r
}

// ---------------------------------------------------------------- herramientas

func openDiskWindow() {
	t := newTableWindow("Estado de los discos", []vcol{
		{"Disco", 220, false}, {"Tipo", 110, false}, {"Conexión", 80, false}, {"Capacidad", 90, true},
		{"Estado", 280, false}, {"Temperatura", 90, true}, {"Desgaste SSD", 95, true},
		{"Horas encendido", 150, true}, {"Errores sin corregir", 130, true},
	}, []bool{false, false, false, true, false, true, true, true, true}, 980, 420)
	t.post(nil, "Consultando a Windows el estado de los discos…")
	go func() {
		ds, vols, err := diskHealth()
		var foot []string
		if err != nil {
			foot = append(foot, "No se pudo consultar el estado: "+err.Error())
		}
		worst := ""
		for _, d := range ds {
			if d.Verdict == "Mal" || (d.Verdict == "Atención" && worst == "") {
				worst = d.Name + ": " + d.Advice()
			}
		}
		if worst != "" {
			foot = append(foot, "⚠ "+worst)
		} else if len(ds) > 0 {
			foot = append(foot, "No se detectaron problemas en los discos.")
		}
		if v := VolumeSummary(vols); v != "" {
			foot = append(foot, "Espacio: "+v)
		}
		t.post(DiskRows(ds), strings.Join(foot, "\r\n"))
	}()
}

func openProgramsWindow() {
	t := newTableWindow("Programas que más ocupan", []vcol{
		{"Programa", 320, false}, {"Tamaño", 100, true}, {"Editor", 200, false},
		{"Versión", 110, false}, {"Instalado", 90, false}, {"Carpeta", 320, false},
	}, []bool{false, true, false, false, false, false}, 980, 560)
	t.post(nil, "Leyendo la lista de programas instalados…")
	go func() {
		ps, err := listPrograms(func(done, total int) {
			if done%5 == 0 {
				t.post(nil, fmt.Sprintf("Midiendo lo que ocupa cada programa: %d de %d…", done, total))
			}
		})
		if err != nil {
			t.post(nil, err.Error())
			return
		}
		t.post(ProgramRows(ps), ProgramsSummary(ps)+
			"\r\nOrdenados de mayor a menor. «~» = tamaño indicado por el instalador (no se pudo medir la carpeta). "+
			"Haz clic en una columna para ordenar.")
	}()
}
