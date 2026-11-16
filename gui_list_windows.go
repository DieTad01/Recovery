//go:build windows

package main

// Lista «virtual» de Windows (ListView en modo LVS_OWNERDATA): la lista no
// guarda los datos, los pide al programa cuando los va a dibujar. Así
// puede mostrar cientos de miles de archivos sin esfuerzo.

import (
	"syscall"
	"unsafe"
)

type vcol struct {
	title string
	width int // en unidades de 96 ppp
	right bool
}

type vlist struct {
	hwnd       syscall.Handle
	cols       []vcol
	n          int
	checkboxes bool
	text       func(row, col int) string
	image      func(row int) int32 // -1 = sin icono
	check      func(row int) int   // 0 = sin casilla, 1 = sin marcar, 2 = marcada
	onToggle   func(rows []int)
	onSort     func(col int)
	onActivate func(row int)
	onSelect   func(row int) // -1 = nada seleccionado
	states     uintptr       // casillas con el estilo de la marca
}

var gLists = map[syscall.Handle]*vlist{}

func newVList(parent syscall.Handle, id int, cols []vcol, checkboxes bool, img uintptr, single bool) *vlist {
	style := uintptr(wsChild | wsVisible | wsTabstop | lvsReport | lvsOwnerData | lvsShowSelAlways)
	if single {
		style |= lvsSingleSel
	}
	h, _, _ := pCreateWindowExW.Call(0, // sin borde 3D: el marco lo dibuja la ventana
		uintptr(unsafe.Pointer(wstr("SysListView32"))), 0, style,
		0, 0, 100, 100, uintptr(parent), uintptr(id), gInst, 0)
	l := &vlist{hwnd: syscall.Handle(h), cols: cols, checkboxes: checkboxes}
	ex := uintptr(lvsExFullRow | lvsExDoubleBuf)
	if checkboxes {
		ex |= lvsExCheckboxes
	}
	sendMsg(l.hwnd, lvmSetExStyle, ex, ex)
	if checkboxes {
		sendMsg(l.hwnd, lvmSetCallback, lvisStateImgMask, 0)
	}
	if img != 0 {
		sendMsg(l.hwnd, lvmSetImageList, lvsilSmall, img)
	}
	for i, c := range cols {
		lc := lvcolumn{mask: lvcfFmt | lvcfWidth | lvcfText, cx: int32(sc(c.width)), pszText: wstr(c.title)}
		if c.right {
			lc.fmt = lvcfmtRight
		}
		sendMsg(l.hwnd, lvmInsertColumnW, uintptr(i), uintptr(unsafe.Pointer(&lc)))
	}
	gLists[l.hwnd] = l
	sendMsg(l.hwnd, wmSetFont, uintptr(app.fonts[fBody]), 0)
	if hd := syscall.Handle(sendMsg(l.hwnd, 0x101F, 0, 0)); hd != 0 { // LVM_GETHEADER
		gHeaders[hd] = l
		subclassHeader(hd)
	}
	l.applyColors()
	return l
}

// fitLastColumn estira la última columna hasta el borde de la lista.
func fitLastColumn(l *vlist) {
	if len(l.cols) > 0 {
		sendMsg(l.hwnd, 0x101E, uintptr(len(l.cols)-1), ^uintptr(1)) // LVM_SETCOLUMNWIDTH, LVSCW_AUTOSIZE_USEHEADER (-2)
	}
}

// rescale ajusta los anchos de columna tras un cambio de ppp.
func (l *vlist) rescale() {
	for i, c := range l.cols {
		lc := lvcolumn{mask: lvcfWidth, cx: int32(sc(c.width))}
		sendMsg(l.hwnd, lvmSetColumnW, uintptr(i), uintptr(unsafe.Pointer(&lc)))
	}
}

// setCount fija el número de filas y redibuja.
func (l *vlist) setCount(n int) {
	l.n = n
	sendMsg(l.hwnd, lvmSetItemCount, uintptr(n), 0)
	invalidate(l.hwnd)
}

func (l *vlist) refresh() { invalidate(l.hwnd) }

func (l *vlist) nextItem(start int, flags uintptr) int {
	return int(int32(sendMsg(l.hwnd, lvmGetNextItem, uintptr(int64(start)), flags)))
}

// selectedRows devuelve las filas seleccionadas.
func (l *vlist) selectedRows() []int {
	var out []int
	for i := l.nextItem(-1, lvniSelected); i >= 0 && len(out) <= l.n; i = l.nextItem(i, lvniSelected) {
		out = append(out, i)
	}
	return out
}

func (l *vlist) focused() int {
	if i := l.nextItem(-1, lvniFocused|lvniSelected); i >= 0 {
		return i
	}
	return l.nextItem(-1, lvniSelected)
}

// selectRow selecciona una fila y la hace visible.
func (l *vlist) selectRow(row int) {
	if row < 0 || row >= l.n {
		return
	}
	it := lvitem{stateMask: lvisSelected | lvisFocused, state: lvisSelected | lvisFocused}
	sendMsg(l.hwnd, lvmSetItemState, uintptr(row), uintptr(unsafe.Pointer(&it)))
	sendMsg(l.hwnd, lvmEnsureVisible, uintptr(row), 0)
}

// selectOnly deja seleccionada solo esa fila (-1: ninguna).
func (l *vlist) selectOnly(row int) {
	none := lvitem{stateMask: lvisSelected | lvisFocused}
	sendMsg(l.hwnd, lvmSetItemState, ^uintptr(0), uintptr(unsafe.Pointer(&none)))
	l.selectRow(row)
}

// copyText escribe s en un búfer UTF-16 de Windows.
func copyText(dst uintptr, max int32, s string) {
	if dst == 0 || max <= 0 {
		return
	}
	u, _ := syscall.UTF16FromString(s)
	buf := unsafe.Slice((*uint16)(mem(dst)), max)
	n := copy(buf[:max-1], u)
	buf[n] = 0
}

// handleListNotify atiende las notificaciones de cualquier lista virtual.
func handleListNotify(lp uintptr) (bool, uintptr) {
	from := syscall.Handle(rdPtr(lp, 0))
	code := rdU32(lp, 16)
	l := gLists[from]
	if l == nil {
		return false, 0
	}
	switch code {
	case nmCustomDraw:
		return true, listCustomDraw(l, lp)
	case lvnGetDispInfoW:
		it := lp + 24
		mask := rdU32(it, 0)
		row := int(rdI32(it, 4))
		col := int(rdI32(it, 8))
		if row < 0 || row >= l.n {
			return true, 0
		}
		if mask&lvifText != 0 && l.text != nil {
			copyText(rdPtr(it, 24), rdI32(it, 32), l.text(row, col))
		}
		if mask&lvifImage != 0 && col == 0 && l.image != nil {
			wrI32(it, 36, l.image(row))
		}
		if mask&lvifState != 0 && l.checkboxes && l.check != nil {
			st := rdU32(it, 12) &^ lvisStateImgMask
			wrU32(it, 12, st|uint32(l.check(row))<<12)
			wrU32(it, 16, rdU32(it, 16)|lvisStateImgMask)
		}
		return true, 0
	case nmClick:
		if !l.checkboxes || l.onToggle == nil {
			return true, 0
		}
		ht := lvhittest{x: rdI32(lp, 44), y: rdI32(lp, 48)}
		sendMsg(l.hwnd, lvmHitTest, 0, uintptr(unsafe.Pointer(&ht)))
		if ht.iItem >= 0 && ht.flags&lvhtOnStateIcon != 0 && l.check(int(ht.iItem)) != 0 {
			l.onToggle([]int{int(ht.iItem)})
		}
		return true, 0
	case lvnKeyDown:
		if l.checkboxes && l.onToggle != nil && uint16(rdU32(lp, 24)) == vkSpace {
			if rows := l.selectedRows(); len(rows) > 0 {
				l.onToggle(rows)
			}
			return true, 1
		}
	case nmDblClk:
		if l.onActivate != nil {
			if row := int(rdI32(lp, 24)); row >= 0 {
				l.onActivate(row)
			}
		}
		return true, 0
	case lvnItemChanged:
		if l.onSelect != nil && (rdU32(lp, 32)^rdU32(lp, 36))&(lvisSelected|lvisFocused) != 0 {
			l.onSelect(l.focused())
		}
		return true, 0
	case lvnColumnClick:
		if l.onSort != nil {
			l.onSort(int(rdI32(lp, 28)))
		}
		return true, 0
	}
	return false, 0
}
