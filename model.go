package main

// Run es un tramo de clusters contiguos de un archivo.
type Run struct {
	LCN int64 // cluster inicial en el volumen; -1 = hueco (sparse)
	Len int64 // longitud en clusters
}

// DataSeg es un fragmento del atributo $DATA (un archivo muy fragmentado
// reparte su lista de tramos entre varios registros MFT).
type DataSeg struct {
	StartVCN int64
	Runs     []Run
}

// Indicadores de FileRec.Flags.
const (
	fInUse      uint16 = 1 << iota // el registro estaba en uso
	fDir                           // es una carpeta
	fLive                          // pertenece a la MFT del sistema de archivos actual
	fResident                      // los datos están dentro del propio registro
	fCompressed                    // compresión NTFS (LZNT1)
	fEncrypted                     // cifrado EFS
	fHasData                       // tiene atributo $DATA sin nombre
	fReparse                       // punto de reanálisis (enlace, OneDrive...)
	fExt                           // registro de extensión de otro registro base
	fDup                           // duplicado descartado
	fAttrList                      // tiene $ATTRIBUTE_LIST (datos repartidos en extensiones)
)

const noRec = 0xFFFFFFFF

// FileRec resume un registro MFT encontrado en el disco.
type FileRec struct {
	Off       int64 // posición del registro en el dispositivo
	RecNo     uint32
	BaseRec   uint32
	ParentRec uint32
	Seq       uint16
	BaseSeq   uint16
	ParentSeq uint16
	Flags     uint16
	Group     int16 // contenedor (partición) donde se encontró
	CompUnit  uint8
	Name      string
	Size      int64
	InitSize  int64
	MTime     int64 // FILETIME
	Segs      []DataSeg

	// Calculado tras el escaneo (no se guarda en el índice).
	parent   *FileRec
	resolved bool
	rooted   bool
	path     string
}

func (r *FileRec) has(f uint16) bool { return r.Flags&f != 0 }

// firstLCN devuelve el primer cluster real del archivo, o -1.
func (r *FileRec) firstLCN() int64 {
	for _, sg := range r.Segs {
		if sg.StartVCN != 0 {
			continue
		}
		for _, ru := range sg.Runs {
			if ru.LCN >= 0 {
				return ru.LCN
			}
			return -1
		}
	}
	return -1
}

// flatten devuelve la lista de tramos en orden de VCN. gaps indica que
// faltan fragmentos (registros de extensión perdidos).
func (r *FileRec) flatten() (runs []Run, gaps bool) {
	segs := r.Segs
	if len(segs) > 1 {
		segs = append([]DataSeg(nil), segs...)
		// orden por VCN inicial (inserción: suelen ser muy pocos)
		for i := 1; i < len(segs); i++ {
			for j := i; j > 0 && segs[j].StartVCN < segs[j-1].StartVCN; j-- {
				segs[j], segs[j-1] = segs[j-1], segs[j]
			}
		}
	}
	var vcn int64
	for _, sg := range segs {
		if sg.StartVCN < vcn {
			continue // duplicado o solapado
		}
		if sg.StartVCN > vcn {
			runs = append(runs, Run{-1, sg.StartVCN - vcn})
			gaps = true
			vcn = sg.StartVCN
		}
		for _, ru := range sg.Runs {
			runs = append(runs, ru)
			vcn += ru.Len
		}
	}
	return runs, gaps
}
