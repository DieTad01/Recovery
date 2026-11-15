//go:build windows

package main

// Interfaz gráfica para Windows, escrita directamente sobre la API Win32
// (sin dependencias ni cgo, para poder compilarse desde cualquier sistema).
// Una sola ventana: elegir unidad y destino, opciones y un botón grande.

import (
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	procRegisterClassExW   = user32.NewProc("RegisterClassExW")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW     = user32.NewProc("DefWindowProcW")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procSendMessageW       = user32.NewProc("SendMessageW")
	procPostMessageW       = user32.NewProc("PostMessageW")
	procSetWindowTextW     = user32.NewProc("SetWindowTextW")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procGetWindowTextLenW  = user32.NewProc("GetWindowTextLengthW")
	procEnableWindow       = user32.NewProc("EnableWindow")
	procShowWindow         = user32.NewProc("ShowWindow")
	procUpdateWindow       = user32.NewProc("UpdateWindow")
	procLoadCursorW        = user32.NewProc("LoadCursorW")
	procLoadIconW          = user32.NewProc("LoadIconW")
	procMessageBoxW        = user32.NewProc("MessageBoxW")
	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procGetStockObject     = gdi32.NewProc("GetStockObject")
	procCreateFontW        = gdi32.NewProc("CreateFontW")
	procInitCommonControls = comctl32.NewProc("InitCommonControlsEx")
	procSHBrowseForFolderW = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDL   = shell32.NewProc("SHGetPathFromIDListW")
	procCoInitialize       = ole32.NewProc("CoInitialize")
)

const (
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsBorder       = 0x00800000
	wsTabstop      = 0x00010000
	wsVScroll      = 0x00200000
	wsGroup        = 0x00020000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsExClientEdge = 0x00000200

	bsDefPushButton = 0x00000001
	bsAutoRadio     = 0x00000009
	bsAutoCheckBox  = 0x00000003
	bsGroupBox      = 0x00000007
	esMultiline     = 0x00000004
	esAutoVScroll   = 0x00000040
	esReadonly      = 0x00000800
	cbsDropDownList = 0x00000003
	cbsHasStrings   = 0x00000200
	ssLeft          = 0x00000000

	wmDestroy    = 0x0002
	wmClose      = 0x0010
	wmCommand    = 0x0111
	wmSetFont    = 0x0030
	wmApp        = 0x8000
	wmSetRedraw  = 0x000B
	bnClicked    = 0
	cbnSelChange = 1

	cbAddString   = 0x0143
	cbGetCurSel   = 0x0147
	cbSetCurSel   = 0x014E
	emSetSel      = 0x00B1
	emReplaceSel  = 0x00C2
	emScrollCaret = 0x00B7
	bmGetCheck    = 0x00F0
	bmSetCheck    = 0x00F1
	bstChecked    = 1

	pbmSetRange32 = 0x0406
	pbmSetPos     = 0x0402
	pbmSetMarquee = 0x040A
	pbsMarquee    = 0x08

	swShow = 5

	mbOK          = 0
	mbIconError   = 0x10
	mbIconWarning = 0x30
	mbIconInfo    = 0x40

	defaultGUIFont = 17

	wmAppDone = wmApp + 1 // la recuperación terminó (enviado desde el worker)
)

// Identificadores de los controles.
const (
	idCombo = 1001 + iota
	idDest
	idBrowse
	idRadioPers
	idRadioAll
	idQuick
	idEmails
	idStart
	idCancel
	idOpen
	idExit
)

type wndclassex struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type tMsg struct {
	hwnd    syscall.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

type browseinfo struct {
	hwndOwner      syscall.Handle
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

type initCommonControlsEx struct {
	size uint32
	icc  uint32
}

// Estado global de la ventana.
var (
	gHwnd      syscall.Handle
	gFont      syscall.Handle
	gCombo     syscall.Handle
	gDest      syscall.Handle
	gRadioPers syscall.Handle
	gRadioAll  syscall.Handle
	gQuick     syscall.Handle
	gEmails    syscall.Handle
	gStart     syscall.Handle
	gCancel    syscall.Handle
	gOpen      syscall.Handle
	gLog       syscall.Handle
	gProgress  syscall.Handle
	gStatus    syscall.Handle

	gSources  []Source
	gCfg      *Config
	gRunning  int32
	gLastErr  error
	gAutotest bool
)

func sendMsg(h syscall.Handle, msg uint32, w, l uintptr) uintptr {
	r, _, _ := procSendMessageW.Call(uintptr(h), uintptr(msg), w, l)
	return r
}

func setText(h syscall.Handle, s string) {
	procSetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(utf16Ptr(s))))
}

func getText(h syscall.Handle) string {
	n, _, _ := procGetWindowTextLenW.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func enable(h syscall.Handle, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	procEnableWindow.Call(uintptr(h), v)
}

func isChecked(h syscall.Handle) bool {
	return sendMsg(h, bmGetCheck, 0, 0) == bstChecked
}

func setChecked(h syscall.Handle, on bool) {
	v := uintptr(0)
	if on {
		v = bstChecked
	}
	sendMsg(h, bmSetCheck, v, 0)
}

func messageBox(text, title string, flags uint32) {
	procMessageBoxW.Call(uintptr(gHwnd),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr(title))), uintptr(flags))
}

func newControl(class, text string, style int64, x, y, w, h int, id int) syscall.Handle {
	hwnd, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr(class))),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(uint32(style)),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(gHwnd), uintptr(id), 0, 0)
	hc := syscall.Handle(hwnd)
	if gFont != 0 {
		sendMsg(hc, wmSetFont, uintptr(gFont), 1)
	}
	return hc
}

// appendLog añade una línea al cuadro de texto del informe.
func guiAppendLog(s string) {
	u := syscall.StringToUTF16(s + "\r\n")
	sendMsg(gLog, emSetSel, ^uintptr(0), ^uintptr(0))
	sendMsg(gLog, emReplaceSel, 0, uintptr(unsafe.Pointer(&u[0])))
	sendMsg(gLog, emScrollCaret, 0, 0)
}

// guiReporter envía la salida del motor a los controles de la ventana.
type guiReporter struct{}

func (guiReporter) line(s string)   { guiAppendLog(s) }
func (guiReporter) status(s string) { setText(gStatus, s) }
func (guiReporter) progress(frac float64) {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	sendMsg(gProgress, pbmSetPos, uintptr(int(frac*100)), 0)
}

func wndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCommand:
		id := int(wParam & 0xFFFF)
		notif := uint32(wParam>>16) & 0xFFFF
		switch id {
		case idStart:
			if notif == bnClicked {
				onStart()
			}
		case idCancel:
			if notif == bnClicked {
				atomic.StoreInt32(&stopFlag, 1)
				setText(gStatus, "Cancelando, espera un momento...")
			}
		case idBrowse:
			if notif == bnClicked {
				if p := browseFolder(); p != "" {
					setText(gDest, p)
				}
			}
		case idOpen:
			if notif == bnClicked {
				if p := getText(gDest); p != "" {
					procShellExecuteW.Call(0,
						uintptr(unsafe.Pointer(utf16Ptr("open"))),
						uintptr(unsafe.Pointer(utf16Ptr(p))), 0, 0, swShow)
				}
			}
		case idExit:
			if notif == bnClicked {
				procPostMessageW.Call(uintptr(gHwnd), wmClose, 0, 0)
			}
		}
		return 0
	case wmAppDone:
		onDone()
		return 0
	case wmClose:
		if atomic.LoadInt32(&gRunning) != 0 {
			messageBox("Hay una recuperación en curso. Pulsa «Cancelar» y espera a que termine antes de cerrar.",
				"Recovery", mbIconWarning)
			return 0
		}
		procPostQuitMessage.Call(0)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

// browseFolder abre el diálogo de selección de carpeta de Windows.
func browseFolder() string {
	bi := browseinfo{
		hwndOwner: gHwnd,
		lpszTitle: utf16Ptr("Elige la carpeta donde guardar lo recuperado (en OTRA unidad)"),
		ulFlags:   0x0001 | 0x0010 | 0x0040, // RETURNONLYFSDIRS | EDITBOX | NEWDIALOGSTYLE
	}
	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	buf := make([]uint16, 520)
	procSHGetPathFromIDL.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	return syscall.UTF16ToString(buf)
}

// onStart valida los datos y lanza la recuperación en segundo plano.
func onStart() {
	if atomic.LoadInt32(&gRunning) != 0 {
		return
	}
	sel := int(sendMsg(gCombo, cbGetCurSel, 0, 0))
	if sel < 0 || sel >= len(gSources) {
		messageBox("Elige primero la unidad que quieres recuperar.", "Recovery", mbIconWarning)
		return
	}
	dst := getText(gDest)
	if dst == "" {
		messageBox("Elige la carpeta donde guardar lo recuperado.", "Recovery", mbIconWarning)
		return
	}
	src := gSources[sel].Path
	if fatal, warn := checkDestination(src, dst); fatal != "" {
		messageBox(fatal, "No se puede guardar ahí", mbIconError)
		return
	} else if warn != "" {
		messageBox(warn, "Aviso", mbIconWarning)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		messageBox("No se puede crear la carpeta de destino:\n"+err.Error(), "Error", mbIconError)
		return
	}

	gCfg.Source = src
	gCfg.Out = dst
	gCfg.Todo = isChecked(gRadioAll)
	gCfg.Quick = isChecked(gQuick)
	gCfg.Emails = isChecked(gEmails)
	gCfg.Yes = true
	gCfg.Interactive = false

	for _, h := range []syscall.Handle{gCombo, gDest, gBrowseBtn, gRadioPers, gRadioAll, gQuick, gEmails, gStart, gOpen} {
		enable(h, false)
	}
	enable(gCancel, true)
	setText(gLog, "")
	sendMsg(gProgress, pbmSetPos, 0, 0)
	out = guiReporter{}
	logLines = nil
	atomic.StoreInt32(&stopFlag, 0)
	atomic.StoreInt32(&gRunning, 1)

	go func() {
		gLastErr = run(gCfg)
		procPostMessageW.Call(uintptr(gHwnd), wmAppDone, 0, 0)
	}()
}

var gBrowseBtn syscall.Handle

// onDone se ejecuta al terminar la recuperación.
func onDone() {
	atomic.StoreInt32(&gRunning, 0)
	for _, h := range []syscall.Handle{gCombo, gDest, gBrowseBtn, gRadioPers, gRadioAll, gQuick, gEmails, gStart, gOpen} {
		enable(h, true)
	}
	enable(gCancel, false)
	sendMsg(gProgress, pbmSetPos, 100, 0)
	if gLastErr != nil {
		setText(gStatus, "Error: "+gLastErr.Error())
		messageBox("No se pudo completar:\n"+gLastErr.Error(), "Recovery", mbIconError)
	} else {
		setText(gStatus, "Listo. Pulsa «Abrir carpeta» para ver lo recuperado.")
		if !gAutotest {
			messageBox("Recuperación terminada.\n\nRevisa el resumen y pulsa «Abrir carpeta» para ver los archivos.",
				"Recovery", mbIconInfo)
		}
	}
	if gAutotest && os.Getenv("RECOVERY_GUI_KEEP") == "" {
		procPostQuitMessage.Call(0)
	}
}

func createFont() syscall.Handle {
	// Altura -16 (negativa = tamaño de carácter en píxeles). Se usa una
	// variable para que la conversión a uintptr sea en tiempo de ejecución.
	height := -16
	h, _, _ := procCreateFontW.Call(
		uintptr(height),
		0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))))
	if h == 0 {
		s, _, _ := procGetStockObject.Call(defaultGUIFont)
		return syscall.Handle(s)
	}
	return syscall.Handle(h)
}

func buildWindow() bool {
	// Controles comunes (barra de progreso).
	icc := initCommonControlsEx{size: 8, icc: 0x20 | 0x10}
	procInitCommonControls.Call(uintptr(unsafe.Pointer(&icc)))
	procCoInitialize.Call(0)

	hInst, _, _ := procGetModuleHandleW.Call(0)
	cursor, _, _ := procLoadCursorW.Call(0, 32512) // IDC_ARROW
	icon, _, _ := procLoadIconW.Call(0, 32512)     // IDI_APPLICATION
	className := utf16Ptr("RecoveryWnd")
	wc := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc:   syscall.NewCallback(wndProc),
		hInstance:     syscall.Handle(hInst),
		hIcon:         syscall.Handle(icon),
		hCursor:       syscall.Handle(cursor),
		hbrBackground: syscall.Handle(16), // COLOR_BTNFACE+1
		lpszClassName: className,
		hIconSm:       syscall.Handle(icon),
	}
	if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return false
	}
	style := wsCaption | wsSysMenu | wsMinimizeBox
	hwnd, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("Recovery "+version+" — Recuperador de archivos"))),
		uintptr(uint32(style)),
		80, 40, 726, 650, 0, 0, hInst, 0)
	if hwnd == 0 {
		return false
	}
	gHwnd = syscall.Handle(hwnd)
	gFont = createFont()

	newControl("STATIC",
		"Recupera archivos de un equipo restaurado, formateado o con archivos borrados. Solo LEE\r\n"+
			"la unidad; nunca escribe en ella. Guarda lo recuperado en OTRO disco (externo o USB).",
		wsChild|wsVisible|ssLeft, 15, 12, 690, 40, 0)

	newControl("STATIC", "Unidad a recuperar:", wsChild|wsVisible|ssLeft, 15, 64, 155, 20, 0)
	gCombo = newControl("COMBOBOX", "", wsChild|wsVisible|wsTabstop|wsVScroll|cbsDropDownList|cbsHasStrings,
		175, 60, 520, 300, idCombo)

	newControl("STATIC", "Guardar en:", wsChild|wsVisible|ssLeft, 15, 97, 155, 20, 0)
	gDest = newControl("EDIT", "", wsChild|wsVisible|wsTabstop|wsBorder, 175, 95, 410, 24, idDest)
	gBrowseBtn = newControl("BUTTON", "Examinar...", wsChild|wsVisible|wsTabstop, 595, 93, 100, 27, idBrowse)

	newControl("STATIC", "¿Qué recuperar?", wsChild|wsVisible|ssLeft, 15, 130, 155, 20, 0)
	gRadioPers = newControl("BUTTON", "Mis archivos personales: documentos, fotos, videos, música...",
		wsChild|wsVisible|wsTabstop|wsGroup|bsAutoRadio, 175, 128, 520, 22, idRadioPers)
	gRadioAll = newControl("BUTTON", "Absolutamente todo, incluido Windows y los programas (mucho más)",
		wsChild|wsVisible|wsTabstop|bsAutoRadio, 175, 152, 520, 22, idRadioAll)
	setChecked(gRadioPers, true)

	gQuick = newControl("BUTTON", "Búsqueda rápida (solo lo borrado hace poco; es mucho más corta)",
		wsChild|wsVisible|wsTabstop|bsAutoCheckBox, 175, 182, 520, 22, idQuick)
	gEmails = newControl("BUTTON", "Buscar también mi correo: Hotmail, Outlook, Gmail...  (tarda más)",
		wsChild|wsVisible|wsTabstop|bsAutoCheckBox, 175, 206, 520, 22, idEmails)

	gStart = newControl("BUTTON", "Recuperar", wsChild|wsVisible|wsTabstop|bsDefPushButton, 175, 240, 185, 36, idStart)
	gCancel = newControl("BUTTON", "Cancelar", wsChild|wsVisible|wsTabstop, 375, 240, 130, 36, idCancel)
	enable(gCancel, false)

	gProgress = newControl("msctls_progress32", "", wsChild|wsVisible, 15, 288, 680, 20, 0)
	sendMsg(gProgress, pbmSetRange32, 0, 100)

	gLog = newControl("EDIT", "",
		wsChild|wsVisible|wsBorder|wsVScroll|esMultiline|esReadonly|esAutoVScroll,
		15, 315, 680, 238, 0)
	// Borde hundido para el cuadro de texto.
	procSetWindowLongPtrAddExStyle(gLog, wsExClientEdge)

	gStatus = newControl("STATIC", "Elige la unidad y la carpeta de destino, y pulsa «Recuperar».",
		wsChild|wsVisible|ssLeft, 15, 562, 450, 20, 0)
	gOpen = newControl("BUTTON", "Abrir carpeta", wsChild|wsVisible|wsTabstop, 475, 558, 115, 28, idOpen)
	enable(gOpen, false)
	newControl("BUTTON", "Salir", wsChild|wsVisible|wsTabstop, 598, 558, 97, 28, idExit)

	fillSources()
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	return true
}

// procSetWindowLongPtrAddExStyle añade un estilo extendido a un control.
var procSetWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
var procGetWindowLongPtrW = user32.NewProc("GetWindowLongPtrW")
var procSetWindowPos = user32.NewProc("SetWindowPos")

func procSetWindowLongPtrAddExStyle(h syscall.Handle, ex uintptr) {
	const gwlExStyle = ^uintptr(19) // -20
	cur, _, _ := procGetWindowLongPtrW.Call(uintptr(h), gwlExStyle)
	procSetWindowLongPtrW.Call(uintptr(h), gwlExStyle, cur|ex)
	// Fuerza el redibujado del marco.
	procSetWindowPos.Call(uintptr(h), 0, 0, 0, 0, 0, 0x0001|0x0002|0x0004|0x0020) // NOMOVE|NOSIZE|NOZORDER|FRAMECHANGED
}

// fillSources rellena el desplegable con las unidades detectadas.
func fillSources() {
	gSources = listSources()
	for _, s := range gSources {
		sendMsg(gCombo, cbAddString, 0, uintptr(unsafe.Pointer(utf16Ptr(s.Label))))
	}
	// Preselecciona la primera unidad con letra (normalmente C:).
	pick := 0
	for i, s := range gSources {
		if s.Letter != "" {
			pick = i
			break
		}
	}
	if len(gSources) > 0 {
		sendMsg(gCombo, cbSetCurSel, uintptr(pick), 0)
	}
}

// runGUIApp crea la ventana y ejecuta el bucle de mensajes.
func runGUIApp(cfg *Config) int {
	// La ventana y su bucle de mensajes deben vivir en el MISMO hilo del
	// sistema operativo; si no, Windows no entrega los mensajes.
	runtime.LockOSThread()
	gCfg = cfg
	keepAwake()
	if os.Getenv("RECOVERY_GUI_AUTOTEST") != "" {
		gAutotest = true
	}
	if !buildWindow() {
		messageBox("No se pudo abrir la ventana del programa.\n\nAbre una consola (cmd) como administrador y ejecuta:\n   recovery.exe -consola",
			"Recovery", mbIconError)
		return 1
	}
	if gAutotest {
		autotestDrive()
	}
	var msg tMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return 0
}

// autotestDrive rellena la ventana desde variables de entorno y pulsa
// «Recuperar» automáticamente (solo para las pruebas).
func autotestDrive() {
	want := os.Getenv("RECOVERY_GUI_SRC")
	for i, s := range gSources {
		if s.Path == want {
			sendMsg(gCombo, cbSetCurSel, uintptr(i), 0)
		}
	}
	setText(gDest, os.Getenv("RECOVERY_GUI_DST"))
	if os.Getenv("RECOVERY_GUI_ALL") != "" {
		setChecked(gRadioAll, true)
		setChecked(gRadioPers, false)
	}
	if os.Getenv("RECOVERY_GUI_EMAILS") != "" {
		setChecked(gEmails, true)
	}
	onStart()
}
