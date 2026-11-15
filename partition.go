package main

import "fmt"

// Source describe una unidad que el usuario puede elegir.
type Source struct {
	Path   string
	Label  string
	Size   int64
	Letter string
	Disk   bool

	// Datos para la interfaz gráfica.
	FS        string // sistema de archivos (NTFS, FAT32...)
	VolLabel  string // nombre del volumen
	Free      int64  // espacio libre
	Removable bool
	Model     string // modelo del disco físico
	Bus       string // USB, SATA, NVMe...
	SSD       bool
	Letters   string // letras que contiene un disco físico
}

// Container es una región del dispositivo que puede contener un volumen
// (una partición, o el dispositivo entero si no hay tabla de particiones).
type Container struct {
	Start, Size int64
	Desc        string
}

// readContainers lee la tabla de particiones (MBR o GPT). Si el
// dispositivo es directamente un volumen devuelve un único contenedor.
func readContainers(d *Device) []Container {
	whole := []Container{{0, d.Size, "dispositivo completo"}}
	sec := make([]byte, 512)
	if n, _ := d.ReadAt(sec, 0); n < 512 {
		return whole
	}
	// ¿Es un sector de arranque de volumen (NTFS/FAT/exFAT/BitLocker)?
	oem := string(sec[3:11])
	if oem == "NTFS    " || oem == "EXFAT   " || oem == "-FVE-FS-" || oem == "MSDOS5.0" || string(sec[82:87]) == "FAT32" {
		return whole
	}
	if sec[510] != 0x55 || sec[511] != 0xAA {
		return whole
	}
	var parts []Container
	gpt := false
	for i := 0; i < 4; i++ {
		e := sec[446+16*i:]
		typ := e[4]
		lba := int64(le32(e[8:]))
		cnt := int64(le32(e[12:]))
		if typ == 0xEE {
			gpt = true
			break
		}
		if typ == 0 || cnt == 0 {
			continue
		}
		if typ == 0x05 || typ == 0x0F || typ == 0x85 {
			parts = append(parts, readEBR(d, lba*512)...)
			continue
		}
		parts = append(parts, Container{lba * 512, cnt * 512, fmt.Sprintf("partición MBR %d (tipo %02X)", i+1, typ)})
	}
	if gpt {
		parts = readGPT(d)
	}
	var ok []Container
	for _, p := range parts {
		if p.Start > 0 && p.Start < d.Size && p.Size > 0 {
			if p.Start+p.Size > d.Size {
				p.Size = d.Size - p.Start
			}
			ok = append(ok, p)
		}
	}
	if len(ok) == 0 {
		return whole
	}
	// Contenedor final para lo que queda fuera de las particiones.
	ok = append(ok, Container{-1, 0, "espacio fuera de las particiones"})
	return ok
}

func readEBR(d *Device, extStart int64) []Container {
	var out []Container
	cur := extStart
	sec := make([]byte, 512)
	for i := 0; i < 64; i++ {
		if n, _ := d.ReadAt(sec, cur); n < 512 || sec[510] != 0x55 || sec[511] != 0xAA {
			break
		}
		e := sec[446:]
		if e[4] != 0 && le32(e[12:]) != 0 {
			out = append(out, Container{cur + int64(le32(e[8:]))*512, int64(le32(e[12:])) * 512,
				fmt.Sprintf("partición lógica %d", i+5)})
		}
		n := sec[462:]
		if n[4] == 0 || le32(n[8:]) == 0 {
			break
		}
		cur = extStart + int64(le32(n[8:]))*512
	}
	return out
}

func readGPT(d *Device) []Container {
	for _, ss := range []int64{512, 4096} {
		hdr := make([]byte, 512)
		if n, _ := d.ReadAt(hdr, ss); n < 512 || string(hdr[:8]) != "EFI PART" {
			continue
		}
		entLBA := int64(le64(hdr[72:]))
		cnt := int(le32(hdr[80:]))
		esz := int(le32(hdr[84:]))
		if esz < 128 || esz > 1024 || cnt <= 0 || cnt > 1024 {
			continue
		}
		tbl := make([]byte, cnt*esz)
		if n, _ := d.ReadAt(tbl, entLBA*ss); n < len(tbl) {
			continue
		}
		var out []Container
		for i := 0; i < cnt; i++ {
			e := tbl[i*esz:]
			if allZero(e[:16]) {
				continue
			}
			first := int64(le64(e[32:]))
			last := int64(le64(e[40:]))
			if last < first {
				continue
			}
			name := utf16String(e[56:128])
			for len(name) > 0 && name[len(name)-1] == 0 {
				name = name[:len(name)-1]
			}
			desc := fmt.Sprintf("partición GPT %d", i+1)
			if name != "" {
				desc += " (" + name + ")"
			}
			out = append(out, Container{first * ss, (last - first + 1) * ss, desc})
		}
		return out
	}
	return nil
}
