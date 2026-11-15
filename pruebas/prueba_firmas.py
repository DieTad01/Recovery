#!/usr/bin/env python3
"""Prueba de la recuperación por firma: coloca los archivos de muestra en
una imagen SIN sistema de archivos (alineados a 4 KiB, separados por
basura aleatoria) y comprueba que cada uno se extrae idéntico.

Uso: prueba_firmas.py RECOVERY_BIN CARPETA_MUESTRAS CARPETA_TRABAJO
"""
import hashlib, os, random, subprocess, sys

binary, muestras, work = sys.argv[1:4]
random.seed(7)
os.makedirs(work, exist_ok=True)
img = os.path.join(work, "firmas.img")
outdir = os.path.join(work, "firmas_out")
subprocess.run(["rm", "-rf", outdir])

SIN_FIRMA = {".txt", ".csv", ".wfp", ".dat", ".flac"}
files = []
for root, _, names in os.walk(muestras):
    for n in sorted(names):
        p = os.path.join(root, n)
        if os.path.getsize(p) >= 4096:
            files.append(p)
files.sort()
expected = {}
with open(img, "wb") as f:
    f.write(random.randbytes(1 << 20))
    for p in files:
        data = open(p, "rb").read()
        pos = (f.tell() + 4095) // 4096 * 4096
        f.write(b"\0" * (pos - f.tell()))
        f.write(data)
        # relleno del último sector con ceros (como hace Windows) y luego basura
        f.write(b"\0" * ((512 - len(data) % 512) % 512))
        f.write(random.randbytes(random.randrange(1, 40) * 4096))
        expected[hashlib.sha1(data).hexdigest()] = p
    f.write(random.randbytes(1 << 20))

r = subprocess.run([binary, "-origen", img, "-destino", outdir, "-solo-firmas", "-si", "-nuevo"],
                   capture_output=True, text=True)
if r.returncode != 0:
    print(r.stdout[-3000:], r.stderr[-3000:])
    sys.exit("el recuperador falló")

got = {}
for root, _, names in os.walk(outdir):
    for n in names:
        if n.startswith("informe") or n == "escaneo.idx":
            continue
        p = os.path.join(root, n)
        got[hashlib.sha1(open(p, "rb").read()).hexdigest()] = p

ok = bad = 0
for h, p in sorted(expected.items(), key=lambda kv: kv[1]):
    ext = os.path.splitext(p)[1].lower()
    rel = os.path.relpath(p, muestras)
    if h in got:
        ok += 1
        gext = os.path.splitext(got[h])[1].lower()
        dud = " (en Dudosos)" if "Dudosos" in got[h] else ""
        print("  OK      %-60s -> %s%s" % (rel[-60:], gext, dud))
    elif ext in SIN_FIRMA:
        print("  n/a     %-60s (tipo sin firma)" % rel[-60:])
    else:
        bad += 1
        print("  FALLO   %-60s" % rel[-60:])
extra = [p for h, p in got.items() if h not in expected]
print("\nidénticos: %d   fallos: %d   extraídos de más: %d" % (ok, bad, len(extra)))
for p in extra:
    print("  extra:", os.path.relpath(p, outdir), os.path.getsize(p))
sys.exit(1 if bad else 0)
