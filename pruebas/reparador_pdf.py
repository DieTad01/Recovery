import os, re, sys, glob, subprocess, collections
src, out = sys.argv[1], sys.argv[2]
def text(p):
    r = subprocess.run(["pdftotext", "-q", p, "-"], capture_output=True, timeout=60)
    if r.returncode != 0: return None
    return collections.Counter(l.strip() for l in r.stdout.decode("utf-8","replace").splitlines() if l.strip())
def pages(p):
    r = subprocess.run(["pdfinfo", p], capture_output=True, text=True, timeout=60)
    m = re.search(r"Pages:\s+(\d+)", r.stdout)
    return int(m.group(1)) if m else None
res = collections.defaultdict(list)
for f in sorted(glob.glob(os.path.join(out, "*_reparado.pdf"))):
    m = re.match(r"(.*)_m(\d)_(\d*)_reparado\.pdf$", os.path.basename(f))
    name, mode = m.group(1), m.group(2)
    orig = os.path.join(src, name + ".pdf")
    a, b = text(orig), text(f)
    pa, pb = pages(orig), pages(f)
    dmg = text(f.replace("_reparado", "_danado"))
    if a is None: continue
    tot = sum(a.values())
    r = sum(min(v, (b or {}).get(k, 0)) for k, v in a.items()) / tot if tot and b is not None else 0
    rd = sum(min(v, (dmg or {}).get(k, 0)) for k, v in a.items()) / tot if tot and dmg is not None else 0
    wrong = sum(v for k, v in (b or {}).items() if k not in a)
    res[mode].append((b is not None, r, rd, pb, pa, wrong))
    print(f"{os.path.basename(f):32} abre={b is not None} páginas {pb}/{pa} texto conservado {r*100:.0f}% (el dañado sin reparar: {rd*100:.0f}%) líneas ajenas={wrong}")
for k, v in sorted(res.items()):
    ok = sum(1 for x in v if x[0]); avg = sum(x[1] for x in v)/len(v); avgd = sum(x[2] for x in v)/len(v)
    print(f"modo {k}: abren {ok}/{len(v)} · texto conservado {avg*100:.0f}% (sin reparar {avgd*100:.0f}%)")
