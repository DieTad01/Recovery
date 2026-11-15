//go:build !windows

package main

import "syscall"

// usedBytes devuelve el espacio que ocupa realmente un archivo en disco.
func usedBytes(path string) int64 {
	var st syscall.Stat_t
	if syscall.Stat(path, &st) != nil {
		return -1
	}
	return st.Blocks * 512
}
