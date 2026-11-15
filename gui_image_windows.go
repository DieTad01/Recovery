//go:build windows

package main

// Pantalla «Imagen del disco»: copia la unidad a un archivo (tolerando
// sectores dañados y pudiendo continuar después) y luego la analiza.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	comdlg32          = syscall.NewLazyDLL("comdlg32.dll")
	pGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")
)

type openfilename struct {
	lStructSize       uint32
	hwndOwner         syscall.Handle
	hInstance         syscall.Handle
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	Flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	FlagsEx           uint32
}

// Estado de la imagen en curso.
var img struct {
	ctls                         []syscall.Handle
	hTitle, hSrc, hProg, hStats  syscall.Handle
	hNote, hBack, hStop, hAnalyz syscall.Handle
	job                          *ImageJob
	srcName                      string
	ssd                          bool
	prog                         ImageProgress
	err                          error
	done                         bool
}

func buildImagePage() {
	img.hTitle = ctl("STATIC", "Imagen del disco", 0, 0, 0, fTitle)
	img.hSrc = ctl("STATIC", "", 0, 0, 0, fSub)
	app.muted[img.hSrc] = true
	img.hProg = newBar(0)
	img.hStats = ctl("STATIC", "", 0, 0, 0, fSub)
	img.hNote = ctl("STATIC", "", 0x2000, 0, 0, fBody)
	app.muted[img.hNote] = true
	img.hBack = button("‹  Inicio", wsTabstop, idImgBack, btnFrame, fBold)
	img.hStop = button("Detener", wsTabstop, idImgStop, btnFrame, fBold)
	img.hAnalyz = button("Analizar la imagen", wsTabstop, idImgAnalyze, btnFill, fButton)
	img.ctls = []syscall.Handle{img.hTitle, img.hSrc, img.hProg, img.hStats, img.hNote, img.hBack, img.hStop, img.hAnalyz}
}

func layoutImagePage(W, H int) {
	m := sc(32)
	moveWin(img.hTitle, m, sc(22), W-2*m, sc(40))
	moveWin(img.hSrc, m, sc(66), W-2*m, sc(44))
	// (raya en sc(120))
	moveWin(img.hProg, m, sc(142), W-2*m, sc(22))
	moveWin(img.hStats, m, sc(178), W-2*m, sc(110))
	moveWin(img.hNote, m, sc(298), W-2*m, H-sc(298)-sc(100))
	moveWin(img.hBack, m, H-sc(72), sc(116), sc(40))
	moveWin(img.hStop, m+sc(128), H-sc(72), sc(130), sc(40))
	moveWin(img.hAnalyz, W-m-sc(240), H-sc(80), sc(240), sc(52))
	invalidate(app.hwnd)
}

// diskOfLetter busca el disco físico que contiene una unidad con letra.
func diskOfLetter(letter string) (Source, bool) {
	for _, s := range app.sources {
		if s.Disk && strings.Contains(", "+s.Letters+",", " "+letter+":,") {
			return s, true
		}
	}
	return Source{}, false
}

// isSSD indica si la unidad (o el disco que la contiene) es un SSD.
func isSSD(s Source) bool {
	if s.Disk {
		return s.SSD || strings.EqualFold(s.Bus, "NVMe")
	}
	if d, ok := diskOfLetter(s.Letter); ok {
		return isSSD(d)
	}
	return false
}

const ssdAdvice = "Es un SSD (NVMe/SATA). Al borrar o restaurar, los SSD vacían por su cuenta el contenido " +
	"de lo borrado (TRIM), y mientras están encendidos pueden seguir haciéndolo. Lo que el disco ya vació " +
	"no lo puede recuperar ningún programa; lo que queda, conviene copiarlo cuanto antes."

// startImage prepara la imagen de la unidad seleccionada. dest vacío =
// preguntar la carpeta (las pruebas automáticas la pasan directamente).
func startImage(dest string) {
	a := app
	row := a.drives.focused()
	if row < 0 || row >= len(a.sources) {
		msgBox(a.hwnd, "Elige primero la unidad de la que quieres hacer la imagen.", "Recovery", mbIconInfo)
		return
	}
	src := a.sources[row]
	if src.Letter != "" && !a.autotest {
		if d, ok := diskOfLetter(src.Letter); ok {
			q := fmt.Sprintf("Has elegido la unidad %s:. ¿Hacer la imagen del DISCO COMPLETO que la contiene (%s)?\n\n"+
				"Recomendado: así no se queda nada fuera (otras particiones, espacio sin asignar).\n\n"+
				"Sí = disco completo     No = solo la unidad %s:", src.Letter, sourceName(d), src.Letter)
			if msgBox(a.hwnd, q, "Imagen del disco", mbYesNo|mbIconQuest) == idYes {
				src = d
			}
		}
	}
	if dest == "" {
		dest = browseFolder(fmt.Sprintf("¿Dónde guardo la imagen de «%s» (%s)? Elige una carpeta en OTRO disco.",
			sourceName(src), humanSize(src.Size)))
		if dest == "" {
			return
		}
	}
	if fatal, warn := checkDestination(src.Path, dest); fatal != "" {
		msgBox(a.hwnd, fatal, "No se puede guardar ahí", mbIconError)
		return
	} else if warn != "" && !a.autotest {
		if msgBox(a.hwnd, warn+"\n\n¿Guardar ahí de todos modos?", "Atención", mbYesNo|mbIconWarning|mbDefButton2) != idYes {
			return
		}
	}
	if destIsFAT32(dest) {
		msgBox(a.hwnd, "Ese disco está en FAT32, que no admite archivos de más de 4 GB.\n"+
			"Usa un disco en NTFS o exFAT.", "Recovery", mbIconError)
		return
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		msgBox(a.hwnd, "No se puede crear la carpeta:\n"+err.Error(), "Recovery", mbIconError)
		return
	}
	dev, err := OpenDevice(src.Path)
	if err != nil {
		msgBox(a.hwnd, "No se puede leer esa unidad:\n\n"+err.Error(), "Recovery", mbIconError)
		return
	}
	job, err := NewImageJob(dev, dest, imageBaseName(sourceName(src)))
	if err != nil {
		dev.Close()
		msgBox(a.hwnd, err.Error(), "Recovery", mbIconError)
		return
	}
	if job.Resumed && job.Complete() {
		dev.Close()
		if a.autotest || msgBox(a.hwnd, "En esa carpeta ya hay una imagen COMPLETA de esta unidad.\n\n¿Analizarla ahora?",
			"Imagen del disco", mbYesNo|mbIconQuest) == idYes {
			analyzePath(job.ImgPath, "Imagen de "+sourceName(src))
		}
		return
	}
	if !job.Resumed && !a.autotest {
		free := freeSpace(dest)
		if fs := destFS(dest); fs != "" && !strings.EqualFold(fs, "NTFS") {
			// Sin NTFS no hay archivos «dispersos»: la imagen ocupa el disco entero.
			if free >= 0 && free < src.Size {
				dev.Close()
				msgBox(a.hwnd, fmt.Sprintf("El disco destino está en %s. En %s la imagen ocupa SIEMPRE el tamaño completo "+
					"del disco de origen (%s), aunque esté casi vacío, y ahí solo hay %s libres.\n\n"+
					"Solución: usa un disco en NTFS (en NTFS las zonas vacías no ocupan espacio).",
					fs, fs, humanSize(src.Size), humanSize(free)), "No cabe", mbIconError)
				return
			}
			if msgBox(a.hwnd, fmt.Sprintf("El disco destino está en %s: la imagen ocupará el tamaño completo del disco "+
				"de origen (%s) y tardará más, porque las zonas vacías también se escriben.\n\n"+
				"Si puedes, usa un disco en NTFS. ¿Continuar de todos modos?", fs, humanSize(src.Size)),
				"Imagen del disco", mbYesNo|mbIconWarning|mbDefButton2) != idYes {
				dev.Close()
				return
			}
		} else if free >= 0 && free < src.Size {
			q := fmt.Sprintf("La imagen puede llegar a ocupar %s y en el destino hay %s libres.\n\n"+
				"En NTFS las zonas vacías del disco no ocupan espacio, así que puede caber, pero si el disco "+
				"de origen está muy lleno se quedará a medias. ¿Continuar?", humanSize(src.Size), humanSize(free))
			if msgBox(a.hwnd, q, "Espacio", mbYesNo|mbIconWarning) != idYes {
				dev.Close()
				return
			}
		}
	}
	img.job, img.srcName, img.ssd, img.err, img.done = job, sourceName(src), isSSD(src), nil, false
	img.prog = ImageProgress{Size: job.Size}
	done, bad, pend := job.Totals()
	img.prog.Done, img.prog.Bad, img.prog.Pending = done, bad, pend
	setText(img.hSrc, fmt.Sprintf("Origen: %s  (%s)\r\nImagen: %s", img.srcName, humanSize(job.Size), job.ImgPath))
	note := "No desconectes ni uses el disco mientras se copia. Si hay que parar, pulsa «Detener»: " +
		"la próxima vez que hagas la imagen de esta unidad en la misma carpeta, continuará donde se quedó.\r\n\r\n" +
		"El archivo .map guarda qué zonas se copiaron y cuáles no se pudieron leer (es compatible con GNU ddrescue)."
	if job.Resumed {
		note = fmt.Sprintf("Continuando una imagen anterior: ya había %s copiados.\r\n\r\n", humanSize(done)) + note
	}
	if img.ssd {
		note = ssdAdvice + "\r\n\r\n" + note
	}
	setText(img.hNote, note)
	atomic.StoreInt32(&stopFlag, 0)
	a.busy = busyImage
	enable(img.hBack, false)
	enable(img.hStop, true)
	enable(img.hAnalyz, false)
	showPage(pageImage)
	updateImageStats()
	go func() {
		err := job.Run(func(p ImageProgress) {
			a.progMu.Lock()
			img.prog = p
			a.progMu.Unlock()
			if atomic.CompareAndSwapInt32(&a.progPnd, 0, 1) {
				postMsg(a.hwnd, wmAppImgProg, 0, 0)
			}
		})
		a.progMu.Lock()
		img.err = err
		a.progMu.Unlock()
		postMsg(a.hwnd, wmAppImgDone, 0, 0)
	}()
}

var phaseNames = map[int]string{
	1: "copia rápida", 2: "reintentando las zonas que se saltaron",
	3: "leyendo sector a sector las zonas dañadas", 4: "terminada",
}

func updateImageStats() {
	app.progMu.Lock()
	p := img.prog
	app.progMu.Unlock()
	if p.Size <= 0 {
		return
	}
	pct := float64(p.Done) * 100 / float64(p.Size)
	lines := []string{fmt.Sprintf("Copiado: %s de %s  (%.1f %%)", humanSize(p.Done), humanSize(p.Size), pct)}
	if p.Rate > 0 && p.Phase < 4 {
		lines = append(lines, fmt.Sprintf("Velocidad: %s/s  ·  tiempo restante: ~%s", humanSize(int64(p.Rate)), humanDur(p.ETA)))
	}
	bad := "Zonas ilegibles: ninguna"
	if p.Bad > 0 {
		bad = fmt.Sprintf("Zonas ilegibles: %s (quedan a cero en la imagen)", humanSize(p.Bad))
	}
	lines = append(lines, bad)
	if ph := phaseNames[p.Phase]; ph != "" {
		lines = append(lines, "Fase: "+ph)
	}
	setText(img.hStats, strings.Join(lines, "\r\n"))
	setBar(img.hProg, int((p.Done+p.Bad)*1000/p.Size))
}

func imageDone() {
	a := app
	a.busy = busyNone
	atomic.StoreInt32(&stopFlag, 0)
	a.progMu.Lock()
	err := img.err
	a.progMu.Unlock()
	updateImageStats()
	enable(img.hBack, true)
	enable(img.hStop, false)
	job := img.job
	job.Dev.Close()
	complete := err == nil && job.Complete()
	img.done = complete
	enable(img.hAnalyz, err == nil)
	switch {
	case err != nil:
		setText(img.hTitle, "Imagen del disco: error")
		msgBox(a.hwnd, "No se pudo completar la imagen:\n\n"+err.Error(), "Recovery", mbIconError)
	case complete:
		_, bad, _ := job.Totals()
		setText(img.hTitle, "Imagen terminada")
		msg := "Imagen terminada. Ya puedes desconectar el disco del cliente: a partir de ahora se trabaja sobre la imagen."
		if bad > 0 {
			msg += fmt.Sprintf("\n\nNo se pudieron leer %s (sectores dañados); en la imagen quedan a cero.", humanSize(bad))
		}
		if a.autotest {
			autotestAfterImage()
			return
		}
		if msgBox(a.hwnd, msg+"\n\n¿Analizar la imagen ahora?", "Imagen del disco", mbYesNo|mbIconInfo) == idYes {
			analyzePath(job.ImgPath, "Imagen de "+img.srcName)
		}
	default:
		setText(img.hTitle, "Imagen detenida")
		if a.autotest {
			autotestAfterImage()
			return
		}
		msgBox(a.hwnd, "Imagen detenida. Para continuarla, vuelve a elegir la misma unidad y la misma carpeta: "+
			"seguirá donde se quedó.\n\nTambién puedes analizar ya lo copiado.", "Imagen del disco", mbIconInfo)
	}
}

// openImageFile pide un archivo de imagen y lo analiza.
func openImageFile() {
	filter := utf16.Encode([]rune("Imágenes de disco (*.img, *.dd, *.raw, *.bin)\x00*.img;*.dd;*.raw;*.bin;*.001\x00" +
		"Todos los archivos\x00*.*\x00\x00"))
	buf := make([]uint16, 1024)
	ofn := openfilename{
		lStructSize: uint32(unsafe.Sizeof(openfilename{})), hwndOwner: app.hwnd,
		lpstrFilter: &filter[0], lpstrFile: &buf[0], nMaxFile: uint32(len(buf)),
		lpstrTitle: wstr("Abrir una imagen de disco"),
		Flags:      0x1000 | 0x800 | 0x80000 | 0x8, // FILEMUSTEXIST|PATHMUSTEXIST|EXPLORER|NOCHANGEDIR
	}
	if r, _, _ := pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		return
	}
	p := syscall.UTF16ToString(buf)
	analyzePath(p, "Imagen: "+filepath.Base(p))
}

// ---------------------------------------------------------------- pruebas automáticas

func autotestAfterImage() {
	if os.Getenv("RECOVERY_GUI_IMAGE_ANALYZE") != "" && img.job != nil {
		analyzePath(img.job.ImgPath, "Imagen de "+img.srcName)
		return
	}
	if os.Getenv("RECOVERY_GUI_KEEP") == "" {
		pPostQuitMessage.Call(0)
	}
}
