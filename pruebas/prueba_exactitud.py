#!/usr/bin/env python3
"""Mide la EXACTITUD de los estados que muestra el explorador («Bueno»,
«Dañado», «En blanco»...): para cada archivo encontrado compara lo que el
programa recuperaría con el archivo original real.

Requiere las imágenes que crea prueba_ntfs.py (antiguo.img y derivadas).
Añade un escenario nuevo, «reinstalado2»: tras formatear, la instalación
escribe muchos datos temporales, los borra y reutiliza sus registros, de
modo que el contenido antiguo queda sobrescrito SIN dejar rastro en el mapa
del disco (el caso más difícil de detectar).

Uso (como root):  prueba_exactitud.py CARPETA_TRABAJO [--profundo]
"""
import hashlib, json, os, random, subprocess, sys, time

work = os.path.abspath(sys.argv[1])
deep = "--profundo" in sys.argv
mnt = os.path.join(work, "mnt")
os.makedirs(mnt, exist_ok=True)
repo = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def sh(*cmd):
    return subprocess.run(cmd, check=True, capture_output=True, text=True).stdout


def umount():
    for _ in range(30):
        if subprocess.run(["umount", mnt], capture_output=True).returncode == 0:
            return
        time.sleep(0.3)


# 1) Verdad: los archivos originales, tal como estaban antes de borrar/formatear.
truth_path = os.path.join(work, "verdad.json")
sh("ntfs-3g", "-o", "ro", os.path.join(work, "antiguo.img"), mnt)
truth = {}
for d, _, names in os.walk(mnt):
    for n in names:
        p = os.path.join(d, n)
        data = open(p, "rb").read()
        truth[os.path.relpath(p, mnt)] = [hashlib.sha1(data).hexdigest(), len(data)]
umount()
json.dump(truth, open(truth_path, "w"), ensure_ascii=False)
print(f"verdad: {len(truth)} archivos originales")

# 2) Escenario difícil: sobrescritura sin rastro.
img2 = os.path.join(work, "reinstalado2.img")
if not os.path.exists(img2):
    subprocess.run(["cp", "--sparse=always", os.path.join(work, "antiguo.img"), img2], check=True)
    sh("mkntfs", "-F", "-f", "-q", img2)
    sh("ntfs-3g", img2, mnt)
    rnd = random.Random(42)
    st = os.statvfs(mnt)
    target = int(st.f_bavail * st.f_frsize * 0.35)  # temporales: ~35 % del disco
    os.makedirs(os.path.join(mnt, "Temp"))
    written, i = 0, 0
    while written < target:
        sz = rnd.randrange(200_000, 3_000_000)
        with open(os.path.join(mnt, "Temp", f"t{i}.tmp"), "wb") as f:
            f.write(rnd.randbytes(sz))
        written += sz
        i += 1
    subprocess.run(["sync"])
    subprocess.run(["rm", "-rf", os.path.join(mnt, "Temp")], check=True)
    # Reutilizar los registros MFT de los temporales con archivos diminutos.
    os.makedirs(os.path.join(mnt, "Windows", "INF"))
    for k in range(i + 200):
        open(os.path.join(mnt, "Windows", "INF", f"x{k}.inf"), "w").write("[v]\n")
    umount()
    print(f"reinstalado2: {i} temporales ({written >> 20} MB) escritos, borrados y sus registros reutilizados")

# 3) Medir cada imagen.
imgs = ["borrado_win.img", "formateo.img", "reinstalado.img", "reinstalado2.img", "trim.img", "disco.img"]
env = dict(os.environ, RECOVERY_ACC_TRUTH=truth_path)
if deep:
    env["RECOVERY_ACC_DEEP"] = "1"
for name in imgs:
    path = os.path.join(work, name)
    if not os.path.exists(path):
        print(f"(falta {name}, se omite)")
        continue
    env["RECOVERY_ACC_IMG"] = path
    r = subprocess.run(["go", "test", "-run", "TestAccuracy", "-v", "-count=1", "."], cwd=repo,
                       env=env, capture_output=True, text=True)
    lines = [l.strip() for l in r.stdout.splitlines() if "accuracy_test.go" in l]
    print(f"\n=== {name}")
    for l in lines:
        print("   " + l.split(": ", 1)[-1])
    if r.returncode != 0 and "FAIL" in r.stdout:
        print(r.stdout[-1500:])
