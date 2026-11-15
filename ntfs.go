package main

// Estructuras del sistema de archivos NTFS: sector de arranque, registros
// de la MFT, listas de tramos (data runs) y compresión LZNT1.

const (
	attrStdInfo  = 0x10
	attrAttrList = 0x20
	attrFileName = 0x30
	attrData     = 0x80
	attrReparse  = 0xC0
	attrEnd      = 0xFFFFFFFF
)

// BootInfo es la geometría leída de un sector de arranque NTFS.
type BootInfo struct {
	BytesPerSector int
	ClusterSize    int64
	TotalSectors   int64
	MFTLCN         int64
	MirrLCN        int64
	RecSize        int
}

// parseBoot interpreta un sector de arranque NTFS.
func parseBoot(b []byte) (BootInfo, bool) {
	var bi BootInfo
	if len(b) < 512 || string(b[3:11]) != "NTFS    " || b[510] != 0x55 || b[511] != 0xAA {
		return bi, false
	}
	bps := int(le16(b[0x0B:]))
	if bps != 512 && bps != 1024 && bps != 2048 && bps != 4096 {
		return bi, false
	}
	spc := int(b[0x0D])
	var cs int64
	if spc > 0x80 {
		cs = int64(bps) << uint(256-spc)
	} else {
		if spc == 0 || spc&(spc-1) != 0 {
			return bi, false
		}
		cs = int64(bps * spc)
	}
	if cs > 2<<20 {
		return bi, false
	}
	bi.BytesPerSector = bps
	bi.ClusterSize = cs
	bi.TotalSectors = int64(le64(b[0x28:]))
	bi.MFTLCN = int64(le64(b[0x30:]))
	bi.MirrLCN = int64(le64(b[0x38:]))
	c := int8(b[0x40])
	if c > 0 {
		bi.RecSize = int(c) * int(cs)
	} else {
		if -c > 16 {
			return bi, false
		}
		bi.RecSize = 1 << uint(-c)
	}
	if bi.RecSize != 1024 && bi.RecSize != 2048 && bi.RecSize != 4096 {
		return bi, false
	}
	if bi.TotalSectors <= 0 || bi.MFTLCN <= 0 || bi.MFTLCN*cs >= bi.TotalSectors*int64(bps) {
		return bi, false
	}
	return bi, true
}

// applyFixup deshace la "update sequence array" de un registro MFT. Devuelve
// false si el registro está incompleto (parcialmente sobrescrito).
func applyFixup(rec []byte) bool {
	if len(rec) < 48 {
		return false
	}
	usaOfs := int(le16(rec[4:]))
	usaCnt := int(le16(rec[6:]))
	if usaCnt < 2 || usaOfs < 0x28 || usaOfs+usaCnt*2 > len(rec) || (usaCnt-1)*512 > len(rec) {
		return false
	}
	u0, u1 := rec[usaOfs], rec[usaOfs+1]
	for i := 1; i < usaCnt; i++ {
		end := i * 512
		if rec[end-2] != u0 || rec[end-1] != u1 {
			return false
		}
		rec[end-2] = rec[usaOfs+2*i]
		rec[end-1] = rec[usaOfs+2*i+1]
	}
	return true
}

// decodeRuns interpreta una lista de tramos (mapping pairs).
func decodeRuns(b []byte) ([]Run, bool) {
	var runs []Run
	var lcn int64
	i := 0
	for i < len(b) {
		h := b[i]
		if h == 0 {
			return runs, true
		}
		ls := int(h & 0x0F)
		os := int(h >> 4)
		if ls == 0 || ls > 8 || os > 8 || i+1+ls+os > len(b) {
			return runs, false
		}
		var length int64
		for k := 0; k < ls; k++ {
			length |= int64(b[i+1+k]) << uint(8*k)
		}
		if length <= 0 || length > 1<<40 {
			return runs, false
		}
		if os == 0 {
			runs = append(runs, Run{-1, length})
		} else {
			var off int64
			for k := 0; k < os; k++ {
				off |= int64(b[i+1+ls+k]) << uint(8*k)
			}
			shift := uint(64 - 8*os)
			off = off << shift >> shift // extensión de signo
			lcn += off
			if lcn < 0 || lcn > 1<<44 {
				return runs, false
			}
			runs = append(runs, Run{lcn, length})
		}
		i += 1 + ls + os
	}
	return runs, true
}

// isFileSig indica si el sector empieza por la firma de registro MFT.
func isFileSig(b []byte) bool {
	return b[0] == 'F' && b[1] == 'I' && b[2] == 'L' && b[3] == 'E'
}

// parseMFTRecord interpreta un registro MFT (con el fixup ya aplicado).
// Devuelve nil si el registro no aporta nada recuperable.
func parseMFTRecord(rec []byte, off int64) *FileRec {
	if len(rec) < 0x38 {
		return nil
	}
	attrOfs := int(le16(rec[0x14:]))
	flags := le16(rec[0x16:])
	used := int(le32(rec[0x18:]))
	if used > len(rec) {
		used = len(rec)
	}
	if attrOfs < 0x2A || attrOfs+8 > used {
		return nil
	}
	r := &FileRec{Off: off, Seq: le16(rec[0x10:]), RecNo: noRec, ParentRec: noRec}
	if le16(rec[4:]) >= 0x30 {
		r.RecNo = le32(rec[0x2C:])
	}
	if base := le64(rec[0x20:]); base != 0 {
		if (base>>32)&0xFFFF != 0 {
			return nil
		}
		r.BaseRec = uint32(base)
		r.BaseSeq = uint16(base >> 48)
		r.Flags |= fExt
	}
	if flags&1 != 0 {
		r.Flags |= fInUse
	}
	if flags&2 != 0 {
		r.Flags |= fDir
	}
	nameRank := 0
	var fnTime int64
	pos := attrOfs
	for pos+16 <= used {
		typ := le32(rec[pos:])
		if typ == attrEnd {
			break
		}
		alen := int(le32(rec[pos+4:]))
		if alen < 16 || pos+alen > used || typ == 0 || typ > 0x1000 {
			break
		}
		a := rec[pos : pos+alen]
		pos += alen
		nonRes := a[8] != 0
		nameLen := int(a[9])
		switch typ {
		case attrStdInfo:
			if nonRes || alen < 24 {
				continue
			}
			co := int(le16(a[20:]))
			cl := int(le32(a[16:]))
			if cl >= 16 && co+16 <= alen {
				r.MTime = int64(le64(a[co+8:]))
			}
		case attrFileName:
			if nonRes || alen < 24 {
				continue
			}
			co := int(le16(a[20:]))
			cl := int(le32(a[16:]))
			if cl < 66 || co+cl > alen {
				continue
			}
			c := a[co : co+cl]
			nl := int(c[64])
			if 66+2*nl > len(c) || nl == 0 {
				continue
			}
			rank := 2
			switch c[65] { // espacio de nombres
			case 1, 3:
				rank = 3 // Win32
			case 2:
				rank = 1 // nombre corto DOS
			}
			if rank > nameRank {
				nameRank = rank
				r.Name = utf16String(c[66 : 66+2*nl])
				pref := le64(c[0:])
				if (pref>>32)&0xFFFF == 0 {
					r.ParentRec = uint32(pref)
					r.ParentSeq = uint16(pref >> 48)
				} else {
					r.ParentRec = noRec
				}
				fnTime = int64(le64(c[16:]))
			}
		case attrData:
			if nameLen != 0 {
				continue // flujo alternativo (ADS)
			}
			if !nonRes {
				if alen < 24 {
					continue
				}
				cl := int64(le32(a[16:]))
				r.Flags |= fHasData | fResident
				r.Size, r.InitSize = cl, cl
				continue
			}
			if alen < 64 {
				continue
			}
			startVCN := int64(le64(a[16:]))
			runOfs := int(le16(a[32:]))
			if runOfs < 64 || runOfs >= alen || startVCN < 0 {
				continue
			}
			runs, _ := decodeRuns(a[runOfs:])
			if startVCN == 0 {
				r.Size = int64(le64(a[48:]))
				r.InitSize = int64(le64(a[56:]))
				if r.Size < 0 || r.Size > 1<<50 {
					return nil
				}
				if r.InitSize < 0 || r.InitSize > r.Size {
					r.InitSize = r.Size
				}
				af := le16(a[12:])
				if af&0x00FF != 0 {
					r.Flags |= fCompressed
					r.CompUnit = a[34]
				}
				if af&0x4000 != 0 {
					r.Flags |= fEncrypted
				}
			}
			r.Flags &^= fResident
			r.Flags |= fHasData
			r.Segs = append(r.Segs, DataSeg{startVCN, runs})
		case attrReparse:
			r.Flags |= fReparse
		case attrAttrList:
			r.Flags |= fAttrList
		}
	}
	if r.MTime == 0 {
		r.MTime = fnTime
	}
	if r.has(fExt) {
		if len(r.Segs) == 0 {
			return nil
		}
		return r
	}
	if r.Name == "" && !r.has(fHasData) {
		return nil
	}
	return r
}

// residentData extrae los datos residentes del $DATA sin nombre.
func residentData(rec []byte) []byte {
	attrOfs := int(le16(rec[0x14:]))
	used := int(le32(rec[0x18:]))
	if used > len(rec) {
		used = len(rec)
	}
	pos := attrOfs
	for pos+16 <= used {
		typ := le32(rec[pos:])
		if typ == attrEnd {
			break
		}
		alen := int(le32(rec[pos+4:]))
		if alen < 16 || pos+alen > used {
			break
		}
		a := rec[pos : pos+alen]
		pos += alen
		if typ != attrData || a[8] != 0 || a[9] != 0 || alen < 24 {
			continue
		}
		cl := int(le32(a[16:]))
		co := int(le16(a[20:]))
		if co+cl > alen {
			return nil
		}
		return a[co : co+cl]
	}
	return nil
}

// readRecord lee un registro MFT del dispositivo y le aplica el fixup.
func readRecord(d *Device, off int64, recSize int) []byte {
	rec := make([]byte, recSize)
	if n, _ := d.ReadAt(rec, off); n < recSize {
		return nil
	}
	if !isFileSig(rec) || !applyFixup(rec) {
		return nil
	}
	return rec
}

// lznt1Decompress descomprime una unidad de compresión NTFS. out debe
// llegar a cero; devuelve los bytes producidos.
func lznt1Decompress(in, out []byte) int {
	ip, op := 0, 0
	for ip+2 <= len(in) && op < len(out) {
		hdr := le16(in[ip:])
		ip += 2
		if hdr == 0 {
			break
		}
		size := int(hdr&0x0FFF) + 1
		end := ip + size
		if end > len(in) {
			end = len(in)
		}
		chunkStart := op
		chunkEnd := op + 4096
		if chunkEnd > len(out) {
			chunkEnd = len(out)
		}
		if hdr&0x8000 == 0 {
			op += copy(out[op:chunkEnd], in[ip:end])
		} else {
		chunk:
			for ip < end {
				flags := in[ip]
				ip++
				for bit := uint(0); bit < 8 && ip < end; bit++ {
					if op >= chunkEnd {
						break chunk
					}
					if flags&(1<<bit) == 0 {
						out[op] = in[ip]
						op++
						ip++
						continue
					}
					if ip+2 > end {
						break chunk
					}
					tok := int(le16(in[ip:]))
					ip += 2
					p := op - chunkStart
					if p == 0 {
						break chunk // datos corruptos
					}
					lenBits := uint(12)
					for q := p - 1; q >= 0x10; q >>= 1 {
						lenBits--
					}
					length := tok&((1<<lenBits)-1) + 3
					disp := tok>>lenBits + 1
					if disp > p {
						break chunk // datos corruptos
					}
					for k := 0; k < length && op < chunkEnd; k++ {
						out[op] = out[op-disp]
						op++
					}
				}
			}
		}
		ip = end
		op = chunkEnd // cada bloque representa 4096 bytes de salida
	}
	return op
}
