//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func openRaw(path string) (rawReader, int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil || size == 0 {
		if st, e2 := f.Stat(); e2 == nil && st.Size() > 0 {
			size = st.Size()
		}
	}
	if size <= 0 {
		f.Close()
		return nil, 0, 0, fmt.Errorf("no se pudo determinar el tamaño de %s", path)
	}
	return f, size, 1, nil
}

// normalizeSource en Linux/macOS deja la ruta tal cual.
func normalizeSource(s string) string { return strings.TrimSpace(s) }

func isRawDevicePath(p string) bool { return strings.HasPrefix(p, "/dev/") }

// listSources enumera los discos y particiones del sistema.
func listSources() []Source {
	var out []Source
	f, err := os.Open("/proc/partitions")
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) != 4 {
			continue
		}
		blocks, err := strconv.ParseInt(fs[2], 10, 64)
		if err != nil || blocks == 0 {
			continue
		}
		name := fs[3]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
			continue
		}
		out = append(out, Source{
			Path:  "/dev/" + name,
			Label: fmt.Sprintf("/dev/%s  (%s)", name, humanSize(blocks*1024)),
			Size:  blocks * 1024,
		})
	}
	return out
}

func keepAwake() {}

func isAdmin() bool { return os.Geteuid() == 0 }
func relaunchElevated(extra ...string) error {
	return fmt.Errorf("ejecuta el programa con sudo")
}
func describeDrive(*Device) []string { return nil }

// freeSpace devuelve el espacio libre en la carpeta indicada (-1 si no se sabe).
func freeSpace(dir string) int64 {
	for {
		var st syscall.Statfs_t
		if err := syscall.Statfs(dir, &st); err == nil {
			return int64(st.Bavail) * int64(st.Bsize)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return -1
		}
		dir = parent
	}
}

// checkDestination comprueba que el destino no esté en la unidad de origen.
func checkDestination(srcPath, outDir string) (fatal, warn string) {
	var sst syscall.Stat_t
	if err := syscall.Stat(srcPath, &sst); err != nil {
		return "", ""
	}
	if sst.Mode&syscall.S_IFMT != syscall.S_IFBLK {
		return "", ""
	}
	dir := outDir
	for {
		var dst syscall.Stat_t
		if err := syscall.Stat(dir, &dst); err == nil {
			if uint64(dst.Dev) == uint64(sst.Rdev) {
				return "La carpeta de destino está en la MISMA unidad que se quiere recuperar. " +
					"Guardar ahí sobrescribiría los archivos perdidos. Elige otra unidad.", ""
			}
			return "", ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

func isDiskFull(err error) bool {
	for err != nil {
		if e, ok := err.(syscall.Errno); ok {
			return e == syscall.ENOSPC || e == syscall.EDQUOT
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func destFSNote(string) string { return "" }

// setSparse: en Linux los archivos ya son dispersos al no escribir los ceros.
func setSparse(*os.File) {}

func destIsFAT32(string) bool { return false }

// destFS: en Linux no hace falta (los archivos ya son dispersos).
func destFS(string) string { return "" }
