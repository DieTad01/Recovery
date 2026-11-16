import os, re, sys, glob, subprocess, shutil, collections, warnings
warnings.filterwarnings("ignore")
src, out = sys.argv[1], sys.argv[2]
lo = "--lo" in sys.argv
def xlsx_vals(p):
    import openpyxl
    wb = openpyxl.load_workbook(p, data_only=True, read_only=True)
    s = collections.Counter()
    for ws in wb.worksheets:
        for row in ws.iter_rows(values_only=True):
            for v in row:
                if v is not None and str(v).strip() != "":
                    s[str(v).strip()] += 1
    return s
def docx_vals(p):
    import docx
    d = docx.Document(p)
    s = collections.Counter(x.text.strip() for x in d.paragraphs if x.text.strip())
    for t in d.tables:
        for r in t.rows:
            for c in r.cells:
                if c.text.strip(): s[c.text.strip()] += 1
    return s
def pptx_vals(p):
    import pptx
    pr = pptx.Presentation(p)
    s = collections.Counter()
    for sl in pr.slides:
        for sh in sl.shapes:
            if sh.has_text_frame:
                for para in sh.text_frame.paragraphs:
                    t = "".join(r.text for r in para.runs).strip()
                    if t: s[t] += 1
    return s
def txt_vals(p):
    return collections.Counter(l.strip() for l in open(p, encoding="utf-8-sig", errors="replace").read().splitlines() if l.strip())
reader = {"xlsx": xlsx_vals, "docx": docx_vals, "pptx": pptx_vals}
def retained(a, b):
    tot = sum(a.values())
    if tot == 0: return None
    return sum(min(v, b.get(k, 0)) for k, v in a.items()) / tot
res = collections.defaultdict(list)
lofail = []
for f in sorted(glob.glob(os.path.join(out, "*_reparado.*")) + glob.glob(os.path.join(out, "*_rescate.*"))):
    m = re.match(r"(.*)_m(\d)_(\d*)_(reparado|rescate)\.(\w+)$", os.path.basename(f))
    name, mode, kind, ext = m.group(1), m.group(2), m.group(4), m.group(5)
    orig = [p for p in glob.glob(os.path.join(src, name + ".*")) if p.rsplit(".",1)[1] in reader]
    if not orig: continue
    oext = orig[0].rsplit(".",1)[1]
    try:
        a = reader[oext](orig[0])
    except Exception as e:
        continue  # el original no lo abre la biblioteca (muestras sintéticas)
    try:
        b = reader[ext](f) if ext in reader else txt_vals(f)
        ok = True
    except Exception as e:
        ok, b = False, {}
        print("NO ABRE", os.path.basename(f), type(e).__name__, str(e)[:100])
    r = retained(a, b) if ok else 0
    wrong = sum(v for k, v in b.items() if k not in a and "texto perdido" not in k and "se perdió una parte" not in k and not k.startswith("--- Diapositiva")) if ok else 0
    if wrong: print("VALORES QUE NO ESTABAN:", os.path.basename(f), wrong, [k for k in b if k not in a][:3])
    if kind == "rescate" and ext == "txt":
        # texto: cuántas líneas originales aparecen
        r = retained(a, b)
    res[(oext, kind)].append((ok, r, os.path.basename(f)))
    res[(oext, kind+"/m"+mode)].append((ok, r, os.path.basename(f)))
    if lo and kind == "reparado":
        tmp = os.path.join(out, "_lo"); os.makedirs(tmp, exist_ok=True)
        p = subprocess.run(["soffice", "--headless", "--convert-to", "pdf", "--outdir", tmp, f], capture_output=True, text=True, timeout=120)
        pdf = os.path.join(tmp, os.path.basename(f).rsplit(".",1)[0] + ".pdf")
        if not os.path.exists(pdf): lofail.append(os.path.basename(f))
for k, v in sorted(res.items()):
    n = len(v); ok = sum(1 for x in v if x[0])
    rs = [x[1] for x in v if x[0] and x[1] is not None]
    avg = sum(rs)/len(rs) if rs else 0
    full = sum(1 for x in rs if x >= 0.999)
    print(f"{k[0]:5} {k[1]:9}: {n:3} archivos · abren {ok}/{n} · contenido conservado medio {avg*100:.0f}% · completos {full}")
if lo:
    print("LibreOffice no pudo abrir:", len(lofail), lofail[:10])
