package main

import (
	"bytes"
	"crypto/sha1"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchMatcher(t *testing.T) {
	cases := []struct {
		q, dir, name string
		want         bool
	}{
		{"tesis", "Users/Ana/Documents/Tesis", "final.pdf", true}, // carpeta
		{"tesis", "Users/Ana/Documents", "Tesis final.pdf", true}, // nombre
		{"*.docx", "Users/Ana", "Carta.docx", true},
		{"*.docx", "Users/Ana", "Carta.doc", false},
		{"mama", "Fotos", "Cumpleaños de Mamá.jpg", true}, // sin acentos
		{"cumpleanos", "Fotos", "Cumpleaños de Mamá.jpg", true},
		{"Proyecto*", "Videos/Proyecto Filmora", "toma.mov", true},
		{"documents/tesis", `Users\Ana\Documents\Tesis`, "x.pdf", true},
		{"foto?.jpg", "", "foto1.jpg", true},
		{"foto?.jpg", "", "foto12.jpg", false},
		{"nada, *.mp4", "Videos", "clip.mp4", true},
		{"zzz", "Videos", "clip.mp4", false},
	}
	for _, c := range cases {
		if got := ParseSearch(c.q).Match(c.dir, c.name); got != c.want {
			t.Errorf("buscar %q en %s/%s = %v, se esperaba %v", c.q, c.dir, c.name, got, c.want)
		}
	}
	if ParseSearch("  , ; ") != nil {
		t.Error("una búsqueda vacía debe ser nil")
	}
	if !ParseSearch("*.jpg").MatchExt("jpg") || !ParseSearch("pdf").MatchExt("pdf") || ParseSearch("foto*.jpg").MatchExt("jpg") {
		t.Error("MatchExt incorrecto")
	}
}

func TestTreeSelection(t *testing.T) {
	es := []*Entry{
		{Name: "a.jpg", Dir: "Users/Ana/Fotos", Group: grpPath, Size: 100, Ext: "jpg", Cat: "Fotos e imágenes"},
		{Name: "b.jpg", Dir: "Users/Ana/Fotos", Group: grpPath, Size: 200, Ext: "jpg", Cat: "Fotos e imágenes"},
		{Name: "c.docx", Dir: "Users/Ana", Group: grpPath, Size: 50, Ext: "docx", Cat: "Documentos"},
		{Name: "k.dll", Dir: "Windows/System32", Group: grpPath, Size: 999, Ext: "dll", Cat: "Otros", System: true},
		{Name: "x.pdf", Dir: "Users/Ana", Group: grpLost, Size: 70, Ext: "pdf", Cat: "Documentos", State: esLost},
		{Name: "jpg_1.jpg", Dir: "JPG", Group: grpCarved, Size: 30, Ext: "jpg", Cat: "Fotos e imágenes", State: esCarved},
	}
	tr := BuildTree(es, true)
	if len(tr.Entries) != 5 {
		t.Fatalf("visibles = %d, se esperaban 5 (el .dll de Windows oculto)", len(tr.Entries))
	}
	users := tr.Find(rootNames[rootPath] + "/Users")
	if users == nil || users.Count != 3 || users.Size != 350 {
		t.Fatalf("carpeta Users mal contada: %+v", users)
	}
	tr.SetNodeChecked(users, true)
	if tr.Checked != 3 || tr.CheckedSize != 350 || !users.AllChecked() {
		t.Errorf("marcar carpeta: %d archivos, %d bytes", tr.Checked, tr.CheckedSize)
	}
	// El mismo archivo aparece también en «Por tipo»: los contadores van a la par.
	fotos := tr.Find(rootNames[rootTypes] + "/Fotos e imágenes")
	if fotos == nil || fotos.Count != 3 || fotos.Checked != 2 {
		t.Errorf("Por tipo/Fotos: %+v", fotos)
	}
	tr.SetChecked(es[0], false)
	if tr.Checked != 2 || users.AllChecked() {
		t.Error("desmarcar un archivo no actualiza la carpeta")
	}
	tr.SetChecked(es[4], true) // no recuperable: no se puede marcar
	if es[4].Checked {
		t.Error("un archivo no recuperable no se puede marcar")
	}
	if got := len(tr.Search(ParseSearch("*.jpg"))); got != 3 {
		t.Errorf("búsqueda *.jpg = %d, se esperaban 3", got)
	}
	if tr2 := BuildTree(es, false); len(tr2.Entries) != 6 {
		t.Error("sin ocultar deben verse los 6")
	}
}

// TestSessionImage analiza una imagen NTFS real y recupera lo marcado.
// Se activa con RECOVERY_TEST_IMG=ruta (las imágenes no se guardan en el
// repositorio; las crea pruebas/prueba_ntfs.py).
func TestSessionImage(t *testing.T) {
	img := os.Getenv("RECOVERY_TEST_IMG")
	if img == "" {
		t.Skip("RECOVERY_TEST_IMG no definido")
	}
	ss, err := NewSession(img, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	results := 0
	ss.Run(func(ev int, p SessionProgress) {
		if ev == evResults {
			results++
		}
	})
	tr := BuildTree(ss.Entries, true)
	t.Logf("publicaciones: %d   entradas visibles: %d", results, len(tr.Entries))
	for _, r := range tr.Roots {
		t.Logf("  %-36s %6d archivos  %10s", r.Name, r.Count, humanSize(r.Size))
	}
	pics := tr.Find(rootNames[rootPath] + "/Users/Ana María/Pictures")
	if pics == nil {
		t.Fatal("no aparece la carpeta Pictures")
	}
	tr.SetNodeChecked(pics, true)
	// Vista previa: la primera foto debe leerse completa y ser un JPEG.
	var jpg *Entry
	for _, e := range tr.CheckedEntries() {
		if e.Ext == "jpg" {
			jpg = e
			break
		}
	}
	if jpg == nil {
		t.Fatal("no hay fotos JPG marcadas")
	}
	data, trunc := ss.ReadEntry(jpg, 64<<20)
	if trunc || int64(len(data)) != jpg.Size || !bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) {
		t.Errorf("vista previa de %s: %d bytes, truncado=%v", jpg.Name, len(data), trunc)
	}
	dest := t.TempDir()
	res := ss.Recover(tr.CheckedEntries(), dest, nil)
	t.Logf("recuperados: %d buenos, %d dudosos, %d errores", res.Good, res.Doubt, res.Errors)
	if res.Good != int64(tr.Checked) {
		t.Errorf("se marcaron %d y se recuperaron bien %d", tr.Checked, res.Good)
	}
	// Comparar con los originales si están disponibles.
	if src := os.Getenv("RECOVERY_TEST_SRC"); src != "" {
		n := 0
		filepath.Walk(filepath.Join(dest, dirFiles), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(filepath.Join(dest, dirFiles), p)
			a, _ := os.ReadFile(p)
			b, err := os.ReadFile(filepath.Join(src, rel))
			if err != nil || sha1.Sum(a) != sha1.Sum(b) {
				t.Errorf("%s no coincide con el original", rel)
			}
			n++
			return nil
		})
		if n == 0 || !strings.Contains(pics.Path, "Pictures") {
			t.Error("no se comparó nada")
		}
		t.Logf("%d archivos comparados byte a byte con los originales", n)
	}
}
