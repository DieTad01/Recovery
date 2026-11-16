#!/usr/bin/env python3
"""Pruebas de extremo a extremo con imágenes NTFS reales (creadas con ntfs-3g).

Escenarios:
  borrado-windows - borrado simulado como lo hace Windows (ver borrado_windows.py)
  borrado     - se borran los archivos del usuario en un NTFS en uso (con ntfs-3g)
  formateo    - formateo rápido + "reinstalación" que sobrescribe parte del disco
  reinstalado - como el anterior, pero la instalación nueva destruye además
                los registros MFT de las carpetas y de muchos archivos antiguos
                (prueba las carpetas sueltas y la recuperación por firma)
  particion   - el volumen nuevo empieza en otra posición del disco (MBR)
  fragmentado - archivo en cientos de fragmentos (lista de atributos NTFS)
  sector4k    - volumen con sectores y registros MFT de 4096 bytes

Uso (como root):  prueba_ntfs.py RECOVERY_BIN CARPETA_MUESTRAS CARPETA_TRABAJO [escenario...]
"""
import hashlib, os, random, shutil, struct, subprocess, sys, time

binary, muestras, work = sys.argv[1:4]
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
which = sys.argv[4:] or ["borrado-windows", "borrado", "formateo", "reinstalado", "particion", "fragmentado", "sector4k"]
os.makedirs(work, exist_ok=True)
mnt = os.path.join(work, "mnt")
os.makedirs(mnt, exist_ok=True)
SIZE_MB = 320


def sh(*cmd, check=True):
    return subprocess.run(cmd, check=check, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True).stdout


def mount(img, opts="big_writes"):
    sh("ntfs-3g", "-o", opts, img, mnt)


def umount():
    for _ in range(20):
        if subprocess.run(["umount", mnt], capture_output=True).returncode == 0:
            return
        time.sleep(0.3)
    raise SystemExit("no se pudo desmontar")


def sha(path):
    h = hashlib.sha1()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
    return h.hexdigest()


def tree_hashes(root):
    out = {}
    for d, _, names in os.walk(root):
        for n in names:
            p = os.path.join(d, n)
            out[os.path.relpath(p, root)] = (sha(p), os.path.getsize(p))
    return out


def fake_windows(root, n, seed, tag="old", tiny=False):
    """Crea n archivos 'de sistema' (dll, imágenes del sistema...). Con
    tiny=True casi todos son diminutos: ocupan registros MFT pero no datos."""
    rnd = random.Random(seed)
    from PIL import Image
    for i in range(n):
        if tiny and i % 25:
            sub = "Windows/INF/grupo%d" % (i % 3)
            os.makedirs(os.path.join(root, sub), exist_ok=True)
            open(os.path.join(root, sub, "%s_%d.inf" % (tag, i)), "w").write("[Version]\nn=%d\n" % i)
            continue
        sub = ["Windows/System32", "Windows/WinSxS/x86_paquete_%d" % (i % 7), "Program Files/App %d" % (i % 5),
               "ProgramData/Microsoft/Cache"][i % 4]
        os.makedirs(os.path.join(root, sub), exist_ok=True)
        if i % 9 == 0:
            img = Image.new("RGB", (200 + i % 50, 150), (i * 7 % 256, i * 13 % 256, i * 29 % 256))
            img.putpixel((i % 100, i % 100), (255, 255, 255))
            img.save(os.path.join(root, sub, "%s_fondo_%d.%s" % (tag, i, "png" if i % 2 else "jpg")))
        else:
            with open(os.path.join(root, sub, "%s_mod_%d.dll" % (tag, i)), "wb") as f:
                f.write(b"MZ\x90\x00" + rnd.randbytes(rnd.randrange(3000, 90000)))


def populate(img, compressed=True):
    """Sistema de archivos 'antiguo': Windows falso + archivos del usuario."""
    sh("mkntfs", "-F", "-f", "-q", "-L", "Antiguo", img)
    mount(img, "big_writes,compression")
    fake_windows(mnt, 400, 1)
    shutil.copytree(os.path.join(muestras, "Users"), os.path.join(mnt, "Users"))
    user = os.path.join(mnt, "Users", "Ana María")
    rnd = random.Random(99)
    # Dos archivos grandes escritos de forma intercalada -> muy fragmentados
    # (su lista de tramos no cabe en un registro MFT: usa $ATTRIBUTE_LIST).
    a = open(os.path.join(user, "Videos", "grande_fragmentado_A.mp4"), "wb")
    b = open(os.path.join(user, "Videos", "grande_fragmentado_B.bin"), "wb")
    # (estructura MP4 válida: ftyp + mdat con los datos + moov final; así el
    # comprobador de contenido no lo toma por un video roto)
    a.write(b"\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom" + struct.pack(">I", 8 + 700 * 4096) + b"mdat")
    for _ in range(700):
        a.write(rnd.randbytes(4096)); a.flush(); os.fsync(a.fileno())
        b.write(rnd.randbytes(4096)); b.flush(); os.fsync(b.fileno())
    a.write(b"\x00\x00\x00\x08moov")
    a.close(); b.close()
    # Archivo disperso
    with open(os.path.join(user, "Documents", "disperso.bin"), "wb") as f:
        f.write(b"inicio" * 1000); f.seek(3 << 20); f.write(b"final" * 1000)
    # Carpeta con compresión NTFS
    if compressed:
        cdir = os.path.join(user, "Documents", "Comprimidos NTFS")
        os.makedirs(cdir)
        try:
            os.setxattr(cdir, "system.ntfs_attrib_be", b"\x00\x00\x08\x10")
            with open(os.path.join(cdir, "texto repetitivo.txt"), "w") as f:
                for i in range(40000):
                    f.write("línea %d de un texto muy compresible, una y otra vez\n" % (i % 97))
            with open(os.path.join(cdir, "mezcla.bin"), "wb") as f:
                for i in range(60):
                    f.write(rnd.randbytes(30000) if i % 3 == 0 else bytes([i]) * 50000)
        except OSError as e:
            print("  (sin compresión NTFS:", e, ")")
    # Enlace duro
    os.link(os.path.join(user, "Documents", "léeme.txt"), os.path.join(user, "Desktop", "enlace a léeme.txt"))
    orig = tree_hashes(os.path.join(mnt, "Users"))
    umount()
    return {os.path.join("Users", k): v for k, v in orig.items()}


def recover(source, out, *extra):
    shutil.rmtree(out, ignore_errors=True)
    r = subprocess.run([binary, "-origen", source, "-destino", out, "-si", "-nuevo", *extra],
                       capture_output=True, text=True)
    print("\n".join("    | " + l for l in r.stdout.replace("\r", "\n").split("\n") if l.strip() and not l.startswith("  ") or "AVISO" in l or "Volumen original" in l))
    if r.returncode != 0:
        print(r.stdout[-2000:], r.stderr[-2000:])
        raise SystemExit("el recuperador falló")


def evaluate(orig, out, forbid=()):
    """Compara lo recuperado con los originales. Devuelve estadísticas."""
    good = tree_hashes(os.path.join(out, "Archivos")) if os.path.isdir(os.path.join(out, "Archivos")) else {}
    loose = tree_hashes(os.path.join(out, "Carpetas_sueltas")) if os.path.isdir(os.path.join(out, "Carpetas_sueltas")) else {}
    carved = tree_hashes(os.path.join(out, "Por_firma")) if os.path.isdir(os.path.join(out, "Por_firma")) else {}
    doubt = tree_hashes(os.path.join(out, "Dudosos")) if os.path.isdir(os.path.join(out, "Dudosos")) else {}
    by_hash = {h: k for k, (h, _) in orig.items()}
    st = dict(total=0, exact_path=0, exact_loose=0, exact_carved=0, wrong=0, doubt=0, missing=0)
    loose_hashes = {h for h, _ in loose.values()}
    good_hashes = {h for h, _ in good.values()}
    carved_hashes = {h for h, _ in carved.values()}
    missing = []
    for rel, (h, size) in sorted(orig.items()):
        if size == 0:
            continue
        st["total"] += 1
        if (rel in good and good[rel][0] == h) or h in good_hashes:  # (un enlace duro conserva solo uno de sus nombres)
            st["exact_path"] += 1
        elif h in loose_hashes:
            st["exact_loose"] += 1
        elif h in carved_hashes:
            st["exact_carved"] += 1
        else:
            st["missing"] += 1
            missing.append(rel)
    # Nada de lo que está en las carpetas "buenas" puede tener contenido incorrecto.
    wrong = []
    for rel, (h, size) in good.items():
        if rel in orig and orig[rel][0] != h:
            wrong.append("Archivos/" + rel)
    orig_hashes = set(by_hash)
    for rel, (h, size) in loose.items():
        name = os.path.basename(rel)
        cands = [k for k in orig if os.path.basename(k) == name]
        if cands and h not in orig_hashes:
            wrong.append("Carpetas_sueltas/" + rel)
    st["wrong"] = len(wrong)
    st["doubt"] = len(doubt)
    st["carved_total"] = len(carved)
    st["carved_unknown"] = sum(1 for h, _ in carved.values() if h not in orig_hashes)
    # Archivos que no deben aparecer (pertenecen al sistema actual).
    leaked = []
    for t in (good, loose, carved, doubt):
        for rel, (h, _) in t.items():
            if h in forbid:
                leaked.append(rel)
    st["leaked"] = len(leaked)
    print("    total %(total)d | misma ruta %(exact_path)d | carpeta suelta %(exact_loose)d | por firma %(exact_carved)d | "
          "perdidos %(missing)d | dudosos %(doubt)d | INCORRECTOS %(wrong)d | firma extra %(carved_unknown)d | fugas %(leaked)d" % st)
    # Perdidos que SÍ deberían poder recuperarse por firma (tipo con firma y tamaño suficiente).
    SIN_FIRMA = {".txt", ".csv", ".wfp", ".dat", ".flac", ".bin"}
    st["missing_carvable"] = [m for m in missing
                              if os.path.splitext(m)[1].lower() not in SIN_FIRMA and orig[m][1] >= 4096]
    for m in st["missing_carvable"][:12]:
        print("      perdido (tipo con firma):", m)
    for w in wrong[:12]:
        print("      INCORRECTO:", w)
    for l in leaked[:12]:
        print("      FUGA (archivo existente recuperado):", l)
    return st, missing


fails = []

def check(name, cond, msg):
    if not cond:
        fails.append("%s: %s" % (name, msg))
        print("    *** FALLO:", msg)


base = os.path.join(work, "antiguo.img")
with open(base, "wb") as f:
    f.truncate(SIZE_MB << 20)
print("Creando el sistema de archivos antiguo...")
orig = populate(base)
print("  %d archivos de usuario" % len(orig))

if "borrado-windows" in which:
    print("\n[borrado-windows] borrado tal como lo hace Windows (los registros conservan nombre y carpeta)")
    import borrado_windows
    img = os.path.join(work, "borrado_win.img")
    shutil.copy(base, img)
    n = borrado_windows.borrar(img, "Users")
    print("    %d registros MFT liberados" % n)
    mount(img, "ro")
    existing = {h for h, _ in tree_hashes(mnt).values()}
    umount()
    out = os.path.join(work, "out_borrado_win")
    recover(img, out)
    st, missing = evaluate(orig, out, forbid=existing)
    check("borrado-windows", st["wrong"] == 0 and st["leaked"] == 0, "archivos incorrectos o que no estaban perdidos")
    check("borrado-windows", st["exact_path"] == st["total"], "no se recuperó todo con su ruta original")
    check("borrado-windows", st["doubt"] == 0 and st["carved_total"] == 0, "aparecieron dudosos o duplicados por firma")
    names = set(tree_hashes(os.path.join(out, "Archivos")))
    check("borrado-windows", set(k for k, v in orig.items() if v[1] > 0) <= names | {"Users/Ana María/Documents/léeme.txt", "Users/Ana María/Desktop/enlace a léeme.txt"},
          "faltan rutas originales")

if "borrado" in which:
    print("\n[borrado] se borra la carpeta Users (sin escribir nada después)")
    img = os.path.join(work, "borrado.img")
    shutil.copy(base, img)
    mount(img)
    shutil.rmtree(os.path.join(mnt, "Users"))
    existing = {h for h, _ in tree_hashes(mnt).values()}
    umount()
    out = os.path.join(work, "out_borrado")
    recover(img, out)
    st, missing = evaluate(orig, out, forbid=existing)
    check("borrado", st["wrong"] == 0, "hay archivos incorrectos en las carpetas buenas")
    check("borrado", st["leaked"] == 0, "se recuperaron archivos que no estaban perdidos")
    # (ntfs-3g, a diferencia de Windows, elimina el nombre de algunas carpetas
    # al borrarlas; sus archivos aparecen entonces en "Carpetas_sueltas").
    check("borrado", st["exact_path"] + st["exact_loose"] == st["total"], "no se recuperó todo")
    check("borrado", st["exact_path"] >= st["total"] * 0.7, "pocas rutas completas")
    check("borrado", st["carved_total"] == 0, "la búsqueda por firma duplicó archivos ya recuperados")
    # Búsqueda rápida: debe encontrar lo mismo sin recorrer el disco.
    out = os.path.join(work, "out_borrado_rapido")
    recover(img, out, "-rapido")
    st2, _ = evaluate(orig, out, forbid=existing)
    check("borrado-rapido", st2["wrong"] == 0 and st2["exact_path"] == st["exact_path"] and st2["exact_loose"] == st["exact_loose"], "la búsqueda rápida difiere")

    print("\n[borrado+uso] tras borrar se siguen escribiendo archivos nuevos")
    mount(img)
    os.makedirs(os.path.join(mnt, "Nuevo"))
    for i in range(5):
        open(os.path.join(mnt, "Nuevo", "nuevo%d.bin" % i), "wb").write(random.Random(i).randbytes(200000))
    existing = {h for h, _ in tree_hashes(mnt).values()}
    umount()
    out = os.path.join(work, "out_borrado_uso")
    recover(img, out)
    st, missing = evaluate(orig, out, forbid=existing)
    check("borrado+uso", st["wrong"] == 0, "hay archivos incorrectos en las carpetas buenas")
    check("borrado+uso", st["leaked"] == 0, "se recuperaron archivos que no estaban perdidos")
    found = st["exact_path"] + st["exact_loose"] + st["exact_carved"]
    check("borrado+uso", found >= st["total"] * 0.6, "se recuperó menos del 60%% (%d de %d)" % (found, st["total"]))

if "formateo" in which or "reinstalado" in which:
    for name, nnew in (("formateo", 150), ("reinstalado", 475)):
        if name not in which:
            continue
        print("\n[%s] formateo rápido + %d archivos nuevos encima" % (name, nnew))
        img = os.path.join(work, name + ".img")
        shutil.copy(base, img)
        sh("mkntfs", "-F", "-f", "-q", "-L", "Nuevo", img)
        mount(img)
        fake_windows(mnt, nnew, 2, tag="new", tiny=(name == "reinstalado"))
        os.makedirs(os.path.join(mnt, "Users", "Nuevo usuario", "Pictures"), exist_ok=True)
        shutil.copy(os.path.join(muestras, "Users/Ana María/Pictures/Vacaciones 2023/IMG_0000.jpg"),
                    os.path.join(mnt, "Users", "Nuevo usuario", "Pictures", "foto_existente.jpg"))
        # Archivos temporales de la instalación: se escriben y luego se borran.
        for i in range(10):
            open(os.path.join(mnt, "tmp%d.bin" % i), "wb").write(random.Random(1000 + i).randbytes(400000))
        existing_all = tree_hashes(mnt)
        for i in range(10):
            os.remove(os.path.join(mnt, "tmp%d.bin" % i))
        existing = {h for k, (h, _) in existing_all.items() if not k.startswith("tmp")}
        umount()
        out = os.path.join(work, "out_" + name)
        recover(img, out)
        st, missing = evaluate(orig, out, forbid=existing - {v[0] for v in orig.values()})
        check(name, st["wrong"] == 0, "hay archivos incorrectos en las carpetas buenas")
        check(name, st["leaked"] == 0, "se recuperaron archivos que no estaban perdidos")
        found = st["exact_path"] + st["exact_loose"] + st["exact_carved"]
        if name == "formateo":
            check(name, found == st["total"], "no se recuperó todo (%d de %d)" % (found, st["total"]))
            # -carpeta: solo esa carpeta, entera y con su ruta original.
            out = os.path.join(work, "out_formateo_carpeta")
            recover(img, out, "-carpeta", "apuntes")
            got = tree_hashes(os.path.join(out, "Archivos"))
            want = {k: v for k, v in orig.items() if "/Desktop/Apuntes/" in k and v[1] > 0}
            check("formateo -carpeta", set(got) == set(want) and all(got[k][0] == want[k][0] for k in want),
                  "la carpeta no salió entera o salió algo más (%d de %d)" % (len(set(got) & set(want)), len(want)))
            check("formateo -carpeta", not os.path.isdir(os.path.join(out, "Por_firma")),
                  "no debe recuperar archivos sin nombre al buscar una carpeta")
        else:
            # Los registros MFT de muchos archivos ya no existen: los de tipo
            # sin firma (txt...) no tienen forma de localizarse; el resto sí.
            check(name, st["exact_loose"] >= 20, "pocas carpetas sueltas")
            check(name, st["exact_carved"] >= 20, "poca recuperación por firma")
            check(name, len(st["missing_carvable"]) <= 2, "se perdieron archivos que debían salir por firma")

if "particion" in which:
    print("\n[particion] el volumen antiguo empezaba en otra posición del disco")
    disk = os.path.join(work, "disco.img")
    old_start, new_start = 1 << 20, 24 << 20
    total = (SIZE_MB << 20) + (32 << 20)
    with open(disk, "wb") as f:
        f.truncate(total)
        f.seek(old_start)
        with open(base, "rb") as src:
            shutil.copyfileobj(src, f, 1 << 20)
    # Volumen nuevo, más pequeño y desplazado.
    newimg = os.path.join(work, "nuevo.img")
    new_size = 200 << 20
    with open(newimg, "wb") as f:
        f.truncate(new_size)
    sh("mkntfs", "-F", "-f", "-q", "-L", "Nuevo", newimg)
    mount(newimg)
    fake_windows(mnt, 60, 3, tag="new")
    existing = {h for h, _ in tree_hashes(mnt).values()}
    umount()
    # mkntfs -f no pone a cero el volumen: solo se copian al disco las zonas
    # que el sistema nuevo realmente escribió (metadatos y archivos).
    with open(newimg, "rb") as n, open(disk, "r+b") as f:
        pos = 0
        while pos < new_size:
            blk = n.read(1 << 16)
            if blk.strip(b"\0"):
                f.seek(new_start + pos)
                f.write(blk)
            pos += len(blk)
        # Tabla de particiones MBR con la partición nueva.
        mbr = bytearray(512)
        mbr[446:462] = struct.pack("<BBBBBBBBII", 0x80, 0, 2, 0, 0x07, 0xFE, 0xFF, 0xFF, new_start // 512, new_size // 512)
        mbr[510:512] = b"\x55\xAA"
        f.seek(0)
        f.write(mbr)
    out = os.path.join(work, "out_particion")
    recover(disk, out)
    st, missing = evaluate(orig, out, forbid=existing)
    check("particion", st["wrong"] == 0, "hay archivos incorrectos en las carpetas buenas")
    found = st["exact_path"] + st["exact_loose"] + st["exact_carved"]
    check("particion", found >= st["total"] * 0.6, "se recuperó menos del 60%% (%d de %d)" % (found, st["total"]))

if "fragmentado" in which:
    print("\n[fragmentado] archivo repartido en cientos de fragmentos ($ATTRIBUTE_LIST)")
    img = os.path.join(work, "fragmentado.img")
    with open(img, "wb") as f:
        f.truncate(96 << 20)
    sh("mkntfs", "-F", "-f", "-q", img)
    mount(img)
    rnd = random.Random(5)
    os.makedirs(os.path.join(mnt, "relleno"))
    NREL = 9000
    for i in range(NREL):
        open(os.path.join(mnt, "relleno", "r%05d" % i), "wb").write(rnd.randbytes(4096))
    with open(os.path.join(mnt, "tapon.bin"), "wb") as f:  # ocupa todo el espacio libre restante
        for step in (1 << 20, 4096):
            try:
                while True:
                    f.write(b"\xAA" * step); f.flush(); os.fsync(f.fileno())
            except OSError:
                pass
    for i in range(0, NREL, 2):  # deja miles de huecos pequeños repartidos por el volumen
        os.remove(os.path.join(mnt, "relleno", "r%05d" % i))
    os.makedirs(os.path.join(mnt, "Videos"))
    data = (b"\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom" + struct.pack(">I", 8 + (13 << 20)) + b"mdat"
            + rnd.randbytes(13 << 20) + b"\x00\x00\x00\x08moov")
    with open(os.path.join(mnt, "Videos", "muy fragmentado.mp4"), "wb") as f:
        f.write(data)
    umount()
    info = sh("ntfsinfo", "-v", "-F", "Videos/muy fragmentado.mp4", img, check=False)
    nruns = sum(1 for l in info.splitlines() if l.startswith("\t\t\t0x"))
    has_list = "$ATTRIBUTE_LIST" in info
    print("    tramos: %d   usa $ATTRIBUTE_LIST: %s" % (nruns, has_list))
    check("fragmentado", has_list and nruns > 200, "la imagen de prueba no quedó suficientemente fragmentada")
    want = hashlib.sha1(data).hexdigest()
    # a) borrado como lo hace Windows: conserva nombre y carpeta (aquí el
    #    nombre está en un registro de extensión, no en el registro base).
    import borrado_windows
    img2 = os.path.join(work, "fragmentado_win.img")
    shutil.copy(img, img2)
    borrado_windows.borrar(img2, "Videos")
    out = os.path.join(work, "out_fragmentado_win")
    recover(img2, out, "-sin-firmas")
    hits = [k for k, (h, _) in tree_hashes(out).items() if h == want]
    print("    borrado estilo Windows ->", hits)
    check("fragmentado", hits == ["Archivos/Videos/muy fragmentado.mp4"], "no se recuperó idéntico y con su ruta")
    # b) borrado con ntfs-3g, que elimina el nombre: debe salir igualmente,
    #    sin nombre pero con su extensión deducida del contenido.
    mount(img)
    os.remove(os.path.join(mnt, "Videos", "muy fragmentado.mp4"))
    umount()
    out = os.path.join(work, "out_fragmentado")
    recover(img, out, "-sin-firmas")
    hits = [k for k, (h, _) in tree_hashes(out).items() if h == want]
    print("    borrado con ntfs-3g    ->", hits)
    check("fragmentado", len(hits) == 1 and hits[0].endswith(".mp4") and not hits[0].startswith("Dudosos"),
          "el archivo fragmentado sin nombre no se recuperó idéntico")

if "sector4k" in which:
    print("\n[sector4k] volumen con sectores de 4096 bytes (registros MFT de 4 KiB)")
    img = os.path.join(work, "sector4k.img")
    with open(img, "wb") as f:
        f.truncate(96 << 20)
    sh("mkntfs", "-F", "-f", "-q", "-s", "4096", img)
    mount(img)
    shutil.copytree(os.path.join(muestras, "Users", "Ana María", "Pictures"), os.path.join(mnt, "Fotos"))
    shutil.copytree(os.path.join(muestras, "Users", "Ana María", "Documents"), os.path.join(mnt, "Docs"))
    o4 = tree_hashes(mnt)
    shutil.rmtree(os.path.join(mnt, "Fotos")); shutil.rmtree(os.path.join(mnt, "Docs"))
    umount()
    out = os.path.join(work, "out_sector4k")
    recover(img, out)
    st, missing = evaluate(o4, out)
    check("sector4k", st["wrong"] == 0 and st["exact_path"] + st["exact_loose"] == st["total"], "no se recuperó todo")

print()
if fails:
    print("FALLOS:")
    for f in fails:
        print("  -", f)
    sys.exit(1)
print("Todas las pruebas NTFS pasaron.")
