package main

// Búsqueda de direcciones de correo en el disco. Tras restaurar un equipo,
// la antigua dirección de Hotmail/Outlook/Gmail del usuario suele seguir
// escrita muchas veces en el espacio libre (perfil anterior de Windows,
// Outlook, caché del navegador, autocompletar...). Esto NO entra en la
// cuenta ni recupera contraseñas: solo encuentra la dirección para poder
// recuperar la cuenta después en la web del proveedor.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Proveedores de correo personal que se destacan en el informe.
var mailProviders = map[string]string{
	"hotmail": "Microsoft (Hotmail)", "outlook": "Microsoft (Outlook)",
	"live": "Microsoft (Live)", "msn": "Microsoft (MSN)", "passport": "Microsoft",
	"windowslive": "Microsoft", "hotmail.es": "Microsoft (Hotmail)",
	"gmail": "Google (Gmail)", "googlemail": "Google (Gmail)",
	"yahoo": "Yahoo", "ymail": "Yahoo", "rocketmail": "Yahoo",
	"icloud": "Apple (iCloud)", "me": "Apple", "mac": "Apple",
	"proton": "Proton", "protonmail": "Proton", "aol": "AOL",
	"gmx": "GMX", "zoho": "Zoho",
}

// providerOf clasifica un dominio en un proveedor conocido, o "".
func providerOf(domain string) string {
	d := strings.ToLower(domain)
	if p, ok := mailProviders[d]; ok {
		return p
	}
	// La segunda etiqueta desde el final (hotmail en hotmail.com, hotmail.es).
	parts := strings.Split(d, ".")
	if len(parts) >= 2 {
		if p, ok := mailProviders[parts[len(parts)-2]]; ok {
			return p
		}
	}
	return ""
}

func isLocalByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '.' || c == '_' || c == '%' || c == '+' || c == '-'
}

func isDomByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '.' || c == '-'
}

// validDomain comprueba que el dominio tenga forma real (etiquetas válidas
// y un TLD de solo letras). Así se descartan casi todos los falsos
// positivos que aparecerían en datos binarios al azar.
func validDomain(d string) bool {
	if len(d) < 4 || len(d) > 255 || d[0] == '.' || d[0] == '-' || strings.Contains(d, "..") {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 || len(tld) > 24 {
		return false
	}
	for i := 0; i < len(tld); i++ {
		if !(tld[i] >= 'a' && tld[i] <= 'z' || tld[i] >= 'A' && tld[i] <= 'Z') {
			return false
		}
	}
	for _, l := range labels {
		if l == "" || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
	}
	return true
}

// extractEmail intenta leer una dirección de correo cuyo '@' está en at.
// Devuelve la dirección en minúsculas y el índice del byte siguiente, o "".
func extractEmail(b []byte, at int) (string, int) {
	// Parte local hacia la izquierda.
	i := at
	for i > 0 && isLocalByte(b[i-1]) {
		i--
	}
	local := b[i:at]
	if len(local) == 0 || len(local) > 64 || local[0] == '.' || local[len(local)-1] == '.' {
		return "", at + 1
	}
	// Dominio hacia la derecha.
	j := at + 1
	for j < len(b) && isDomByte(b[j]) {
		j++
	}
	domain := b[at+1 : j]
	// Quita puntos o guiones finales que no forman parte del dominio.
	for len(domain) > 0 && (domain[len(domain)-1] == '.' || domain[len(domain)-1] == '-') {
		domain = domain[:len(domain)-1]
		j--
	}
	if !validDomain(string(domain)) {
		return "", at + 1
	}
	return strings.ToLower(string(local) + "@" + string(domain)), j
}

const emailOverlap = 512 // un correo puede quedar partido entre dos bloques

// ScanEmails recorre todo el dispositivo (sin saltarse nada) contando las
// direcciones de correo. Devuelve el recuento por dirección.
func (s *Scan) ScanEmails(progressFn func(pos int64, distinct int)) map[string]int {
	counts := map[string]int{}
	const maxDistinct = 300000
	buf := make([]byte, scanChunk+emailOverlap)
	size := s.Dev.Size
	var pos int64
	for pos < size {
		if stopRequested() {
			break
		}
		n, _ := s.Dev.ReadAt(buf, pos)
		if n <= 0 {
			break
		}
		// No se procesa la zona de solapamiento del final (se verá al
		// principio del siguiente bloque), salvo en el último trozo.
		limit := n
		if pos+int64(n) < size && limit > scanChunk {
			limit = scanChunk
		}
		data := buf[:n]
		for k := 0; k < limit; k++ {
			if data[k] != '@' {
				continue
			}
			addr, next := extractEmail(data, k)
			if addr != "" {
				if _, ok := counts[addr]; ok || len(counts) < maxDistinct {
					counts[addr]++
				}
				k = next - 1
			}
		}
		pos += int64(limit)
		if progressFn != nil {
			progressFn(pos, len(counts))
		}
	}
	return counts
}

type mailRow struct {
	Addr     string
	N        int
	Provider string
}

// sortMail ordena las direcciones: primero las de proveedores conocidos,
// luego por número de apariciones.
func sortMail(counts map[string]int) []mailRow {
	rows := make([]mailRow, 0, len(counts))
	for a, n := range counts {
		dom := a[strings.IndexByte(a, '@')+1:]
		rows = append(rows, mailRow{a, n, providerOf(dom)})
	}
	sort.Slice(rows, func(i, j int) bool {
		pi, pj := rows[i].Provider != "", rows[j].Provider != ""
		if pi != pj {
			return pi
		}
		if rows[i].N != rows[j].N {
			return rows[i].N > rows[j].N
		}
		return rows[i].Addr < rows[j].Addr
	})
	return rows
}

// writeEmails guarda correos_encontrados.txt y devuelve las direcciones de
// proveedores de correo personal (las más interesantes para el usuario).
func writeEmails(dir string, counts map[string]int) ([]mailRow, error) {
	rows := sortMail(counts)
	f, err := os.Create(filepath.Join(dir, "correos_encontrados.txt"))
	if err != nil {
		return nil, err
	}
	bw := bufio.NewWriter(f)
	bw.WriteString("\xEF\xBB\xBF") // BOM para que el Bloc de notas muestre bien los acentos
	fmt.Fprintf(bw, "Direcciones de correo encontradas en el disco\r\n")
	fmt.Fprintf(bw, "Generado por Recovery %s el %s\r\n", version, time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(bw, "Total de direcciones distintas: %d\r\n", len(rows))
	fmt.Fprintf(bw, strings.Repeat("=", 60)+"\r\n\r\n")

	var personal []mailRow
	for _, r := range rows {
		if r.Provider != "" {
			personal = append(personal, r)
		}
	}
	fmt.Fprintf(bw, "CORREOS DE PROVEEDORES PERSONALES (los más probables)\r\n")
	fmt.Fprintf(bw, "Ordenados por número de apariciones en el disco.\r\n\r\n")
	if len(personal) == 0 {
		fmt.Fprintf(bw, "  (no se encontró ninguno)\r\n")
	}
	for _, r := range personal {
		fmt.Fprintf(bw, "  %-45s  %8d veces   %s\r\n", r.Addr, r.N, r.Provider)
	}
	fmt.Fprintf(bw, "\r\n"+strings.Repeat("-", 60)+"\r\n")
	fmt.Fprintf(bw, "TODAS LAS DEMÁS DIRECCIONES\r\n\r\n")
	for _, r := range rows {
		if r.Provider == "" {
			fmt.Fprintf(bw, "  %-45s  %8d veces\r\n", r.Addr, r.N)
		}
	}
	err = bw.Flush()
	if e := f.Close(); err == nil {
		err = e
	}
	return personal, err
}
