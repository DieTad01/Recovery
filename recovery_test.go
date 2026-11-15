package main

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeRuns(t *testing.T) {
	// Tres tramos; el tercero retrocede (desplazamiento negativo).
	runs, ok := decodeRuns([]byte{0x11, 0x30, 0x60, 0x21, 0x10, 0x00, 0x01, 0x11, 0x20, 0xE0, 0x00})
	want := []Run{{0x60, 0x30}, {0x160, 0x10}, {0x140, 0x20}}
	if !ok || len(runs) != len(want) {
		t.Fatalf("runs = %v ok=%v", runs, ok)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Errorf("tramo %d = %+v, se esperaba %+v", i, runs[i], want[i])
		}
	}
	// Tramo disperso (sin desplazamiento).
	runs, ok = decodeRuns([]byte{0x01, 0x10, 0x21, 0x08, 0x34, 0x12, 0x00})
	if !ok || len(runs) != 2 || runs[0] != (Run{-1, 0x10}) || runs[1] != (Run{0x1234, 8}) {
		t.Errorf("disperso: %v ok=%v", runs, ok)
	}
	if _, ok := decodeRuns([]byte{0x31, 0x10}); ok {
		t.Error("una lista truncada debe marcarse como inválida")
	}
}

func TestFixup(t *testing.T) {
	rec := make([]byte, 1024)
	copy(rec, "FILE")
	rec[4], rec[6] = 0x30, 3 // USA en 0x30, 3 entradas
	rec[0x30], rec[0x31] = 0xAB, 0xCD
	rec[0x32], rec[0x33] = 0x11, 0x22 // bytes originales del final del sector 1
	rec[0x34], rec[0x35] = 0x33, 0x44 // ídem sector 2
	rec[510], rec[511] = 0xAB, 0xCD
	rec[1022], rec[1023] = 0xAB, 0xCD
	if !applyFixup(rec) {
		t.Fatal("fixup válido rechazado")
	}
	if rec[510] != 0x11 || rec[511] != 0x22 || rec[1022] != 0x33 || rec[1023] != 0x44 {
		t.Error("fixup mal aplicado")
	}
	rec[1022] = 0 // registro "roto": el segundo sector no lleva la marca
	rec[510], rec[511] = 0xAB, 0xCD
	if applyFixup(rec) {
		t.Error("un registro incompleto debe rechazarse")
	}
}

// lznt1Compress es un compresor LZNT1 sencillo, solo para las pruebas.
func lznt1Compress(in []byte) []byte {
	var out []byte
	for len(in) > 0 {
		chunk := in
		if len(chunk) > 4096 {
			chunk = chunk[:4096]
		}
		in = in[len(chunk):]
		var body []byte
		pos := 0
		for pos < len(chunk) {
			flagAt := len(body)
			body = append(body, 0)
			for bit := uint(0); bit < 8 && pos < len(chunk); bit++ {
				lenBits := uint(12)
				for q := pos - 1; q >= 0x10; q >>= 1 {
					lenBits--
				}
				maxLen := 1<<lenBits + 2
				maxDisp := 1 << (16 - lenBits)
				bestL, bestD := 0, 0
				for d := 1; d <= maxDisp && d <= pos; d++ {
					l := 0
					for pos+l < len(chunk) && l < maxLen && chunk[pos+l] == chunk[pos+l-d] {
						l++
					}
					if l > bestL {
						bestL, bestD = l, d
					}
				}
				if bestL >= 3 {
					tok := uint16((bestD-1)<<lenBits | (bestL - 3))
					body = append(body, byte(tok), byte(tok>>8))
					body[flagAt] |= 1 << bit
					pos += bestL
				} else {
					body = append(body, chunk[pos])
					pos++
				}
			}
		}
		if len(body) >= 4096 && len(chunk) == 4096 {
			out = append(out, 0xFF, 0x3F) // bloque sin comprimir
			out = append(out, chunk...)
		} else {
			hdr := uint16(0xB000 | (len(body) - 1))
			out = append(out, byte(hdr), byte(hdr>>8))
			out = append(out, body...)
		}
	}
	return out
}

func TestLZNT1(t *testing.T) {
	// Vector hecho a mano: 16 veces 'a' = literal + copia de 15 bytes.
	out := make([]byte, 16)
	lznt1Decompress([]byte{0x03, 0xB0, 0x02, 'a', 0x0C, 0x00}, out)
	if !bytes.Equal(out, bytes.Repeat([]byte{'a'}, 16)) {
		t.Fatalf("vector simple: %q", out)
	}
	rnd := rand.New(rand.NewSource(1))
	for round := 0; round < 20; round++ {
		data := make([]byte, 65536)
		for i := 0; i < len(data); {
			if rnd.Intn(3) == 0 {
				n := rnd.Intn(300) + 1
				for j := 0; j < n && i < len(data); j++ {
					data[i] = byte(rnd.Intn(256))
					i++
				}
			} else {
				n, b := rnd.Intn(2000)+3, byte(rnd.Intn(4))
				for j := 0; j < n && i < len(data); j++ {
					data[i] = b + byte(j%3)
					i++
				}
			}
		}
		comp := lznt1Compress(data)
		got := make([]byte, len(data))
		lznt1Decompress(comp, got)
		if !bytes.Equal(got, data) {
			t.Fatalf("ronda %d: la descompresión no reproduce el original", round)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"informe:final?.docx": "informe_final_.docx",
		"CON":                 "_CON",
		"nul.txt":             "_nul.txt",
		"punto final. ":       "punto final",
		"":                    "_",
		"Año nuevo ñ.jpg":     "Año nuevo ñ.jpg",
		"a/b\\c":              "a_b_c",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, se esperaba %q", in, got, want)
		}
	}
	long := sanitizeName(string(bytes.Repeat([]byte("x"), 400)) + ".jpeg")
	if n := len([]rune(long)); n > 180 || filepath.Ext(long) != ".jpeg" {
		t.Errorf("nombre largo mal recortado: %d runas, ext %q", n, filepath.Ext(long))
	}
}

func TestSystemFilter(t *testing.T) {
	sys := [][3]interface{}{
		{"Windows/System32", true, "kernel32.dll"},
		{"Users/Ana/AppData/Local/Temp", true, "x.tmp"},
		{"Windows.old/Program Files/App", true, "app.exe"},
		{"", true, "pagefile.sys"},
		{"carpeta_123", false, "controlador.sys"},
	}
	for _, c := range sys {
		if !isSystemFile(c[0].(string), c[1].(bool), c[2].(string)) {
			t.Errorf("debería considerarse de sistema: %v", c)
		}
	}
	user := [][3]interface{}{
		{"Users/Ana/Documents", true, "tesis.docx"},
		{"Users/Ana/Desktop", true, "juego.exe"},
		{"Windows.old/Users/Ana/Pictures", true, "foto.jpg"},
		{"carpeta_123/Vacaciones", false, "video.mp4"},
		{"", true, "notas.txt"},
	}
	for _, c := range user {
		if isSystemFile(c[0].(string), c[1].(bool), c[2].(string)) {
			t.Errorf("no debería considerarse de sistema: %v", c)
		}
	}
}

func TestMP3Frame(t *testing.T) {
	// MPEG-1 capa III, 128 kbps, 44,1 kHz, sin relleno -> 417 bytes.
	if l, _ := mp3Frame([]byte{0xFF, 0xFB, 0x90, 0x00}); l != 417 {
		t.Errorf("longitud de trama = %d, se esperaba 417", l)
	}
	if l, _ := mp3Frame([]byte{0xFF, 0xFB, 0xF0, 0x00}); l != 0 {
		t.Error("una tasa de bits inválida debe rechazarse")
	}
}

func TestIntervals(t *testing.T) {
	v := mergeIvs([]iv{{100, 200}, {150, 300}, {500, 600}, {10, 20}})
	if len(v) != 3 || v[1] != (iv{100, 300}) {
		t.Fatalf("fusión incorrecta: %v", v)
	}
	if !ivContains(v, 150) || ivContains(v, 300) || ivContains(v, 5) || !ivContains(v, 599) {
		t.Error("ivContains incorrecto")
	}
	if !ivOverlaps(v, 290, 400) || ivOverlaps(v, 300, 500) {
		t.Error("ivOverlaps incorrecto")
	}
	if n := ivOverlapBytes(v, 0, 1000); n != 10+200+100 {
		t.Errorf("ivOverlapBytes = %d", n)
	}
	if n := ivOverlapBytes(v, 250, 550); n != 50+50 {
		t.Errorf("ivOverlapBytes parcial = %d", n)
	}
}

func TestCheckSig(t *testing.T) {
	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 28)...)
	if checkSig("jpg", jpg) != 1 || checkSig("png", jpg) != -1 || checkSig("txt", jpg) != 0 {
		t.Error("checkSig incorrecto")
	}
	if guessExt(jpg) != "jpg" {
		t.Errorf("guessExt = %q", guessExt(jpg))
	}
}

// TestCarvers coloca archivos generados con la biblioteca estándar en una
// imagen cruda rodeados de datos aleatorios y comprueba que se extraen con
// su tamaño exacto.
func TestCarvers(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for i := range img.Pix {
		img.Pix[i] = byte(rnd.Intn(256))
	}
	files := map[string][]byte{}
	var b bytes.Buffer
	png.Encode(&b, img)
	files["png"] = append([]byte(nil), b.Bytes()...)
	b.Reset()
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	files["jpg"] = append([]byte(nil), b.Bytes()...)
	b.Reset()
	pal := image.NewPaletted(image.Rect(0, 0, 64, 64), color.Palette{color.Black, color.White, color.RGBA{255, 0, 0, 255}})
	for i := range pal.Pix {
		pal.Pix[i] = byte(rnd.Intn(3))
	}
	gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{5, 5}})
	files["gif"] = append([]byte(nil), b.Bytes()...)
	b.Reset()
	zw := zip.NewWriter(&b)
	for _, n := range []string{"[Content_Types].xml", "word/document.xml", "word/media/img.bin"} {
		w, _ := zw.Create(n)
		junk := make([]byte, 9000)
		rnd.Read(junk)
		w.Write(junk)
	}
	zw.Close()
	files["docx"] = append([]byte(nil), b.Bytes()...)

	path := filepath.Join(t.TempDir(), "crudo.img")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	junk := make([]byte, 64<<10)
	offs := map[string]int64{}
	var pos int64
	write := func(p []byte) { f.Write(p); pos += int64(len(p)) }
	for _, ext := range []string{"png", "jpg", "gif", "docx"} {
		rnd.Read(junk)
		write(junk)
		offs[ext] = pos
		write(files[ext])
		write(make([]byte, (4096-len(files[ext])%4096)%4096))
	}
	rnd.Read(junk)
	write(junk)
	f.Close()

	dev, err := OpenDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	s := &Scan{Dev: dev}
	s.Prepare(nil)
	s.ScanDevice(nil)
	found := map[int64]Cand{}
	for _, c := range s.Cands {
		found[c.Off] = c
	}
	sr := newSrc(dev)
	for ext, off := range offs {
		c, ok := found[off]
		if !ok {
			t.Errorf("%s: no se detectó la firma en %d", ext, off)
			continue
		}
		res, ok := carvers[c.Kind](sr, off, dev.Size-off)
		if !ok || res.size != int64(len(files[ext])) || res.ext != ext {
			t.Errorf("%s: resultado %+v ok=%v, se esperaba tamaño %d", ext, res, ok, len(files[ext]))
		}
	}
	if len(s.Cands) != len(offs) {
		t.Errorf("se detectaron %d firmas, se esperaban %d (falsos positivos en datos aleatorios)", len(s.Cands), len(offs))
	}
}
