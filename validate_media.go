package main

// Comprobación a fondo de formatos de audio y video que no llevan sumas de
// control: se recorre su estructura completa (paquetes, bloques, fragmentos)
// de modo que si una zona del archivo fue sobrescrita, la cadena se rompe.

import (
	"bytes"
	"io"
	"strconv"
)

var (
	asfHeaderGUID = []byte("\x30\x26\xB2\x75\x8E\x66\xCF\x11\xA6\xD9\x00\xAA\x00\x62\xCE\x6C")
	asfDataGUID   = []byte("\x36\x26\xB2\x75\x8E\x66\xCF\x11\xA6\xD9\x00\xAA\x00\x62\xCE\x6C")
)

// checkASF (wma, wmv): todos los paquetes de datos tienen el mismo tamaño y
// empiezan igual; un paquete sobrescrito no.
func checkASF(r io.ReaderAt, size int64) (int, string) {
	h := readRange(r, 0, 30)
	if len(h) < 30 || !bytes.Equal(h[:16], asfHeaderGUID) {
		return vBad, "no empieza como un archivo de Windows Media"
	}
	hsize := int64(le64(h[16:]))
	if hsize < 30 || hsize > 16<<20 || hsize+50 > size {
		return vBad, "cabecera dañada"
	}
	hb := readRange(r, 0, int(hsize))
	psize := int64(0)
	for p, i := 30, 0; i < int(le32(h[24:])) && p+24 <= len(hb); i++ {
		osz := int(le64(hb[p+16:]))
		if osz < 24 || p+osz > len(hb) {
			return vBad, "cabecera dañada"
		}
		if bytes.Equal(hb[p:p+16], asfFileProps) && osz >= 100 {
			mn, mx := int64(le32(hb[p+92:])), int64(le32(hb[p+96:]))
			if mn == mx {
				psize = mn
			}
		}
		p += osz
	}
	d := readRange(r, hsize, 50)
	if len(d) < 50 || !bytes.Equal(d[:16], asfDataGUID) {
		return vBad, "falta el bloque de datos"
	}
	dsize := int64(le64(d[16:]))
	if dsize < 50 || hsize+dsize > size {
		return vBad, "archivo incompleto (le faltan datos al final)"
	}
	if psize <= 0 {
		return vStruct, "estructura correcta"
	}
	first := readRange(r, hsize+50, 3)
	if len(first) < 3 || first[0] != 0x82 || first[1] != 0 || first[2] != 0 {
		return vStruct, "estructura correcta"
	}
	n := (dsize - 50) / psize
	step := int64(1)
	if n > 200000 {
		step = n / 200000
	}
	for i := int64(0); i < n; i += step {
		b := readRange(r, hsize+50+i*psize, 3)
		if len(b) < 3 || b[0] != 0x82 || b[1] != 0 || b[2] != 0 {
			return vBad, "dañado: el paquete " + strconv.FormatInt(i+1, 10) + " de " + strconv.FormatInt(n, 10) + " no es válido (datos sobrescritos)"
		}
	}
	return vStruct, "estructura completa correcta (" + strconv.FormatInt(n, 10) + " paquetes)"
}

// checkRIFF (avi, wav, webp): la cadena de fragmentos debe llegar justo al
// final; en AVI se recorre también cada fotograma del bloque «movi».
func checkRIFF(r io.ReaderAt, size int64) (int, string) {
	var pos int64
	chunks := 0
	for pos < size {
		h := readRange(r, pos, 12)
		if len(h) < 12 || string(h[:4]) != "RIFF" {
			if pos > 0 && tailPad(r, pos, size) {
				break // relleno final
			}
			return vBad, "estructura rota (datos sobrescritos)"
		}
		end := pos + 8 + int64(le32(h[4:]))
		if end > size {
			return vBad, "archivo incompleto (le faltan datos al final)"
		}
		n, ok := walkRIFF(r, pos+12, end, 0)
		if !ok {
			return vBad, "dañado: su cadena de fragmentos se rompe (datos sobrescritos)"
		}
		chunks += n
		pos = end + end&1
	}
	return vStruct, "estructura completa correcta (" + strconv.Itoa(chunks) + " fragmentos)"
}

func walkRIFF(r io.ReaderAt, pos, end int64, depth int) (int, bool) {
	n := 0
	for pos+8 <= end {
		h := readRange(r, pos, 12)
		if len(h) < 8 || !fourCC(h[:4]) {
			return n, false
		}
		sz := int64(le32(h[4:]))
		next := pos + 8 + sz
		if next > end {
			return n, false
		}
		n++
		if string(h[:4]) == "LIST" && len(h) >= 12 && depth < 4 {
			m, ok := walkRIFF(r, pos+12, next, depth+1)
			if !ok {
				return n, false
			}
			n += m
		}
		pos = next + next&1
	}
	return n, true
}

func fourCC(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// checkTS (ts, mts, m2ts): cada paquete de 188/192 bytes empieza con 0x47.
func checkTS(r io.ReaderAt, size int64, pkt, sync int) (int, string) {
	buf := make([]byte, pkt*4096)
	var pos, n int64
	for pos+int64(pkt) <= size {
		m, _ := r.ReadAt(buf, pos)
		m -= m % pkt
		if m == 0 {
			break
		}
		for i := 0; i < m; i += pkt {
			if buf[i+sync] != 0x47 {
				if tailPad(r, pos+int64(i), size) {
					return vStruct, "estructura completa correcta"
				}
				return vBad, "dañado: el paquete " + strconv.FormatInt(n+1, 10) + " no es válido (datos sobrescritos)"
			}
			n++
		}
		pos += int64(m)
	}
	if n == 0 {
		return vBad, "no contiene video válido"
	}
	return vStruct, "estructura completa correcta (" + strconv.FormatInt(n, 10) + " paquetes)"
}

// ---------------------------------------------------------------- Matroska

var (
	mkvSegment  = 0x18538067
	mkvCluster  = 0x1F43B675
	mkvTracks   = 0x1654AE6B
	mkvClusterK = map[uint32]bool{0xE7: true, 0xA3: true, 0xA0: true, 0xA7: true, 0xAB: true, 0x5854: true, 0xAF: true, 0xBF: true, 0xEC: true}
)

// ebmlID lee el identificador de un elemento EBML.
func ebmlID(b []byte) (uint32, int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n := 1
	for m := byte(0x80); b[0]&m == 0 && n <= 4; m >>= 1 {
		n++
	}
	if n > 4 || n > len(b) {
		return 0, 0
	}
	var id uint32
	for i := 0; i < n; i++ {
		id = id<<8 | uint32(b[i])
	}
	return id, n
}

// checkMKV recorre la estructura: segmento, grupos (clusters) y cada bloque.
// Si el video es H.264 comprueba además los datos de cada fotograma.
func checkMKV(r io.ReaderAt, size int64) (int, string) {
	h := readRange(r, 0, 64)
	if len(h) < 8 || !bytes.HasPrefix(h, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return vBad, "no empieza como un video MKV/WebM"
	}
	hsz, n, _, ok := ebmlSize(h[4:])
	if !ok {
		return vBad, "cabecera dañada"
	}
	pos := 4 + int64(n) + hsz
	b := readRange(r, pos, 12)
	if id, l := ebmlID(b); id != uint32(mkvSegment) || l == 0 {
		return vBad, "estructura rota al principio"
	}
	ssz, sn, sunk, ok := ebmlSize(b[4:])
	if !ok {
		return vBad, "estructura rota al principio"
	}
	pos += 4 + int64(sn)
	end := size
	if !sunk && pos+ssz < size {
		end = pos + ssz
	}
	if !sunk && pos+ssz > size {
		return vBad, "archivo incompleto (le faltan datos al final)"
	}
	avc := map[int64]int{} // pista -> tamaño del prefijo de longitud NAL
	blocks := 0
	for pos < end {
		eb := readRange(r, pos, 12)
		id, il := ebmlID(eb)
		if il == 0 {
			if tailPad(r, pos, end) {
				break
			}
			return vBad, "dañado: estructura rota en el " + strconv.FormatInt(pos*100/size, 10) + " % (datos sobrescritos)"
		}
		esz, en, eunk, ok := ebmlSize(eb[il:])
		if !ok {
			return vBad, "dañado: estructura rota en el " + strconv.FormatInt(pos*100/size, 10) + " % (datos sobrescritos)"
		}
		body := pos + int64(il) + int64(en)
		if id == uint32(mkvCluster) {
			cend := body + esz
			if eunk || cend > end {
				cend = end
			}
			next, nb, ok := walkCluster(r, body, cend, eunk, avc)
			blocks += nb
			if !ok {
				return vBad, "dañado: un bloque de video/audio no es válido en el " + strconv.FormatInt(next*100/size, 10) + " % (datos sobrescritos)"
			}
			pos = next
			continue
		}
		if eunk || body+esz > end {
			return vBad, "dañado: estructura rota en el " + strconv.FormatInt(pos*100/size, 10) + " % (datos sobrescritos)"
		}
		if id == uint32(mkvTracks) && esz < 1<<20 {
			parseMKVTracks(readRange(r, body, int(esz)), avc)
		}
		pos = body + esz
	}
	if blocks == 0 {
		return vStruct, "estructura correcta"
	}
	return vStruct, "estructura completa correcta (" + strconv.Itoa(blocks) + " bloques)"
}

// walkCluster recorre un grupo; devuelve dónde termina.
func walkCluster(r io.ReaderAt, pos, end int64, unknown bool, avc map[int64]int) (int64, int, bool) {
	blocks := 0
	for pos < end {
		eb := readRange(r, pos, 12)
		id, il := ebmlID(eb)
		if il == 0 {
			return pos, blocks, false
		}
		if unknown && !mkvClusterK[id] {
			return pos, blocks, true // empieza el siguiente elemento de nivel 1
		}
		if !mkvClusterK[id] {
			return pos, blocks, false
		}
		esz, en, eunk, ok := ebmlSize(eb[il:])
		body := pos + int64(il) + int64(en)
		if !ok || eunk || body+esz > end {
			return pos, blocks, false
		}
		if id == 0xA3 || id == 0xA0 {
			blocks++
			if id == 0xA3 && !checkMKVBlock(r, body, esz, avc) {
				return pos, blocks, false
			}
		}
		pos = body + esz
	}
	return pos, blocks, true
}

func checkMKVBlock(r io.ReaderAt, pos, size int64, avc map[int64]int) bool {
	if size < 4 {
		return false
	}
	h := readRange(r, pos, 8)
	tr, tn, _, ok := ebmlSize(h)
	if !ok || tr == 0 || tr > 127 || len(h) < tn+3 {
		return false
	}
	ls, isAVC := avc[tr]
	if !isAVC || h[tn+2]&0x06 != 0 || size > 8<<20 { // con «lacing» o enorme: no se mira dentro
		return true
	}
	data := readRange(r, pos+int64(tn)+3, int(size)-tn-3)
	return validNALs(data, ls, int64(len(data)))
}

// parseMKVTracks localiza las pistas H.264/H.265 y su tamaño de prefijo NAL.
func parseMKVTracks(b []byte, avc map[int64]int) {
	for p := 0; p < len(b); {
		id, il := ebmlID(b[p:])
		if il == 0 {
			return
		}
		sz, sn, _, ok := ebmlSize(b[p+il:])
		body := p + il + sn
		if !ok || body+int(sz) > len(b) {
			return
		}
		if id == 0xAE { // TrackEntry
			te := b[body : body+int(sz)]
			var num int64
			codec, priv := "", []byte(nil)
			for q := 0; q < len(te); {
				cid, cl := ebmlID(te[q:])
				if cl == 0 {
					break
				}
				csz, csn, _, ok := ebmlSize(te[q+cl:])
				cb := q + cl + csn
				if !ok || cb+int(csz) > len(te) {
					break
				}
				v := te[cb : cb+int(csz)]
				switch cid {
				case 0xD7:
					for _, x := range v {
						num = num<<8 | int64(x)
					}
				case 0x86:
					codec = string(v)
				case 0x63A2:
					priv = v
				}
				q = cb + int(csz)
			}
			switch {
			case codec == "V_MPEG4/ISO/AVC" && len(priv) > 5:
				avc[num] = int(priv[4]&3) + 1
			case codec == "V_MPEGH/ISO/HEVC" && len(priv) > 22:
				avc[num] = int(priv[21]&3) + 1
			}
		}
		p = body + int(sz)
	}
}

// ---------------------------------------------------------------- SQLite

// checkSQLite comprueba cada página de la base de datos: o es una página
// del árbol de datos con una cabecera coherente, o es una página de
// continuación, o figura como libre.
func checkSQLite(r io.ReaderAt, size int64) (int, string) {
	h := readRange(r, 0, 100)
	if len(h) < 100 || string(h[:16]) != "SQLite format 3\x00" {
		return vBad, "no empieza como una base de datos SQLite"
	}
	ps := int64(be16(h[16:]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 {
		return vBad, "cabecera dañada"
	}
	usable := ps - int64(h[20])
	n := size / ps
	if hn := int64(be32(h[28:])); hn > 0 && be32(h[24:]) == be32(h[92:]) {
		if hn > n {
			return vBad, "base de datos incompleta (le faltan páginas al final)"
		}
		n = hn
	}
	skip := map[int64]bool{}
	// Páginas libres.
	trunk, guard := int64(be32(h[32:])), 0
	for trunk > 0 && trunk <= n && guard < 1<<20 {
		guard++
		skip[trunk] = true
		t := readRange(r, (trunk-1)*ps, int(ps))
		if len(t) < 8 {
			break
		}
		cnt := int64(be32(t[4:]))
		for i := int64(0); i < cnt && 8+4*i+4 <= int64(len(t)); i++ {
			skip[int64(be32(t[8+4*i:]))] = true
		}
		trunk = int64(be32(t))
	}
	// Mapas de punteros (autovacuum) y página de bloqueo.
	if be32(h[52:]) != 0 {
		j := usable/5 + 1
		for p := int64(2); p <= n; p += j {
			skip[p] = true
		}
	}
	skip[1073741824/ps+1] = true
	buf := make([]byte, ps)
	for pg := int64(1); pg <= n; pg++ {
		if skip[pg] {
			continue
		}
		m, _ := r.ReadAt(buf, (pg-1)*ps)
		if int64(m) < ps {
			return vBad, "base de datos incompleta"
		}
		hdr := int64(0)
		if pg == 1 {
			hdr = 100
		}
		b := buf
		switch b[hdr] {
		case 2, 5, 10, 13:
			hl := int64(8)
			if b[hdr] == 2 || b[hdr] == 5 {
				hl = 12
			}
			cells := int64(be16(b[hdr+3:]))
			start := int64(be16(b[hdr+5:]))
			if start == 0 {
				start = 65536
			}
			arr := hdr + hl + 2*cells
			if arr > ps || start > usable || (cells > 0 && start < arr) {
				return vBad, "base de datos dañada: la página " + strconv.FormatInt(pg, 10) + " no es válida (datos sobrescritos)"
			}
			for c := int64(0); c < cells; c++ {
				cp := int64(be16(b[hdr+hl+2*c:]))
				if cp < start || cp >= usable {
					return vBad, "base de datos dañada: la página " + strconv.FormatInt(pg, 10) + " no es válida (datos sobrescritos)"
				}
			}
		default: // página de continuación: empieza con el número de la siguiente
			if allZero(b) {
				return vBad, "base de datos dañada: la página " + strconv.FormatInt(pg, 10) + " está en blanco (contenido perdido)"
			}
			if int64(be32(b)) > n {
				return vBad, "base de datos dañada: la página " + strconv.FormatInt(pg, 10) + " no es válida (datos sobrescritos)"
			}
		}
	}
	return vStruct, "base de datos correcta (" + strconv.FormatInt(n, 10) + " páginas comprobadas)"
}
