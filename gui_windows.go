//go:build windows

package main

// Interfaz gráfica: pantalla de inicio (elegir unidad) y explorador de
// resultados (árbol de carpetas, lista de archivos con casillas, vista
// previa y botón «Recuperar»). Escrita directamente sobre Win32.

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Identificadores de controles.
const (
	idDrives = 100 + iota
	idQuick
	idScan
	idDisk
	idProgs
	idBack
	idSearch
	idStop
	idTree
	idList
	idHideSys
	idSummary
	idVerify
	idRecover
	idMakeImage
	idOpenImage
	idImgBack
	idImgStop
	idImgAnalyze
	idNight
	idSegFiles
	idSegFolders
	idHomeRepair
	idRepSrc
	idRepSrcEdit
	idRepIn
	idRepInEdit
	idRepOut
	idRepOutEdit
	idRepRun
	idRepStop
	idRepOpen
	idRepBack
	idRepList
	idMenuRecover
	idMenuRepair
	idMenuBackup
	idMenuBack
	idHomeMenu
	idBkSrc
	idBkSrcEdit
	idBkDest
	idBkDestEdit
	idBkTimeEdit
	idBkDaysEdit
	idBkCloud
	idBkRun
	idBkSchedule
	idBkStop
	idBkOpen
	idBkRemove
	idBkList
)

// Mensajes propios (los envían los procesos en segundo plano).
const (
	wmAppProgress = wmApp + 1 + iota
	wmAppResults
	wmAppDone
	wmAppPreview
	wmAppRecProg
	wmAppRecDone
	wmAppTable
	wmAppImgProg
	wmAppImgDone
	wmAppVerProg
	wmAppVerDone
	wmAppRepProg
	wmAppRepDone
	wmAppBkProg
	wmAppBkDone
)

const (
	timerSearch  = 1
	timerPreview = 2
)

const (
	pageHome = iota
	pageExplorer
	pageImage
	pageRepair
	pageMenu
	pageBackup
)

const (
	busyNone = iota
	busyScan
	busyRecover
	busyImage
	busyVerify
	busyRepair
	busyBackup
)

// Fuentes.
const (
	fBody = iota
	fBold
	fTitle
	fSub
	fSection
	fButton
	fTitle2
	nFonts
)

var fontSpec = [nFonts]struct {
	px     int
	weight int
}{
	fBody: {13, 400}, fBold: {13, 700}, fTitle: {30, 700}, fSub: {15, 400},
	fSection: {16, 700}, fButton: {15, 700}, fTitle2: {22, 700},
}

type appState struct {
	hwnd     syscall.Handle
	dpi      int
	fonts    [nFonts]syscall.Handle
	ctlFont  map[syscall.Handle]int
	muted    map[syscall.Handle]bool
	brBg     syscall.Handle
	brPanel  syscall.Handle // fondo hundido
	brStrong syscall.Handle // líneas, marcos y botones
	brOff    syscall.Handle // desactivado
	page     int
	busy     int

	// Inicio
	homeCtls             []syscall.Handle
	hTitle, hSub, hSect  syscall.Handle
	drives               *vlist
	hQuick, hHint, hScan syscall.Handle
	hDisk, hProgs, hVer  syscall.Handle
	hNight, hRepair      syscall.Handle
	hHomeMenu            syscall.Handle
	hMakeImg, hOpenImg   syscall.Handle
	sources              []Source

	// Explorador
	expCtls                              []syscall.Handle
	hBack, hSrc, hStatus                 syscall.Handle
	hSearch, hProgress, hStop            syscall.Handle
	hTree, hCrumb, hOverlay              syscall.Handle
	list                                 *vlist
	hCanvas, hPrevName, hPrevI           syscall.Handle
	hHideSys, hSelInfo, hSummary, hRecov syscall.Handle
	hVerify                              syscall.Handle
	hSegFiles, hSegFolders               syscall.Handle
	folderMode                           bool   // el buscador busca carpetas
	folderQ                              string // última búsqueda de carpetas mostrada
	rebuilding                           bool   // se está rehaciendo el árbol (no es el usuario)
	folderCols                           bool   // columnas ordenadas para los resultados de carpetas

	ss       *Session
	srcName  string
	tree     *Tree
	nodes    []*Node
	cur      *Node
	curPath  string
	rows     []lrow
	search   bool
	sortCol  int
	sortDesc bool
	selfEdit bool // el programa está cambiando el texto de búsqueda

	progMu  sync.Mutex
	prog    SessionProgress
	progPnd int32
	recProg struct {
		done, total int
		bytes       int64
	}
	recRes  RecoverResult
	recDest string
	verRes  VerifyResult

	prevGen int64
	prevMu  sync.Mutex
	prev    previewImage
	prevFor *Entry

	autotest bool
}

var app = &appState{ctlFont: map[syscall.Handle]int{}, muted: map[syscall.Handle]bool{}}

var gInst uintptr

// sc pasa de unidades de 96 ppp a píxeles reales.
func sc(v int) int { return v * app.dpi / 96 }

func makeFonts() {
	for i, f := range fontSpec {
		if app.fonts[i] != 0 {
			pDeleteObject.Call(uintptr(app.fonts[i]))
		}
		app.fonts[i] = newFont(sc(f.px), f.weight, "Arial") // la letra de la marca (Helvetica / Arial)
	}
}

// ctl crea un control hijo con su fuente.
func ctl(class, text string, style, ex uintptr, id int, font int) syscall.Handle {
	h, _, _ := pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(wstr(class))),
		uintptr(unsafe.Pointer(wstr(text))), style|wsChild,
		0, 0, 10, 10, uintptr(app.hwnd), uintptr(id), gInst, 0)
	hc := syscall.Handle(h)
	app.ctlFont[hc] = font
	sendMsg(hc, wmSetFont, uintptr(app.fonts[font]), 0)
	return hc
}

func applyFonts() {
	for h, f := range app.ctlFont {
		sendMsg(h, wmSetFont, uintptr(app.fonts[f]), 1)
	}
	for _, l := range gLists {
		sendMsg(l.hwnd, wmSetFont, uintptr(app.fonts[fBody]), 1)
	}
}

// ---------------------------------------------------------------- arranque

func runGUIApp(cfg *Config) int {
	runtime.LockOSThread() // ventana y bucle de mensajes en el mismo hilo
	pSetProcessDPIAware.Call()
	icc := struct{ size, icc uint32 }{8, 0x1 | 0x2 | 0x20 | 0x4000}
	pInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))
	pCoInitialize.Call(0)
	keepAwake()
	app.autotest = os.Getenv("RECOVERY_GUI_AUTOTEST") != ""

	h, _, _ := pGetModuleHandleW.Call(0)
	gInst = h
	hdc, _, _ := pGetDC.Call(0)
	d, _, _ := pGetDeviceCaps.Call(hdc, 90) // LOGPIXELSY
	pReleaseDC.Call(0, hdc)
	app.dpi = int(d)
	if app.dpi < 96 {
		app.dpi = 96
	}
	makeFonts()
	setPalette(savedNight())

	cursor, _, _ := pLoadCursorW.Call(0, 32512)
	iconBig, _, _ := pLoadImageW.Call(gInst, uintptr(unsafe.Pointer(wstr("APP"))), 1, 32, 32, 0)
	iconSm, _, _ := pLoadImageW.Call(gInst, uintptr(unsafe.Pointer(wstr("APP"))), 1, 16, 16, 0)
	wc := wndclassex{
		cbSize: uint32(unsafe.Sizeof(wndclassex{})), style: 3,
		lpfnWndProc: syscall.NewCallback(mainProc), hInstance: syscall.Handle(gInst),
		hIcon: syscall.Handle(iconBig), hCursor: syscall.Handle(cursor),
		lpszClassName: wstr("RecoveryMain"), hIconSm: syscall.Handle(iconSm),
	}
	if r, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return 1
	}
	registerCanvasClass(syscall.Handle(cursor))
	registerBarClass(syscall.Handle(cursor))
	registerTableClass(syscall.Handle(cursor), syscall.Handle(iconBig), syscall.Handle(iconSm))

	// Tamaño inicial: 1100×720 (ajustado a la pantalla).
	r := rect{0, 0, int32(sc(1100)), int32(sc(720))}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), wsOverlappedWin, 0, wsExControlPar)
	w, ht := int(r.right-r.left), int(r.bottom-r.top)
	var work rect
	pSystemParametersInf.Call(0x30, 0, uintptr(unsafe.Pointer(&work)), 0) // SPI_GETWORKAREA
	if ww := int(work.right - work.left); ww > 0 && w > ww {
		w = ww
	}
	if wh := int(work.bottom - work.top); wh > 0 && ht > wh {
		ht = wh
	}
	x := int(work.left) + (int(work.right-work.left)-w)/2
	y := int(work.top) + (int(work.bottom-work.top)-ht)/2
	hw, _, _ := pCreateWindowExW.Call(wsExControlPar, uintptr(unsafe.Pointer(wstr("RecoveryMain"))),
		uintptr(unsafe.Pointer(wstr("Recovery · Delio"))), wsOverlappedWin|wsClipChildren,
		uintptr(max(x, 0)), uintptr(max(y, 0)), uintptr(w), uintptr(ht), 0, 0, gInst, 0)
	if hw == 0 {
		return 1
	}
	app.hwnd = syscall.Handle(hw)
	styleCaption(app.hwnd)
	if pGetDpiForWindow.Find() == nil {
		if wd, _, _ := pGetDpiForWindow.Call(hw); int(wd) != app.dpi && wd >= 96 {
			app.dpi = int(wd)
			makeFonts()
		}
	}
	buildHome()
	buildExplorer()
	buildImagePage()
	buildRepairPage()
	buildMenuPage()
	buildBackupPage()
	applyTheme()
	fillDrives()
	first := appMode
	if startScreen != "" {
		first = startScreen
	}
	switch first {
	case "recuperar":
		setText(app.hwnd, "Recovery · Delio")
		showPage(pageHome)
	case "reparar":
		setText(app.hwnd, "Reparador · Delio")
		showPage(pageRepair)
	case "respaldo":
		setText(app.hwnd, "Respaldo · Delio")
		bkRefresh()
		showPage(pageBackup)
	default:
		setText(app.hwnd, "Herramientas · Delio")
		updateMenuStatus()
		showPage(pageMenu)
	}
	if inMenu() {
		setText(app.hwnd, "Herramientas · Delio")
	}
	pShowWindow.Call(hw, swShow)
	pUpdateWindow.Call(hw)
	if !isAdmin() && !app.autotest && app.page == pageHome {
		msgBox(app.hwnd, "El programa no tiene permisos de administrador, así que no podrá leer las unidades.\n\n"+
			"Ciérralo, haz clic derecho sobre recovery.exe y elige «Ejecutar como administrador».",
			"Recovery", mbIconWarning)
	}
	if app.autotest && !autotestRepair() && !autotestBackup() {
		if app.page == pageMenu && os.Getenv("RECOVERY_GUI_SRC") != "" {
			showPage(pageHome)
		}
		autotestStart()
	}

	var m tMsg
	pGetAncestor := user32.NewProc("GetAncestor")
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		root, _, _ := pGetAncestor.Call(uintptr(m.hwnd), 2) // GA_ROOT
		if root != 0 {
			if done, _, _ := pIsDialogMessageW.Call(root, uintptr(unsafe.Pointer(&m))); done != 0 {
				continue
			}
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return 0
}

// ---------------------------------------------------------------- pantalla de inicio

func buildHome() {
	a := app
	a.hTitle = ctl("STATIC", "Recovery", wsVisible, 0, 0, fTitle)
	a.hSub = ctl("STATIC", "Recupera archivos borrados, de discos formateados o de equipos restaurados. "+
		"Solo lee el disco: nunca escribe en él.", wsVisible, 0, 0, fSub)
	a.muted[a.hSub] = true
	a.hSect = ctl("STATIC", "¿Dónde estaban los archivos?", wsVisible, 0, 0, fSection)
	a.drives = newVList(a.hwnd, idDrives, []vcol{
		{"Unidad", 380, false}, {"Sistema de archivos", 130, false}, {"Capacidad", 110, true},
		{"Libre", 110, true}, {"Tipo", 260, false},
	}, false, sysLargeList(), true)
	a.drives.text = driveText
	a.drives.image = driveImage
	a.drives.onActivate = func(int) { startScan() }
	a.hQuick = button("Análisis rápido: solo lo borrado hace poco (tarda minutos). Desmárcalo si el equipo se formateó o restauró.",
		wsVisible|wsTabstop, idQuick, btnCheck, fBody)
	a.hHint = ctl("STATIC", "Consejo: guarda siempre lo recuperado en OTRO disco (externo o USB) y usa el equipo lo menos posible hasta terminar.",
		wsVisible, 0, 0, fBody)
	a.muted[a.hHint] = true
	a.hScan = button("Buscar archivos", wsVisible|wsTabstop, idScan, btnFill, fButton)
	a.hDisk = button("Estado de los discos", wsVisible|wsTabstop, idDisk, btnLink, fBold)
	a.hProgs = button("Programas que más ocupan", wsVisible|wsTabstop, idProgs, btnLink, fBold)
	a.hVer = ctl("STATIC", "Recovery "+version+" · delioph.tech", wsVisible|0x2, 0, 0, fBody) // SS_RIGHT
	a.muted[a.hVer] = true
	a.hMakeImg = button("Crear imagen del disco…", wsVisible|wsTabstop, idMakeImage, btnFrame, fBold)
	a.hOpenImg = button("Abrir imagen…", wsVisible|wsTabstop, idOpenImage, btnFrame, fBold)
	a.hNight = button("Noche", wsVisible|wsTabstop, idNight, btnFrame, fBold)
	a.hRepair = button("Reparar archivos dañados…", wsVisible|wsTabstop, idHomeRepair, btnFrame, fBold)
	a.hHomeMenu = button("‹  Menú", wsTabstop, idHomeMenu, btnFrame, fBold)
	a.homeCtls = []syscall.Handle{a.hTitle, a.hSub, a.hSect, a.drives.hwnd, a.hQuick, a.hHint, a.hScan,
		a.hDisk, a.hProgs, a.hVer, a.hMakeImg, a.hOpenImg, a.hNight, a.hRepair, a.hHomeMenu}
}

var sysLarge uintptr

func sysLargeList() uintptr {
	if sysLarge == 0 {
		var sfi shfileinfo
		r, _, _ := pSHGetFileInfoW.Call(uintptr(unsafe.Pointer(wstr("C:\\"))), 0,
			uintptr(unsafe.Pointer(&sfi)), unsafe.Sizeof(sfi), shgfiSysIconIdx|shgfiLargeIcon)
		sysLarge = r
	}
	return sysLarge
}

func sysSmallList() uintptr {
	if sysImgSmall == 0 {
		fileIcon("/")
	}
	return sysImgSmall
}

func fillDrives() {
	app.sources = listSources()
	app.drives.setCount(len(app.sources))
	pick := 0
	sys := strings.TrimSuffix(strings.ToUpper(os.Getenv("SystemDrive")), ":")
	for i, s := range app.sources {
		if s.Letter != "" && (sys == "" || s.Letter == sys) {
			pick = i
			break
		}
	}
	app.drives.selectRow(pick)
}

func sourceName(s Source) string {
	if s.Disk {
		n := strings.TrimPrefix(s.Path, `\\.\PhysicalDrive`)
		name := "Disco físico " + n
		if s.Model != "" {
			name += " — " + s.Model
		}
		return name
	}
	label := s.VolLabel
	if label == "" {
		label = "Disco local"
		if s.Removable {
			label = "Unidad extraíble"
		}
	}
	return fmt.Sprintf("%s (%s:)", label, s.Letter)
}

func driveText(row, col int) string {
	s := app.sources[row]
	switch col {
	case 0:
		return sourceName(s)
	case 1:
		if s.Disk {
			return "—"
		}
		if s.FS == "" {
			return "Sin formato / dañado"
		}
		return s.FS
	case 2:
		return humanSize(s.Size)
	case 3:
		if s.Disk || s.Free <= 0 {
			return ""
		}
		return humanSize(s.Free)
	case 4:
		var parts []string
		if s.Disk {
			parts = append(parts, "Disco completo")
			if s.Letters != "" {
				parts = append(parts, "contiene "+s.Letters)
			}
		} else if s.Removable {
			parts = append(parts, "Extraíble")
		} else {
			parts = append(parts, "Unidad")
		}
		if s.Bus != "" {
			parts = append(parts, s.Bus)
		}
		if s.SSD {
			parts = append(parts, "SSD")
		}
		return strings.Join(parts, " · ")
	}
	return ""
}

func driveImage(row int) int32 {
	s := app.sources[row]
	if s.Letter != "" {
		return driveIcon(s.Letter + `:\`)
	}
	return driveIcon(`C:\`)
}

// ---------------------------------------------------------------- páginas y diseño

func showPage(p int) {
	app.page = p
	for _, h := range app.homeCtls {
		show(h, p == pageHome)
	}
	for _, h := range app.expCtls {
		show(h, p == pageExplorer)
	}
	for _, h := range img.ctls {
		show(h, p == pageImage)
	}
	for _, h := range rep.ctls {
		show(h, p == pageRepair)
	}
	for _, h := range menu.ctls {
		show(h, p == pageMenu)
	}
	for _, h := range bk.ctls {
		show(h, p == pageBackup)
	}
	layout()
	invalidate(app.hwnd)
	if p == pageHome {
		pSetFocus.Call(uintptr(app.drives.hwnd))
	}
}

func layout() {
	a := app
	W, H := clientSize(a.hwnd)
	if W == 0 {
		return
	}
	if a.page == pageHome {
		m := sc(32)
		// Cabecera: logotipo Delio | Recovery, y a la derecha el menú.
		lx := m + logoWidth(sc(homeLogoH)) + sc(36)
		moveWin(a.hTitle, lx, sc(26), sc(260), sc(40))
		moveWin(a.hProgs, W-m-sc(214), sc(28), sc(214), sc(36))
		moveWin(a.hDisk, W-m-sc(214)-sc(14)-sc(170), sc(28), sc(170), sc(36))
		moveWin(a.hSub, m, sc(86), W-2*m, sc(22))
		// (la onda se dibuja entre sc(118) y sc(158))
		moveWin(a.hSect, m, sc(170), W-2*m, sc(24))
		moveFramed(a.hwnd, a.drives.hwnd, m, sc(200), W-2*m, H-sc(200)-sc(170))
		moveWin(a.hQuick, m, H-sc(152), W-2*m-sc(270), sc(24))
		moveWin(a.hHint, m, H-sc(120), W-2*m-sc(270), sc(40))
		moveWin(a.hScan, W-m-sc(240), H-sc(154), sc(240), sc(52))
		moveWin(a.hMakeImg, m, H-sc(62), sc(220), sc(40))
		moveWin(a.hOpenImg, m+sc(232), H-sc(62), sc(160), sc(40))
		moveWin(a.hRepair, m+sc(404), H-sc(62), sc(240), sc(40))
		moveWin(a.hHomeMenu, m+sc(404), H-sc(62), sc(120), sc(40))
		show(a.hRepair, !inMenu())
		show(a.hHomeMenu, inMenu())
		moveWin(a.hNight, W-m-sc(96), H-sc(62), sc(96), sc(40))
		moveWin(a.hVer, W-m-sc(96)-sc(16)-sc(260), H-sc(52), sc(260), sc(20))
		show(a.hVer, W >= sc(1060))
		fitLastColumn(a.drives)
		invalidate(a.hwnd)
		return
	}
	if a.page == pageImage {
		layoutImagePage(W, H)
		return
	}
	if a.page == pageRepair {
		layoutRepairPage(W, H)
		return
	}
	if a.page == pageMenu {
		layoutMenuPage(W, H)
		return
	}
	if a.page == pageBackup {
		layoutBackupPage(W, H)
		return
	}
	busy := a.busy != busyNone
	moveWin(a.hBack, sc(16), sc(16), sc(96), sc(36))
	// Arriba a la derecha: [Archivos|Carpetas] y el buscador.
	sw := sc(300)
	if W < sc(1100) {
		sw = sc(240)
	}
	segW := sc(92)
	segX := W - sc(16) - sw - sc(10) - 2*segW + lw()
	moveWin(a.hSegFiles, segX, sc(16), segW, sc(36))
	moveWin(a.hSegFolders, segX+segW-lw(), sc(16), segW, sc(36))
	moveWin(a.hSrc, sc(126), sc(10), segX-sc(16)-sc(126), sc(24))
	moveWin(a.hStatus, sc(126), sc(36), segX-sc(16)-sc(126), sc(20))
	moveField(a.hwnd, a.hSearch, W-sc(16)-sw, sc(16), sw, sc(36))
	y0 := sc(72)
	if busy {
		moveWin(a.hProgress, sc(16), sc(76), W-sc(32)-sc(130), sc(16))
		moveWin(a.hStop, W-sc(16)-sc(120), sc(68), sc(120), sc(32))
		y0 = sc(108)
	}
	show(a.hProgress, busy && a.page == pageExplorer)
	show(a.hStop, busy && a.page == pageExplorer)
	yb := H - sc(64)
	treeW := sc(270)
	pw := sc(290)
	if W < sc(1000) {
		pw = sc(230)
	}
	px := W - sc(16) - pw
	lx := sc(16) + treeW + sc(10)
	lw := px - sc(10) - lx
	moveFramed(a.hwnd, a.hTree, sc(16), y0, treeW, yb-y0-sc(8))
	moveWin(a.hCrumb, lx, y0, lw, sc(22))
	moveFramed(a.hwnd, a.list.hwnd, lx, y0+sc(26), lw, yb-y0-sc(26)-sc(8))
	// La columna «Nombre» ocupa lo que queda para que se vean nombre,
	// tamaño, fecha y estado sin desplazarse. En los resultados de «buscar
	// carpeta» se ven nombre, ubicación y estado (puede haber varias copias).
	if a.folderCols {
		avail := lw - sc(140) - sc(28)
		nameW := avail * 2 / 5
		if nameW < sc(140) {
			nameW = sc(140)
		}
		locW := avail - nameW
		if locW < sc(180) {
			locW = sc(180)
		}
		a.list.cols[0].width = nameW * 96 / a.dpi
		a.list.cols[5].width = locW * 96 / a.dpi
	} else {
		nameW := lw - sc(80+118+140) - sc(28)
		if nameW < sc(180) {
			nameW = sc(180)
		}
		a.list.cols[0].width = nameW * 96 / a.dpi
		a.list.cols[5].width = 260
	}
	a.list.rescale()
	moveWin(a.hOverlay, lx+sc(20), y0+sc(90), lw-sc(40), sc(60))
	moveFramed(a.hwnd, a.hCanvas, px, y0, pw, sc(230))
	moveWin(a.hPrevName, px, y0+sc(240), pw, sc(40))
	moveWin(a.hPrevI, px, y0+sc(282), pw, yb-y0-sc(282)-sc(8))
	moveWin(a.hHideSys, sc(16), yb+sc(20), sc(316), sc(24))
	moveWin(a.hSummary, sc(340), yb+sc(14), sc(136), sc(36))
	moveWin(a.hVerify, sc(486), yb+sc(14), sc(160), sc(36))
	moveWin(a.hSelInfo, sc(658), yb+sc(22), W-sc(658)-sc(16)-sc(214), sc(22))
	moveWin(a.hRecov, W-sc(16)-sc(196), yb+sc(10), sc(196), sc(46))
	invalidate(a.hwnd)
}

// ---------------------------------------------------------------- ventana principal

func mainProc(hwnd syscall.Handle, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmSize:
		layout()
		return 0
	case wmGetMinMax:
		wrI32(lp, 24, int32(sc(880)))
		wrI32(lp, 28, int32(sc(580)))
		return 0
	case wmEraseBkgnd:
		w, h := clientSize(hwnd)
		fillR(wp, rect{0, 0, int32(w), int32(h)}, app.brBg)
		return 1
	case wmPaint:
		paintMain(hwnd)
		return 0
	case wmSetCursor:
		if onSetCursor(wp) {
			return 1
		}
	case wmCtlColorStat:
		hdc := wp
		if app.muted[syscall.Handle(lp)] {
			pSetTextColor.Call(hdc, uintptr(cMuted))
		} else {
			pSetTextColor.Call(hdc, uintptr(cText))
		}
		if syscall.Handle(lp) == app.hOverlay {
			pSetBkColor.Call(hdc, uintptr(cPanel)) // va encima de la lista
			return uintptr(app.brPanel)
		}
		pSetBkColor.Call(hdc, uintptr(cBg))
		return uintptr(app.brBg)
	case wmCtlColorEdit:
		pSetTextColor.Call(wp, uintptr(cText))
		pSetBkColor.Call(wp, uintptr(cPanel))
		return uintptr(app.brPanel)
	case wmDrawItem:
		drawButton(lp)
		return 1
	case wmNotify:
		if ok, r := handleListNotify(lp); ok {
			return r
		}
		if syscall.Handle(rdPtr(lp, 0)) == app.hTree {
			if rdU32(lp, 16) == nmCustomDraw {
				return treeCustomDraw(lp)
			}
			treeNotify(lp)
		}
		return 0
	case wmCommand:
		code := int(wp >> 16 & 0xFFFF)
		if kind, ok := btnKind[syscall.Handle(lp)]; ok && lp != 0 {
			if kind == btnCheck && (code == 0 || code == 5) { // BN_CLICKED / BN_DOUBLECLICKED
				h := syscall.Handle(lp)
				ownerChecks[h] = !ownerChecks[h]
				invalidate(h)
				code = 0
			}
			if code != 0 {
				return 0 // los botones actúan una sola vez aunque se haga doble clic
			}
		}
		onCommand(int(wp&0xFFFF), code)
		return 0
	case wmTimer:
		pKillTimer.Call(uintptr(hwnd), wp)
		switch wp {
		case timerSearch:
			applySearch()
		case timerPreview:
			startPreview()
		}
		return 0
	case wmAppProgress:
		atomic.StoreInt32(&app.progPnd, 0)
		updateStatus()
		return 0
	case wmAppResults:
		rebuildTree()
		updateStatus()
		return 0
	case wmAppDone:
		scanDone()
		return 0
	case wmAppPreview:
		if int64(wp) == atomic.LoadInt64(&app.prevGen) {
			invalidate(app.hCanvas)
		}
		return 0
	case wmAppRecProg:
		updateStatus()
		return 0
	case wmAppRecDone:
		recoverDone()
		return 0
	case wmAppTable:
		tableUpdate(syscall.Handle(wp))
		return 0
	case wmAppImgProg:
		atomic.StoreInt32(&app.progPnd, 0)
		updateImageStats()
		return 0
	case wmAppImgDone:
		imageDone()
		return 0
	case wmAppVerProg:
		atomic.StoreInt32(&app.progPnd, 0)
		updateStatus()
		return 0
	case wmAppVerDone:
		verifyDone()
		return 0
	case wmAppRepProg:
		repairProgress()
		return 0
	case wmAppRepDone:
		repairDone()
		return 0
	case wmAppBkProg:
		backupProgress(int(wp))
		return 0
	case wmAppBkDone:
		backupDone()
		return 0
	case wmDpiChanged:
		app.dpi = int(wp & 0xFFFF)
		makeFonts()
		applyFonts()
		for _, l := range gLists {
			l.rescale()
		}
		r := (*rect)(mem(lp))
		pSetWindowPos.Call(uintptr(hwnd), 0, uintptr(r.left), uintptr(r.top),
			uintptr(r.right-r.left), uintptr(r.bottom-r.top), 0x4|0x10) // NOZORDER|NOACTIVATE
		layout()
		return 0
	case wmClose:
		if app.busy != busyNone && !app.autotest {
			what := "el análisis"
			switch app.busy {
			case busyRecover:
				what = "la recuperación"
			case busyImage:
				what = "la imagen del disco (podrás continuarla después)"
			case busyRepair:
				what = "la revisión de archivos"
			case busyBackup:
				what = "el respaldo"
			}
			if msgBox(hwnd, "Todavía no ha terminado "+what+". ¿Salir de todos modos?", "Recovery",
				mbYesNo|mbIconQuest|mbDefButton2) != idYes {
				return 0
			}
			atomic.StoreInt32(&stopFlag, 1)
		}
		pDestroyWindow.Call(uintptr(hwnd))
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wp, lp)
	return r
}

const homeLogoH = 46 // alto del logotipo en la pantalla de inicio (96 ppp)

// paintMain dibuja lo que no son controles: el logotipo, la onda, las
// líneas que separan las zonas y los marcos de listas y campos.
func paintMain(hwnd syscall.Handle) {
	var ps paintstruct
	hdc, _, _ := pBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	W, H := clientSize(hwnd)
	t := int32(lw())
	switch app.page {
	case pageHome:
		m := sc(32)
		lh := sc(homeLogoH)
		drawLogo(hdc, m, sc(22), lh)
		// Raya vertical entre «Delio» y «Recovery».
		x := int32(m + logoWidth(lh) + sc(17))
		fillR(hdc, rect{x, int32(sc(26)), x + t, int32(sc(22) + lh)}, app.brStrong)
		drawWave(hdc, 0, W, sc(138), sc(18))
	case pageExplorer:
		y := sc(64)
		if app.busy != busyNone {
			y = sc(104)
		}
		for _, yy := range []int{y, H - sc(64)} {
			fillR(hdc, rect{0, int32(yy), int32(W), int32(yy) + t}, app.brStrong)
		}
	case pageImage:
		fillR(hdc, rect{0, int32(sc(120)), int32(W), int32(sc(120)) + t}, app.brStrong)
	case pageMenu:
		m := sc(32)
		lh := sc(homeLogoH)
		drawLogo(hdc, m, sc(22), lh)
		x := int32(m + logoWidth(lh) + sc(17))
		fillR(hdc, rect{x, int32(sc(26)), x + t, int32(sc(22) + lh)}, app.brStrong)
		drawWave(hdc, 0, W, sc(138), sc(18))
	case pageRepair, pageBackup:
		m := sc(32)
		lh := sc(repLogoH)
		drawLogo(hdc, m, sc(14), lh)
		x := int32(m + logoWidth(lh) + sc(14))
		fillR(hdc, rect{x, int32(sc(16)), x + t, int32(sc(14) + lh)}, app.brStrong)
		fillR(hdc, rect{0, int32(sc(98)), int32(W), int32(sc(98)) + t}, app.brStrong)
	}
	paintBoxes(hdc, hwnd)
	pEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
}

func onCommand(id, code int) {
	switch id {
	case idScan:
		startScan()
	case idDisk:
		openDiskWindow()
	case idProgs:
		openProgramsWindow()
	case idBack:
		goHome()
	case idStop:
		atomic.StoreInt32(&stopFlag, 1)
		setText(app.hStatus, "Deteniendo... enseguida verás lo encontrado.")
		enable(app.hStop, false)
	case idSearch:
		if code == enChange && !app.selfEdit {
			pSetTimer.Call(uintptr(app.hwnd), timerSearch, 300, 0)
		}
	case idSegFiles:
		setFolderMode(false)
	case idSegFolders:
		setFolderMode(true)
	case idHideSys:
		rebuildTree()
	case idNight:
		toggleNight()
	case idSummary:
		showSummary()
	case idVerify:
		doVerify()
	case idRecover:
		doRecover("")
	case idMakeImage:
		startImage("")
	case idOpenImage:
		openImageFile()
	case idImgStop:
		atomic.StoreInt32(&stopFlag, 1)
		enable(img.hStop, false)
		setText(img.hTitle, "Deteniendo la imagen…")
	case idImgBack:
		if app.busy == busyNone {
			fillDrives()
			showPage(pageHome)
		}
	case idMenuRecover, idMenuRepair, idMenuBackup, idMenuBack:
		menuOpen(id)
	case idHomeMenu:
		if app.busy == busyNone {
			menuOpen(idMenuBack)
		}
	case idBkSrc, idBkDest, idBkRun, idBkSchedule, idBkStop, idBkOpen, idBkRemove:
		backupCommand(id, code)
	case idHomeRepair:
		if strings.TrimSpace(getText(rep.hSrc)) == "" && app.recDest != "" {
			setText(rep.hSrc, app.recDest) // lo último que se recuperó
			repSuggestOut()
		}
		showPage(pageRepair)
	case idRepSrc, idRepSrcEdit, idRepIn, idRepOut, idRepRun, idRepStop, idRepOpen, idRepBack:
		repairCommand(id, code)
	case idImgAnalyze:
		if img.job != nil {
			analyzePath(img.job.ImgPath, "Imagen de "+img.srcName)
		}
	}
}

// browseFolder abre el diálogo de Windows para elegir una carpeta.
func browseFolder(title string) string {
	bi := browseinfo{hwndOwner: app.hwnd, lpszTitle: wstr(title), ulFlags: 0x1 | 0x10 | 0x40}
	pidl, _, _ := pSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	buf := make([]uint16, 520)
	pSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	return syscall.UTF16ToString(buf)
}

func openFolder(p string) {
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(wstr("open"))), uintptr(unsafe.Pointer(wstr(p))), 0, 0, swShow)
}
