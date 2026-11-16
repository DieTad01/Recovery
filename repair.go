package main

// Reparador: revisa archivos ya recuperados (o cualquier carpeta), arregla
// los dañados en lo posible y busca copias sanas de ellos en otras carpetas.
// Nunca modifica los originales: todo lo que crea va a otra carpeta.

import (
	"strings"
)

// repairOut es el resultado de intentar arreglar un archivo.
type repairOut struct {
	Repaired    []byte // el archivo arreglado, en su mismo formato (nil si no se pudo)
	RepairedExt string
	Complete    bool   // el arreglado tiene todo su contenido (no se perdió nada)
	Rescue      []byte // los datos rescatados en un formato sencillo (nil si no hay)
	RescueExt   string
	RescueTag   string // «datos rescatados», «texto rescatado», «miniatura»...
	Notes       []string
}

// repairData intenta arreglar el contenido de un archivo según su tipo.
func repairData(data []byte, ext string) *repairOut {
	switch repairKind(ext) {
	case "zip":
		return repairOOXML(data, ext)
	case "pdf":
		return repairPDF(data)
	case "ole":
		return repairOLE(data, ext)
	case "jpg":
		return repairJPEG(data)
	case "text":
		return repairText(data, ext)
	}
	return &repairOut{Notes: []string{"este tipo de archivo no se puede reparar"}}
}

func joinNotes(n []string) string { return strings.Join(n, "; ") }
