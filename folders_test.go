package main

import (
	"strings"
	"testing"
	"time"
)

func TestFolderScore(t *testing.T) {
	cases := []struct {
		q, rel string
		want   int
	}{
		{"empresa", "Users/Ana/Documents/Empresa", fsExact},
		{"EMPRESA", "Users/Ana/Documents/empresa", fsExact},
		{"empresa", "Users/Ana/Documents/Empresa 2024", fsPrefix},
		{"empresa", "Users/Ana/Documents/Mi empresa", fsSub},
		{"empresa", "Users/Ana/Documents/Empresa/Facturas", fsNone}, // solo el nombre de la carpeta
		{"contab*", "Users/Ana/Contabilidad", fsWild},
		{"contab*", "Users/Ana/Mi Contabilidad", fsNone},
		{"*contab*", "Users/Ana/Mi Contabilidad", fsWild},
		{"contabilidad", "Users/Ana/Contabilidad", fsExact},
		{"contabilidad", "Users/Ana/Contabilidad", fsExact},
		{"administracion", "Users/Ana/Administración", fsExact}, // sin acentos
		{"documents/empresa", "Users/Ana/Documents/Empresa", fsWild},
		{"documents/empre", "Users/Ana/Documents/Empresa", fsSub},
		{`documents\empresa`, "Users/Ana/Documents/Empresa", fsWild},
		{"documents/empresa", "Users/Ana/Documents/Empresa/Facturas", fsNone},
		{"documents/*", "Users/Ana/Documents/Empresa", fsWild},
		{"nada, empresa", "X/Empresa", fsExact},
	}
	for _, c := range cases {
		name := c.rel[strings.LastIndexByte(c.rel, '/')+1:]
		if got := ParseSearch(c.q).FolderScore(c.rel, name); got != c.want {
			t.Errorf("carpeta %q frente a %q = %d, se esperaba %d", c.rel, c.q, got, c.want)
		}
	}
}

func TestInFolder(t *testing.T) {
	m := ParseSearch("Empresa")
	yes := []string{"Users/Ana/Documents/Empresa", "Users/Ana/Documents/Empresa/Facturas/2024", "carpeta_512/Empresa", "EMPRESA"}
	no := []string{"", "Users/Ana/Documents", "Users/Ana/Empresa 2024", "Users/Ana/Mi empresa", "carpeta_512"}
	for _, d := range yes {
		if !m.InFolder(d) {
			t.Errorf("%q debería estar dentro de «Empresa»", d)
		}
	}
	for _, d := range no {
		if m.InFolder(d) {
			t.Errorf("%q no debería estar dentro de «Empresa»", d)
		}
	}
	if got := m.FolderOf("Users/Ana/Empresa/Viejo/Empresa"); got != "Users/Ana/Empresa" {
		t.Errorf("FolderOf debe dar la carpeta más alta, dio %q", got)
	}
	if !ParseSearch("Contab*").InFolder("Users/Ana/Contabilidad 2024/enero") {
		t.Error("comodines en -carpeta")
	}
	// Los nombres que pone el programa a las carpetas sin nombre no cuentan.
	if ParseSearch("carpeta*").InFolder("carpeta_512/x") || !syntheticDir("carpeta_512") || syntheticDir("carpeta_nueva") {
		t.Error("carpetas sin nombre")
	}
}

func folderTestTree() (*Tree, []*Entry) {
	d := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	es := []*Entry{
		{Name: "balance.xlsx", Dir: "Users/Ana/Documents/Empresa", Group: grpPath, Size: 100, MTime: d, State: esVerified, Deep: vOK},
		{Name: "nomina enero.xlsx", Dir: "Users/Ana/Documents/Empresa/Nominas", Group: grpPath, Size: 200, MTime: d.AddDate(0, 1, 0)},
		{Name: "contrato.docx", Dir: "Users/Ana/Documents/Empresa/Nominas", Group: grpPath, Size: 50, State: esCorrupt},
		{Name: "viejo.xlsx", Dir: "Users/Ana/Documents/Empresa", Group: grpLost, Size: 70, State: esLost},
		{Name: "logo empresa.png", Dir: "Users/Ana/Pictures", Group: grpPath, Size: 10},
		{Name: "otra.pdf", Dir: "Users/Ana/Empresa 2019", Group: grpPath, Size: 5},
		{Name: "nomina febrero.xlsx", Dir: "carpeta_777", Group: grpLoose, Size: 30},
		{Name: "nomina marzo.xlsx", Dir: "carpeta_777", Group: grpLoose, Size: 30},
		{Name: "nomina abril.xlsx", Dir: "carpeta_888/Respaldo", Group: grpLoose, Size: 30},
		{Name: "todo.txt", Dir: "Users/Bob/Empresa", Group: grpLost, Size: 9, State: esLost},
	}
	for _, e := range es {
		e.Ext = extOf(e.Name)
		e.Cat = category(e.Ext)
	}
	return BuildTree(es, true), es
}

func TestFindFolders(t *testing.T) {
	tr, _ := folderTestTree()
	hits := tr.FindFolders(ParseSearch("empresa"))
	var got []string
	for _, h := range hits {
		got = append(got, h.Node.relPath())
	}
	want := []string{
		"Users/Ana/Documents/Empresa", // exacta, con más archivos
		"Users/Bob/Empresa",           // exacta, pero no recuperable
		"Users/Ana/Empresa 2019",      // empieza por «empresa»
		"Users/Ana/Pictures",          // contiene «logo empresa.png»
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("carpetas encontradas:\n  %s\nse esperaban:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	h := hits[0]
	if h.Score != fsExact || h.Lost == nil || h.Lost.Count != 1 || h.Node.Count != 3 {
		t.Errorf("Empresa: puntuación %d, gemela %v, archivos %d", h.Score, h.Lost, h.Node.Count)
	}
	if hits[1].Node.Root != rootLost || hits[3].Score != fsNone || hits[3].Matches != 1 {
		t.Error("orden o tipo de resultados incorrecto")
	}
	if loc := h.Node.Location(); loc != `\Users\Ana\Documents` {
		t.Errorf("ubicación %q", loc)
	}
	if !h.Node.Newest.Equal(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("fecha más reciente %v", h.Node.Newest)
	}

	// Carpeta que perdió su nombre: se encuentra por los archivos de dentro.
	hits = tr.FindFolders(ParseSearch("nomina"))
	if len(hits) < 3 || hits[0].Node.Name != "Nominas" || hits[0].Score != fsPrefix {
		t.Fatalf("«nomina»: %+v", hits)
	}
	if hits[1].Node.Name != "carpeta_777" || hits[1].Matches != 2 || hits[1].Node.Location() != `(sin ubicación)\` {
		t.Errorf("la carpeta sin nombre con 2 nóminas debe ir después: %s (%d)", hits[1].Node.Path, hits[1].Matches)
	}
	if hits[2].Node.Name != "Respaldo" || !strings.HasPrefix(hits[2].Node.Location(), `(sin ubicación)\carpeta_888`) {
		t.Errorf("tercera: %s", hits[2].Node.Path)
	}
	// Los archivos de «Nominas» no se repiten como «contiene archivos».
	for _, h := range hits[1:] {
		if h.Node.Name == "Empresa" {
			t.Error("Empresa no debe salir: su nómina ya está en la carpeta Nominas encontrada")
		}
	}
	if len(tr.FindFolders(ParseSearch("zzz"))) != 0 {
		t.Error("nada debe coincidir con zzz")
	}
}

func TestFolderReport(t *testing.T) {
	tr, _ := folderTestTree()
	emp := tr.Find(rootNames[rootPath] + "/Users/Ana/Documents/Empresa")
	rep := tr.FolderReport(emp, nil)
	for _, s := range []string{`Carpeta: \Users\Ana\Documents\Empresa`, "3 archivos", "1 subcarpeta\r",
		"Comprobado correcto", "Dañados: 1", "No recuperables: 1"} {
		if !strings.Contains(rep, s) {
			t.Errorf("falta %q en:\n%s", s, rep)
		}
	}
	st := tr.Stats(emp, tr.Twin(emp))
	if st.Sum.totalN() != 4 || st.Sum.Lost.N != 1 || st.Sum.Verified.N != 1 {
		t.Errorf("estadísticas: %+v", st.Sum)
	}
	// Carpetas marcadas enteras, para el resumen de lo recuperado.
	tr.SetNodeChecked(emp, true)
	nom := tr.Find(rootNames[rootPath] + "/Users/Ana/Pictures")
	tr.SetNodeChecked(nom, true)
	cf := tr.CheckedFolders()
	if len(cf) != 2 || cf[0] != emp || cf[1] != nom {
		t.Errorf("carpetas marcadas: %v", cf)
	}
	if txt := tr.FoldersText("CARPETAS", 50); !strings.Contains(txt, `Carpeta: \Users\Ana\Documents\Empresa`) || !strings.Contains(txt, `Carpeta: \Users\Ana\Pictures`) {
		t.Errorf("resumen de carpetas:\n%s", txt)
	}
	tr.SetChecked(emp.Files[0], false)
	if cf := tr.CheckedFolders(); len(cf) != 2 || cf[0].Name != "Nominas" {
		t.Errorf("al desmarcar un archivo de Empresa queda entera solo Nominas: %v", cf)
	}
	bob := tr.Find(rootNames[rootLost] + "/Users/Bob/Empresa")
	if rep := tr.FolderReport(bob, nil); !strings.Contains(rep, "Ninguno de estos archivos se puede recuperar") {
		t.Errorf("carpeta no recuperable:\n%s", rep)
	}
}

func TestPlanFolders(t *testing.T) {
	mk := func(rel string, size int64, overw int64) *Item {
		return &Item{Rel: rel, Rec: &FileRec{Size: size}, Overw: overw, Total: size}
	}
	p := &Plan{Items: []*Item{
		mk(dirFiles+"/Users/Ana/Empresa/a.xlsx", 100, 0),
		mk(dirFiles+"/Users/Ana/Empresa/Sub/b.xlsx", 200, 50),
		mk(dirLoose+"/carpeta_9/Empresa/c.docx", 10, 0),
		mk(dirFiles+"/Particion_2/Archivos/Empresa/d.txt", 1, 0), // carpeta «Archivos» del usuario
	}, Skipped: []SkipRow{{Rel: "Users/Ana/Empresa/e.pdf", Size: 7}}}
	fs := PlanFolders(p, ParseSearch("empresa"))
	if len(fs) != 3 {
		t.Fatalf("carpetas: %+v", fs)
	}
	if fs[0].Path != `\Users\Ana\Empresa` || fs[0].Files != 2 || fs[0].Partial != 1 || fs[0].Lost != 1 || fs[0].Bytes != 300 {
		t.Errorf("primera: %+v", fs[0])
	}
	if fs[1].Path != `(sin ubicación)\carpeta_9\Empresa` && fs[2].Path != `(sin ubicación)\carpeta_9\Empresa` {
		t.Errorf("falta la carpeta sin ubicación: %+v", fs)
	}
	found := false
	for _, f := range fs {
		found = found || f.Path == `\Archivos\Empresa`
	}
	if !found {
		t.Errorf("la carpeta del usuario «Archivos» no debe confundirse con la del programa: %+v", fs)
	}
}
