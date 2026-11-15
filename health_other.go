//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// diskHealth en Linux: tipo y tamaño desde /sys y, si está instalado,
// el resultado de «smartctl -H».
func diskHealth() ([]DiskHealth, []VolumeInfo, error) {
	var out []DiskHealth
	dirs, _ := filepath.Glob("/sys/block/*")
	for _, d := range dirs {
		name := filepath.Base(d)
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
			continue
		}
		read := func(p string) string { b, _ := os.ReadFile(filepath.Join(d, p)); return strings.TrimSpace(string(b)) }
		sectors, _ := strconv.ParseInt(read("size"), 10, 64)
		h := DiskHealth{Name: "/dev/" + name, Size: sectors * 512, Temp: -1, TempMax: -1, Wear: -1,
			Hours: -1, ReadErr: -1, WriteErr: -1, ReadUncor: -1, Health: "Desconocido"}
		if m := read("device/model"); m != "" {
			h.Name += " — " + m
		}
		h.Media = "SSD"
		if read("queue/rotational") == "1" {
			h.Media = "HDD (disco duro)"
		}
		if _, err := exec.LookPath("smartctl"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			o, _ := exec.CommandContext(ctx, "smartctl", "-H", "/dev/"+name).CombinedOutput()
			cancel()
			switch s := string(o); {
			case strings.Contains(s, "PASSED") || strings.Contains(s, ": OK"):
				h.Health = "Bueno"
			case strings.Contains(s, "FAILED"):
				h.Health = "MALO"
			}
		}
		h.judge()
		out = append(out, h)
	}
	return out, nil, nil
}
