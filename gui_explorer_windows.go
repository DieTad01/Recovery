//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// lrow es una fila de la lista: una carpeta o un archivo.
type lrow struct {
	n *Node
	e *Entry
}

func buildExplorer() {
	a := app
	a.hBack = ctl("BUTTON", "‹  Inicio", wsVisible|wsTabstop, 0, idBack, fBody)
	a.hSrc = ctl("STATIC", "", wsVisible|ssEndEllip, 0, 0, fSection)
	a.hStatus = ctl("STATIC", "", wsVisible|ssEndEllip, 0, 0, fBody)
	a.muted[a.hStatus] = true
	a.hSearch = ctl("EDIT", "", wsVisible|wsTabstop|wsBorder|esAutoHScrol, 0, idSearch, fBody)
	sendMsg(a.hSearch, emSetCueBanner, 1, uintptr(unsafe.Pointer(wstr("Buscar archivos o carpetas (ej.: tesis, *.jpg)"))))
	a.hProgress = ctl("msctls_progress32", "", wsVisible, 0, 0, fBody)
	sendMsg(a.hProgress, pbmSetRange32, 0, 1000)
	a.hStop = ctl("BUTTON", "Detener", wsVisible|wsTabstop, 0, idStop, fBody)

	th, _, _ := pCreateWindowExW.Call(wsExClientEdge, uintptr(unsafe.Pointer(wstr("SysTreeView32"))), 0,
		wsChild|wsVisible|wsTabstop|tvsHasButtons|tvsLinesAtRoot|tvsShowSelAlways|tvsFullRowSelect,
		0, 0, 10, 10, uintptr(a.hwnd), idTree, gInst, 0)
	a.hTree = syscall.Handle(th)
	a.ctlFont[a.hTree] = fBody
	sendMsg(a.hTree, wmSetFont, uintptr(a.fonts[fBody]), 0)
	sendMsg(a.hTree, tvmSetImageList, 0, sysSmallList())
	setTheme(a.hTree, "Explorer")

	a.hCrumb = ctl("STATIC", "", wsVisible|ssEndEllip, 0, 0, fBold)
	a.list = newVList(a.hwnd, idList, []vcol{
		{"Nombre", 240, false}, {"Tamaño", 80, true}, {"Modificado", 118, false},
		{"Estado", 120, false}, {"Tipo", 130, false}, {"Ubicación", 260, false},
	}, true, sysSmallList(), false)
	sendMsg(a.list.hwnd, wmSetFont, uintptr(a.fonts[fBody]), 0)
	a.list.text = rowText
	a.list.image = rowImage
	a.list.check = rowCheck
	a.list.onToggle = toggleRows
	a.list.onSort = sortBy
	a.list.onActivate = activateRow
	a.list.onSelect = func(int) { pSetTimer.Call(uintptr(app.hwnd), timerPreview, 120, 0) }
	a.hOverlay = ctl("STATIC", "", wsVisible|0x1, 0, 0, fSub) // SS_CENTER
	a.muted[a.hOverlay] = true

	a.hCanvas = newCanvas()
	a.hPrevName = ctl("STATIC", "", wsVisible|0x2000, 0, 0, fBold) // SS_EDITCONTROL (parte palabras largas)
	a.hPrevI = ctl("STATIC", "", wsVisible|0x2000, 0, 0, fBody)
	a.muted[a.hPrevI] = true

	a.hHideSys = ctl("BUTTON", "Ocultar archivos de Windows y de programas", wsVisible|wsTabstop|bsAutoCheck, 0, idHideSys, fBody)
	setChecked(a.hHideSys, true)
	a.hSelInfo = ctl("STATIC", "", wsVisible|ssEndEllip, 0, 0, fBody)
	a.hRecov = ctl("BUTTON", "Recuperar", wsVisible|wsTabstop|bsOwnerDraw, 0, idRecover, fButton)

	a.expCtls = []syscall.Handle{a.hBack, a.hSrc, a.hStatus, a.hSearch, a.hProgress, a.hStop, a.hTree,
		a.hCrumb, a.list.hwnd, a.hOverlay, a.hCanvas, a.hPrevName, a.hPrevI, a.hHideSys, a.hSelInfo, a.hRecov}
}

// ---------------------------------------------------------------- análisis

func startScan() {
	a := app
	row := a.drives.focused()
	if row < 0 || row >= len(a.sources) {
		msgBox(a.hwnd, "Elige primero la unidad donde estaban los archivos.", "Recovery", mbIconInfo)
		return
	}
	src := a.sources[row]
	ss, err := NewSession(src.Path, isChecked(a.hQuick))
	if err != nil {
		msgBox(a.hwnd, "No se puede leer esa unidad:\n\n"+err.Error(), "Recovery", mbIconError)
		return
	}
	a.ss = ss
	a.srcName = sourceName(src)
	a.tree, a.cur, a.curPath, a.rows, a.search = nil, nil, "", nil, false
	a.nodes = nil
	sendMsg(a.hTree, tvmDeleteItem, 0, tviRoot)
	a.list.setCount(0)
	a.selfEdit = true
	setText(a.hSearch, "")
	a.selfEdit = false
	setText(a.hSrc, a.srcName)
	setText(a.hCrumb, "")
	setText(a.hOverlay, "Leyendo el disco…\r\nLos archivos irán apareciendo aquí.")
	show(a.hOverlay, true)
	clearPreview()
	atomic.StoreInt32(&stopFlag, 0)
	a.busy = busyScan
	enable(a.hStop, true)
	enable(a.hBack, false)
	enable(a.hRecov, false)
	updateSelInfo()
	showPage(pageExplorer)
	updateStatus()
	go ss.Run(func(ev int, p SessionProgress) {
		a.progMu.Lock()
		a.prog = p
		a.progMu.Unlock()
		switch ev {
		case evProgress:
			if atomic.CompareAndSwapInt32(&a.progPnd, 0, 1) {
				postMsg(a.hwnd, wmAppProgress, 0, 0)
			}
		case evResults:
			postMsg(a.hwnd, wmAppResults, 0, 0)
		case evDone:
			postMsg(a.hwnd, wmAppDone, 0, 0)
		}
	})
}

func goHome() {
	if app.busy != busyNone {
		return
	}
	if app.ss != nil {
		app.ss.Close()
		app.ss = nil
	}
	app.tree = nil
	fillDrives()
	showPage(pageHome)
}

func totalFound() (int, int64) {
	if app.tree == nil {
		return 0, 0
	}
	n, sz := 0, int64(0)
	for _, r := range []int{rootPath, rootLoose, rootCarved} {
		n += app.tree.Roots[r].Count
		sz += app.tree.Roots[r].Size
	}
	return n, sz
}

// updateStatus muestra el avance en la barra superior.
func updateStatus() {
	a := app
	if a.busy == busyRecover {
		a.progMu.Lock()
		rp := a.recProg
		a.progMu.Unlock()
		setText(a.hStatus, fmt.Sprintf("Recuperando: %s de %s archivos · %s guardados",
			groupDigits(int64(rp.done)), groupDigits(int64(rp.total)), humanSize(rp.bytes)))
		if rp.total > 0 {
			sendMsg(a.hProgress, pbmSetPos, uintptr(rp.done*1000/rp.total), 0)
		}
		return
	}
	a.progMu.Lock()
	p := a.prog
	a.progMu.Unlock()
	n, sz := totalFound()
	found := ""
	if n > 0 {
		found = fmt.Sprintf(" · %s archivos encontrados (%s)", groupDigits(int64(n)), humanSize(sz))
	}
	var s string
	switch p.Stage {
	case stagePrepare:
		s = "Leyendo la tabla de archivos del disco…"
	case stageDeep:
		s = fmt.Sprintf("Analizando todo el disco: %.0f %% · faltan ~%s%s", p.Frac*100, humanDur(p.ETA), found)
	case stageAnalyze:
		s = "Reconstruyendo carpetas…" + found
	case stageCarve:
		s = fmt.Sprintf("Buscando archivos sin nombre (fotos, videos, documentos…): %.0f %%%s", p.Frac*100, found)
	case stageDone:
		s = "Análisis terminado" + found
		if p.Interrupted {
			s = "Análisis detenido" + found
		}
	}
	if a.busy == busyScan && p.Stage != stagePrepare && p.Stage != stageDone && n > 0 {
		s += " · ya puedes explorar"
	}
	setText(a.hStatus, s)
	sendMsg(a.hProgress, pbmSetPos, uintptr(p.Frac*1000), 0)
}

func scanDone() {
	a := app
	a.busy = busyNone
	atomic.StoreInt32(&stopFlag, 0)
	enable(a.hBack, true)
	enable(a.hRecov, true)
	updateStatus()
	layout()
	if n, _ := totalFound(); n == 0 && a.tree != nil && a.tree.Roots[rootLost].Count == 0 {
		setText(a.hOverlay, "No se encontraron archivos en esta unidad.")
		show(a.hOverlay, true)
	}
	if a.autotest {
		autotestAfterScan()
	}
}

// ---------------------------------------------------------------- árbol

func rebuildTree() {
	a := app
	if a.ss == nil {
		return
	}
	checked := map[string]bool{}
	if a.tree != nil {
		for _, e := range a.tree.Entries {
			if e.Checked {
				checked[e.Key] = true
			}
		}
	}
	a.ss.mu.RLock()
	entries := a.ss.Entries
	a.ss.mu.RUnlock()
	for _, e := range entries {
		e.Checked = checked[e.Key] && e.Checkable()
	}
	a.tree = BuildTree(entries, isChecked(a.hHideSys))
	prev := a.curPath
	sendMsg(a.hTree, tvmDeleteItem, 0, tviRoot)
	a.nodes = nil
	for _, r := range a.tree.Roots {
		if r.Count > 0 {
			tvInsert(tviRoot, r)
		}
	}
	n, _ := totalFound()
	show(a.hOverlay, n == 0 && a.tree.Roots[rootLost].Count == 0)
	if n == 0 {
		setText(a.hOverlay, "Leyendo el disco…\r\nLos archivos irán apareciendo aquí.")
	}
	target := a.tree.Find(prev)
	if target == nil {
		for _, r := range a.tree.Roots {
			if r.Count > 0 {
				target = r
				break
			}
		}
	}
	if target != nil {
		selectNode(target)
	}
	if a.search {
		applySearch()
	}
	updateSelInfo()
}

func nodeIcon(n *Node) int32 {
	depth := 0
	for p := n.Parent; p != nil; p = p.Parent {
		depth++
	}
	if (n.Root == rootTypes && depth == 2) || (n.Root == rootCarved && depth == 1) {
		if !strings.HasPrefix(n.Name, "(") {
			return fileIcon(strings.ToLower(n.Name))
		}
	}
	return fileIcon("/")
}

func tvInsert(parent uintptr, n *Node) uintptr {
	idx := len(app.nodes)
	app.nodes = append(app.nodes, n)
	text := fmt.Sprintf("%s  (%s)", n.Name, groupDigits(int64(n.Count)))
	icon := nodeIcon(n)
	ins := tvinsert{hParent: parent, hInsertAfter: tviLast, item: tvitem{
		mask:    tvifText | tvifImage | tvifSelImage | tvifParam | tvifChildren,
		pszText: wstr(text), iImage: icon, iSelectedImage: icon, lParam: uintptr(idx)}}
	if len(n.Kids) > 0 {
		ins.item.cChildren = 1
	}
	h := sendMsg(app.hTree, tvmInsertItemW, 0, uintptr(unsafe.Pointer(&ins)))
	n.Handle = h
	n.Inserted = false
	return h
}

func insertKids(n *Node) {
	if n.Inserted {
		return
	}
	n.Inserted = true
	for _, k := range n.Kids {
		tvInsert(n.Handle, k)
	}
}

// selectNode abre una carpeta del árbol (desplegando las superiores).
func selectNode(n *Node) {
	var chain []*Node
	for p := n.Parent; p != nil; p = p.Parent {
		chain = append([]*Node{p}, chain...)
	}
	for _, p := range chain {
		insertKids(p)
		sendMsg(app.hTree, tvmExpand, tveExpand, p.Handle)
	}
	sendMsg(app.hTree, tvmSelectItem, tvgnCaret, n.Handle)
	sendMsg(app.hTree, tvmEnsureVisible, 0, n.Handle)
	openNode(n)
}

func treeNotify(lp uintptr) {
	code := rdU32(lp, 16)
	idx := int(rdPtr(lp, 136)) // itemNew.lParam
	if idx < 0 || idx >= len(app.nodes) {
		return
	}
	n := app.nodes[idx]
	switch code {
	case tvnItemExpandingW:
		insertKids(n)
	case tvnSelChangedW:
		if n != app.cur || app.search {
			app.selfEdit = true
			setText(app.hSearch, "")
			app.selfEdit = false
			openNode(n)
		}
	}
}

// ---------------------------------------------------------------- lista

func openNode(n *Node) {
	a := app
	a.cur, a.curPath, a.search = n, n.Path, false
	a.rows = a.rows[:0]
	for _, k := range n.Kids {
		a.rows = append(a.rows, lrow{n: k})
	}
	for _, e := range n.Files {
		a.rows = append(a.rows, lrow{e: e})
	}
	sortRows()
	setText(a.hCrumb, strings.ReplaceAll(n.Path, "/", "  ›  "))
	a.list.setCount(len(a.rows))
	clearPreview()
}

func applySearch() {
	a := app
	if a.tree == nil {
		return
	}
	text := strings.TrimSpace(getText(a.hSearch))
	m := ParseSearch(text)
	if m == nil {
		if a.search && a.cur != nil {
			openNode(a.cur)
		}
		return
	}
	a.search = true
	a.rows = a.rows[:0]
	res := a.tree.Search(m)
	for _, e := range res {
		a.rows = append(a.rows, lrow{e: e})
	}
	sortRows()
	setText(a.hCrumb, fmt.Sprintf("Resultados de «%s»: %s archivos", text, groupDigits(int64(len(res)))))
	a.list.setCount(len(a.rows))
	clearPreview()
}

func displayDir(e *Entry) string {
	d := strings.ReplaceAll(e.Dir, "/", `\`)
	switch e.Group {
	case grpLoose:
		return `(sin ubicación)\` + d
	case grpCarved:
		return "(sin nombre)"
	}
	return `\` + d
}

func rowText(row, col int) string {
	if row >= len(app.rows) {
		return ""
	}
	r := app.rows[row]
	if r.n != nil {
		switch col {
		case 0:
			return r.n.Name
		case 1:
			return humanSize(r.n.Size)
		case 3:
			return groupDigits(int64(r.n.Count)) + " archivos"
		case 4:
			return "Carpeta"
		case 5:
			if r.n.Parent != nil {
				return strings.ReplaceAll(r.n.Parent.Path, "/", `\`)
			}
		}
		return ""
	}
	e := r.e
	switch col {
	case 0:
		return e.Name
	case 1:
		return humanSize(e.Size)
	case 2:
		if !e.MTime.IsZero() {
			return e.MTime.Local().Format("02/01/2006 15:04")
		}
	case 3:
		return e.StateText()
	case 4:
		return typeName(e.Ext)
	case 5:
		return displayDir(e)
	}
	return ""
}

func rowImage(row int) int32 {
	if row >= len(app.rows) {
		return -1
	}
	if r := app.rows[row]; r.n != nil {
		return fileIcon("/")
	} else {
		return fileIcon(r.e.Ext)
	}
}

func rowCheck(row int) int {
	if row >= len(app.rows) {
		return 0
	}
	r := app.rows[row]
	if r.n != nil {
		if r.n.Sel == 0 {
			return 0
		}
		if r.n.AllChecked() {
			return 2
		}
		return 1
	}
	if !r.e.Checkable() {
		return 0
	}
	if r.e.Checked {
		return 2
	}
	return 1
}

func toggleRows(rows []int) {
	a := app
	if a.tree == nil || len(rows) == 0 {
		return
	}
	// Todas al estado contrario de la primera.
	on := rowCheck(rows[0]) != 2
	for _, i := range rows {
		if i >= len(a.rows) {
			continue
		}
		if r := a.rows[i]; r.n != nil {
			a.tree.SetNodeChecked(r.n, on)
		} else {
			a.tree.SetChecked(r.e, on)
		}
	}
	a.list.refresh()
	updateSelInfo()
}

func activateRow(row int) {
	if row < len(app.rows) && app.rows[row].n != nil {
		selectNode(app.rows[row].n)
	}
}

func sortBy(col int) {
	if app.sortCol == col {
		app.sortDesc = !app.sortDesc
	} else {
		app.sortCol, app.sortDesc = col, col == 1 || col == 2 // tamaño y fecha: mayor primero
	}
	sortRows()
	app.list.refresh()
}

func sortRows() {
	col, desc := app.sortCol, app.sortDesc
	less := func(a, b lrow) bool {
		if (a.n != nil) != (b.n != nil) {
			return a.n != nil // carpetas primero
		}
		var c int
		switch {
		case a.n != nil:
			if col == 1 {
				c = cmp64(a.n.Size, b.n.Size)
			} else {
				c = strings.Compare(fold(a.n.Name), fold(b.n.Name))
			}
		case col == 1:
			c = cmp64(a.e.Size, b.e.Size)
		case col == 2:
			c = cmp64(a.e.MTime.Unix(), b.e.MTime.Unix())
		case col == 3:
			c = a.e.State - b.e.State
		case col == 4:
			c = strings.Compare(a.e.Ext, b.e.Ext)
		case col == 5:
			c = strings.Compare(fold(a.e.Dir), fold(b.e.Dir))
		}
		if c == 0 {
			na, nb := "", ""
			if a.e != nil {
				na, nb = fold(a.e.Name), fold(b.e.Name)
			}
			c = strings.Compare(na, nb)
		}
		if desc {
			return c > 0
		}
		return c < 0
	}
	sort.SliceStable(app.rows, func(i, j int) bool { return less(app.rows[i], app.rows[j]) })
}

func cmp64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func updateSelInfo() {
	if app.tree == nil || app.tree.Checked == 0 {
		setText(app.hSelInfo, "Marca las casillas de lo que quieres recuperar.")
		return
	}
	setText(app.hSelInfo, fmt.Sprintf("Marcados: %s archivos · %s",
		groupDigits(int64(app.tree.Checked)), humanSize(app.tree.CheckedSize)))
}

// ---------------------------------------------------------------- recuperar

// doRecover guarda lo marcado; dest vacío = preguntar la carpeta.
func doRecover(dest string) {
	a := app
	if a.ss == nil || a.tree == nil {
		return
	}
	if a.busy != busyNone {
		msgBox(a.hwnd, "Espera a que termine el análisis o pulsa «Detener».", "Recovery", mbIconInfo)
		return
	}
	sel := a.tree.CheckedEntries()
	if len(sel) == 0 {
		// Sin casillas: se toma lo seleccionado en la lista.
		seen := map[*Entry]bool{}
		var walk func(n *Node)
		walk = func(n *Node) {
			for _, e := range n.Files {
				if e.Checkable() && !seen[e] {
					seen[e] = true
					sel = append(sel, e)
				}
			}
			for _, k := range n.Kids {
				walk(k)
			}
		}
		for _, i := range a.list.selectedRows() {
			if i >= len(a.rows) {
				continue
			}
			if r := a.rows[i]; r.n != nil {
				walk(r.n)
			} else if r.e.Checkable() && !seen[r.e] {
				seen[r.e] = true
				sel = append(sel, r.e)
			}
		}
	}
	if len(sel) == 0 {
		msgBox(a.hwnd, "Marca las casillas de los archivos o carpetas que quieres recuperar.", "Recovery", mbIconInfo)
		return
	}
	var total int64
	for _, e := range sel {
		total += e.Size
	}
	if dest == "" {
		dest = browseFolder(fmt.Sprintf("¿Dónde guardo los %s archivos (%s)? Elige una carpeta en OTRO disco.",
			groupDigits(int64(len(sel))), humanSize(total)))
		if dest == "" {
			return
		}
	}
	if fatal, warn := checkDestination(a.ss.Source, dest); fatal != "" {
		msgBox(a.hwnd, fatal, "No se puede guardar ahí", mbIconError)
		return
	} else if warn != "" && !a.autotest {
		if msgBox(a.hwnd, warn+"\n\n¿Guardar ahí de todos modos?", "Atención", mbYesNo|mbIconWarning|mbDefButton2) != idYes {
			return
		}
	}
	if free := freeSpace(dest); free >= 0 && free < total && !a.autotest {
		if msgBox(a.hwnd, fmt.Sprintf("En el destino hay %s libres y lo marcado ocupa %s.\nSe guardará hasta llenarlo. ¿Continuar?",
			humanSize(free), humanSize(total)), "Poco espacio", mbYesNo|mbIconWarning) != idYes {
			return
		}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		msgBox(a.hwnd, "No se puede crear la carpeta:\n"+err.Error(), "Recovery", mbIconError)
		return
	}
	a.recDest = dest
	a.busy = busyRecover
	atomic.StoreInt32(&stopFlag, 0)
	enable(a.hStop, true)
	enable(a.hBack, false)
	enable(a.hRecov, false)
	layout()
	updateStatus()
	ss := a.ss
	go func() {
		last := time.Time{}
		res := ss.Recover(sel, dest, func(done, total int, bytes int64) {
			a.progMu.Lock()
			a.recProg.done, a.recProg.total, a.recProg.bytes = done, total, bytes
			a.progMu.Unlock()
			if time.Since(last) > 200*time.Millisecond || done == total {
				last = time.Now()
				postMsg(a.hwnd, wmAppRecProg, 0, 0)
			}
		})
		a.progMu.Lock()
		a.recRes = res
		a.progMu.Unlock()
		postMsg(a.hwnd, wmAppRecDone, 0, 0)
	}()
}

func recoverDone() {
	a := app
	a.busy = busyNone
	atomic.StoreInt32(&stopFlag, 0)
	enable(a.hBack, true)
	enable(a.hRecov, true)
	a.progMu.Lock()
	res := a.recRes
	a.progMu.Unlock()
	layout()
	updateStatus()
	msg := fmt.Sprintf("Se recuperaron %s archivos (%s) en:\n%s", groupDigits(res.Good), humanSize(res.Bytes), a.recDest)
	if res.Doubt > 0 {
		msg += fmt.Sprintf("\n\n%s archivos pueden estar dañados: están en la carpeta «%s».", groupDigits(res.Doubt), dirDoubt)
	}
	if res.Empty > 0 {
		msg += fmt.Sprintf("\n\n%s archivos tenían el contenido en blanco y no se guardaron (pasa en los SSD).", groupDigits(res.Empty))
	}
	if res.Errors > 0 {
		msg += fmt.Sprintf("\n\n%s archivos no se pudieron escribir.", groupDigits(res.Errors))
	}
	if res.Full {
		msg += "\n\nEl destino se quedó SIN ESPACIO: no se guardó todo."
	}
	if res.Stopped {
		msg += "\n\nLa recuperación se detuvo antes de terminar."
	}
	if a.autotest {
		if os.Getenv("RECOVERY_GUI_KEEP") == "" {
			pPostQuitMessage.Call(0)
		}
		return
	}
	if msgBox(a.hwnd, msg+"\n\n¿Abrir la carpeta ahora?", "Recuperación terminada", mbYesNo|mbIconInfo) == idYes {
		openFolder(a.recDest)
	}
}

// ---------------------------------------------------------------- pruebas automáticas

// autotestStart permite manejar la ventana con variables de entorno (lo
// usan las pruebas bajo Wine; no afecta al uso normal).
func autotestStart() {
	switch os.Getenv("RECOVERY_GUI_TOOL") {
	case "disco":
		openDiskWindow()
	case "programas":
		openProgramsWindow()
	}
	src := os.Getenv("RECOVERY_GUI_SRC")
	if src == "" {
		return
	}
	for i, s := range app.sources {
		if strings.EqualFold(s.Path, src) {
			app.drives.selectRow(i)
		}
	}
	setChecked(app.hQuick, os.Getenv("RECOVERY_GUI_QUICK") != "")
	startScan()
}

func autotestAfterScan() {
	if p := os.Getenv("RECOVERY_GUI_OPEN"); p != "" && app.tree != nil {
		if n := app.tree.Find(p); n != nil {
			selectNode(n)
			pick := os.Getenv("RECOVERY_GUI_PICK")
			for i, r := range app.rows {
				if r.e != nil && (pick == "" || strings.EqualFold(r.e.Name, pick)) {
					app.list.selectRow(i)
					break
				}
			}
		}
	}
	if q := os.Getenv("RECOVERY_GUI_SEARCH"); q != "" {
		setText(app.hSearch, q)
		applySearch()
	}
	if c := os.Getenv("RECOVERY_GUI_CHECK"); c != "" && app.tree != nil {
		for _, p := range strings.Split(c, ";") {
			if n := app.tree.Find(p); n != nil {
				app.tree.SetNodeChecked(n, true)
			}
		}
		app.list.refresh()
		updateSelInfo()
	}
	if d := os.Getenv("RECOVERY_GUI_DST"); d != "" {
		doRecover(filepath.Clean(d))
		return
	}
	if os.Getenv("RECOVERY_GUI_KEEP") == "" {
		pPostQuitMessage.Call(0)
	}
}
