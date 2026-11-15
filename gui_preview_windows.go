//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var bigIcons = map[string]syscall.Handle{}

func registerCanvasClass(cursor syscall.Handle) {
	wc := wndclassex{
		cbSize: uint32(unsafe.Sizeof(wndclassex{})), style: 3,
		lpfnWndProc: syscall.NewCallback(canvasProc), hInstance: syscall.Handle(gInst),
		hCursor: cursor, lpszClassName: wstr("RecoveryCanvas"),
	}
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
}

func newCanvas() syscall.Handle {
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(wstr("RecoveryCanvas"))), 0,
		wsChild|wsVisible, 0, 0, 10, 10, uintptr(app.hwnd), 0, gInst, 0)
	return syscall.Handle(h)
}

func canvasProc(hwnd syscall.Handle, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		paintCanvas(hwnd)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wp, lp)
	return r
}

func paintCanvas(hwnd syscall.Handle) {
	var ps paintstruct
	hdc, _, _ := pBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	W, H := clientSize(hwnd)
	all := rect{0, 0, int32(W), int32(H)}
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&all)), uintptr(app.brPanel))
	app.prevMu.Lock()
	p := app.prev
	app.prevMu.Unlock()
	pSetBkMode.Call(hdc, 1)
	switch {
	case p.pix != nil:
		x, y := (W-p.w)/2, (H-p.h)/2
		bmi := bitmapinfoheader{biSize: 40, biWidth: int32(p.w), biHeight: -int32(p.h), biPlanes: 1, biBitCount: 32}
		pSetStretchBltMod.Call(hdc, 4) // HALFTONE
		pStretchDIBits.Call(hdc, uintptr(x), uintptr(y), uintptr(p.w), uintptr(p.h), 0, 0,
			uintptr(p.w), uintptr(p.h), uintptr(unsafe.Pointer(&p.pix[0])), uintptr(unsafe.Pointer(&bmi)),
			0, 0x00CC0020) // DIB_RGB_COLORS, SRCCOPY
	case p.text != "":
		r := rect{int32(sc(10)), int32(sc(8)), int32(W - sc(10)), int32(H - sc(8))}
		of, _, _ := pSelectObject.Call(hdc, uintptr(app.fonts[fBody]))
		pSetTextColor.Call(hdc, uintptr(cText))
		pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(wstr(p.text))), ^uintptr(0), uintptr(unsafe.Pointer(&r)),
			0x10|0x800|0x2000) // WORDBREAK|NOPREFIX|EDITCONTROL
		pSelectObject.Call(hdc, of)
	case p.ext != "" || p.msg != "":
		ext := p.ext
		if ext == "" {
			ext = "txt"
		}
		ic := bigIcons[ext]
		if ic == 0 {
			ic = largeIcon(ext)
			bigIcons[ext] = ic
		}
		sz := sc(48)
		if ic != 0 {
			pDrawIconEx.Call(hdc, uintptr((W-sz)/2), uintptr(H/2-sz-sc(4)), uintptr(ic), uintptr(sz), uintptr(sz), 0, 0, 3)
		}
		if p.msg != "" {
			r := rect{int32(sc(12)), int32(H/2 + sc(8)), int32(W - sc(12)), int32(H - sc(6))}
			of, _, _ := pSelectObject.Call(hdc, uintptr(app.fonts[fBody]))
			pSetTextColor.Call(hdc, uintptr(cMuted))
			pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(wstr(p.msg))), ^uintptr(0), uintptr(unsafe.Pointer(&r)),
				0x1|0x10|0x800) // CENTER|WORDBREAK|NOPREFIX
			pSelectObject.Call(hdc, of)
		}
	}
}

func setPreview(p previewImage) {
	app.prevMu.Lock()
	app.prev = p
	app.prevMu.Unlock()
	invalidate(app.hCanvas)
}

func clearPreview() {
	atomic.AddInt64(&app.prevGen, 1)
	setPreview(previewImage{})
	setText(app.hPrevName, "")
	setText(app.hPrevI, "Selecciona un archivo para ver su vista previa.")
}

// startPreview muestra los datos del elemento seleccionado y carga su
// vista previa en segundo plano.
func startPreview() {
	a := app
	row := a.list.focused()
	if row < 0 || row >= len(a.rows) || a.ss == nil {
		clearPreview()
		return
	}
	gen := atomic.AddInt64(&a.prevGen, 1)
	r := a.rows[row]
	if r.n != nil {
		setText(a.hPrevName, r.n.Name)
		setText(a.hPrevI, fmt.Sprintf("Carpeta\r\n%s archivos · %s\r\n\r\nMarca su casilla para recuperar todo su contenido.",
			groupDigits(int64(r.n.Count)), humanSize(r.n.Size)))
		setPreview(previewImage{ext: "/"})
		return
	}
	e := r.e
	setText(a.hPrevName, e.Name)
	info := []string{"Tamaño: " + humanSize(e.Size)}
	if !e.MTime.IsZero() {
		info = append(info, "Modificado: "+e.MTime.Local().Format("02/01/2006 15:04"))
	}
	info = append(info, "Tipo: "+typeName(e.Ext), "Estado: "+e.StateDetail(), "Ubicación: "+displayDir(e))
	setText(a.hPrevI, strings.Join(info, "\r\n"))

	kind := previewKind(e.Ext)
	switch {
	case e.State == esLost:
		setPreview(previewImage{ext: e.Ext, msg: "Este archivo ya no se puede recuperar."})
		return
	case kind == pvNone:
		setPreview(previewImage{ext: e.Ext, msg: "Sin vista previa para este tipo de archivo."})
		return
	case kind == pvImage && e.Size > 48<<20:
		setPreview(previewImage{ext: e.Ext, msg: "Imagen demasiado grande para la vista previa."})
		return
	}
	setPreview(previewImage{ext: e.Ext, msg: "Cargando vista previa…"})
	limit := int64(48 << 20)
	if kind == pvText {
		limit = 16 << 10
	}
	W, H := clientSize(a.hCanvas)
	maxW, maxH := W-sc(12), H-sc(12)
	ss := a.ss
	go func() {
		data, _ := ss.ReadEntry(e, limit)
		p := makePreview(data, e.Ext, maxW, maxH)
		if atomic.LoadInt64(&a.prevGen) != gen {
			return // el usuario ya eligió otro archivo
		}
		a.prevMu.Lock()
		a.prev = p
		a.prevMu.Unlock()
		postMsg(a.hwnd, wmAppPreview, uintptr(gen), 0)
	}()
}
