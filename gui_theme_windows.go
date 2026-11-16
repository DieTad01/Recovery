//go:build windows

package main

// Estilo de la marca Delio (delioph.tech): un solo fondo plano color caqui,
// tinta casi negra, letra de palo seco en negrita para lo importante, trazos
// rectos del mismo grosor y botones cuadrados (lleno o con marco) que se
// invierten al pasar el mouse. El modo nocturno usa los mismos dos colores
// al revés, como en la página.

import (
	"bytes"
	_ "embed"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	dwmapi                 = syscall.NewLazyDLL("dwmapi.dll")
	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	pPolyline              = gdi32.NewProc("Polyline")
	pCreateDIBSection      = gdi32.NewProc("CreateDIBSection")
	pImageListCreate       = comctl32.NewProc("ImageList_Create")
	pImageListAddMasked    = comctl32.NewProc("ImageList_AddMasked")
	pImageListDestroy      = comctl32.NewProc("ImageList_Destroy")
	pIsWindowVisible       = user32.NewProc("IsWindowVisible")
	pGetWindowRect         = user32.NewProc("GetWindowRect")
	pMapWindowPoints       = user32.NewProc("MapWindowPoints")
	pRedrawWindow          = user32.NewProc("RedrawWindow")
	pSetCursor             = user32.NewProc("SetCursor")
	pDrawFocusRect         = user32.NewProc("DrawFocusRect")
)

//go:embed marca/logotipo-tinta.png
var logoTinta []byte

//go:embed marca/logotipo-caqui.png
var logoCaqui []byte

// ---------------------------------------------------------------- paleta

type palette struct {
	bg     uint32 // fondo de la página
	sunken uint32 // fondo hundido: listas, campos y vista previa
	ink    uint32 // texto
	soft   uint32 // texto secundario
	strong uint32 // lo más marcado: botones, marcos, líneas, selección
	off    uint32 // controles desactivados
}

var (
	paletteDay = palette{
		bg: rgb(0xa0, 0xa0, 0x84), sunken: rgb(0x91, 0x91, 0x75), ink: rgb(0x1e, 0x1e, 0x19),
		soft: rgb(0x30, 0x30, 0x28), strong: rgb(0, 0, 0), off: rgb(0x6c, 0x6c, 0x59),
	}
	paletteNight = palette{
		bg: rgb(0x1b, 0x1b, 0x17), sunken: rgb(0x27, 0x27, 0x1f), ink: rgb(0xa0, 0xa0, 0x84),
		soft: rgb(0x8f, 0x8f, 0x76), strong: rgb(0xa0, 0xa0, 0x84), off: rgb(0x5a, 0x5a, 0x4b),
	}
)

// Colores en uso (los cambia setPalette).
var (
	cBg, cPanel, cText, cMuted, cStrong, cOff uint32
	night                                     bool
)

func setPalette(n bool) {
	night = n
	p := paletteDay
	if n {
		p = paletteNight
	}
	cBg, cPanel, cText, cMuted, cStrong, cOff = p.bg, p.sunken, p.ink, p.soft, p.strong, p.off
	for _, b := range []*syscall.Handle{&app.brBg, &app.brPanel, &app.brStrong, &app.brOff} {
		if *b != 0 {
			pDeleteObject.Call(uintptr(*b))
		}
	}
	app.brBg, app.brPanel, app.brStrong, app.brOff = newBrush(cBg), newBrush(cPanel), newBrush(cStrong), newBrush(cOff)
	logoCache = map[int]*logoBitmap{}
}

// lw es el grosor de todas las líneas y marcos (1,6 px en la página).
func lw() int {
	v := (16*app.dpi/96 + 5) / 10
	if v < 1 {
		v = 1
	}
	return v
}

// ---------------------------------------------------------------- tema guardado

func themeFile() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "Recovery", "tema.txt")
}

func savedNight() bool {
	switch strings.ToLower(os.Getenv("RECOVERY_GUI_THEME")) {
	case "noche":
		return true
	case "dia", "día":
		return false
	}
	if f := themeFile(); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			return strings.TrimSpace(string(b)) == "noche"
		}
	}
	return false
}

func saveNight(n bool) {
	f := themeFile()
	if f == "" || app.autotest {
		return
	}
	os.MkdirAll(filepath.Dir(f), 0o755)
	v := "dia"
	if n {
		v = "noche"
	}
	os.WriteFile(f, []byte(v), 0o644)
}

// toggleNight cambia entre día y noche y vuelve a pintar todo.
func toggleNight() {
	setPalette(!night)
	saveNight(night)
	applyTheme()
}

// applyTheme lleva los colores actuales a todas las ventanas y controles.
func applyTheme() {
	setText(app.hNight, map[bool]string{false: "Noche", true: "Día"}[night])
	for _, l := range gLists {
		l.applyColors()
	}
	if app.hTree != 0 {
		sendMsg(app.hTree, 0x111D, 0, uintptr(cPanel))  // TVM_SETBKCOLOR
		sendMsg(app.hTree, 0x111E, 0, uintptr(cText))   // TVM_SETTEXTCOLOR
		sendMsg(app.hTree, 0x1128, 0, uintptr(cStrong)) // TVM_SETLINECOLOR
	}
	wins := []syscall.Handle{app.hwnd}
	for h := range gTables {
		wins = append(wins, h)
	}
	for _, h := range wins {
		styleCaption(h)
		pRedrawWindow.Call(uintptr(h), 0, 0, 0x1|0x4|0x80|0x400) // INVALIDATE|ERASE|ALLCHILDREN|FRAME
	}
}

// styleCaption pinta la barra de título con los colores de la marca
// (Windows 11; en versiones anteriores solo se aplica el modo oscuro).
func styleCaption(h syscall.Handle) {
	if pDwmSetWindowAttribute.Find() != nil {
		return
	}
	set := func(attr uintptr, v uint32) {
		pDwmSetWindowAttribute.Call(uintptr(h), attr, uintptr(unsafe.Pointer(&v)), 4)
	}
	d := uint32(0)
	if night {
		d = 1
	}
	set(20, d)       // DWMWA_USE_IMMERSIVE_DARK_MODE
	set(35, cBg)     // DWMWA_CAPTION_COLOR
	set(36, cText)   // DWMWA_TEXT_COLOR
	set(34, cStrong) // DWMWA_BORDER_COLOR
}

// ---------------------------------------------------------------- dibujo básico

func fillR(hdc uintptr, r rect, br syscall.Handle) {
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), uintptr(br))
}

// frame dibuja un marco de grosor w por dentro de r.
func frame(hdc uintptr, r rect, w int, br syscall.Handle) {
	t := int32(w)
	fillR(hdc, rect{r.left, r.top, r.right, r.top + t}, br)
	fillR(hdc, rect{r.left, r.bottom - t, r.right, r.bottom}, br)
	fillR(hdc, rect{r.left, r.top, r.left + t, r.bottom}, br)
	fillR(hdc, rect{r.right - t, r.top, r.right, r.bottom}, br)
}

func drawText(hdc uintptr, s string, r rect, font int, color uint32, flags uintptr) rect {
	pSetBkMode.Call(hdc, 1)
	pSetTextColor.Call(hdc, uintptr(color))
	of, _, _ := pSelectObject.Call(hdc, uintptr(app.fonts[font]))
	pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(wstr(s))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), flags)
	pSelectObject.Call(hdc, of)
	return r
}

// textSize mide un texto en una línea.
func textSize(hdc uintptr, s string, font int) (int, int) {
	r := rect{0, 0, 4000, 400}
	r = drawText(hdc, s, r, font, 0, 0x400|0x20|0x800) // CALCRECT|SINGLELINE|NOPREFIX
	return int(r.right - r.left), int(r.bottom - r.top)
}

// childRect devuelve la posición de un control dentro de su ventana.
func childRect(parent, h syscall.Handle) rect {
	var r rect
	pGetWindowRect.Call(uintptr(h), uintptr(unsafe.Pointer(&r)))
	pMapWindowPoints.Call(0, uintptr(parent), uintptr(unsafe.Pointer(&r)), 2)
	return r
}

func isVisible(h syscall.Handle) bool {
	r, _, _ := pIsWindowVisible.Call(uintptr(h))
	return r != 0
}

// ---------------------------------------------------------------- marcos de los controles

// Las listas, el árbol, los campos y la vista previa no usan el borde 3D de
// Windows: la ventana que los contiene les dibuja un marco plano.
type boxSpec struct {
	parent syscall.Handle
	r      rect // marco exterior, en coordenadas de la ventana
	fill   bool // pintar el interior con el fondo hundido (campos de texto)
}

var boxes = map[syscall.Handle]boxSpec{}

// moveFramed coloca un control dentro de un marco que ocupa (x, y, w, h).
func moveFramed(parent, c syscall.Handle, x, y, w, h int) {
	t := lw()
	boxes[c] = boxSpec{parent: parent, r: rect{int32(x), int32(y), int32(x + w), int32(y + h)}}
	moveWin(c, x+t, y+t, w-2*t, h-2*t)
}

// moveField coloca un campo de texto de una línea centrado en su caja.
func moveField(parent, c syscall.Handle, x, y, w, h int) {
	boxes[c] = boxSpec{parent: parent, r: rect{int32(x), int32(y), int32(x + w), int32(y + h)}, fill: true}
	fh := sc(fontSpec[fBody].px) + sc(5)
	moveWin(c, x+sc(10), y+(h-fh)/2, w-sc(20), fh)
}

func paintBoxes(hdc uintptr, parent syscall.Handle) {
	for c, b := range boxes {
		if b.parent != parent || !isVisible(c) {
			continue
		}
		if b.fill {
			fillR(hdc, b.r, app.brPanel)
		}
		frame(hdc, b.r, lw(), app.brStrong)
	}
}

// ---------------------------------------------------------------- botones

const (
	btnFrame = iota // con marco (por defecto)
	btnFill         // lleno: la acción principal de cada pantalla
	btnLink         // texto subrayado, como el menú de la página
	btnCheck        // casilla
	btnSeg          // opción de un selector de dos (Archivos | Carpetas): lleno si está elegida
)

var (
	btnKind = map[syscall.Handle]int{}
	btnHot  syscall.Handle
)

// button crea un botón dibujado con el estilo de la marca.
func button(text string, style uintptr, id, kind, font int) syscall.Handle {
	h := ctl("BUTTON", text, style|bsOwnerDraw, 0, id, font)
	btnKind[h] = kind
	if kind == btnCheck || kind == btnSeg {
		ownerChecks[h] = false
	}
	return h
}

// setHot marca el botón que está bajo el mouse (se dibuja invertido).
func setHot(h syscall.Handle) {
	if _, ok := btnKind[h]; !ok || !isEnabled(h) {
		h = 0
	}
	if h == btnHot {
		return
	}
	old := btnHot
	btnHot = h
	if old != 0 {
		invalidate(old)
	}
	if h != 0 {
		invalidate(h)
	}
}

var handCursor uintptr

// onSetCursor: al pasar sobre un botón, mano y botón invertido (como en la página).
func onSetCursor(child uintptr) bool {
	h := syscall.Handle(child)
	setHot(h)
	if h == btnHot && h != 0 {
		if handCursor == 0 {
			handCursor, _, _ = pLoadCursorW.Call(0, 32649) // IDC_HAND
		}
		pSetCursor.Call(handCursor)
		return true
	}
	return false
}

// drawButton dibuja un botón o casilla (WM_DRAWITEM).
func drawButton(lp uintptr) {
	h := syscall.Handle(rdPtr(lp, 24))
	hdc := rdPtr(lp, 32)
	state := rdU32(lp, 16)
	r := rect{rdI32(lp, 40), rdI32(lp, 44), rdI32(lp, 48), rdI32(lp, 52)}
	disabled := state&4 != 0
	hot := !disabled && (h == btnHot || state&1 != 0)
	focus := state&0x10 != 0 && state&0x200 == 0 // ODS_FOCUS sin ODS_NOFOCUSRECT
	text := getText(h)
	font := app.ctlFont[h]
	t := lw()
	fillR(hdc, r, app.brBg)
	center := uintptr(0x1 | 0x4 | 0x20 | 0x800) // CENTER|VCENTER|SINGLELINE|NOPREFIX
	switch btnKind[h] {
	case btnFill, btnFrame:
		filled := btnKind[h] == btnFill
		if hot {
			filled = !filled
		}
		fg := cStrong
		switch {
		case disabled && btnKind[h] == btnFill:
			fillR(hdc, r, app.brOff)
			fg = cBg
		case disabled:
			frame(hdc, r, t, app.brOff)
			fg = cOff
		case filled:
			fillR(hdc, r, app.brStrong)
			fg = cBg
		default:
			frame(hdc, r, t, app.brStrong)
		}
		drawText(hdc, text, r, font, fg, center)
		if focus {
			in := rect{r.left + int32(t+sc(3)), r.top + int32(t+sc(3)), r.right - int32(t+sc(3)), r.bottom - int32(t+sc(3))}
			pSetTextColor.Call(hdc, uintptr(fg))
			pSetBkColor.Call(hdc, uintptr(map[bool]uint32{true: cStrong, false: cBg}[filled && !disabled]))
			pDrawFocusRect.Call(hdc, uintptr(unsafe.Pointer(&in)))
		}
	case btnSeg:
		on := ownerChecks[h]
		fg := cStrong
		switch {
		case disabled:
			frame(hdc, r, t, app.brOff)
			fg = cOff
		case on:
			fillR(hdc, r, app.brStrong)
			fg = cBg
		case hot:
			fillR(hdc, r, app.brPanel)
			frame(hdc, r, t, app.brStrong)
		default:
			frame(hdc, r, t, app.brStrong)
		}
		drawText(hdc, text, r, font, fg, center)
		if focus {
			in := rect{r.left + int32(t+sc(3)), r.top + int32(t+sc(3)), r.right - int32(t+sc(3)), r.bottom - int32(t+sc(3))}
			pSetTextColor.Call(hdc, uintptr(fg))
			pSetBkColor.Call(hdc, uintptr(map[bool]uint32{true: cStrong, false: cBg}[on && !disabled]))
			pDrawFocusRect.Call(hdc, uintptr(unsafe.Pointer(&in)))
		}
	case btnLink:
		fg := cStrong
		if disabled {
			fg = cOff
		}
		drawText(hdc, text, r, font, fg, center)
		tw, th := textSize(hdc, text, font)
		x0 := (r.left + r.right - int32(tw)) / 2
		y := (r.top+r.bottom+int32(th))/2 + int32(sc(3))
		u := int32(t)
		if hot {
			u = int32(sc(3))
		}
		if y+u > r.bottom {
			y = r.bottom - u
		}
		br := app.brStrong
		if disabled {
			br = app.brOff
		}
		fillR(hdc, rect{x0, y, x0 + int32(tw), y + u}, br)
		if focus {
			in := rect{x0 - int32(sc(4)), r.top + int32(sc(2)), x0 + int32(tw) + int32(sc(4)), r.bottom - int32(sc(1))}
			pSetTextColor.Call(hdc, uintptr(fg))
			pSetBkColor.Call(hdc, uintptr(cBg))
			pDrawFocusRect.Call(hdc, uintptr(unsafe.Pointer(&in)))
		}
	case btnCheck:
		bs := int32(sc(16))
		fg := cText
		br := app.brStrong
		if disabled {
			fg, br = cOff, app.brOff
		}
		// Texto (puede ocupar varias líneas) y casilla centrada con la primera.
		tr := rect{r.left + bs + int32(sc(10)), r.top, r.right, r.bottom}
		calc := drawText(hdc, text, tr, font, fg, 0x10|0x400|0x800) // WORDBREAK|CALCRECT|NOPREFIX
		th := calc.bottom - calc.top
		ty := r.top + (r.bottom-r.top-th)/2
		drawText(hdc, text, rect{tr.left, ty, tr.right, ty + th}, font, fg, 0x10|0x800)
		_, lh := textSize(hdc, "Ág", font)
		by := ty + (int32(lh)-bs)/2
		box := rect{r.left, by, r.left + bs, by + bs}
		frame(hdc, box, t, br)
		if ownerChecks[h] {
			g := int32(t + sc(2))
			fillR(hdc, rect{box.left + g, box.top + g, box.right - g, box.bottom - g}, br)
		} else if hot {
			g := int32(t + sc(4))
			fillR(hdc, rect{box.left + g, box.top + g, box.right - g, box.bottom - g}, app.brOff)
		}
		if focus {
			in := rect{tr.left - int32(sc(3)), ty - int32(sc(1)), calc.right + int32(sc(3)), ty + th + int32(sc(1))}
			pSetTextColor.Call(hdc, uintptr(fg))
			pSetBkColor.Call(hdc, uintptr(cBg))
			pDrawFocusRect.Call(hdc, uintptr(unsafe.Pointer(&in)))
		}
	}
}

// ---------------------------------------------------------------- barra de progreso

// Barra plana: marco y relleno del color fuerte (sin el verde de Windows).
var barPos = map[syscall.Handle]int{}

func registerBarClass(cursor syscall.Handle) {
	wc := wndclassex{
		cbSize: uint32(unsafe.Sizeof(wndclassex{})), style: 3,
		lpfnWndProc: syscall.NewCallback(barProc), hInstance: syscall.Handle(gInst),
		hCursor: cursor, lpszClassName: wstr("RecoveryBar"),
	}
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
}

func newBar(style uintptr) syscall.Handle {
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(wstr("RecoveryBar"))), 0,
		wsChild|style, 0, 0, 10, 10, uintptr(app.hwnd), 0, gInst, 0)
	return syscall.Handle(h)
}

// setBar pone la barra en v milésimas.
func setBar(h syscall.Handle, v int) {
	if v < 0 {
		v = 0
	}
	if v > 1000 {
		v = 1000
	}
	if barPos[h] != v {
		barPos[h] = v
		invalidate(h)
	}
}

func barProc(hwnd syscall.Handle, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		var ps paintstruct
		hdc, _, _ := pBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		W, H := clientSize(hwnd)
		all := rect{0, 0, int32(W), int32(H)}
		fillR(hdc, all, app.brBg)
		t := lw()
		frame(hdc, all, t, app.brStrong)
		in := W - 2*t - 2*sc(2)
		fw := in * barPos[hwnd] / 1000
		g := int32(t + sc(2))
		if fw > 0 {
			fillR(hdc, rect{g, g, g + int32(fw), int32(H) - g}, app.brStrong)
		}
		pEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wp, lp)
	return r
}

// ---------------------------------------------------------------- listas y árbol

const (
	nmCustomDraw  = 0xFFFFFFF4 // NM_CUSTOMDRAW
	cddsPrePaint  = 0x1
	cddsPostPaint = 0x2
	cddsItemPre   = 0x10001
	cdrfNewFont   = 0x2
	cdrfSkipDef   = 0x4
	cdrfNotifyPst = 0x10
	cdrfNotifyItm = 0x20
)

var gHeaders = map[syscall.Handle]*vlist{}

// applyColors pone los colores de la marca en una lista.
func (l *vlist) applyColors() {
	sendMsg(l.hwnd, 0x1001, 0, uintptr(cPanel)) // LVM_SETBKCOLOR
	sendMsg(l.hwnd, 0x1026, 0, uintptr(cPanel)) // LVM_SETTEXTBKCOLOR
	sendMsg(l.hwnd, 0x1024, 0, uintptr(cText))  // LVM_SETTEXTCOLOR
	if l.checkboxes {
		if l.states != 0 {
			pImageListDestroy.Call(l.states)
		}
		l.states = checkImages()
		sendMsg(l.hwnd, lvmSetImageList, 2, l.states) // LVSIL_STATE
	}
	invalidate(l.hwnd)
	if hd := syscall.Handle(sendMsg(l.hwnd, 0x101F, 0, 0)); hd != 0 { // LVM_GETHEADER
		invalidate(hd)
	}
}

// listCustomDraw: filas seleccionadas en color fuerte con el texto del fondo.
func listCustomDraw(l *vlist, lp uintptr) uintptr {
	switch rdU32(lp, 24) {
	case cddsPrePaint:
		return cdrfNotifyItm
	case cddsItemPre:
		item := rdPtr(lp, 56)
		if sendMsg(l.hwnd, 0x102C, item, lvisSelected)&lvisSelected != 0 { // LVM_GETITEMSTATE
			wrU32(lp, 80, cBg)
			wrU32(lp, 84, cStrong)
		} else {
			wrU32(lp, 80, cText)
			wrU32(lp, 84, cPanel)
		}
		wrU32(lp, 64, rdU32(lp, 64)&^(0x1|0x10)) // sin CDIS_SELECTED ni CDIS_FOCUS
		return cdrfNewFont
	}
	return 0
}

// Encabezados de las listas: planos, en negrita y con una línea debajo. Se
// dibujan sustituyendo el procedimiento de la ventana del encabezado (así
// funciona igual en todas las versiones de Windows).
var (
	pSetWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW   = user32.NewProc("CallWindowProcW")
	headerOrig         = map[syscall.Handle]uintptr{}
	headerProcCB       = syscall.NewCallback(headerProc)
)

func subclassHeader(hd syscall.Handle) {
	old, _, _ := pSetWindowLongPtrW.Call(uintptr(hd), ^uintptr(3), headerProcCB) // GWLP_WNDPROC (-4)
	headerOrig[hd] = old
}

func headerProc(hwnd syscall.Handle, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		paintHeader(hwnd)
		return 0
	case 0x0002: // WM_DESTROY
		orig := headerOrig[hwnd]
		delete(headerOrig, hwnd)
		delete(gHeaders, hwnd)
		r, _, _ := pCallWindowProcW.Call(orig, uintptr(hwnd), uintptr(msg), wp, lp)
		return r
	}
	r, _, _ := pCallWindowProcW.Call(headerOrig[hwnd], uintptr(hwnd), uintptr(msg), wp, lp)
	return r
}

func paintHeader(hd syscall.Handle) {
	var ps paintstruct
	hdc, _, _ := pBeginPaint.Call(uintptr(hd), uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(uintptr(hd), uintptr(unsafe.Pointer(&ps)))
	W, H := clientSize(hd)
	t := int32(lw())
	fillR(hdc, rect{0, 0, int32(W), int32(H)}, app.brBg)
	l := gHeaders[hd]
	n := int(sendMsg(hd, 0x1200, 0, 0)) // HDM_GETITEMCOUNT
	for i := 0; l != nil && i < n && i < len(l.cols); i++ {
		var r rect
		sendMsg(hd, 0x1207, uintptr(i), uintptr(unsafe.Pointer(&r))) // HDM_GETITEMRECT
		if r.right-r.left > 4 {
			fillR(hdc, rect{r.right - 1, r.top + (r.bottom-r.top)/4, r.right, r.bottom - (r.bottom-r.top)/4}, app.brOff)
		}
		tr := rect{r.left + int32(sc(7)), r.top, r.right - int32(sc(7)), r.bottom - t}
		fl := uintptr(0x4 | 0x20 | 0x8000 | 0x800) // VCENTER|SINGLELINE|END_ELLIPSIS|NOPREFIX
		if l.cols[i].right {
			fl |= 0x2
		}
		drawText(hdc, l.cols[i].title, tr, fBold, cText, fl)
	}
	fillR(hdc, rect{0, int32(H) - t, int32(W), int32(H)}, app.brStrong)
}

// treeCustomDraw: como la lista.
func treeCustomDraw(lp uintptr) uintptr {
	switch rdU32(lp, 24) {
	case cddsPrePaint:
		return cdrfNotifyItm
	case cddsItemPre:
		if rdU32(lp, 64)&0x1 != 0 { // CDIS_SELECTED
			wrU32(lp, 80, cBg)
			wrU32(lp, 84, cStrong)
		} else {
			wrU32(lp, 80, cText)
			wrU32(lp, 84, cPanel)
		}
		wrU32(lp, 64, rdU32(lp, 64)&^(0x1|0x10))
		return cdrfNewFont
	}
	return 0
}

// checkImages crea las casillas de las listas con el estilo de la marca
// (imagen 0 = sin marcar, 1 = marcada; la lista las numera desde 1). Llevan
// un borde exterior del color del fondo para que se vean también sobre una
// fila seleccionada. Lo transparente se marca con magenta.
func checkImages() uintptr {
	s := sc(16)
	if v, _, _ := pGetSystemMetrics.Call(49); int(v) > s { // SM_CXSMICON
		s = int(v)
	}
	w := s * 2
	bmi := bitmapinfoheader{biSize: 40, biWidth: int32(w), biHeight: -int32(s), biPlanes: 1, biBitCount: 32}
	var bits unsafe.Pointer
	hbm, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 || bits == nil {
		return 0
	}
	px := unsafe.Slice((*uint32)(bits), w*s)
	const key = 0x00FF00FF          // magenta: transparente
	argb := func(c uint32) uint32 { // COLORREF (00BBGGRR) -> píxel BGR
		return (c&0xFF)<<16 | c&0xFF00 | (c>>16)&0xFF
	}
	t := lw()
	box := s * 13 / 16
	o := (s - box) / 2
	for img := 0; img < 2; img++ {
		for y := 0; y < s; y++ {
			for x := 0; x < s; x++ {
				c := uint32(key)
				dx, dy := x-o, y-o
				switch {
				case dx >= -1 && dy >= -1 && dx <= box && dy <= box &&
					(dx == -1 || dy == -1 || dx == box || dy == box):
					c = argb(cBg) // borde exterior
				case dx >= 0 && dy >= 0 && dx < box && dy < box:
					in := dx >= t && dy >= t && dx < box-t && dy < box-t
					g := t + max(1, s/8)
					mark := img == 1 && dx >= g && dy >= g && dx < box-g && dy < box-g
					switch {
					case !in || mark:
						c = argb(cStrong)
					default:
						c = argb(cPanel)
					}
				}
				px[y*w+img*s+x] = c
			}
		}
	}
	il, _, _ := pImageListCreate.Call(uintptr(s), uintptr(s), 0x20|0x1, 2, 0) // ILC_COLOR32|ILC_MASK
	pImageListAddMasked.Call(il, hbm, key)
	pDeleteObject.Call(hbm)
	return il
}

// ---------------------------------------------------------------- logotipo y onda

type logoBitmap struct {
	w, h int
	pix  []byte // BGRA ya mezclado con el fondo
}

var (
	logoCache = map[int]*logoBitmap{}
	logoSrc   [2]*image.NRGBA
)

func logoImage() *image.NRGBA {
	i := 0
	data := logoTinta
	if night {
		i, data = 1, logoCaqui
	}
	if logoSrc[i] == nil {
		m, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		n := image.NewNRGBA(m.Bounds())
		draw.Draw(n, n.Bounds(), m, m.Bounds().Min, draw.Src)
		logoSrc[i] = n
	}
	return logoSrc[i]
}

// logoWidth es el ancho del logotipo a una altura dada.
func logoWidth(h int) int {
	src := logoImage()
	if src == nil {
		return 0
	}
	return h * src.Bounds().Dx() / src.Bounds().Dy()
}

// drawLogo dibuja el logotipo «Delio» con altura h (reducido con calidad y
// mezclado con el color del fondo).
func drawLogo(hdc uintptr, x, y, h int) {
	src := logoImage()
	if src == nil || h <= 0 {
		return
	}
	lb := logoCache[h]
	if lb == nil {
		w := logoWidth(h)
		sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
		lb = &logoBitmap{w: w, h: h, pix: make([]byte, w*h*4)}
		br, bgc, bb := float64(cBg&0xFF), float64(cBg>>8&0xFF), float64(cBg>>16&0xFF)
		for ty := 0; ty < h; ty++ {
			y0, y1 := ty*sh/h, (ty+1)*sh/h
			for tx := 0; tx < w; tx++ {
				x0, x1 := tx*sw/w, (tx+1)*sw/w
				var r, g, b, a, n float64
				for sy := y0; sy < max(y1, y0+1); sy++ {
					for sx := x0; sx < max(x1, x0+1); sx++ {
						o := src.PixOffset(sx, sy)
						al := float64(src.Pix[o+3]) / 255
						r += float64(src.Pix[o]) * al
						g += float64(src.Pix[o+1]) * al
						b += float64(src.Pix[o+2]) * al
						a += al
						n++
					}
				}
				af := a / n
				var cr, cg, cb float64
				if a > 0 {
					cr, cg, cb = r/a, g/a, b/a
				}
				o := (ty*w + tx) * 4
				lb.pix[o+2] = byte(math.Round(cr*af + br*(1-af)))
				lb.pix[o+1] = byte(math.Round(cg*af + bgc*(1-af)))
				lb.pix[o+0] = byte(math.Round(cb*af + bb*(1-af)))
			}
		}
		logoCache[h] = lb
	}
	bmi := bitmapinfoheader{biSize: 40, biWidth: int32(lb.w), biHeight: -int32(lb.h), biPlanes: 1, biBitCount: 32}
	pStretchDIBits.Call(hdc, uintptr(x), uintptr(y), uintptr(lb.w), uintptr(lb.h), 0, 0,
		uintptr(lb.w), uintptr(lb.h), uintptr(unsafe.Pointer(&lb.pix[0])), uintptr(unsafe.Pointer(&bmi)),
		0, 0x00CC0020)
}

// drawWave dibuja la onda de la portada de la página: tranquila en las
// orillas y abierta al centro.
func drawWave(hdc uintptr, x0, x1, mid, amp int) {
	type point struct{ x, y int32 }
	var pts []point
	w := float64(x1 - x0)
	center := float64(x0) + w/2
	open := math.Min(w*0.26, float64(sc(300)))
	k := float64(app.dpi) / 96
	for x := x0; x <= x1; x++ {
		d := (float64(x) - center) / open
		bell := 0.06 + 0.94*math.Exp(-d*d)
		breath := 0.6 + 0.4*math.Sin(float64(x)/(90*k))
		y := float64(mid) - float64(amp)*bell*breath*math.Sin(float64(x)/(5.2*k))
		pts = append(pts, point{int32(x), int32(math.Round(y))})
	}
	pen, _, _ := pCreatePen.Call(0, uintptr(max(1, lw()-1)), uintptr(cStrong))
	op, _, _ := pSelectObject.Call(hdc, pen)
	pPolyline.Call(hdc, uintptr(unsafe.Pointer(&pts[0])), uintptr(len(pts)))
	pSelectObject.Call(hdc, op)
	pDeleteObject.Call(pen)
}
