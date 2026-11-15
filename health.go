package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// DiskHealth es el estado de un disco físico.
type DiskHealth struct {
	Name, Media, Bus, Health, Op string
	Size                         int64
	Temp, TempMax, Wear          int   // -1 = desconocido
	Hours                        int64 // -1 = desconocido
	ReadErr, WriteErr, ReadUncor int64 // -1 = desconocido
	Verdict                      string
	Notes                        []string
}

// VolumeInfo es el espacio de una unidad con letra.
type VolumeInfo struct {
	Letter, Label, FS string
	Size, Free        int64
}

var (
	mediaNames = map[int]string{0: "Desconocido", 3: "HDD (disco duro)", 4: "SSD", 5: "SCM"}
	busNames   = map[int]string{1: "SCSI", 2: "ATAPI", 3: "ATA", 4: "FireWire", 5: "SSA", 6: "Fibre Channel",
		7: "USB", 8: "RAID", 9: "iSCSI", 10: "SAS", 11: "SATA", 12: "SD", 13: "MMC", 14: "Virtual",
		15: "Virtual", 16: "Espacios de almacenamiento", 17: "NVMe"}
	healthNames = map[int]string{0: "Healthy", 1: "Warning", 2: "Unhealthy", 5: "Unknown"}
	opNames     = map[int]string{0: "Unknown", 1: "Other", 2: "OK", 3: "Degraded", 4: "Stressed",
		5: "Predictive Failure", 6: "Error", 7: "Non-Recoverable Error", 8: "Starting", 9: "Stopping",
		10: "Stopped", 11: "In Service", 12: "No Contact", 13: "Lost Communication", 14: "Aborted",
		15: "Dormant", 16: "Supporting Entity in Error", 17: "Completed"}
	esText = map[string]string{
		"Healthy": "Bueno", "Warning": "Advertencia", "Unhealthy": "MALO", "Unknown": "Desconocido",
		"OK": "Correcto", "Degraded": "Degradado", "Stressed": "Sobrecargado",
		"Predictive Failure": "Fallo previsto", "Error": "Error", "Non-Recoverable Error": "Error irrecuperable",
		"Lost Communication": "Sin comunicación", "No Contact": "Sin contacto", "Stopped": "Detenido",
		"In Service": "En servicio", "Starting": "Iniciando", "Other": "Otro", "Unspecified": "Desconocido",
		"HDD": "HDD (disco duro)",
	}
)

// enumText convierte un valor JSON (número, texto o lista) en texto.
func enumText(raw json.RawMessage, names map[int]string) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if strings.HasPrefix(s, "[") {
		var parts []json.RawMessage
		if json.Unmarshal(raw, &parts) != nil {
			return ""
		}
		var out []string
		for _, p := range parts {
			if t := enumText(p, names); t != "" {
				out = append(out, t)
			}
		}
		return strings.Join(out, ", ")
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		if t, ok := esText[str]; ok {
			return t
		}
		return str
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return s
	}
	if v, ok := names[n]; ok {
		if t, ok := esText[v]; ok {
			return t
		}
		return v
	}
	return s
}

// jsonInt lee un número; -1 si falta o es null (dato desconocido).
func jsonInt(raw json.RawMessage) int64 {
	if t := strings.TrimSpace(string(raw)); t == "" || t == "null" {
		return -1
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		return -1
	}
	return int64(f)
}

// parseDiskJSON interpreta la salida del script de PowerShell (un objeto
// o una lista de objetos).
func parseDiskJSON(b []byte) ([]DiskHealth, error) {
	b = []byte(strings.TrimSpace(strings.TrimPrefix(string(b), "\uFEFF")))
	if len(b) == 0 {
		return nil, fmt.Errorf("Windows no devolvió información de los discos")
	}
	var objs []map[string]json.RawMessage
	if b[0] == '{' {
		var one map[string]json.RawMessage
		if err := json.Unmarshal(b, &one); err != nil {
			return nil, err
		}
		objs = append(objs, one)
	} else if err := json.Unmarshal(b, &objs); err != nil {
		return nil, err
	}
	var out []DiskHealth
	for _, o := range objs {
		d := DiskHealth{
			Media: enumText(o["Media"], mediaNames), Bus: enumText(o["Bus"], busNames),
			Health: enumText(o["Health"], healthNames), Op: enumText(o["Op"], opNames),
			Size: jsonInt(o["Size"]), Temp: int(jsonInt(o["Temp"])), TempMax: int(jsonInt(o["TempMax"])),
			Wear: int(jsonInt(o["Wear"])), Hours: jsonInt(o["Hours"]),
			ReadErr: jsonInt(o["ReadErr"]), WriteErr: jsonInt(o["WriteErr"]), ReadUncor: jsonInt(o["ReadUncorr"]),
		}
		json.Unmarshal(o["Name"], &d.Name)
		if d.Temp == 0 {
			d.Temp = -1 // 0 °C significa «no lo informa»
		}
		d.judge()
		out = append(out, d)
	}
	return out, nil
}

// judge decide el veredicto y explica por qué.
func (d *DiskHealth) judge() {
	bad, warn := false, false
	switch d.Health {
	case "MALO":
		bad = true
		d.Notes = append(d.Notes, "Windows indica que el disco está en mal estado")
	case "Advertencia":
		warn = true
		d.Notes = append(d.Notes, "Windows muestra una advertencia sobre el disco")
	}
	for _, w := range []string{"Fallo previsto", "Error irrecuperable", "Error"} {
		if strings.Contains(d.Op, w) {
			bad = true
			d.Notes = append(d.Notes, "estado: "+strings.ToLower(w))
			break
		}
	}
	if strings.Contains(d.Op, "Degradado") {
		warn = true
		d.Notes = append(d.Notes, "funciona degradado")
	}
	if d.ReadUncor > 0 {
		bad = true
		d.Notes = append(d.Notes, fmt.Sprintf("%d errores de lectura que no se pudieron corregir", d.ReadUncor))
	}
	if d.Wear >= 90 {
		bad = true
		d.Notes = append(d.Notes, fmt.Sprintf("SSD gastado al %d %%", d.Wear))
	} else if d.Wear >= 75 {
		warn = true
		d.Notes = append(d.Notes, fmt.Sprintf("SSD gastado al %d %%", d.Wear))
	}
	hot := 70
	if strings.HasPrefix(d.Media, "HDD") {
		hot = 55
	}
	if d.Temp >= hot {
		warn = true
		d.Notes = append(d.Notes, fmt.Sprintf("temperatura alta (%d °C)", d.Temp))
	}
	switch {
	case bad:
		d.Verdict = "Mal"
	case warn:
		d.Verdict = "Atención"
	case d.Health == "Bueno":
		d.Verdict = "Bueno"
	default:
		d.Verdict = "Desconocido"
	}
}

// Advice da una recomendación según el veredicto.
func (d *DiskHealth) Advice() string {
	switch d.Verdict {
	case "Mal":
		return "Copia cuanto antes lo importante y sustituye este disco. Si hay que recuperar archivos de él, úsalo lo mínimo."
	case "Atención":
		return "Vigílalo y mantén copias de seguridad al día."
	case "Bueno":
		return "Sin problemas detectados."
	}
	return "No se pudo obtener el estado de salud de este disco (es habitual en discos USB)."
}

func fmtOr(v int64, unit string) string {
	if v < 0 {
		return "—"
	}
	return groupDigits(v) + unit
}

// DiskRows prepara las filas para mostrar en una tabla.
func DiskRows(ds []DiskHealth) [][]string {
	var rows [][]string
	for _, d := range ds {
		temp := "—"
		if d.Temp >= 0 {
			temp = fmt.Sprintf("%d °C", d.Temp)
		}
		wear := "—"
		if d.Wear >= 0 && !strings.HasPrefix(d.Media, "HDD") {
			wear = fmt.Sprintf("%d %%", d.Wear)
		}
		hours := fmtOr(d.Hours, " h")
		if d.Hours > 24*365 {
			hours += fmt.Sprintf(" (%.1f años)", float64(d.Hours)/(24*365))
		}
		state := d.Verdict
		if len(d.Notes) > 0 {
			state += ": " + strings.Join(d.Notes, "; ")
		}
		rows = append(rows, []string{d.Name, d.Media, d.Bus, humanSize(d.Size), state, temp, wear, hours,
			fmtOr(d.ReadUncor, "")})
	}
	return rows
}

// VolumeSummary resume el espacio de las unidades.
func VolumeSummary(vs []VolumeInfo) string {
	var parts []string
	for _, v := range vs {
		if v.Size <= 0 {
			continue
		}
		used := 100 - v.Free*100/v.Size
		parts = append(parts, fmt.Sprintf("%s: %s libres de %s (%d %% usado)", v.Letter, humanSize(v.Free), humanSize(v.Size), used))
	}
	return strings.Join(parts, "   ·   ")
}
