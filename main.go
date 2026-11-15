// Recovery: recuperador de archivos para unidades NTFS formateadas,
// restauradas o con archivos borrados. Solo LEE la unidad de origen.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const version = "2.2"

// Config son las opciones elegidas por el usuario.
type Config struct {
	Source      string
	Out         string
	Todo        bool
	Quick       bool
	NoCarve     bool
	OnlyCarve   bool
	ListOnly    bool
	IgnoreMap   bool
	Yes         bool
	NewScan     bool
	Exts        map[string]bool
	Search      string
	ImageDir    string
	MinKB       int64
	Interactive bool
}

var stdin = bufio.NewReader(os.Stdin)

func ask(prompt string) string {
	fmt.Print(prompt)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

func askYesNo(prompt string, def bool) bool {
	suffix := " [S/n]: "
	if !def {
		suffix = " [s/N]: "
	}
	for {
		a := strings.ToLower(ask(prompt + suffix))
		switch a {
		case "":
			return def
		case "s", "si", "sí", "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
}

// statusLine muestra una línea de progreso que se va sobrescribiendo.
func statusLine(s string) { out.status(s) }

// progress comunica el avance (0..1) a la interfaz; en consola no hace nada.
func progress(frac float64) { out.progress(frac) }

var logLines []string

// say muestra una línea y la guarda para informe.txt.
func say(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	logLines = append(logLines, msg)
	out.line(msg)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Recovery %s - recuperador de archivos (NTFS + búsqueda por firmas)

Sin opciones se abre un asistente paso a paso.

Uso:  recovery -origen <unidad|disco|imagen> -destino <carpeta en OTRA unidad> [opciones]

  -origen X        Unidad a analizar: C:  |  disco0  |  \\.\PhysicalDrive0  |  imagen.img  |  /dev/sdb1
  -destino DIR     Carpeta donde guardar lo recuperado (NUNCA en la unidad de origen)
  -modo M          personal (por defecto: sin archivos de Windows ni programas) | todo
  -rapido          Solo archivos borrados recientemente (lee la MFT actual, tarda minutos)
  -sin-firmas      No buscar archivos por firma (solo los que conservan nombre y carpeta)
  -solo-firmas     Solo búsqueda por firma
  -solo-listar     Analiza y genera el informe, sin recuperar nada todavía
  -ext LISTA       Recuperar solo estas extensiones, p. ej.  -ext jpg,png,mp4,docx
  -min-kb N        Tamaño mínimo de los archivos recuperados por firma (por defecto 4)
  -ignorar-mapa    No saltar las zonas ocupadas por archivos actuales (más lento, más duplicados)
  -nuevo           No reutilizar un escaneo anterior guardado en el destino
  -si              No pedir confirmaciones
  -consola         Usar la consola de texto en vez de la ventana (Windows)
  -buscar TEXTO    Recuperar solo lo que coincida: nombre o carpeta (tesis), comodines (*.jpg), varios con comas
  -imagen CARPETA  Crea una imagen del origen en esa carpeta (tolera sectores dañados; si se
                   interrumpe, al repetir la orden continúa). Luego: -origen CARPETA\imagen.img
  -estado-disco    Muestra el estado de salud de los discos y termina
  -programas       Muestra los programas instalados que más ocupan y termina (Windows)
  -listar          Muestra las unidades disponibles y termina
`, version)
}

func main() {
	var cfg Config
	modo := "personal"
	exts := ""
	listar, pausa, consola := false, false, false
	estadoDisco, programas := false, false
	flag.StringVar(&cfg.Source, "origen", "", "")
	flag.StringVar(&cfg.Out, "destino", "", "")
	flag.StringVar(&modo, "modo", "personal", "")
	flag.BoolVar(&cfg.Quick, "rapido", false, "")
	flag.BoolVar(&cfg.NoCarve, "sin-firmas", false, "")
	flag.BoolVar(&cfg.OnlyCarve, "solo-firmas", false, "")
	flag.BoolVar(&cfg.ListOnly, "solo-listar", false, "")
	flag.StringVar(&exts, "ext", "", "")
	flag.Int64Var(&cfg.MinKB, "min-kb", 4, "")
	flag.BoolVar(&cfg.IgnoreMap, "ignorar-mapa", false, "")
	flag.BoolVar(&cfg.NewScan, "nuevo", false, "")
	flag.BoolVar(&cfg.Yes, "si", false, "")
	flag.BoolVar(&listar, "listar", false, "")
	flag.BoolVar(&pausa, "pausa", false, "")
	flag.BoolVar(&consola, "consola", false, "")
	flag.StringVar(&cfg.Search, "buscar", "", "")
	flag.StringVar(&cfg.ImageDir, "imagen", "", "")
	flag.BoolVar(&estadoDisco, "estado-disco", false, "")
	flag.BoolVar(&programas, "programas", false, "")
	flag.Usage = usage
	flag.Parse()

	cfg.Todo = strings.EqualFold(modo, "todo")
	if exts != "" {
		cfg.Exts = map[string]bool{}
		for _, e := range strings.FieldsFunc(strings.ToLower(exts), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
			cfg.Exts[strings.TrimPrefix(e, ".")] = true
		}
	}
	// En Windows, sin opciones de línea de comandos, se abre la ventana.
	if estadoDisco || programas {
		attachConsole()
		if estadoDisco {
			printDiskHealth()
		}
		if programas {
			printPrograms()
		}
		if pausa {
			ask("Pulsa Intro para salir...")
		}
		return
	}

	if cfg.ImageDir != "" {
		attachConsole()
		code := 0
		if err := runImage(&cfg); err != nil {
			fmt.Println("\nERROR:", err)
			code = 1
		}
		if pausa {
			ask("Pulsa Intro para salir...")
		}
		os.Exit(code)
	}

	guiMode := runtime.GOOS == "windows" && !consola && !listar && cfg.Source == ""
	if guiMode {
		// La ventana necesita permisos de administrador para leer el disco.
		if !isAdmin() {
			if err := relaunchElevated(); err == nil {
				return
			}
		}
		os.Exit(runGUIApp(&cfg))
	}

	// Modo consola. Bajo Windows el programa se compila como aplicación de
	// ventana, así que hay que engancharse a la consola que lo lanzó.
	attachConsole()
	cfg.Interactive = cfg.Source == "" && !listar

	// También hace falta ser administrador para leer una unidad en crudo.
	needRaw := cfg.Interactive || listar || isRawDevicePath(normalizeSource(cfg.Source))
	if runtime.GOOS == "windows" && needRaw && !isAdmin() {
		if err := relaunchElevated(); err == nil {
			return
		}
		fmt.Println("AVISO: el programa no tiene permisos de administrador; no podrá leer las unidades.")
		fmt.Println("Haz clic derecho sobre recovery.exe y elige \"Ejecutar como administrador\".")
		fmt.Println()
		pausa = true
	}

	code := 0
	if listar {
		printSources(listSources())
	} else {
		if cfg.Interactive {
			pausa = true
			if !wizard(&cfg) {
				code = 1
			}
		}
		if code == 0 {
			if err := run(&cfg); err != nil {
				statusLine("")
				fmt.Println()
				fmt.Println("ERROR:", err)
				code = 1
			}
		}
	}
	if pausa {
		fmt.Println()
		ask("Pulsa Intro para salir...")
	}
	os.Exit(code)
}

func printSources(srcs []Source) {
	if len(srcs) == 0 {
		fmt.Println("  (no se detectó ninguna unidad; ¿se está ejecutando como administrador?)")
	}
	for i, s := range srcs {
		fmt.Printf("  [%d] %s\n", i+1, s.Label)
	}
}

// wizard pregunta paso a paso qué recuperar y dónde guardarlo.
func wizard(cfg *Config) bool {
	fmt.Println("==============================================================")
	fmt.Printf("  RECOVERY %s  -  Recuperador de archivos perdidos\n", version)
	fmt.Println("==============================================================")
	fmt.Println()
	fmt.Println("Antes de empezar:")
	fmt.Println("  * Este programa solo LEE la unidad dañada; no la modifica.")
	fmt.Println("  * Lo recuperado debe guardarse en OTRA unidad (disco externo o USB).")
	fmt.Println("  * Cuanto menos se use el equipo afectado, más se podrá recuperar.")
	fmt.Println()
	srcs := listSources()
	fmt.Println("Unidades detectadas:")
	printSources(srcs)
	fmt.Printf("  [%d] Usar un archivo de imagen de disco (.img, .dd, .bin)\n", len(srcs)+1)
	fmt.Println()
	fmt.Println("Consejo: elige la unidad donde estaban los archivos (normalmente C:).")
	fmt.Println("El \"disco físico completo\" sirve si la unidad no aparece o se cambiaron las particiones.")
	for cfg.Source == "" {
		a := ask("\n¿De qué unidad quieres recuperar? Número: ")
		if a == "" {
			return false
		}
		n, err := strconv.Atoi(a)
		switch {
		case err == nil && n >= 1 && n <= len(srcs):
			cfg.Source = srcs[n-1].Path
		case err == nil && n == len(srcs)+1:
			cfg.Source = strings.Trim(ask("Ruta del archivo de imagen: "), `"`)
		default:
			fmt.Println("Escribe uno de los números de la lista.")
		}
	}
	fmt.Println()
	fmt.Println("¿Dónde guardo lo recuperado? Debe ser una carpeta en OTRA unidad con espacio libre.")
	for cfg.Out == "" {
		ej := `E:\Recuperado`
		if runtime.GOOS != "windows" {
			ej = "/media/usb/Recuperado"
		}
		a := strings.Trim(ask("Carpeta de destino (por ejemplo "+ej+"): "), `"`)
		if a == "" {
			return false
		}
		if fatal, _ := checkDestination(normalizeSource(cfg.Source), a); fatal != "" {
			fmt.Println("  " + fatal)
			continue
		}
		if err := os.MkdirAll(a, 0o755); err != nil {
			fmt.Println("  No se puede crear esa carpeta:", err)
			continue
		}
		cfg.Out = a
	}
	fmt.Println()
	fmt.Println("¿Qué quieres recuperar?")
	fmt.Println("  [1] Archivos personales: documentos, fotos, videos, música, escritorio, descargas... (recomendado)")
	fmt.Println("  [2] Absolutamente todo, incluidos los archivos de Windows y de los programas (ocupa mucho más)")
	cfg.Todo = ask("Opción [1]: ") == "2"
	fmt.Println()
	fmt.Println("Tipo de búsqueda:")
	fmt.Println("  [1] Completa: analiza toda la unidad. Es la adecuada tras restaurar o formatear. Puede tardar horas.")
	fmt.Println("  [2] Rápida: solo archivos borrados hace poco (unos minutos).")
	cfg.Quick = ask("Opción [1]: ") == "2"
	fmt.Println()
	return true
}

func kindSummary(cands []Cand) string {
	var n [kMax]int
	for _, c := range cands {
		if c.Kind < kMax {
			n[c.Kind]++
		}
	}
	var parts []string
	for k := uint8(1); k < kMax; k++ {
		if n[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s: %s", kindNames[k], groupDigits(int64(n[k]))))
		}
	}
	return strings.Join(parts, ", ")
}

func run(cfg *Config) error {
	src := normalizeSource(cfg.Source)
	if cfg.Out == "" {
		return fmt.Errorf("falta la carpeta de destino (-destino)")
	}
	out, err := filepath.Abs(cfg.Out)
	if err != nil {
		return err
	}
	if fatal, warn := checkDestination(src, out); fatal != "" {
		return fmt.Errorf("%s", fatal)
	} else if warn != "" {
		fmt.Println("AVISO: " + warn)
		if !cfg.Yes && !askYesNo("¿Continuar de todos modos?", false) {
			return fmt.Errorf("cancelado")
		}
	}
	dev, err := OpenDevice(src)
	if err != nil {
		return err
	}
	defer dev.Close()
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("no se puede crear la carpeta de destino: %v", err)
	}

	// Ctrl+C detiene de forma ordenada; una segunda vez sale de inmediato.
	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, os.Interrupt)
	go func() {
		for range sigc {
			if atomic.SwapInt32(&stopFlag, 1) != 0 {
				fmt.Println("\nInterrumpido.")
				os.Exit(130)
			}
		}
	}()

	keepAwake()
	say("Recovery %s  -  %s", version, time.Now().Format("2006-01-02 15:04"))
	say("Origen:  %s  (%s)", src, humanSize(dev.Size))
	say("Destino: %s", out)
	for _, n := range describeDrive(dev) {
		say("AVISO: %s", n)
	}
	if n := destFSNote(out); n != "" {
		say("AVISO: %s", n)
	}
	say("")

	s := &Scan{Dev: dev, Opts: ScanOpts{Quick: cfg.Quick, NoCarve: cfg.NoCarve, NoMFT: cfg.OnlyCarve, IgnoreMap: cfg.IgnoreMap}}

	// ---- Paso 1: sistema de archivos actual --------------------------
	say("Paso 1 de 4: leyendo el sistema de archivos actual...")
	t0 := time.Now()
	lastP := time.Time{}
	s.Prepare(func() {
		if time.Since(lastP) > 400*time.Millisecond {
			lastP = time.Now()
			statusLine(fmt.Sprintf("  leyendo la tabla de archivos (MFT)...  %s", humanDur(time.Since(t0))))
		}
	})
	statusLine("")
	if len(s.Vols) == 0 {
		say("  No hay un NTFS válido: se analizará toda la unidad como espacio sin formato.")
	}
	for _, v := range s.Vols {
		say("  NTFS en %s: %s archivos existentes (no se tocan), clusters de %d bytes.",
			s.Conts[v.Cont].Desc, groupDigits(v.Existing), v.CS)
	}
	for _, n := range s.Notes {
		say("  AVISO: %s", n)
	}
	s.Notes = nil
	nPrep := len(s.Recs)
	if stopRequested() {
		return fmt.Errorf("interrumpido")
	}

	// ---- Paso 2: escaneo completo -----------------------------------
	idxPath := filepath.Join(out, "escaneo.idx")
	if cfg.Quick {
		say("Paso 2 de 4: omitido (búsqueda rápida).")
		s.ScannedTo = dev.Size
	} else {
		if !cfg.NewScan {
			if idx, err := loadIndex(idxPath); err == nil && idx.DevSize == dev.Size && idx.Opts == s.Opts {
				pct := float64(idx.ScannedTo) * 100 / float64(dev.Size)
				q := fmt.Sprintf("Hay un escaneo anterior guardado (%.0f%% de la unidad). ¿Usarlo?", pct)
				if cfg.Yes || !cfg.Interactive || askYesNo(q, true) {
					s.Recs = append(s.Recs, idx.Recs...)
					s.OldMFT, s.Cands, s.Boots, s.ScannedTo = idx.OldMFT, idx.Cands, idx.Boots, idx.ScannedTo
					say("  Se reutiliza el escaneo guardado en %s (%.0f%%).", idxPath, pct)
				}
			}
		}
		if s.ScannedTo < dev.Size {
			say("Paso 2 de 4: analizando toda la unidad (Ctrl+C para pausar; se puede continuar después)...")
			start, startPos := time.Now(), s.ScannedTo
			lastP, lastSave := time.Time{}, time.Now()
			s.ScanDevice(func(pos int64) {
				if time.Since(lastP) < 500*time.Millisecond {
					return
				}
				lastP = time.Now()
				el := time.Since(start).Seconds()
				rate := float64(pos-startPos) / (el + 0.001)
				eta := time.Duration(float64(dev.Size-pos)/(rate+1)) * time.Second
				progress(float64(pos) / float64(dev.Size))
				statusLine(fmt.Sprintf("  %5.1f%%  %s de %s  ·  %s/s  ·  faltan ~%s  ·  registros NTFS: %s  ·  firmas: %s",
					float64(pos)*100/float64(dev.Size), humanSize(pos), humanSize(dev.Size), humanSize(int64(rate)),
					humanDur(eta), groupDigits(int64(len(s.Recs)-nPrep)), groupDigits(int64(len(s.Cands)))))
				if time.Since(lastSave) > 10*time.Minute {
					saveIndex(idxPath, s, s.Recs[nPrep:])
					lastSave = time.Now()
				}
			})
			statusLine("")
			if err := saveIndex(idxPath, s, s.Recs[nPrep:]); err != nil {
				say("  AVISO: no se pudo guardar el escaneo (%v).", err)
			}
			say("  Analizado en %s.", humanDur(time.Since(start)))
		} else {
			say("Paso 2 de 4: ya estaba completo.")
		}
		if s.Interrupted {
			pct := float64(s.ScannedTo) * 100 / float64(dev.Size)
			say("  Escaneo PAUSADO al %.0f%%. El avance queda guardado: al volver a ejecutar con el mismo destino continuará.", pct)
			if cfg.Yes || !cfg.Interactive || !askYesNo("¿Recuperar ahora lo encontrado hasta este punto?", false) {
				return nil
			}
			atomic.StoreInt32(&stopFlag, 0)
		}
	}
	if dev.BadBytes > 0 {
		say("  AVISO: %s de la unidad no se pudieron leer (sectores dañados).", humanSize(dev.BadBytes))
	}

	// ---- Paso 3: reconstrucción -------------------------------------
	say("Paso 3 de 4: reconstruyendo carpetas y comprobando el estado de los archivos...")
	s.attachExtensions()
	s.detectGeometries(say)
	flt := Filter{Todo: cfg.Todo, Exts: cfg.Exts, Search: ParseSearch(cfg.Search)}
	if flt.Search != nil {
		say("  Búsqueda: «%s»", flt.Search.Raw)
	}
	plan := &Plan{ByCat: map[string]*catStat{}}
	if !cfg.OnlyCarve {
		plan = s.BuildPlan(flt)
	}
	say("")
	say("RESULTADO DEL ANÁLISIS")
	say("  Archivos perdidos con nombre y carpeta (NTFS): %s  (%s)", groupDigits(int64(len(plan.Items))), humanSize(plan.TotalSize))
	if len(plan.Items) > 0 {
		say("     con su ruta completa: %s   ·   en carpetas sueltas (sin carpeta superior): %s",
			groupDigits(plan.Rooted), groupDigits(plan.Loose))
		say("     aparentemente intactos: %s   ·   parcialmente sobrescritos: %s",
			groupDigits(plan.Intact), groupDigits(plan.Partial))
		for _, c := range catOrder {
			if cs := plan.ByCat[c]; cs != nil {
				say("     %-18s %10s archivos  %10s", c+":", groupDigits(cs.N), humanSize(cs.Size))
			}
		}
		if n := plan.Intact + plan.Partial; n > 0 {
			say("     en buen estado: %d%% de los archivos (%s completos, %s dañados en parte)",
				plan.Intact*100/n, groupDigits(plan.Intact), groupDigits(plan.Partial))
		}
	}
	if plan.NOverw > 0 {
		say("  Sobrescritos por completo (irrecuperables): %s", groupDigits(plan.NOverw))
	}
	if plan.NNoData > 0 {
		say("  Sin datos en el disco (p. ej. archivos solo en la nube de OneDrive): %s", groupDigits(plan.NNoData))
	}
	if plan.NSystem > 0 {
		say("  Archivos de Windows y de programas omitidos (usa el modo \"todo\" para incluirlos): %s", groupDigits(plan.NSystem))
	}
	if plan.NFiltered > 0 {
		say("  Omitidos por el filtro de extensiones: %s", groupDigits(plan.NFiltered))
	}
	if len(s.Cands) > 0 {
		say("  Posibles archivos adicionales localizables por firma (sin nombre): hasta %s", groupDigits(int64(len(s.Cands))))
		say("     %s", kindSummary(s.Cands))
	}
	for g, ge := range s.geoms {
		if ge.Samples >= 5 && ge.Zero*10 >= ge.Samples*8 {
			say("  AVISO: en %s el contenido de casi todos los archivos antiguos está en blanco (ceros).", s.Conts[g].Desc)
			say("         Es lo que ocurre en los SSD con TRIM: quedan los nombres pero el contenido fue vaciado.")
		} else if ge.Samples >= 5 && !ge.Confirmed {
			say("  AVISO: en %s no se pudo confirmar dónde empezaba el volumen antiguo; los archivos con nombre", s.Conts[g].Desc)
			say("         pueden salir dañados. La recuperación por firma no depende de esto.")
		}
	}
	say("")

	rep, err := NewReport(out)
	if err != nil {
		return fmt.Errorf("no se puede escribir en el destino: %v", err)
	}
	defer writeLog(out)
	if cfg.ListOnly {
		for _, sk := range plan.Skipped {
			rep.Row("NO RECUPERABLE", "NTFS", sk.Rel, sk.Size, sk.MTime, sk.Reason)
		}
		for _, it := range plan.Items {
			st, det := "ENCONTRADO", ""
			if it.Overw > 0 {
				det = fmt.Sprintf("%d%% sobrescrito", it.Overw*100/max64(it.Total, 1))
			}
			rep.Row(st, originOf(it.Rec), it.Rel, it.Rec.Size, it.Rec.MTime, det)
		}
		rep.Close()
		say("Listado guardado en %s", filepath.Join(out, "informe.csv"))
		say("Para recuperar, vuelve a ejecutar sin -solo-listar (se reutilizará el escaneo).")
		return nil
	}
	defer rep.Close()

	// ---- Paso 4: recuperación ---------------------------------------
	free := freeSpace(out)
	if free >= 0 {
		say("Espacio libre en el destino: %s.  Necesario para los archivos con nombre: %s (más lo que aparezca por firma).",
			humanSize(free), humanSize(plan.TotalSize))
		if plan.TotalSize > free {
			say("AVISO: no cabe todo. Se recuperará hasta llenar el destino; después podrás continuar en otro disco.")
		}
	}
	if len(plan.Items) == 0 && len(s.Cands) == 0 {
		say("No se encontró ningún archivo que recuperar en esta unidad.")
		return nil
	}
	if !cfg.Yes && !askYesNo("¿Empezar la recuperación?", true) {
		say("Cancelado. El escaneo queda guardado para la próxima vez.")
		return nil
	}
	say("Paso 4 de 4: recuperando...")
	rc := NewRecoverer(s, out, rep, flt)
	rc.MinCarve = cfg.MinKB * 1024
	rc.Validate = true
	t1 := time.Now()
	rc.RecoverMFT(plan)
	if flt.Search != nil && !flt.Search.CanMatchCarved() {
		say("  (La búsqueda por nombre no incluye los archivos sin nombre localizados por su contenido.)")
	} else if !cfg.NoCarve && !rc.Full && !stopRequested() {
		rc.RecoverCarved()
	}
	for _, d := range []string{dirFiles, dirLoose, dirDoubt, dirCarved} {
		removeEmptyDirs(filepath.Join(out, d))
		os.Remove(filepath.Join(out, d))
	}
	say("")
	say("RECUPERACIÓN TERMINADA en %s", humanDur(time.Since(t1)))
	say("  Recuperados con nombre y carpeta:        %s", groupDigits(rc.NGood))
	say("  Recuperados por firma (sin nombre):      %s", groupDigits(rc.NCarved))
	say("  Total recuperado en buen estado:         %s", humanSize(rc.Bytes))
	say("  Dudosos o dañados (carpeta \"%s\"):   %s", dirDoubt, groupDigits(rc.NDoubt+rc.NCarvedDoubt))
	if rc.NVerified+rc.NStructOK+rc.NBadFound > 0 {
		say("  Comprobación del contenido al guardar:")
		say("     verificados (sumas de control OK):    %s", groupDigits(rc.NVerified))
		say("     estructura correcta:                  %s", groupDigits(rc.NStructOK))
		say("     dañados descubiertos (a \"%s\"):    %s", dirDoubt, groupDigits(rc.NBadFound))
	}
	if rc.NEmpty > 0 {
		say("  Con contenido en blanco (no guardados):  %s", groupDigits(rc.NEmpty))
		say("     (se conserva el nombre pero el disco devuelve ceros: es lo que deja un SSD con TRIM)")
	}
	if rc.NAlready > 0 {
		say("  Ya recuperados en una ejecución anterior: %s", groupDigits(rc.NAlready))
	}
	if rc.NErr > 0 {
		say("  Errores al escribir:                     %s (ver informe.csv)", groupDigits(rc.NErr))
	}
	say("")
	say("  %-18s -> archivos con su ruta original", dirFiles)
	say("  %-18s -> carpetas cuya carpeta superior ya no existe", dirLoose)
	say("  %-18s -> archivos encontrados por su contenido, ordenados por tipo", dirCarved)
	say("  %-18s -> archivos incompletos o que quizá no abran", dirDoubt)
	say("  informe.csv        -> detalle de cada archivo (se abre con Excel)")
	if rc.Full {
		say("")
		say("AVISO: el destino se quedó SIN ESPACIO. Libera espacio o conecta otro disco y vuelve a ejecutar")
		say("       con la misma carpeta de destino: se continuará sin repetir lo ya recuperado.")
	}
	if stopRequested() {
		say("")
		say("Recuperación interrumpida. Al volver a ejecutar con el mismo destino se continuará.")
	}
	return nil
}

func writeLog(out string) {
	var sb strings.Builder
	for _, l := range logLines {
		sb.WriteString(l)
		sb.WriteString("\r\n")
	}
	os.WriteFile(filepath.Join(out, "informe.txt"), []byte(sb.String()), 0o644)
}

// printDiskHealth muestra en la consola el estado de los discos.
func printDiskHealth() {
	ds, vols, err := diskHealth()
	if err != nil {
		fmt.Println("AVISO:", err)
	}
	fmt.Println("ESTADO DE LOS DISCOS")
	for _, d := range ds {
		fmt.Printf("\n  %s  (%s, %s, %s)\n", d.Name, d.Media, d.Bus, humanSize(d.Size))
		fmt.Printf("     Veredicto: %s", d.Verdict)
		if len(d.Notes) > 0 {
			fmt.Printf(" — %s", strings.Join(d.Notes, "; "))
		}
		fmt.Printf("\n     %s\n", d.Advice())
		if d.Temp >= 0 {
			fmt.Printf("     Temperatura: %d °C\n", d.Temp)
		}
		if d.Wear >= 0 {
			fmt.Printf("     Desgaste (SSD): %d %%\n", d.Wear)
		}
		if d.Hours >= 0 {
			fmt.Printf("     Horas encendido: %s\n", groupDigits(d.Hours))
		}
	}
	if v := VolumeSummary(vols); v != "" {
		fmt.Println("\n  Espacio:", v)
	}
}

// printPrograms muestra en la consola los programas que más ocupan.
func printPrograms() {
	ps, err := listPrograms(nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("PROGRAMAS INSTALADOS (de mayor a menor)")
	for _, r := range ProgramRows(ps) {
		fmt.Printf("  %12s  %s   %s\n", r[1], r[0], r[2])
	}
	fmt.Println("\n  " + ProgramsSummary(ps))
}

// runImage crea (o continúa) la imagen del origen en la carpeta indicada.
func runImage(cfg *Config) error {
	if cfg.Source == "" {
		return fmt.Errorf("falta la unidad de origen (-origen)")
	}
	src := normalizeSource(cfg.Source)
	dir, err := filepath.Abs(cfg.ImageDir)
	if err != nil {
		return err
	}
	if fatal, _ := checkDestination(src, dir); fatal != "" {
		return fmt.Errorf("%s", fatal)
	}
	if destIsFAT32(dir) {
		return fmt.Errorf("el destino está en FAT32 (máximo 4 GB por archivo): usa NTFS o exFAT")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dev, err := OpenDevice(src)
	if err != nil {
		return err
	}
	defer dev.Close()
	name := src
	if !isRawDevicePath(src) {
		name = strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	}
	base := imageBaseName(strings.Trim(strings.NewReplacer(`\\.\`, "", `/dev/`, "", `:`, "").Replace(name), `\/`))
	job, err := NewImageJob(dev, dir, base)
	if err != nil {
		return err
	}
	if fs := destFS(dir); fs != "" && !strings.EqualFold(fs, "NTFS") && !job.Resumed {
		say("AVISO: el destino está en %s: la imagen ocupará el tamaño completo del disco (%s), aunque esté casi vacío.",
			fs, humanSize(dev.Size))
		if free := freeSpace(dir); free >= 0 && free < dev.Size {
			return fmt.Errorf("no cabe: hay %s libres y en %s la imagen ocupa %s. Usa un disco en NTFS",
				humanSize(free), fs, humanSize(dev.Size))
		}
	}
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	go func() { <-sigc; atomic.StoreInt32(&stopFlag, 1) }()
	fmt.Printf("Imagen de %s (%s)\n  -> %s\n", src, humanSize(dev.Size), job.ImgPath)
	if job.Resumed {
		done, _, _ := job.Totals()
		if job.Complete() {
			fmt.Println("La imagen ya estaba completa.")
			return nil
		}
		fmt.Printf("Continuando la imagen anterior (%s ya copiados).\n", humanSize(done))
	}
	err = job.Run(func(p ImageProgress) {
		statusLine(fmt.Sprintf("  %5.1f%%  %s de %s  ·  %s/s  ·  faltan ~%s  ·  ilegible: %s  ·  fase %d",
			float64(p.Done)*100/float64(p.Size), humanSize(p.Done), humanSize(p.Size), humanSize(int64(p.Rate)),
			humanDur(p.ETA), humanSize(p.Bad), p.Phase))
	})
	statusLine("")
	if err != nil {
		return err
	}
	done, bad, pend := job.Totals()
	fmt.Printf("\nCopiado: %s · ilegible: %s · pendiente: %s\n", humanSize(done), humanSize(bad), humanSize(pend))
	if pend > 0 {
		fmt.Println("Imagen detenida: repite la misma orden para continuar.")
	} else {
		fmt.Printf("Imagen terminada. Para recuperar desde ella:\n  recovery -origen \"%s\" -destino <carpeta>\n", job.ImgPath)
	}
	return nil
}
