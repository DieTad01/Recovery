package main

import "strings"

// Firmas conocidas por extensión: sirven para comprobar que el contenido
// recuperado corresponde realmente al tipo de archivo que dice su nombre.

type sigDef struct {
	off   int
	magic string
}

var sigGroups = []struct {
	exts string
	sigs []sigDef
	kind uint8
}{
	{"jpg jpeg jpe jfif", []sigDef{{0, "\xFF\xD8\xFF"}}, kJPEG},
	{"png", []sigDef{{0, "\x89PNG\r\n\x1a\n"}}, kPNG},
	{"gif", []sigDef{{0, "GIF8"}}, kGIF},
	{"bmp dib", []sigDef{{0, "BM"}}, kBMP},
	{"tif tiff dng nef cr2 arw pef sr2 nrw", []sigDef{{0, "II*\x00"}, {0, "MM\x00*"}}, kTIFF},
	{"orf", []sigDef{{0, "IIRO"}, {0, "IIRS"}, {0, "II*\x00"}}, kTIFF},
	{"rw2", []sigDef{{0, "IIU\x00"}}, kTIFF},
	{"psd psb", []sigDef{{0, "8BPS"}}, kPSD},
	{"pdf", []sigDef{{0, "%PDF"}}, kPDF},
	{"ai", []sigDef{{0, "%PDF"}, {0, "%!PS"}}, 0},
	{"zip docx xlsx pptx docm xlsm pptm dotx xltx potx ppsx odt ods odp odg jar apk epub vsdx kmz cbz xpi ipa",
		[]sigDef{{0, "PK\x03\x04"}, {0, "PK\x05\x06"}}, kZIP},
	{"doc xls ppt pps dot xlt msi msg vsd pub mpp", []sigDef{{0, "\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1"}}, kOLE},
	{"rtf", []sigDef{{0, "{\\rtf"}}, kRTF},
	{"mp4 m4v m4a m4b 3gp 3g2 heic heif avif cr3 f4v", []sigDef{{4, "ftyp"}}, kBMFF},
	{"mov qt", []sigDef{{4, "ftyp"}, {4, "moov"}, {4, "mdat"}, {4, "wide"}, {4, "free"}, {4, "skip"}, {4, "pnot"}}, kBMFF},
	{"avi wav webp ani", []sigDef{{0, "RIFF"}}, kRIFF},
	{"mkv webm mka mk3d", []sigDef{{0, "\x1A\x45\xDF\xA3"}}, kMKV},
	{"wmv wma asf", []sigDef{{0, "\x30\x26\xB2\x75\x8E\x66\xCF\x11"}}, kASF},
	{"ogg oga ogv opus spx", []sigDef{{0, "OggS"}}, kOGG},
	{"flac", []sigDef{{0, "fLaC"}, {0, "ID3"}}, 0},
	{"rar", []sigDef{{0, "Rar!\x1a\x07"}}, kRAR},
	{"7z", []sigDef{{0, "7z\xBC\xAF\x27\x1C"}}, k7Z},
	{"gz tgz", []sigDef{{0, "\x1F\x8B"}}, 0},
	{"bz2", []sigDef{{0, "BZh"}}, 0},
	{"xz", []sigDef{{0, "\xFD7zXZ"}}, 0},
	{"cab", []sigDef{{0, "MSCF"}}, 0},
	{"exe dll sys mui ocx scr cpl efi drv ax", []sigDef{{0, "MZ"}}, 0},
	{"lnk", []sigDef{{0, "L\x00\x00\x00\x01\x14\x02\x00"}}, 0},
	{"ico", []sigDef{{0, "\x00\x00\x01\x00"}}, 0},
	{"ttf", []sigDef{{0, "\x00\x01\x00\x00"}, {0, "true"}}, 0},
	{"otf", []sigDef{{0, "OTTO"}}, 0},
	{"sqlite sqlite3", []sigDef{{0, "SQLite format 3\x00"}}, kSQLITE},
	{"pst ost", []sigDef{{0, "!BDN"}}, kPST},
	{"class", []sigDef{{0, "\xCA\xFE\xBA\xBE"}}, 0},
	{"swf", []sigDef{{0, "FWS"}, {0, "CWS"}, {0, "ZWS"}}, 0},
	{"flv", []sigDef{{0, "FLV\x01"}}, 0},
	{"mid midi", []sigDef{{0, "MThd"}}, 0},
	{"wim esd", []sigDef{{0, "MSWIM\x00\x00\x00"}}, 0},
	{"evtx", []sigDef{{0, "ElfFile\x00"}}, 0},
	{"xcf", []sigDef{{0, "gimp xcf"}}, 0},
	{"blend", []sigDef{{0, "BLENDER"}}, 0},
	{"dwg", []sigDef{{0, "AC10"}}, 0},
	{"woff", []sigDef{{0, "wOFF"}}, 0},
	{"woff2", []sigDef{{0, "wOF2"}}, 0},
}

var (
	sigByExt  = map[string][]sigDef{}
	kindByExt = map[string]uint8{}
)

func init() {
	for _, g := range sigGroups {
		for _, e := range strings.Fields(g.exts) {
			sigByExt[e] = g.sigs
			if g.kind != 0 {
				kindByExt[e] = g.kind
			}
		}
	}
	kindByExt["mp3"] = kMP3
	kindByExt["ts"] = kTS
	kindByExt["mts"] = kM2TS
	kindByExt["m2ts"] = kM2TS
}

// sigKnown indica si se conoce una firma para esa extensión.
func sigKnown(ext string) bool {
	switch ext {
	case "mp3", "mpg", "mpeg", "vob", "ts", "mts", "m2ts":
		return true
	}
	_, ok := sigByExt[ext]
	return ok
}

// checkSig compara el inicio de un archivo con la firma esperada para su
// extensión: 1 = coincide, -1 = no coincide, 0 = tipo sin firma conocida.
func checkSig(ext string, head []byte) int {
	if len(head) < 12 {
		return 0
	}
	switch ext {
	case "mp3":
		if string(head[:3]) == "ID3" || (head[0] == 0xFF && head[1]&0xE0 == 0xE0) || string(head[:4]) == "RIFF" {
			return 1
		}
		return -1
	case "mpg", "mpeg", "vob":
		if head[0] == 0 && head[1] == 0 && head[2] == 1 && (head[3] == 0xBA || head[3] == 0xB3) {
			return 1
		}
		return -1
	case "ts", "mts", "m2ts":
		if len(head) >= 400 { // varios paquetes seguidos: 0x47 cada 188 o 192 bytes
			if head[0] == 0x47 && head[188] == 0x47 && head[376] == 0x47 ||
				head[4] == 0x47 && head[196] == 0x47 && head[388] == 0x47 {
				return 1
			}
		} else if (head[0] == 0x47 || head[4] == 0x47) && !isPrintable(head) {
			return 1
		}
		if ext == "ts" && isPrintable(head) {
			return 0 // .ts de TypeScript (código fuente)
		}
		return -1
	case "pub": // también son claves públicas SSH (texto)
		if strings.HasPrefix(string(head), "ssh-") || strings.HasPrefix(string(head), "ecdsa-") ||
			strings.HasPrefix(string(head), "-----BEGIN") {
			return 1
		}
	}
	sigs, ok := sigByExt[ext]
	if !ok {
		return 0
	}
	for _, sg := range sigs {
		if sg.off+len(sg.magic) <= len(head) && string(head[sg.off:sg.off+len(sg.magic)]) == sg.magic {
			return 1
		}
	}
	return -1
}

// guessExt intenta deducir la extensión a partir del contenido.
func guessExt(head []byte) string {
	if len(head) < 12 {
		return ""
	}
	for _, e := range []string{"jpg", "png", "gif", "pdf", "zip", "doc", "rtf", "mp4", "avi", "mkv", "wmv",
		"ogg", "flac", "rar", "7z", "gz", "psd", "tif", "exe", "sqlite", "pst", "mp3", "bmp"} {
		if checkSig(e, head) == 1 {
			if e == "avi" {
				switch string(head[8:12]) {
				case "WAVE":
					return "wav"
				case "WEBP":
					return "webp"
				}
			}
			if e == "bmp" && (le32(head[6:]) != 0 || le32(head[2:]) < 54) {
				continue
			}
			return e
		}
	}
	return ""
}

// Categorías para el resumen.
var catByExt = map[string]string{}

var catOrder = []string{"Fotos e imágenes", "Videos", "Audio", "Documentos", "Comprimidos", "Otros"}

func init() {
	add := func(cat, exts string) {
		for _, e := range strings.Fields(exts) {
			catByExt[e] = cat
		}
	}
	add("Fotos e imágenes", "jpg jpeg jpe jfif png gif bmp tif tiff heic heif avif webp psd psb raw dng nef cr2 cr3 arw orf rw2 pef sr2 svg ico xcf ai eps cdr")
	add("Videos", "mp4 m4v mov avi mkv webm wmv asf flv mpg mpeg vob mts m2ts ts 3gp 3g2 f4v ogv wfp wve prproj veg")
	add("Audio", "mp3 wav m4a m4b wma flac ogg oga opus aac aiff aif mid midi amr")
	add("Documentos", "pdf doc docx docm xls xlsx xlsm ppt pptx pptm pps ppsx odt ods odp rtf txt csv epub pub vsd vsdx mpp msg pst ost one xml html htm md tex json")
	add("Comprimidos", "zip rar 7z gz tgz bz2 xz tar iso cab")
}

func category(ext string) string {
	if c, ok := catByExt[ext]; ok {
		return c
	}
	return "Otros"
}

// isPrintable indica si los bytes parecen texto (sin bytes de control raros).
func isPrintable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}
