package main

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Medición del Reparador con archivos reales: los daña de cuatro formas
// (como quedan tras una recuperación) y guarda los dañados y lo que sale de
// repararlos, para comprobarlo después con programas independientes
// (pruebas/reparador_*.py). Se activa con REP_SRC=carpeta REP_DST=carpeta
// REP_EXT="docx xlsx pptx" (o pdf, o "xls doc ppt").
func TestRepairMeasure(t *testing.T) {
	src, dst := os.Getenv("REP_SRC"), os.Getenv("REP_DST")
	if src == "" {
		t.Skip()
	}
	os.MkdirAll(dst, 0o755)
	var files []string
	filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			e := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
			if strings.Contains(os.Getenv("REP_EXT"), e) {
				files = append(files, p)
			}
		}
		return nil
	})
	rnd := rand.New(rand.NewSource(1))
	for fi, f := range files {
		data, _ := os.ReadFile(f)
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f), "."))
		for mode := 0; mode < 4; mode++ {
			d := append([]byte(nil), data...)
			n := len(d)
			switch mode {
			case 0: // sobrescrito en medio (4-16 KB)
				off := (n / 2) &^ 4095
				l := min(4096*(1+rnd.Intn(4)), n-off)
				rnd.Read(d[off : off+l])
			case 1: // final perdido (último 30 %)
				off := (n * 7 / 10) &^ 4095
				rnd.Read(d[off:])
			case 2: // zona en blanco (TRIM) en el primer tercio
				off := (n / 3) &^ 4095
				l := min(8192, n-off)
				clear(d[off : off+l])
			case 3: // principio perdido (primeros 4 KB)
				rnd.Read(d[:min(4096, n)])
			}
			v, why := validateContent(bytes.NewReader(d), int64(n), ext)
			r := repairData(d, ext)
			base := filepath.Join(dst, strings.TrimSuffix(filepath.Base(f), "."+ext)+"_m"+string(rune('0'+mode))+"_"+itoa(fi))
			os.WriteFile(base+"_danado."+ext, d, 0o644)
			st := "SIN-REPARAR"
			if r.Repaired != nil {
				os.WriteFile(base+"_reparado."+ext, r.Repaired, 0o644)
				rv, _ := validateContent(bytes.NewReader(r.Repaired), int64(len(r.Repaired)), ext)
				st = map[int]string{vOK: "reparado-OK", vStruct: "reparado-struct", vBad: "REPARADO-MALO", vUnknown: "reparado-?"}[rv]
				if r.Complete {
					st += "+completo"
				}
			}
			if r.Rescue != nil {
				os.WriteFile(base+"_rescate."+r.RescueExt, r.Rescue, 0o644)
				st += " +rescate"
			}
			t.Logf("%-40s m%d dañado=%v %-14s | %s", filepath.Base(f), mode, v == vBad, st, truncStr(why+" || "+joinNotes(r.Notes), 160))
		}
	}
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
func itoa(i int) string { return strconv.Itoa(i) }
