package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
)

// Report escribe informe.csv con una fila por archivo tratado.
type Report struct {
	f *os.File
	b *bufio.Writer
	w *csv.Writer
}

func NewReport(dir string) (*Report, error) {
	f, err := os.Create(filepath.Join(dir, "informe.csv"))
	if err != nil {
		return nil, err
	}
	b := bufio.NewWriterSize(f, 1<<20)
	b.WriteString("\xEF\xBB\xBF") // BOM: para que Excel muestre bien los acentos
	w := csv.NewWriter(b)
	w.Comma = ';'
	w.UseCRLF = true
	w.Write([]string{"Estado", "Origen", "Ruta", "Tamaño (bytes)", "Modificado", "Detalle"})
	return &Report{f, b, w}, nil
}

func (r *Report) Row(estado, origen, ruta string, size int64, mtime int64, detalle string) {
	if r == nil {
		return
	}
	fecha := ""
	if t := ftToTime(mtime); !t.IsZero() {
		fecha = t.Local().Format("2006-01-02 15:04")
	}
	r.w.Write([]string{estado, origen, ruta, fmt.Sprintf("%d", size), fecha, detalle})
}

func (r *Report) Close() {
	if r == nil {
		return
	}
	r.w.Flush()
	r.b.Flush()
	r.f.Close()
}
