package main

import "testing"

func TestParseDiskJSON(t *testing.T) {
	// Formato con textos (lo habitual en Windows 10/11).
	txt := `[{"Name":"Samsung SSD 870 EVO 1TB","Media":"SSD","Bus":"SATA","Size":1000204886016,
	  "Health":"Healthy","Op":["OK"],"Temp":38,"TempMax":70,"Wear":3,"Hours":5210,
	  "ReadErr":0,"WriteErr":0,"ReadUncorr":0},
	 {"Name":"WDC WD10EZEX","Media":"HDD","Bus":"SATA","Size":1000204886016,"Health":"Warning",
	  "Op":["Predictive Failure"],"Temp":49,"TempMax":null,"Wear":null,"Hours":41000,
	  "ReadErr":12,"WriteErr":0,"ReadUncorr":7}]`
	ds, err := parseDiskJSON([]byte(txt))
	if err != nil || len(ds) != 2 {
		t.Fatalf("err=%v n=%d", err, len(ds))
	}
	if ds[0].Verdict != "Bueno" || ds[0].Media != "SSD" || ds[0].Temp != 38 || ds[0].Wear != 3 {
		t.Errorf("SSD sano mal interpretado: %+v", ds[0])
	}
	if ds[1].Verdict != "Mal" || ds[1].Wear != -1 || ds[1].ReadUncor != 7 {
		t.Errorf("disco con fallo previsto mal interpretado: %+v", ds[1])
	}
	// Formato numérico (enumeraciones sin traducir) y un solo objeto.
	num := "\uFEFF" + `{"Name":"KINGSTON NVMe","Media":4,"Bus":17,"Size":512110190592,"Health":1,
	  "Op":[2],"Temp":0,"Wear":82,"Hours":null,"ReadUncorr":0}`
	ds, err = parseDiskJSON([]byte(num))
	if err != nil || len(ds) != 1 {
		t.Fatalf("err=%v", err)
	}
	d := ds[0]
	if d.Media != "SSD" || d.Bus != "NVMe" || d.Health != "Advertencia" || d.Op != "Correcto" {
		t.Errorf("enumeraciones: %+v", d)
	}
	if d.Temp != -1 || d.Hours != -1 || d.Verdict != "Atención" {
		t.Errorf("valores desconocidos o veredicto: %+v", d)
	}
	if rows := DiskRows(ds); len(rows) != 1 || rows[0][5] != "—" || rows[0][6] != "82 %" {
		t.Errorf("filas: %v", rows)
	}
	if _, err := parseDiskJSON([]byte("  ")); err == nil {
		t.Error("una respuesta vacía debe dar error")
	}
}

func TestPreviewDecode(t *testing.T) {
	if s := decodeText([]byte("Hola\r\nAño ñandú"), 100); s != "Hola\r\nAño ñandú" {
		t.Errorf("UTF-8: %q", s)
	}
	if s := decodeText([]byte{0xFF, 0xFE, 'H', 0, 'i', 0}, 100); s != "Hi" {
		t.Errorf("UTF-16: %q", s)
	}
	if s := decodeText([]byte{'a', 0xF1, 'o'}, 100); s != "año" {
		t.Errorf("Windows-1252: %q", s)
	}
	if p := makePreview([]byte("no es una imagen"), "jpg", 100, 100); p.pix != nil || p.msg == "" {
		t.Error("una imagen dañada debe dar un mensaje")
	}
}
