package main

import (
	"encoding/binary"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf16"
)

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }
func be16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func be64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// humanSize devuelve un tamaño legible (KB, MB, GB...).
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := "KMGTP"
	f := float64(n)
	i := -1
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %cB", f, units[i])
}

// humanDur devuelve una duración aproximada legible.
func humanDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min", (s+30)/60)
	default:
		return fmt.Sprintf("%d h %02d min", s/3600, (s%3600)/60)
	}
}

// groupDigits formatea un entero con separador de miles.
func groupDigits(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// utf16String convierte bytes UTF-16LE en string.
func utf16String(b []byte) string {
	n := len(b) / 2
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = le16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitizeName convierte un nombre NTFS en un nombre de archivo válido en
// Windows y en Linux.
func sanitizeName(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7F, r == 0xFFFD:
			sb.WriteByte('_')
		case strings.ContainsRune(`<>:"/\|?*`, r):
			sb.WriteByte('_')
		default:
			sb.WriteRune(r)
		}
	}
	out := strings.TrimRight(sb.String(), " .")
	if out == "" {
		out = "_"
	}
	base := out
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if reservedNames[strings.ToUpper(base)] {
		out = "_" + out
	}
	// Limitar longitud del componente (deja margen para sufijos).
	if r := []rune(out); len(r) > 180 {
		ext := path.Ext(out)
		if len([]rune(ext)) > 20 {
			ext = ""
		}
		out = string(r[:180-len([]rune(ext))]) + ext
	}
	return out
}

// extOf devuelve la extensión en minúsculas y sin punto.
func extOf(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || i == len(name)-1 {
		return ""
	}
	e := name[i+1:]
	if len(e) > 10 {
		return ""
	}
	return strings.ToLower(e)
}

// ftToTime convierte un FILETIME de Windows a time.Time.
func ftToTime(ft int64) time.Time {
	const epochDiff = 116444736000000000 // 1601 -> 1970 en unidades de 100 ns
	if ft <= epochDiff || ft > epochDiff+int64(250*365*24*3600)*10000000 {
		return time.Time{}
	}
	t := ft - epochDiff
	return time.Unix(t/10000000, (t%10000000)*100)
}

func allZero(b []byte) bool {
	for len(b) >= 8 {
		if le64(b) != 0 {
			return false
		}
		b = b[8:]
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
