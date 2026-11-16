//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	h *FolderHit // resultado de «buscar carpeta»
}

const (
	cueFiles   = "Buscar archivos (ej.: tesis, *.jpg)"
	cueFolders = "Nombre de la carpeta (ej.: Empresa, Contab*)"
)

func buildExplorer() {
	a := app
	a.hBack = button("‹  Inicio", wsVisible|wsTabstop, idBack, btnFrame, fBold)
	a.hSrc = ctl("STATIC", "", wsVisible|ssEndEllip, 0, 0, fSection)
	a.hStatus = ctl("STATIC", "", wsVisible|ssEndEllip, 0, 0, fBody)
	a.muted[a.hStatus] = true
	a.hSearch = ctl("EDIT", "", wsVisible|wsTabstop|esAutoHScrol, 0, idSearch, fBody)
	sendMsg(a.hSearch, emSetCueBanner, 1, uintptr(unsafe.Pointer(wstr(cueFiles))))
	// Qué busca el buscador: archivos (por defecto) o una carpeta concreta.
	a.hSegFiles = button("Archivos", wsVisible|wsTabstop, idSegFiles, btnSeg, fBold)
	a.hSegFolders = button("Carpetas", wsVisible|wsTabstop, idSegFolders, btnSeg, fBold)
	ownerChecks[a.hSegFiles] = true
	a.hProgress = newBar(wsVisible)
	a.hStop = button("Detener", wsVisible|wsTabstop, idStop, btnFrame, fBold)

	th, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(wstr("SysTreeView32"))), 0,
		wsChild|wsVisible|wsTabstop|tvsHasButtons|tvsLinesAtRoot|tvsShowSelAlways|tvsFullRowSelect,
		0, 0, 10, 10, uintptr(a.hwnd), idTree, gInst, 0)
	a.hTree = syscall.Handle(th)
	a.ctlFont[a.hTree] = fBody
	sendMsg(a.hTree, wmSetFont, uintptr(a.fonts[fBody]), 0)
	sendMsg(a.hTree, tvmSetImageList, 0, sysSmallList())

	a.hCrumb = ctl("STATIC", "", wsVisible|ssEndEllip, 0, 0, fBold)
	a.list = newVList(a.hwnd, idList, []vcol{
		{"Nombre", 240, false}, {"Tamaño", 80, true}, {"Modificado", 118, false},
		{"Estado", 140, false}, {"Tipo", 130, false}, {"Ubicación", 260, false},
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

	a.hHideSys = button("Ocultar archivos de Windows y de programas", wsVisible|wsTabstop, idHideSys, btnCheck, fBody)
	setChecked(a.hHideSys, true)
	a.hSelInfo = ctl("STATIC", "", wsVisible|ssEndEllip, 0, 0, fBody)
	a.hSummary = button("Ver resumen…", wsVisible|wsTabstop, idSummary, btnFrame, fBold)
	a.hVerify = button("Verificar a fondo…", wsVisible|wsTabstop, idVerify, btnFrame, fBold)
	a.hRecov = button("Recuperar", wsVisible|wsTabstop, idRecover, btnFill, fButton)

	a.expCtls = []syscall.Handle{a.hBack, a.hSrc, a.hStatus, a.hSegFiles, a.hSegFolders, a.hSearch, a.hProgress, a.hStop, a.hTree,
		a.hCrumb, a.list.hwnd, a.hOverlay, a.hCanvas, a.hPrevName, a.hPrevI, a.hHideSys, a.hSummary, a.hVerify, a.hSelInfo, a.hRecov}
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
	if isSSD(src) && !a.autotest {
		q := ssdAdvice + "\n\nLo más seguro es pulsar «No» y usar «Crear imagen del disco…» primero; " +
			"después se analiza la imagen.\n\n¿Analizar directamente la unidad de todos modos?"
		if msgBox(a.hwnd, q, "Es un SSD", mbYesNo|mbIconWarning|mbDefButton2) != idYes {
			return
		}
	}
	analyzePath(src.Path, sourceName(src))
}

// analyzePath analiza una unidad o un archivo de imagen y abre el explorador.
func analyzePath(path, name string) {
	a := app
	if a.busy != busyNone {
		return
	}
	if a.ss != nil {
		a.ss.Close()
		a.ss = nil
	}
	ss, err := NewSession(path, isChecked(a.hQuick))
	if err != nil {
		msgBox(a.hwnd, "No se puede leer:\n\n"+err.Error(), "Recovery", mbIconError)
		return
	}
	a.ss = ss
	a.srcName = name
	a.tree, a.cur, a.curPath, a.rows, a.search = nil, nil, "", nil, false
	a.folderQ = ""
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
	enable(a.hVerify, false)
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
			setBar(a.hProgress, rp.done*1000/rp.total)
		}
		return
	}
	if a.busy == busyVerify {
		a.progMu.Lock()
		rp := a.recProg
		a.progMu.Unlock()
		setText(a.hStatus, fmt.Sprintf("Verificando a fondo: %s de %s archivos · %s leídos",
			groupDigits(int64(rp.done)), groupDigits(int64(rp.total)), humanSize(rp.bytes)))
		if rp.total > 0 {
			setBar(a.hProgress, rp.done*1000/rp.total)
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
	case stageVerify:
		s = fmt.Sprintf("Comprobando qué archivos conservan su contenido: %.0f %%%s", p.Frac*100, found)
	case stageCarve:
		s = fmt.Sprintf("Buscando archivos sin nombre (fotos, videos, documentos…): %.0f %%%s", p.Frac*100, found)
	case stageDone:
		head := "Análisis terminado"
		if p.Interrupted {
			head = "Análisis detenido"
		}
		s = head + found
		if a.tree != nil {
			if h := summarize(a.tree.Entries).Headline(); h != "" {
				s = head + found + " · " + h
			}
		}
	}
	if p.Zeroed > 0 {
		s += fmt.Sprintf(" · %s en blanco (el disco ya los vació)", groupDigits(int64(p.Zeroed)))
	}
	if p.Bad > 0 && p.Stage != stageDone {
		s += fmt.Sprintf(" · %s sobrescritos", groupDigits(int64(p.Bad)))
	}
	if a.busy == busyScan && p.Stage != stagePrepare && p.Stage != stageDone && n > 0 {
		s += " · ya puedes explorar"
	}
	setText(a.hStatus, s)
	setBar(a.hProgress, int(p.Frac*1000))
}

func scanDone() {
	a := app
	a.busy = busyNone
	atomic.StoreInt32(&stopFlag, 0)
	enable(a.hBack, true)
	enable(a.hRecov, true)
	enable(a.hVerify, true)
	updateStatus()
	layout()
	if n, _ := totalFound(); n == 0 && a.tree != nil && a.tree.Roots[rootLost].Count == 0 {
		setText(a.hOverlay, "No se encontraron archivos en esta unidad.")
		show(a.hOverlay, true)
	}
	// Avisos del análisis (por ejemplo, una partición cifrada con BitLocker).
	if notes := a.ss.Scan.Notes; len(notes) > 0 && !a.autotest {
		msgBox(a.hwnd, strings.Join(notes, "\n\n"), "Aviso", mbIconWarning)
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
	// Al rehacer el árbol se vuelve a la misma carpeta y, si se estaba
	// buscando, se repite la búsqueda (no se borra lo que escribió el usuario).
	wasSearch := a.search
	a.rebuilding = true
	if target != nil {
		selectNode(target)
	}
	a.rebuilding = false
	if wasSearch {
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
		if app.rebuilding {
			return
		}
		if n != app.cur || app.search {
			app.selfEdit = true
			setText(app.hSearch, "")
			app.selfEdit = false
			app.folderQ = ""
			openNode(n)
		}
	}
}

// ---------------------------------------------------------------- lista

func openNode(n *Node) {
	a := app
	setFolderColumns(false)
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
		a.folderQ = ""
		if a.search && a.cur != nil {
			openNode(a.cur)
		}
		return
	}
	a.search = true
	a.rows = a.rows[:0]
	if a.folderMode {
		applyFolderSearch(text, m)
		return
	}
	setFolderColumns(false)
	res := a.tree.Search(m)
	for _, e := range res {
		a.rows = append(a.rows, lrow{e: e})
	}
	sortRows()
	setText(a.hCrumb, fmt.Sprintf("Resultados de «%s»: %s archivos", text, groupDigits(int64(len(res)))))
	a.list.setCount(len(a.rows))
	clearPreview()
}

// setFolderMode elige qué busca el buscador: archivos o carpetas.
func setFolderMode(on bool) {
	a := app
	a.folderMode = on
	setChecked(a.hSegFiles, !on)
	setChecked(a.hSegFolders, on)
	cue := cueFiles
	if on {
		cue = cueFolders
	}
	sendMsg(a.hSearch, emSetCueBanner, 1, uintptr(unsafe.Pointer(wstr(cue))))
	invalidate(a.hSearch)
	a.folderQ = ""
	if strings.TrimSpace(getText(a.hSearch)) != "" {
		applySearch()
	}
	pSetFocus.Call(uintptr(a.hSearch))
}

// setFolderColumns ordena las columnas para los resultados de carpetas:
// Nombre, Ubicación, Estado, Modificado, Tamaño (o vuelve al orden normal).
func setFolderColumns(on bool) {
	a := app
	if a.folderCols == on {
		return
	}
	a.folderCols = on
	order := []int32{0, 1, 2, 3, 4, 5}
	if on {
		order = []int32{0, 5, 3, 2, 1, 4}
	}
	sendMsg(a.list.hwnd, 0x103A, uintptr(len(order)), uintptr(unsafe.Pointer(&order[0]))) // LVM_SETCOLUMNORDERARRAY
	layout()
}

// applyFolderSearch muestra las carpetas que coinciden: primero por su
// nombre y después las que contienen archivos con ese nombre (para las
// carpetas cuyo nombre se perdió).
func applyFolderSearch(text string, m *Matcher) {
	a := app
	setFolderColumns(true)
	hits := a.tree.FindFolders(m)
	named, inside := 0, 0
	for i := range hits {
		a.rows = append(a.rows, lrow{n: hits[i].Node, h: &hits[i]})
		if hits[i].Score > fsNone {
			named++
		} else {
			inside++
		}
	}
	var crumb string
	switch {
	case named > 0 && inside > 0:
		crumb = fmt.Sprintf("%s con «%s» en su nombre · después, %s con archivos así dentro",
			plural(named, "carpeta", "carpetas"), text, plural(inside, "carpeta", "carpetas"))
	case named > 0:
		crumb = fmt.Sprintf("%s con «%s» en su nombre · doble clic para abrir", plural(named, "carpeta", "carpetas"), text)
	case inside > 0:
		crumb = fmt.Sprintf("Ninguna carpeta se llama «%s»; %s archivos así dentro (pudo perder su nombre)",
			text, plural(inside, "carpeta tiene", "carpetas tienen"))
	default:
		crumb = fmt.Sprintf("Ninguna carpeta ni archivo se llama «%s». Prueba con parte del nombre o con *", text)
	}
	setText(a.hCrumb, crumb)
	a.list.setCount(len(a.rows))
	if text != a.folderQ {
		// Búsqueda nueva: se elige la más probable y se muestran sus datos.
		// (Si solo se actualizan los resultados durante el análisis, se
		// respeta la fila que haya elegido el usuario.)
		a.folderQ = text
		clearPreview()
		if len(a.rows) > 0 {
			a.list.selectOnly(0)
		}
	}
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
	if n := r.n; n != nil {
		switch col {
		case 0:
			return n.Name
		case 1:
			return humanSize(n.Size)
		case 2:
			if !n.Newest.IsZero() {
				return n.Newest.Local().Format("02/01/2006 15:04")
			}
		case 3:
			switch {
			case r.h != nil && r.h.Score == fsNone:
				all := len(n.Files)
				if r.h.Lost != nil {
					all += len(r.h.Lost.Files)
				}
				return fmt.Sprintf("%s de %s coinciden", groupDigits(int64(r.h.Matches)), groupDigits(int64(all)))
			case n.Root == rootLost:
				return groupDigits(int64(n.Count)) + " no recuperables"
			}
			return plural(n.Count, "archivo", "archivos")
		case 4:
			return "Carpeta"
		case 5:
			if r.h != nil {
				return n.Location()
			}
			if n.Parent != nil {
				return strings.ReplaceAll(n.Parent.Path, "/", `\`)
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

// activateRow (doble clic): abre la carpeta; en los resultados de una
// búsqueda, un archivo abre la carpeta donde está (así se encuentra una
// carpeta que perdió su nombre a partir de un archivo que tenía dentro).
func activateRow(row int) {
	if row >= len(app.rows) {
		return
	}
	r := app.rows[row]
	if r.n != nil {
		selectNode(r.n)
		return
	}
	if !app.search || r.e == nil {
		return
	}
	for _, n := range r.e.nodes {
		if n.Root == rootTypes {
			continue
		}
		selectNode(n)
		for i, rr := range app.rows {
			if rr.e == r.e {
				app.list.selectOnly(i)
				break
			}
		}
		return
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
			switch col {
			case 1:
				c = cmp64(a.n.Size, b.n.Size)
			case 2:
				c = cmp64(a.n.Newest.Unix(), b.n.Newest.Unix())
			case 3:
				c = a.n.Count - b.n.Count
			case 5:
				c = strings.Compare(fold(a.n.Path), fold(b.n.Path))
			}
			if c == 0 {
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

// showSummary muestra el resumen con porcentajes de lo encontrado.
func showSummary() {
	if app.tree == nil {
		return
	}
	sum := summarize(app.tree.Entries)
	if sum.totalN() == 0 {
		msgBox(app.hwnd, "Todavía no se ha encontrado nada que resumir.", "Resumen", mbIconInfo)
		return
	}
	scope := "Archivos personales"
	if !isChecked(app.hHideSys) {
		scope = "Todos los archivos (incluidos Windows y programas)"
	}
	msgBox(app.hwnd, app.tree.FoldersText("CARPETAS MARCADAS", 4)+scope+":\r\n\r\n"+sum.Text(), "Resumen — "+app.srcName, mbIconInfo)
}

// writeSummaryFile guarda resumen.txt junto a lo recuperado.
func writeSummaryFile(dir string) {
	if app.tree == nil {
		return
	}
	body := "RESUMEN DE LA RECUPERACIÓN\r\nOrigen: " + app.srcName + "\r\n\r\n" +
		app.tree.FoldersText("CARPETAS RECUPERADAS ENTERAS", 50) + "TODO LO ENCONTRADO EN EL DISCO\r\n\r\n" +
		summarize(app.tree.Entries).Text()
	os.WriteFile(filepath.Join(dir, "resumen.txt"), []byte("\xEF\xBB\xBF"+body), 0o644)
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
	enable(a.hVerify, false)
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
	enable(a.hVerify, true)
	a.progMu.Lock()
	res := a.recRes
	a.progMu.Unlock()
	rebuildTree() // los estados incluyen ahora lo comprobado al guardar
	writeSummaryFile(a.recDest)
	layout()
	updateStatus()
	msg := fmt.Sprintf("Se recuperaron %s archivos (%s) en:\n%s", groupDigits(res.Good), humanSize(res.Bytes), a.recDest)
	if res.Verified+res.StructOK > 0 {
		msg += fmt.Sprintf("\n\nComprobados al guardar: %s con su contenido verificado (sumas de control correctas)"+
			" y %s con la estructura correcta.", groupDigits(res.Verified), groupDigits(res.StructOK))
		if rest := res.Good - res.Verified - res.StructOK; rest > 0 {
			msg += fmt.Sprintf(" Otros %s son de tipos que no se pueden comprobar (txt sin estructura, dll, bin...).", groupDigits(rest))
		}
	}
	if res.BadFound > 0 {
		msg += fmt.Sprintf("\n\nAl comprobarlos, %s resultaron dañados (su contenido fue sobrescrito) y se movieron a «%s».",
			groupDigits(res.BadFound), dirDoubt)
	}
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
	if res.Doubt > 0 {
		msg += "\n\nPara intentar arreglar los dañados (o buscar copias sanas de ellos), usa " +
			"«Reparar archivos dañados…» en la pantalla de inicio."
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

// ---------------------------------------------------------------- verificar a fondo

// verifySelection: lo marcado o, si no hay nada marcado, todo lo visible.
func verifySelection() []*Entry {
	a := app
	sel := a.tree.CheckedEntries()
	if len(sel) > 0 {
		return sel
	}
	for _, r := range []int{rootPath, rootLoose, rootCarved} {
		var walk func(n *Node)
		walk = func(n *Node) {
			for _, e := range n.Files {
				if e.Checkable() {
					sel = append(sel, e)
				}
			}
			for _, k := range n.Kids {
				walk(k)
			}
		}
		walk(a.tree.Roots[r])
	}
	return sel
}

// doVerify lee entero cada archivo y comprueba su contenido.
func doVerify() {
	a := app
	if a.ss == nil || a.tree == nil || a.busy != busyNone {
		return
	}
	sel := verifySelection()
	if len(sel) == 0 {
		msgBox(a.hwnd, "No hay archivos que verificar.", "Recovery", mbIconInfo)
		return
	}
	var total int64
	for _, e := range sel {
		total += e.Size
	}
	what := "los archivos marcados"
	if a.tree.Checked == 0 {
		what = "todos los archivos encontrados"
	}
	if !a.autotest && msgBox(a.hwnd, fmt.Sprintf("Se leerán enteros %s (%s archivos, %s) para comprobar si su contenido "+
		"es correcto: fotos, documentos de Office, PDF, comprimidos, audio y video.\n\nNo se escribe nada en el disco. "+
		"Puede tardar (es como copiarlos). Puedes detenerlo cuando quieras.\n\n¿Empezar?",
		what, groupDigits(int64(len(sel))), humanSize(total)), "Verificar a fondo", mbYesNo|mbIconInfo) != idYes {
		return
	}
	a.busy = busyVerify
	atomic.StoreInt32(&stopFlag, 0)
	enable(a.hStop, true)
	enable(a.hBack, false)
	enable(a.hRecov, false)
	enable(a.hVerify, false)
	layout()
	updateStatus()
	ss := a.ss
	go func() {
		last := time.Time{}
		res := ss.DeepVerify(sel, func(done, total int, bytes int64) {
			a.progMu.Lock()
			a.recProg.done, a.recProg.total, a.recProg.bytes = done, total, bytes
			a.progMu.Unlock()
			if time.Since(last) > 200*time.Millisecond || done == total {
				last = time.Now()
				if atomic.CompareAndSwapInt32(&a.progPnd, 0, 1) {
					postMsg(a.hwnd, wmAppVerProg, 0, 0)
				}
			}
		})
		a.progMu.Lock()
		a.verRes = res
		a.progMu.Unlock()
		postMsg(a.hwnd, wmAppVerDone, 0, 0)
	}()
}

func verifyDone() {
	a := app
	a.busy = busyNone
	atomic.StoreInt32(&stopFlag, 0)
	enable(a.hBack, true)
	enable(a.hRecov, true)
	enable(a.hVerify, true)
	a.progMu.Lock()
	res := a.verRes
	a.progMu.Unlock()
	rebuildTree()
	layout()
	updateStatus()
	msg := fmt.Sprintf("Archivos comprobados: %s\n\n"+
		"  Verificados (contenido correcto, con sumas de control):  %s\n"+
		"  Estructura correcta (su tipo no lleva sumas de control):  %s\n"+
		"  DAÑADOS (contenido sobrescrito):  %s\n"+
		"  Sin forma de comprobar (txt sin estructura, dll, bin...):  %s",
		groupDigits(int64(res.Checked)), groupDigits(int64(res.OK)), groupDigits(int64(res.Struct)),
		groupDigits(int64(res.Bad)), groupDigits(int64(res.Unknown)))
	if res.Bad > 0 {
		msg += "\n\nLos dañados aparecen ahora como «Dañado» en la columna Estado."
	}
	if res.Stopped {
		msg += "\n\nLa verificación se detuvo antes de terminar."
	}
	if a.autotest {
		autotestAfterVerify()
		return
	}
	msgBox(a.hwnd, msg, "Verificación terminada", mbIconInfo)
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
	if d := os.Getenv("RECOVERY_GUI_IMAGE"); d != "" {
		startImage(d)
		return
	}
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
	if q := os.Getenv("RECOVERY_GUI_FOLDER"); q != "" {
		app.selfEdit = true
		setText(app.hSearch, q)
		app.selfEdit = false
		setFolderMode(true)
	}
	if r := os.Getenv("RECOVERY_GUI_CHECKROW"); r != "" {
		i, _ := strconv.Atoi(r)
		toggleRows([]int{i})
	}
	if r := os.Getenv("RECOVERY_GUI_ACTIVATE"); r != "" { // doble clic en esa fila
		i, _ := strconv.Atoi(r)
		activateRow(i)
	}
	if r := os.Getenv("RECOVERY_GUI_ROWS_OUT"); r != "" {
		startPreview()
		dumpRows(r)
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
	if os.Getenv("RECOVERY_GUI_VERIFY") != "" {
		doVerify() // sigue en autotestAfterVerify
		return
	}
	autotestFinish()
}

// dumpRows guarda lo que muestra la lista (para las pruebas automáticas).
func dumpRows(path string) {
	var b strings.Builder
	b.WriteString("crumb\t" + getText(app.hCrumb) + "\n")
	for i := range app.rows {
		var cols []string
		for c := 0; c < 6; c++ {
			cols = append(cols, rowText(i, c))
		}
		fmt.Fprintf(&b, "row\t%d\t%s\n", rowCheck(i), strings.Join(cols, "\t"))
	}
	b.WriteString("panel\t" + strings.ReplaceAll(getText(app.hPrevI), "\r\n", " | ") + "\n")
	os.WriteFile(path, []byte(b.String()), 0o644)
}

func autotestAfterVerify() {
	if f := os.Getenv("RECOVERY_GUI_VERIFY_OUT"); f != "" {
		r := app.verRes
		os.WriteFile(f, []byte(fmt.Sprintf("comprobados=%d verificados=%d estructura=%d danados=%d sin_comprobar=%d\n",
			r.Checked, r.OK, r.Struct, r.Bad, r.Unknown)), 0o644)
	}
	autotestFinish()
}

func autotestFinish() {
	if os.Getenv("RECOVERY_GUI_SUMMARY") != "" {
		showSummary()
	}
	if d := os.Getenv("RECOVERY_GUI_DST"); d != "" {
		doRecover(filepath.Clean(d))
		return
	}
	if os.Getenv("RECOVERY_GUI_KEEP") == "" {
		pPostQuitMessage.Call(0)
	}
}
