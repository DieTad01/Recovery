package main

import (
	"strings"
	"unicode/utf8"
)

// Búsqueda de archivos y carpetas por nombre entre lo que se puede
// recuperar. Admite varios términos separados por comas o punto y coma:
//
//	tesis              el nombre o alguna carpeta de la ruta contiene «tesis»
//	*.docx             archivos que terminan en .docx (comodines * y ?)
//	Proyecto*          carpetas o archivos que empiezan por «Proyecto»
//	Documents/Trabajo  fragmento de ruta
//
// No distingue mayúsculas ni acentos («mama» encuentra «Mamá»).

type searchPat struct {
	s    string // texto normalizado
	wild bool   // contiene * o ?
	path bool   // contiene una barra: se compara con la ruta completa
}

// Matcher decide si un archivo coincide con la búsqueda.
type Matcher struct {
	Raw  string
	pats []searchPat
}

var foldReplacer = strings.NewReplacer(
	"á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ë", "e", "ê", "e",
	"í", "i", "ì", "i", "ï", "i", "î", "i",
	"ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o",
	"ú", "u", "ù", "u", "ü", "u", "û", "u",
	"ñ", "n", "ç", "c", `\`, "/",
)

// fold pasa a minúsculas, quita acentos y unifica las barras.
func fold(s string) string { return foldReplacer.Replace(strings.ToLower(s)) }

// ParseSearch interpreta el texto de búsqueda; devuelve nil si está vacío.
func ParseSearch(text string) *Matcher {
	m := &Matcher{Raw: strings.TrimSpace(text)}
	for _, t := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		t = fold(strings.Trim(strings.TrimSpace(t), `"`))
		t = strings.Trim(t, "/")
		if t == "" {
			continue
		}
		m.pats = append(m.pats, searchPat{
			s:    t,
			wild: strings.ContainsAny(t, "*?"),
			path: strings.Contains(t, "/"),
		})
	}
	if len(m.pats) == 0 {
		return nil
	}
	return m
}

// Match indica si el archivo (carpeta + nombre) coincide con algún término.
func (m *Matcher) Match(dir, name string) bool {
	full := fold(name)
	if dir != "" {
		full = fold(dir) + "/" + full
	}
	for _, p := range m.pats {
		switch {
		case p.wild && p.path:
			if wildMatch(p.s, full) {
				return true
			}
		case p.wild:
			for _, comp := range strings.Split(full, "/") {
				if wildMatch(p.s, comp) {
					return true
				}
			}
		default:
			if strings.Contains(full, p.s) {
				return true
			}
		}
	}
	return false
}

// MatchExt indica si un archivo sin nombre (recuperado por su contenido)
// de esa extensión coincide: solo con términos del tipo «*.jpg» o «pdf».
func (m *Matcher) MatchExt(ext string) bool {
	ext = fold(ext)
	for _, p := range m.pats {
		if p.path {
			continue
		}
		if p.wild && wildMatch(p.s, "archivo."+ext) && !strings.HasPrefix(p.s, "archivo") {
			// «*.jpg», «*.jp*», «*»... pero no «foto*.jpg», que exige un nombre.
			if strings.HasPrefix(p.s, "*.") || p.s == "*" {
				return true
			}
		}
		if !p.wild && strings.TrimPrefix(p.s, ".") == ext {
			return true
		}
	}
	return false
}

// CanMatchCarved indica si la búsqueda puede incluir archivos sin nombre.
func (m *Matcher) CanMatchCarved() bool {
	for _, k := range kindNames {
		for _, e := range strings.Split(k, "/") {
			if e != "" && m.MatchExt(e) {
				return true
			}
		}
	}
	for _, e := range []string{"jpg", "png", "pdf", "docx", "xlsx", "pptx", "doc", "xls", "mp4", "mov", "avi", "mp3", "zip"} {
		if m.MatchExt(e) {
			return true
		}
	}
	return false
}

// wildMatch compara con comodines: * (cualquier texto) y ? (un carácter).
func wildMatch(pat, s string) bool {
	px, sx := 0, 0
	nextPx, nextSx := -1, -1
	for px < len(pat) || sx < len(s) {
		if px < len(pat) {
			switch pat[px] {
			case '*':
				nextPx, nextSx = px, sx+1
				px++
				continue
			case '?':
				if sx < len(s) {
					_, w := utf8.DecodeRuneInString(s[sx:])
					px++
					sx += w
					continue
				}
			default:
				if sx < len(s) && s[sx] == pat[px] {
					px++
					sx++
					continue
				}
			}
		}
		if nextSx > 0 && nextSx <= len(s) {
			px, sx = nextPx, nextSx
			// avanzar una runa completa
			if sx-1 < len(s) {
				_, w := utf8.DecodeRuneInString(s[sx-1:])
				sx = sx - 1 + w
				nextSx = sx
			}
			continue
		}
		return false
	}
	return true
}

// ---------------------------------------------------------------- carpetas

// Puntuación de una carpeta frente a la búsqueda (mayor = más parecida).
const (
	fsNone   = iota
	fsSub    // el nombre contiene el término
	fsPrefix // el nombre empieza por el término
	fsWild   // coincide con los comodines, o con el fragmento de ruta
	fsExact  // el nombre es exactamente el término
)

// FolderScore compara una carpeta con la búsqueda. rel es su ruta (con
// «/»), que se usa con los términos que llevan barra; name, su nombre.
func (m *Matcher) FolderScore(rel, name string) int {
	n, full := fold(name), fold(rel)
	best := fsNone
	for _, p := range m.pats {
		s := fsNone
		switch {
		case p.path && p.wild:
			if wildMatch(p.s, full) || wildMatch("*/"+p.s, full) {
				s = fsWild
			}
		case p.path:
			// El fragmento debe terminar en esta carpeta (no en una superior).
			if i := strings.LastIndex(full, p.s); i >= 0 && !strings.Contains(full[i+len(p.s):], "/") {
				s = fsSub
				if i+len(p.s) == len(full) && (i == 0 || full[i-1] == '/') {
					s = fsWild
				}
			}
		case p.wild:
			if wildMatch(p.s, n) {
				s = fsWild
			}
		case n == p.s:
			s = fsExact
		case strings.HasPrefix(n, p.s):
			s = fsPrefix
		case strings.Contains(n, p.s):
			s = fsSub
		}
		if s > best {
			best = s
		}
	}
	return best
}

// FolderOf devuelve la carpeta de la ruta dir (la más alta) cuyo nombre es
// exactamente uno de los términos o coincide con sus comodines, o "" si
// el archivo no está dentro de ninguna.
func (m *Matcher) FolderOf(dir string) string {
	if dir == "" {
		return ""
	}
	comps := strings.Split(dir, "/")
	for i, c := range comps {
		if syntheticDir(c) {
			continue
		}
		rel := strings.Join(comps[:i+1], "/")
		if m.FolderScore(rel, c) >= fsWild {
			return rel
		}
	}
	return ""
}

// InFolder indica si un archivo de la carpeta dir está dentro (a cualquier
// profundidad) de una carpeta buscada.
func (m *Matcher) InFolder(dir string) bool { return m.FolderOf(dir) != "" }

// syntheticDir indica si el nombre lo puso el programa (la carpeta original
// ya no tenía nombre): no se compara con la búsqueda.
func syntheticDir(name string) bool {
	if name == "sin_nombre" || name == lostPrefix+"bucle" {
		return true
	}
	if !strings.HasPrefix(name, lostPrefix) || len(name) == len(lostPrefix) {
		return false
	}
	for _, r := range name[len(lostPrefix):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
