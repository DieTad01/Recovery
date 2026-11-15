package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"testing"
)

// TestAccuracy compara el estado que muestra el explorador de cada archivo
// con la realidad (el archivo original). Lo lanza pruebas/prueba_exactitud.py.
func TestAccuracy(t *testing.T) {
	img, truthPath := os.Getenv("RECOVERY_ACC_IMG"), os.Getenv("RECOVERY_ACC_TRUTH")
	if img == "" || truthPath == "" {
		t.Skip("RECOVERY_ACC_IMG / RECOVERY_ACC_TRUTH no definidos")
	}
	raw, err := os.ReadFile(truthPath)
	if err != nil {
		t.Fatal(err)
	}
	var truth map[string][2]any
	if err := json.Unmarshal(raw, &truth); err != nil {
		t.Fatal(err)
	}
	byPath := map[string]string{}
	byHash := map[string]bool{}
	byName := map[string][]string{} // nombre -> hashes de originales con ese nombre
	for p, v := range truth {
		h := v[0].(string)
		byPath[p] = h
		byHash[h] = true
		byName[path.Base(p)] = append(byName[path.Base(p)], h)
	}

	ss, err := NewSession(img, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	ss.Run(func(int, SessionProgress) {})
	if os.Getenv("RECOVERY_ACC_DEEP") != "" {
		var all []*Entry
		for _, e := range ss.Entries {
			if e.Checkable() {
				all = append(all, e)
			}
		}
		ss.DeepVerify(all, nil)
	}

	type cell struct {
		n, ok      int
		bytes, okB int64
	}
	labels := []string{"Verificado", "Estructura OK", "Bueno", "Dañado", "En blanco", "No recuperable"}
	cells := map[string]*cell{}
	for _, l := range labels {
		cells[l] = &cell{}
	}
	label := func(e *Entry) string {
		switch e.State {
		case esVerified:
			return "Verificado"
		case esIntact, esCarved:
			if e.Deep == vStruct {
				return "Estructura OK"
			}
			return "Bueno"
		case esPartial, esCarvedDoubt, esCorrupt:
			return "Dañado"
		case esZeroed:
			return "En blanco"
		}
		return "No recuperable"
	}
	strip := regexp.MustCompile(`^Particion_\d+/`)
	unknown := 0
	var known []*Entry
	for _, e := range ss.Entries {
		if e.Size == 0 {
			continue
		}
		// ¿Qué original es? (si no se sabe, no se puede medir)
		var want []string
		switch e.Group {
		case grpPath, grpLost:
			if h, ok := byPath[strip.ReplaceAllString(path.Join(e.Dir, e.Name), "")]; ok {
				want = []string{h}
			}
		case grpLoose:
			want = byName[e.Name]
		case grpCarved:
			want = nil // se mide aparte: coincide con algún original o no
		}
		var got string
		if e.State != esLost {
			data, _ := ss.ReadEntry(e, 1<<31)
			s := sha1.Sum(data)
			got = hex.EncodeToString(s[:])
		}
		exact := false
		if e.Group == grpCarved {
			exact = byHash[got]
			if l := label(e); !exact && l != "Bueno" && l != "Verificado" && l != "Estructura OK" {
				unknown++
				continue
			}
		} else {
			if len(want) == 0 {
				unknown++
				continue
			}
			for _, w := range want {
				if w == got {
					exact = true
				}
			}
		}
		known = append(known, e)
		if l := label(e); exact != (l == "Bueno" || l == "Verificado" || l == "Estructura OK") {
			t.Logf("  · %s pero %s: %s/%s (%d bytes) — %s", l, map[bool]string{true: "EXACTO", false: "NO exacto"}[exact],
				e.Dir, e.Name, e.Size, e.Why)
		}
		c := cells[label(e)]
		c.n++
		c.bytes += e.Size
		if exact {
			c.ok++
			c.okB += e.Size
		}
	}
	var tn, tok int
	var tb, tokB int64
	for _, l := range labels {
		c := cells[l]
		tn += c.n
		tok += c.ok
		tb += c.bytes
		tokB += c.okB
		if c.n == 0 {
			continue
		}
		t.Logf("%-15s %5d archivos · exactos al original: %5d (%5.1f %%)", l, c.n, c.ok, 100*float64(c.ok)/float64(c.n))
	}
	s := summarize(known)
	pred := s.GoodPct() // lo que el resumen diría
	real := pct(tokB, tb)
	t.Logf("RESUMEN: «sin señales de daño» %.1f %% · «comprobado correcto» %.1f %% · «sin comprobar» %.1f %% ; en realidad exactos: %.1f %% (por tamaño) · error %+.1f puntos",
		pred, s.VerifiedPct(), pct(s.Good.Bytes, s.totalBytes()), real, pred-real)
	if v, st := cells["Verificado"], cells["Estructura OK"]; v.n+st.n > 0 {
		t.Logf("PRECISIÓN de «Verificado» + «Estructura OK»: %.1f %%  (%d de %d son exactos)",
			100*float64(v.ok+st.ok)/float64(v.n+st.n), v.ok+st.ok, v.n+st.n)
	}
	good := &cell{}
	for _, l := range []string{"Verificado", "Estructura OK", "Bueno"} {
		good.n += cells[l].n
		good.ok += cells[l].ok
	}
	if good.n > 0 {
		t.Logf("PRECISIÓN de «Bueno/Verificado»: %.1f %%  (%d de %d son exactos)", 100*float64(good.ok)/float64(good.n), good.ok, good.n)
	}
	bad := cells["Dañado"].ok + cells["En blanco"].ok + cells["No recuperable"].ok
	t.Logf("FALSAS ALARMAS (marcados como dañados/en blanco pero exactos): %d · sin original conocido: %d", bad, unknown)
	_ = fmt.Sprint
}

// TestValidateDir pasa el comprobador por archivos SANOS: no debe marcar
// ninguno como dañado (falsas alarmas). RECOVERY_VAL_DIR=carpeta.
func TestValidateDir(t *testing.T) {
	dir := os.Getenv("RECOVERY_VAL_DIR")
	if dir == "" {
		t.Skip("RECOVERY_VAL_DIR no definido")
	}
	count := map[int]int{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		v, why := validateFile(p, extOf(d.Name()))
		count[v]++
		if v == vBad {
			t.Errorf("FALSA ALARMA %s: %s", p, why)
		} else if os.Getenv("RECOVERY_VAL_V") != "" {
			t.Logf("%d %s: %s", v, p, why)
		}
		return nil
	})
	t.Logf("correctos(sumas) %d · estructura %d · sin forma de comprobar %d · dañados %d",
		count[vOK], count[vStruct], count[vUnknown], count[vBad])
}
