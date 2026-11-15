#!/usr/bin/env python3
"""Simula sobre una imagen NTFS (desmontada) el borrado tal como lo hace
Windows: el registro MFT se marca como libre y se incrementa su número de
secuencia, pero CONSERVA sus atributos (nombre, carpeta superior y lista
de tramos), y sus clusters se liberan en el mapa del volumen.

ntfs-3g, en cambio, elimina el nombre de algunos registros al borrar, así
que no sirve para reproducir fielmente lo que encuentra el recuperador en
un disco real.

Uso como módulo:  borrar(imagen, "Users")   -> nº de registros liberados
"""
import struct, sys


def _runs(buf):
    out, lcn, i = [], 0, 0
    while i < len(buf) and buf[i]:
        ls, os_ = buf[i] & 15, buf[i] >> 4
        length = int.from_bytes(buf[i + 1:i + 1 + ls], "little")
        if os_:
            lcn += int.from_bytes(buf[i + 1 + ls:i + 1 + ls + os_], "little", signed=True)
            out.append((lcn, length))
        else:
            out.append((None, length))
        i += 1 + ls + os_
    return out


def _fixup(rec):
    rec = bytearray(rec)
    uo, uc = struct.unpack_from("<HH", rec, 4)
    for i in range(1, uc):
        rec[i * 512 - 2:i * 512] = rec[uo + 2 * i:uo + 2 * i + 2]
    return rec


def _attrs(rec):
    pos = struct.unpack_from("<H", rec, 0x14)[0]
    while pos + 8 <= len(rec):
        typ, alen = struct.unpack_from("<II", rec, pos)
        if typ == 0xFFFFFFFF or alen == 0:
            break
        yield typ, rec[pos:pos + alen]
        pos += alen


def borrar(img, top="Users"):
    f = open(img, "r+b")
    boot = f.read(512)
    cs = struct.unpack_from("<H", boot, 0x0B)[0] * boot[0x0D]
    mft_lcn = struct.unpack_from("<Q", boot, 0x30)[0]
    c = struct.unpack_from("<b", boot, 0x40)[0]
    rs = c * cs if c > 0 else 1 << -c

    def data_runs(rec, typ=0x80):
        res = []
        for t, a in _attrs(rec):
            if t == typ and a[8] and a[9] == 0:
                res += _runs(a[struct.unpack_from("<H", a, 32)[0]:])
        return res

    f.seek(mft_lcn * cs)
    rec0 = _fixup(f.read(rs))
    mft_runs = data_runs(rec0)
    # posición en la imagen de cada registro
    offs = []
    for lcn, n in mft_runs:
        offs += [lcn * cs + i * rs for i in range(n * cs // rs)]
    recs = {}
    for i, o in enumerate(offs):
        f.seek(o)
        r = f.read(rs)
        if r[:4] == b"FILE" and struct.unpack_from("<H", r, 0x16)[0] & 1:
            recs[i] = _fixup(r)
    parent, name, base_of = {}, {}, {}
    for i, r in recs.items():
        base = struct.unpack_from("<Q", r, 0x20)[0] & 0xFFFFFFFFFFFF
        base_of[i] = base if base else i
        for t, a in _attrs(r):
            if t == 0x30:
                co = struct.unpack_from("<H", a, 20)[0]
                cc = a[co:]
                owner = base_of[i]
                parent[owner] = struct.unpack_from("<Q", cc, 0)[0] & 0xFFFFFFFFFFFF
                name[owner] = cc[66:66 + 2 * cc[64]].decode("utf-16le")
    tops = [i for i in name if name[i] == top and parent[i] == 5]
    if not tops:
        raise SystemExit("no existe /" + top)
    kids = {}
    for i, p in parent.items():
        kids.setdefault(p, []).append(i)
    doomed, stack = set(), list(tops)
    while stack:
        i = stack.pop()
        if i in doomed:
            continue
        doomed.add(i)
        stack += kids.get(i, [])
    doomed |= {i for i, b in base_of.items() if b in doomed}
    # mapa de clusters ($Bitmap, registro 6)
    bm_runs = data_runs(recs[6])
    bm = bytearray()
    for lcn, n in bm_runs:
        f.seek(lcn * cs)
        bm += f.read(n * cs)
    for i in doomed:
        r = recs[i]
        for t, a in _attrs(r):
            if a[8]:  # no residente: liberar sus clusters
                for lcn, n in _runs(a[struct.unpack_from("<H", a, 32)[0]:]):
                    if lcn is not None:
                        for cl in range(lcn, lcn + n):
                            bm[cl >> 3] &= ~(1 << (cl & 7)) & 0xFF
        seq, = struct.unpack_from("<H", r, 0x10)
        flags, = struct.unpack_from("<H", r, 0x16)
        f.seek(offs[i] + 0x10)
        f.write(struct.pack("<H", (seq + 1) & 0xFFFF or 1))
        f.seek(offs[i] + 0x16)
        f.write(struct.pack("<H", flags & ~1))
    pos = 0
    for lcn, n in bm_runs:
        f.seek(lcn * cs)
        f.write(bm[pos:pos + n * cs])
        pos += n * cs
    f.close()
    return len(doomed)


if __name__ == "__main__":
    print(borrar(sys.argv[1], sys.argv[2] if len(sys.argv) > 2 else "Users"), "registros liberados")
