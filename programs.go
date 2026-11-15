package main

import (
	"fmt"
	"sort"
	"strings"
)

// Program es un programa instalado.
type Program struct {
	Name, Publisher, Version, Location, Installed string
	Size                                          int64
	Estimated                                     bool // tamaño declarado por el instalador (no medido)
}

func sortPrograms(ps []Program) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Size != ps[j].Size {
			return ps[i].Size > ps[j].Size
		}
		return strings.ToLower(ps[i].Name) < strings.ToLower(ps[j].Name)
	})
}

// fmtInstallDate convierte «20240131» en «31/01/2024».
func fmtInstallDate(s string) string {
	if len(s) == 8 {
		return s[6:8] + "/" + s[4:6] + "/" + s[0:4]
	}
	return s
}

// ProgramRows prepara las filas para mostrar en una tabla.
func ProgramRows(ps []Program) [][]string {
	var rows [][]string
	for _, p := range ps {
		size := "—"
		if p.Size > 0 {
			size = humanSize(p.Size)
			if p.Estimated {
				size = "~" + size
			}
		}
		rows = append(rows, []string{p.Name, size, p.Publisher, p.Version, fmtInstallDate(p.Installed), p.Location})
	}
	return rows
}

// ProgramsSummary resume la lista.
func ProgramsSummary(ps []Program) string {
	var total int64
	for _, p := range ps {
		total += p.Size
	}
	return fmt.Sprintf("%d programas instalados · ocupan en total %s", len(ps), humanSize(total))
}
