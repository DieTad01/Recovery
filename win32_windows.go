//go:build windows

package main

// Acceso a la API de ventanas de Windows (Win32) sin dependencias externas.

import (
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	uxtheme  = syscall.NewLazyDLL("uxtheme.dll")

	pRegisterClassExW    = user32.NewProc("RegisterClassExW")
	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pGetMessageW         = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessageW    = user32.NewProc("DispatchMessageW")
	pIsDialogMessageW    = user32.NewProc("IsDialogMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pSendMessageW        = user32.NewProc("SendMessageW")
	pPostMessageW        = user32.NewProc("PostMessageW")
	pSetWindowTextW      = user32.NewProc("SetWindowTextW")
	pGetWindowTextW      = user32.NewProc("GetWindowTextW")
	pGetWindowTextLenW   = user32.NewProc("GetWindowTextLengthW")
	pEnableWindow        = user32.NewProc("EnableWindow")
	pShowWindow          = user32.NewProc("ShowWindow")
	pUpdateWindow        = user32.NewProc("UpdateWindow")
	pLoadCursorW         = user32.NewProc("LoadCursorW")
	pLoadImageW          = user32.NewProc("LoadImageW")
	pMessageBoxW         = user32.NewProc("MessageBoxW")
	pMoveWindow          = user32.NewProc("MoveWindow")
	pGetClientRect       = user32.NewProc("GetClientRect")
	pInvalidateRect      = user32.NewProc("InvalidateRect")
	pBeginPaint          = user32.NewProc("BeginPaint")
	pEndPaint            = user32.NewProc("EndPaint")
	pFillRect            = user32.NewProc("FillRect")
	pDrawTextW           = user32.NewProc("DrawTextW")
	pGetDC               = user32.NewProc("GetDC")
	pReleaseDC           = user32.NewProc("ReleaseDC")
	pSetTimer            = user32.NewProc("SetTimer")
	pKillTimer           = user32.NewProc("KillTimer")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pSetFocus            = user32.NewProc("SetFocus")
	pSetWindowPos        = user32.NewProc("SetWindowPos")
	pAdjustWindowRectEx  = user32.NewProc("AdjustWindowRectEx")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pSetProcessDPIAware  = user32.NewProc("SetProcessDPIAware")
	pGetDpiForWindow     = user32.NewProc("GetDpiForWindow")
	pDrawIconEx          = user32.NewProc("DrawIconEx")
	pDestroyIcon         = user32.NewProc("DestroyIcon")
	pSystemParametersInf = user32.NewProc("SystemParametersInfoW")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pIsWindowEnabled     = user32.NewProc("IsWindowEnabled")
	pGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")

	pCreateFontW      = gdi32.NewProc("CreateFontW")
	pDeleteObject     = gdi32.NewProc("DeleteObject")
	pGetDeviceCaps    = gdi32.NewProc("GetDeviceCaps")
	pCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	pCreatePen        = gdi32.NewProc("CreatePen")
	pSetBkMode        = gdi32.NewProc("SetBkMode")
	pSetBkColor       = gdi32.NewProc("SetBkColor")
	pSetTextColor     = gdi32.NewProc("SetTextColor")
	pSelectObject     = gdi32.NewProc("SelectObject")
	pRoundRect        = gdi32.NewProc("RoundRect")
	pStretchDIBits    = gdi32.NewProc("StretchDIBits")
	pSetStretchBltMod = gdi32.NewProc("SetStretchBltMode")

	pInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
	pSHGetFileInfoW       = shell32.NewProc("SHGetFileInfoW")
	pSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	pSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	pCoInitialize         = ole32.NewProc("CoInitialize")
	pSetWindowTheme       = uxtheme.NewProc("SetWindowTheme")
)

const (
	wsOverlappedWin = 0x00CF0000
	wsChild         = 0x40000000
	wsVisible       = 0x10000000
	wsTabstop       = 0x00010000
	wsGroup         = 0x00020000
	wsClipChildren  = 0x02000000
	wsBorder        = 0x00800000
	wsVScroll       = 0x00200000
	wsExClientEdge  = 0x00000200
	wsExControlPar  = 0x00010000

	bsPushButton = 0x0
	bsAutoCheck  = 0x3
	bsOwnerDraw  = 0xB
	esAutoHScrol = 0x80
	ssNotify     = 0x100
	ssEndEllip   = 0x4000
	ssPathEllip  = 0x8000

	wmCreate       = 0x0001
	wmDestroy      = 0x0002
	wmSize         = 0x0005
	wmPaint        = 0x000F
	wmClose        = 0x0010
	wmEraseBkgnd   = 0x0014
	wmGetMinMax    = 0x0024
	wmSetFont      = 0x0030
	wmNotify       = 0x004E
	wmCommand      = 0x0111
	wmTimer        = 0x0113
	wmCtlColorEdit = 0x0133
	wmSetCursor    = 0x0020
	wmCtlColorBtn  = 0x0135
	wmCtlColorStat = 0x0138
	wmDrawItem     = 0x002B
	wmDpiChanged   = 0x02E0
	wmApp          = 0x8000

	bnClicked = 0
	enChange  = 0x0300

	bmGetCheck = 0x00F0
	bmSetCheck = 0x00F1

	emSetCueBanner = 0x1501

	pbmSetRange32 = 0x0406
	pbmSetPos     = 0x0402

	swHide = 0
	swShow = 5

	mbOK          = 0x0
	mbYesNo       = 0x4
	mbIconError   = 0x10
	mbIconQuest   = 0x20
	mbIconWarning = 0x30
	mbIconInfo    = 0x40
	mbDefButton2  = 0x100
	idYes         = 6

	// ListView
	lvsReport        = 0x1
	lvsShowSelAlways = 0x8
	lvsSingleSel     = 0x4
	lvsOwnerData     = 0x1000
	lvsNoSortHeader  = 0x8000
	lvsExFullRow     = 0x20
	lvsExCheckboxes  = 0x4
	lvsExDoubleBuf   = 0x10000
	lvmSetImageList  = 0x1003
	lvmGetNextItem   = 0x100C
	lvmSetCallback   = 0x100B
	lvmEnsureVisible = 0x1013
	lvmRedrawItems   = 0x1015
	lvmHitTest       = 0x1012
	lvmSetItemState  = 0x102B
	lvmSetItemCount  = 0x102F
	lvmSetExStyle    = 0x1036
	lvmInsertColumnW = 0x1061
	lvmSetColumnW    = 0x1060
	lvmDeleteColumn  = 0x101C
	lvifText         = 0x1
	lvifImage        = 0x2
	lvifState        = 0x8
	lvisSelected     = 0x2
	lvisFocused      = 0x1
	lvisStateImgMask = 0xF000
	lvniSelected     = 0x2
	lvniFocused      = 0x1
	lvhtOnStateIcon  = 0x8
	lvsilSmall       = 1
	lvcfFmt          = 0x1
	lvcfWidth        = 0x2
	lvcfText         = 0x4
	lvcfmtRight      = 0x1

	// Notificaciones (códigos negativos)
	nmClick           = ^uint32(1)   // -2
	nmDblClk          = ^uint32(2)   // -3
	lvnItemChanged    = ^uint32(100) // -101
	lvnColumnClick    = ^uint32(107) // -108
	lvnKeyDown        = ^uint32(154) // -155
	lvnGetDispInfoW   = ^uint32(176) // -177
	tvnSelChangedW    = ^uint32(450) // -451
	tvnItemExpandingW = ^uint32(453) // -454

	// TreeView
	tvsHasButtons    = 0x1
	tvsLinesAtRoot   = 0x4
	tvsShowSelAlways = 0x20
	tvsTrackSelect   = 0x200
	tvsFullRowSelect = 0x1000
	tvmInsertItemW   = 0x1132
	tvmDeleteItem    = 0x1101
	tvmExpand        = 0x1102
	tvmSelectItem    = 0x110B
	tvmSetImageList  = 0x1109
	tvmEnsureVisible = 0x1114
	tvifText         = 0x1
	tvifImage        = 0x2
	tvifParam        = 0x4
	tvifSelImage     = 0x20
	tvifChildren     = 0x40
	tvgnCaret        = 0x9
	tveExpand        = 0x2

	vkSpace = 0x20
)

var (
	tviRoot = ^uintptr(0xFFFF) // TVI_ROOT = -0x10000
	tviLast = ^uintptr(0xFFFD) // TVI_LAST = -0xFFFE
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

type rect struct{ left, top, right, bottom int32 }

type paintstruct struct {
	hdc         syscall.Handle
	fErase      int32
	rcPaint     rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
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

type lvcolumn struct {
	mask       uint32
	fmt        int32
	cx         int32
	pszText    *uint16
	cchTextMax int32
	iSubItem   int32
	iImage     int32
	iOrder     int32
	cxMin      int32
	cxDefault  int32
	cxIdeal    int32
}

type lvitem struct {
	mask       uint32
	iItem      int32
	iSubItem   int32
	state      uint32
	stateMask  uint32
	pszText    *uint16
	cchTextMax int32
	iImage     int32
	lParam     uintptr
	iIndent    int32
	iGroupID   int32
	cColumns   uint32
	puColumns  uintptr
	piColFmt   uintptr
	iGroup     int32
}

type lvhittest struct {
	x, y     int32
	flags    uint32
	iItem    int32
	iSubItem int32
	iGroup   int32
}

type tvitem struct {
	mask           uint32
	hItem          uintptr
	state          uint32
	stateMask      uint32
	pszText        *uint16
	cchTextMax     int32
	iImage         int32
	iSelectedImage int32
	cChildren      int32
	lParam         uintptr
}

type tvinsert struct {
	hParent      uintptr
	hInsertAfter uintptr
	item         tvitem
	pad          [32]byte // TVITEMEX es más grande que TVITEM
}

type shfileinfo struct {
	hIcon         syscall.Handle
	iIcon         int32
	dwAttributes  uint32
	szDisplayName [260]uint16
	szTypeName    [80]uint16
}

type bitmapinfoheader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

// ---------------------------------------------------------------- utilidades

// mem convierte una dirección que llega de Windows en un puntero. La
// memoria pertenece a Windows (no al recolector de Go).
func mem(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

func rdU32(p uintptr, off uintptr) uint32 { return *(*uint32)(mem(p + off)) }
func rdI32(p uintptr, off uintptr) int32  { return *(*int32)(mem(p + off)) }
func rdPtr(p uintptr, off uintptr) uintptr {
	return *(*uintptr)(mem(p + off))
}
func wrU32(p uintptr, off uintptr, v uint32) { *(*uint32)(mem(p + off)) = v }
func wrI32(p uintptr, off uintptr, v int32)  { *(*int32)(mem(p + off)) = v }

func sendMsg(h syscall.Handle, msg uint32, w, l uintptr) uintptr {
	r, _, _ := pSendMessageW.Call(uintptr(h), uintptr(msg), w, l)
	return r
}

func postMsg(h syscall.Handle, msg uint32, w, l uintptr) {
	pPostMessageW.Call(uintptr(h), uintptr(msg), w, l)
}

func wstr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func setText(h syscall.Handle, s string) {
	pSetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(wstr(s))))
}

func getText(h syscall.Handle) string {
	n, _, _ := pGetWindowTextLenW.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func enable(h syscall.Handle, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	pEnableWindow.Call(uintptr(h), v)
}

func isEnabled(h syscall.Handle) bool {
	r, _, _ := pIsWindowEnabled.Call(uintptr(h))
	return r != 0
}

func show(h syscall.Handle, on bool) {
	v := uintptr(swHide)
	if on {
		v = swShow
	}
	pShowWindow.Call(uintptr(h), v)
}

// ownerChecks guarda el estado de las casillas dibujadas por el programa.
var ownerChecks = map[syscall.Handle]bool{}

func isChecked(h syscall.Handle) bool {
	if v, ok := ownerChecks[h]; ok {
		return v
	}
	return sendMsg(h, bmGetCheck, 0, 0) == 1
}

func setChecked(h syscall.Handle, on bool) {
	if _, ok := ownerChecks[h]; ok {
		ownerChecks[h] = on
		invalidate(h)
		return
	}
	v := uintptr(0)
	if on {
		v = 1
	}
	sendMsg(h, bmSetCheck, v, 0)
}

func moveWin(h syscall.Handle, x, y, w, ht int) {
	if w < 0 {
		w = 0
	}
	if ht < 0 {
		ht = 0
	}
	pMoveWindow.Call(uintptr(h), uintptr(x), uintptr(y), uintptr(w), uintptr(ht), 1)
}

func clientSize(h syscall.Handle) (int, int) {
	var r rect
	pGetClientRect.Call(uintptr(h), uintptr(unsafe.Pointer(&r)))
	return int(r.right - r.left), int(r.bottom - r.top)
}

func invalidate(h syscall.Handle) { pInvalidateRect.Call(uintptr(h), 0, 1) }

func rgb(r, g, b byte) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }

func newBrush(c uint32) syscall.Handle {
	h, _, _ := pCreateSolidBrush.Call(uintptr(c))
	return syscall.Handle(h)
}

func newFont(size int, weight int, face string) syscall.Handle {
	h := -size // tamaño de carácter en píxeles
	r, _, _ := pCreateFontW.Call(uintptr(h), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(wstr(face))))
	return syscall.Handle(r)
}

func msgBox(owner syscall.Handle, text, title string, flags uint32) int {
	r, _, _ := pMessageBoxW.Call(uintptr(owner), uintptr(unsafe.Pointer(wstr(text))),
		uintptr(unsafe.Pointer(wstr(title))), uintptr(flags))
	return int(r)
}

func setTheme(h syscall.Handle, name string) {
	if pSetWindowTheme.Find() == nil {
		pSetWindowTheme.Call(uintptr(h), uintptr(unsafe.Pointer(wstr(name))), 0)
	}
}

// ---------------------------------------------------------------- iconos del sistema

const (
	shgfiIcon        = 0x100
	shgfiTypeName    = 0x400
	shgfiSysIconIdx  = 0x4000
	shgfiLargeIcon   = 0x0
	shgfiSmallIcon   = 0x1
	shgfiUseFileAttr = 0x10
	fileAttrDir      = 0x10
	fileAttrNormal   = 0x80
)

var (
	sysImgSmall uintptr
	iconCache   = map[string]int32{}
	typeCache   = map[string]string{}
)

// fileIcon devuelve el índice del icono de Windows para una extensión
// (o de carpeta si ext == "/"), en la lista de iconos pequeños del sistema.
func fileIcon(ext string) int32 {
	if i, ok := iconCache[ext]; ok {
		return i
	}
	var sfi shfileinfo
	name, attr := "archivo."+ext, uint32(fileAttrNormal)
	if ext == "/" {
		name, attr = "carpeta", fileAttrDir
	}
	r, _, _ := pSHGetFileInfoW.Call(uintptr(unsafe.Pointer(wstr(name))), uintptr(attr),
		uintptr(unsafe.Pointer(&sfi)), unsafe.Sizeof(sfi),
		shgfiSysIconIdx|shgfiSmallIcon|shgfiUseFileAttr|shgfiTypeName)
	if sysImgSmall == 0 {
		sysImgSmall = r
	}
	iconCache[ext] = sfi.iIcon
	if ext != "/" {
		typeCache[ext] = syscall.UTF16ToString(sfi.szTypeName[:])
	}
	return sfi.iIcon
}

// typeName devuelve la descripción de Windows de un tipo de archivo.
func typeName(ext string) string {
	if ext == "" {
		return "Archivo"
	}
	if _, ok := typeCache[ext]; !ok {
		fileIcon(ext)
	}
	if t := typeCache[ext]; t != "" {
		return t
	}
	return "Archivo " + toUpperASCII(ext)
}

func toUpperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// largeIcon devuelve un icono grande (32x32) para la vista previa.
func largeIcon(ext string) syscall.Handle {
	var sfi shfileinfo
	name, attr := "archivo."+ext, uint32(fileAttrNormal)
	if ext == "/" {
		name, attr = "carpeta", fileAttrDir
	}
	pSHGetFileInfoW.Call(uintptr(unsafe.Pointer(wstr(name))), uintptr(attr),
		uintptr(unsafe.Pointer(&sfi)), unsafe.Sizeof(sfi), shgfiIcon|shgfiLargeIcon|shgfiUseFileAttr)
	return sfi.hIcon
}

// driveIcon devuelve el índice del icono de una unidad («C:\»).
func driveIcon(root string) int32 {
	var sfi shfileinfo
	pSHGetFileInfoW.Call(uintptr(unsafe.Pointer(wstr(root))), 0,
		uintptr(unsafe.Pointer(&sfi)), unsafe.Sizeof(sfi), shgfiSysIconIdx|shgfiSmallIcon)
	return sfi.iIcon
}
