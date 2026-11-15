//go:build windows

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	shell32                   = syscall.NewLazyDLL("shell32.dll")
	procGetLogicalDrives      = kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW         = kernel32.NewProc("GetDriveTypeW")
	procGetVolumeInformationW = kernel32.NewProc("GetVolumeInformationW")
	procGetDiskFreeSpaceExW   = kernel32.NewProc("GetDiskFreeSpaceExW")
	procShellExecuteW         = shell32.NewProc("ShellExecuteW")
	procIsUserAnAdmin         = shell32.NewProc("IsUserAnAdmin")
)

const (
	ioctlDiskGetDriveGeometry   = 0x00070000
	ioctlDiskGetLengthInfo      = 0x0007405C
	ioctlDiskGetPartitionInfoEx = 0x00070048
	ioctlDiskGetDriveGeometryEx = 0x000700A0
	fsctlAllowExtendedDasdIO    = 0x00090083
	ioctlVolumeGetDiskExtents   = 0x00560000
	ioctlStorageQueryProperty   = 0x002D1400

	errorHandleEOF      = syscall.Errno(38)
	errorDiskFull       = syscall.Errno(112)
	errorHandleDiskFull = syscall.Errno(39)
)

// winFile lee de un identificador de Windows con lecturas posicionadas.
type winFile struct{ h syscall.Handle }

func (w *winFile) ReadAt(p []byte, off int64) (int, error) {
	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > 16<<20 {
			chunk = chunk[:16<<20]
		}
		var o syscall.Overlapped
		o.Offset = uint32(off)
		o.OffsetHigh = uint32(off >> 32)
		var done uint32
		err := syscall.ReadFile(w.h, chunk, &done, &o)
		if err != nil {
			if err == errorHandleEOF {
				return total + int(done), io.EOF
			}
			return total + int(done), err
		}
		if done == 0 {
			return total, io.EOF
		}
		total += int(done)
		p = p[done:]
		off += int64(done)
	}
	return total, nil
}

func (w *winFile) Close() error { return syscall.CloseHandle(w.h) }

func isRawDevicePath(p string) bool {
	return strings.HasPrefix(p, `\\.\`) || strings.HasPrefix(p, `\\?\`)
}

func openHandle(path string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	return syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, 0, 0)
}

func ioctl(h syscall.Handle, code uint32, in []byte, out []byte) (uint32, error) {
	var inP, outP *byte
	if len(in) > 0 {
		inP = &in[0]
	}
	if len(out) > 0 {
		outP = &out[0]
	}
	var ret uint32
	err := syscall.DeviceIoControl(h, code, inP, uint32(len(in)), outP, uint32(len(out)), &ret, nil)
	return ret, err
}

func openRaw(path string) (rawReader, int64, int, error) {
	h, err := openHandle(path)
	if err != nil {
		if err == syscall.ERROR_ACCESS_DENIED {
			return nil, 0, 0, fmt.Errorf("acceso denegado a %s: ejecuta el programa como administrador", path)
		}
		return nil, 0, 0, fmt.Errorf("no se pudo abrir %s: %v", path, err)
	}
	if !isRawDevicePath(path) {
		var fi syscall.ByHandleFileInformation
		if err := syscall.GetFileInformationByHandle(h, &fi); err != nil {
			syscall.CloseHandle(h)
			return nil, 0, 0, err
		}
		size := int64(fi.FileSizeHigh)<<32 | int64(fi.FileSizeLow)
		if size <= 0 {
			syscall.CloseHandle(h)
			return nil, 0, 0, fmt.Errorf("%s está vacío", path)
		}
		return &winFile{h}, size, 1, nil
	}
	// Permite leer el volumen completo, incluido el final (sector de
	// arranque de respaldo). Si falla no importa.
	ioctl(h, fsctlAllowExtendedDasdIO, nil, nil)

	sector := 512
	geo := make([]byte, 24)
	var geoSize int64
	if n, err := ioctl(h, ioctlDiskGetDriveGeometry, nil, geo); err == nil && n >= 24 {
		bps := int(le32(geo[20:]))
		if bps >= 512 && bps <= 65536 && bps&(bps-1) == 0 {
			sector = bps
		}
		geoSize = int64(le64(geo[0:])) * int64(le32(geo[12:])) * int64(le32(geo[16:])) * int64(bps)
	}
	size := deviceSize(h)
	if size <= 0 {
		size = geoSize
	}
	w := &winFile{h}
	if size <= 0 {
		size = probeSize(w, sector)
	}
	if size <= 0 {
		syscall.CloseHandle(h)
		return nil, 0, 0, fmt.Errorf("no se pudo determinar el tamaño de %s", path)
	}
	return w, size, sector, nil
}

// deviceSize pregunta a Windows el tamaño de un volumen o disco, probando
// varias consultas porque no todos los controladores admiten las mismas.
func deviceSize(h syscall.Handle) int64 {
	b := make([]byte, 256)
	if n, err := ioctl(h, ioctlDiskGetLengthInfo, nil, b[:8]); err == nil && n >= 8 {
		if v := int64(le64(b)); v > 0 {
			return v
		}
	}
	if n, err := ioctl(h, ioctlDiskGetPartitionInfoEx, nil, b[:144]); err == nil && n >= 24 {
		if v := int64(le64(b[16:])); v > 0 {
			return v
		}
	}
	if n, err := ioctl(h, ioctlDiskGetDriveGeometryEx, nil, b); err == nil && n >= 32 {
		if v := int64(le64(b[24:])); v > 0 {
			return v
		}
	}
	return 0
}

// probeSize averigua el tamaño leyendo: último recurso cuando el sistema
// no lo informa. Busca el último sector legible por bisección.
func probeSize(w *winFile, sector int) int64 {
	buf := make([]byte, sector)
	ok := func(sec int64) bool {
		n, _ := w.ReadAt(buf, sec*int64(sector))
		return n == sector
	}
	if !ok(0) {
		return 0
	}
	lo, hi := int64(0), int64(1)
	for ok(hi) {
		lo, hi = hi, hi*2
		if hi > 1<<50/int64(sector) {
			return 0
		}
	}
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if ok(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi * int64(sector)
}

var (
	reLetter = regexp.MustCompile(`^([A-Za-z]):?[\\/]?$`)
	reDisk   = regexp.MustCompile(`(?i)^(?:disco|disk|physicaldrive)\s*(\d+)$`)
)

// normalizeSource acepta "C:", "C", "disco0", "PhysicalDrive1" o una ruta.
func normalizeSource(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
	if m := reLetter.FindStringSubmatch(s); m != nil {
		return `\\.\` + strings.ToUpper(m[1]) + ":"
	}
	if m := reDisk.FindStringSubmatch(s); m != nil {
		return `\\.\PhysicalDrive` + m[1]
	}
	return s
}

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// volumeDiskNumbers devuelve los discos físicos donde reside una letra.
func volumeDiskNumbers(letter string) []int {
	h, err := openHandle(`\\.\` + letter + ":")
	if err != nil {
		return nil
	}
	defer syscall.CloseHandle(h)
	out := make([]byte, 8+24*16)
	n, err := ioctl(h, ioctlVolumeGetDiskExtents, nil, out)
	if err != nil || n < 8 {
		return nil
	}
	cnt := int(le32(out))
	var res []int
	for i := 0; i < cnt && 8+24*(i+1) <= int(n); i++ {
		res = append(res, int(le32(out[8+24*i:])))
	}
	return res
}

type storageInfo struct {
	model  string
	bus    uint32
	ssd    bool
	ssdOK  bool
	trim   bool
	trimOK bool
}

func queryStorage(h syscall.Handle) storageInfo {
	var si storageInfo
	in := make([]byte, 12) // STORAGE_PROPERTY_QUERY: PropertyId, QueryType
	out := make([]byte, 1024)
	if n, err := ioctl(h, ioctlStorageQueryProperty, in, out); err == nil && n >= 36 {
		str := func(ofs uint32) string {
			if ofs == 0 || ofs >= n {
				return ""
			}
			e := ofs
			for e < n && out[e] != 0 {
				e++
			}
			return strings.TrimSpace(string(out[ofs:e]))
		}
		vendor := str(le32(out[12:]))
		product := str(le32(out[16:]))
		si.model = strings.TrimSpace(vendor + " " + product)
		si.bus = le32(out[28:])
	}
	in[0] = 7 // StorageDeviceSeekPenaltyProperty
	if n, err := ioctl(h, ioctlStorageQueryProperty, in, out[:16]); err == nil && n >= 9 {
		si.ssdOK = true
		si.ssd = out[8] == 0
	}
	in[0] = 8 // StorageDeviceTrimProperty
	if n, err := ioctl(h, ioctlStorageQueryProperty, in, out[:16]); err == nil && n >= 9 {
		si.trimOK = true
		si.trim = out[8] != 0
	}
	return si
}

func busName(b uint32) string {
	switch b {
	case 7:
		return "USB"
	case 11:
		return "SATA"
	case 17:
		return "NVMe"
	case 12:
		return "SD"
	case 13:
		return "MMC"
	}
	return ""
}

// describeDrive devuelve avisos sobre el tipo de unidad (SSD/TRIM).
func describeDrive(d *Device) []string {
	w, ok := d.r.(*winFile)
	if !ok || !isRawDevicePath(d.Path) {
		return nil
	}
	si := queryStorage(w.h)
	var notes []string
	if si.ssdOK && si.ssd {
		msg := "Esta unidad es un SSD."
		if si.trimOK && si.trim {
			msg += " Tiene TRIM activo: en los SSD, Windows suele vaciar físicamente el contenido de los " +
				"archivos borrados. Es posible que aparezcan nombres y carpetas pero que parte del contenido " +
				"ya no exista; el programa lo detecta y lo indica en el informe."
		}
		notes = append(notes, msg)
	}
	return notes
}

// listSources enumera las unidades (letras) y los discos físicos.
func listSources() []Source {
	var out []Source
	mask, _, _ := procGetLogicalDrives.Call()
	lettersByDisk := map[int][]string{}
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root := letter + `:\`
		dt, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))))
		if dt != 2 && dt != 3 { // extraíble o fijo
			continue
		}
		var label, fsname [261]uint16
		procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))),
			uintptr(unsafe.Pointer(&label[0])), 261, 0, 0, 0,
			uintptr(unsafe.Pointer(&fsname[0])), 261)
		var size int64
		if h, err := openHandle(`\\.\` + letter + ":"); err == nil {
			size = deviceSize(h)
			syscall.CloseHandle(h)
		}
		if size == 0 {
			var avail, total, free uint64
			procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))),
				uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
			size = int64(total)
		}
		fs := syscall.UTF16ToString(fsname[:])
		if fs == "" {
			fs = "sin formato / RAW"
		}
		lab := syscall.UTF16ToString(label[:])
		desc := fmt.Sprintf("Unidad %s:  %s, %s", letter, fs, humanSize(size))
		if lab != "" {
			desc += fmt.Sprintf(", \"%s\"", lab)
		}
		if dt == 2 {
			desc += " (extraíble)"
		}
		for _, dn := range volumeDiskNumbers(letter) {
			lettersByDisk[dn] = append(lettersByDisk[dn], letter+":")
		}
		out = append(out, Source{Path: `\\.\` + letter + ":", Label: desc, Size: size, Letter: letter})
	}
	for n := 0; n < 32; n++ {
		path := fmt.Sprintf(`\\.\PhysicalDrive%d`, n)
		h, err := openHandle(path)
		if err != nil {
			continue
		}
		size := deviceSize(h)
		si := queryStorage(h)
		syscall.CloseHandle(h)
		if size <= 0 {
			continue
		}
		desc := fmt.Sprintf("Disco físico %d completo:  %s", n, humanSize(size))
		if si.model != "" {
			desc += ", " + si.model
		}
		if bn := busName(si.bus); bn != "" {
			desc += " [" + bn + "]"
		}
		if si.ssdOK && si.ssd {
			desc += " [SSD]"
		}
		if ls := lettersByDisk[n]; len(ls) > 0 {
			desc += "  (contiene " + strings.Join(ls, ", ") + ")"
		}
		out = append(out, Source{Path: path, Label: desc, Size: size, Disk: true})
	}
	return out
}

var procAttachConsole = kernel32.NewProc("AttachConsole")

// attachConsole engancha la salida a la consola que lanzó el programa.
// Como el .exe se compila como aplicación de ventana, sin esto no se
// vería nada al ejecutarlo desde una consola (cmd o PowerShell).
func attachConsole() {
	const attachParent = ^uintptr(0) // ATTACH_PARENT_PROCESS (-1)
	if r, _, _ := procAttachConsole.Call(attachParent); r == 0 {
		return // ya hay consola, o no hay ninguna (lanzado desde el explorador)
	}
	reopen := func(name string, write bool) *os.File {
		access := uint32(syscall.GENERIC_READ)
		if write {
			access = syscall.GENERIC_WRITE
		}
		h, err := syscall.CreateFile(utf16Ptr(name), access,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
		if err != nil {
			return nil
		}
		return os.NewFile(uintptr(h), name)
	}
	if f := reopen("CONOUT$", true); f != nil {
		os.Stdout = f
		os.Stderr = f
	}
	if f := reopen("CONIN$", false); f != nil {
		os.Stdin = f
		stdin = bufio.NewReader(os.Stdin)
	}
}

// keepAwake evita que Windows suspenda el equipo durante un escaneo largo.
func keepAwake() {
	go func() {
		runtime.LockOSThread()
		const esContinuous, esSystemRequired = 0x80000000, 0x00000001
		kernel32.NewProc("SetThreadExecutionState").Call(esContinuous | esSystemRequired)
		select {} // el estado dura mientras viva este hilo
	}()
}

func isAdmin() bool {
	r, _, _ := procIsUserAnAdmin.Call()
	return r != 0
}

func quoteArg(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"") {
		return a
	}
	return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
}

// relaunchElevated vuelve a lanzar el programa pidiendo permisos de
// administrador (UAC).
func relaunchElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := make([]string, 0, len(os.Args))
	for _, a := range os.Args[1:] {
		args = append(args, quoteArg(a))
	}
	args = append(args, "-pausa")
	cwd, _ := os.Getwd()
	r, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("runas"))),
		uintptr(unsafe.Pointer(utf16Ptr(exe))),
		uintptr(unsafe.Pointer(utf16Ptr(strings.Join(args, " ")))),
		uintptr(unsafe.Pointer(utf16Ptr(cwd))),
		1)
	if r <= 32 {
		return fmt.Errorf("no se concedieron permisos de administrador (código %d)", r)
	}
	return nil
}

// freeSpace devuelve el espacio libre en la carpeta indicada (-1 si no se sabe).
func freeSpace(dir string) int64 {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return -1
	}
	root := filepath.VolumeName(abs) + `\`
	var avail, total, free uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))),
		uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return -1
	}
	return int64(avail)
}

// destFSNote avisa de limitaciones del sistema de archivos de destino.
func destFSNote(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	root := filepath.VolumeName(abs) + `\`
	var fsname [261]uint16
	r, _, _ := procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))),
		0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&fsname[0])), 261)
	if r == 0 {
		return ""
	}
	if strings.EqualFold(syscall.UTF16ToString(fsname[:]), "FAT32") {
		return "El destino está en FAT32: no admite archivos de más de 4 GB (los videos grandes fallarán). " +
			"Es mejor un disco en NTFS o exFAT."
	}
	return ""
}

// checkDestination comprueba que el destino no esté en la unidad de origen.
func checkDestination(srcPath, outDir string) (fatal, warn string) {
	abs, err := filepath.Abs(outDir)
	if err != nil {
		return "", ""
	}
	vol := strings.ToUpper(filepath.VolumeName(abs))
	if len(vol) != 2 || vol[1] != ':' {
		return "", ""
	}
	up := strings.ToUpper(srcPath)
	const same = "La carpeta de destino está en la MISMA unidad que se quiere recuperar. " +
		"Guardar ahí sobrescribiría los archivos perdidos. Elige una carpeta en OTRA unidad " +
		"(un disco externo o una memoria USB)."
	if up == `\\.\`+vol {
		return same, ""
	}
	if strings.HasPrefix(up, `\\.\PHYSICALDRIVE`) {
		var n int
		fmt.Sscanf(up[len(`\\.\PHYSICALDRIVE`):], "%d", &n)
		for _, dn := range volumeDiskNumbers(vol[:1]) {
			if dn == n {
				return "", "El destino (" + vol + ") está en el MISMO disco físico que se va a escanear. " +
					"Todo lo que se escriba ahí puede sobrescribir archivos recuperables de esa partición. " +
					"Lo seguro es guardar en otro disco."
			}
		}
	}
	if !isRawDevicePath(srcPath) {
		// Imagen: no debe guardarse dentro de sí misma, nada más que comprobar.
		return "", ""
	}
	return "", ""
}

func isDiskFull(err error) bool {
	for err != nil {
		if e, ok := err.(syscall.Errno); ok {
			return e == errorDiskFull || e == errorHandleDiskFull
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
