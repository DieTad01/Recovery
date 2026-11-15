package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDetect mide qué parte de los daños detecta la comprobación a fondo,
// por tipo de archivo: toma archivos sanos (RECOVERY_DET_DIRS, separados por
// «:»), los daña de tres maneras y mira si se marcan como dañados.
func TestDetect(t *testing.T) {
	dirs := os.Getenv("RECOVERY_DET_DIRS")
	if dirs == "" {
		t.Skip("RECOVERY_DET_DIRS no definido")
	}
	type st struct{ n, alarm, d1, d2, d3 int }
	stats := map[string]*st{}
	rnd := rand.New(rand.NewSource(7))
	for _, dir := range strings.Split(dirs, ":") {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			ext := extOf(d.Name())
			info, _ := d.Info()
			if info == nil || info.Size() < 48<<10 || info.Size() > 64<<20 {
				return nil
			}
			s := stats[ext]
			if s == nil {
				s = &st{}
				stats[ext] = s
			}
			if s.n >= 40 {
				return nil
			}
			orig, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if v, _ := validateContent(bytes.NewReader(orig), int64(len(orig)), ext); v == vUnknown {
				delete(stats, ext)
				return nil
			} else if v == vBad {
				s.alarm++
			}
			s.n++
			bad := func(b []byte) int {
				v, _ := validateContent(bytes.NewReader(b), int64(len(b)), ext)
				if v == vBad {
					return 1
				}
				return 0
			}
			sz := len(orig)
			// 1) un bloque de 4 KB con datos ajenos a mitad del archivo
			b := append([]byte(nil), orig...)
			o := sz / 2 / 4096 * 4096
			rnd.Read(b[o:min(o+4096, sz)])
			s.d1 += bad(b)
			// 2) 64 KB con datos ajenos (un trozo de otro archivo) al 30 %
			b = append([]byte(nil), orig...)
			o = sz * 3 / 10 / 4096 * 4096
			rnd.Read(b[o:min(o+65536, sz)])
			s.d2 += bad(b)
			// 3) el último 30 % en blanco
			b = append([]byte(nil), orig...)
			o = sz * 7 / 10 / 4096 * 4096
			clear(b[o:])
			s.d3 += bad(b)
			return nil
		})
	}
	var exts []string
	for e := range stats {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	var out strings.Builder
	fmt.Fprintf(&out, "\n%-6s %5s %9s %12s %12s %12s\n", "tipo", "n", "fals.alar", "4KB ajenos", "64KB ajenos", "final blanco")
	for _, e := range exts {
		s := stats[e]
		pc := func(x int) string { return fmt.Sprintf("%3.0f %%", 100*float64(x)/float64(s.n)) }
		fmt.Fprintf(&out, "%-6s %5d %9d %12s %12s %12s\n", e, s.n, s.alarm, pc(s.d1), pc(s.d2), pc(s.d3))
	}
	t.Log(out.String())
}
