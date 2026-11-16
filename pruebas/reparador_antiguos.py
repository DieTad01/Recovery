import os, re, sys, glob, collections, warnings
warnings.filterwarnings("ignore")
ref, out = sys.argv[1], sys.argv[2]
sys.path.insert(0, os.path.dirname(__file__))
def xlsx_vals(p):
    import openpyxl
    wb = openpyxl.load_workbook(p, data_only=True, read_only=True)
    s = collections.Counter()
    for ws in wb.worksheets:
        for row in ws.iter_rows(values_only=True):
            for v in row:
                if v is None or str(v).strip() == "": continue
                if isinstance(v, float) and v.is_integer(): v = int(v)
                elif isinstance(v, float): v = float("%.11g" % v)
                elif isinstance(v, str):
                    try: v = float("%.11g" % float(v)); v = int(v) if v.is_integer() else v
                    except ValueError: pass
                if hasattr(v, "year"): continue  # fechas: en el rescate salen como número
                s[str(v).strip()] += 1
    return s
def txt_vals(p):
    t = open(p, encoding="utf-8-sig", errors="replace").read()
    return collections.Counter(w for w in re.findall(r"\w+", t))
def pptx_vals(p):
    import pptx
    s = collections.Counter()
    for sl in pptx.Presentation(p).slides:
        for sh in sl.shapes:
            if sh.has_text_frame:
                s.update(re.findall(r"\w+", sh.text_frame.text))
    return s
res = collections.defaultdict(list)
for f in sorted(glob.glob(os.path.join(out, "*_rescate.*"))):
    m = re.match(r"(.*)_m(\d)_(\d*)_rescate\.(\w+)$", os.path.basename(f))
    name, mode, ext = m.group(1), m.group(2), m.group(4)
    if os.path.exists(os.path.join(ref, name + ".xlsx")):
        a, kind = xlsx_vals(os.path.join(ref, name + ".xlsx")), "xls"
        try: b = xlsx_vals(f); ok = True
        except Exception as e: b, ok = {}, False; print("NO ABRE", f, e)
    elif os.path.exists(os.path.join(ref, name + ".txt")):
        a, kind = txt_vals(os.path.join(ref, name + ".txt")), "doc"; b, ok = txt_vals(f), True
    elif os.path.exists(os.path.join(ref, name + ".pptx")):
        a, kind = pptx_vals(os.path.join(ref, name + ".pptx")), "ppt"; b, ok = txt_vals(f), True
    else:
        continue
    tot = sum(a.values())
    r = sum(min(v, b.get(k, 0)) for k, v in a.items()) / tot if tot else 0
    wrong = sum(v for k, v in b.items() if k not in a and "perdido" not in k and k not in ("texto","Diapositiva","aquí","falta","un","trozo"))
    res[(kind, mode)].append((ok, r, wrong))
    if wrong > tot * 0.02: print("ajenos", os.path.basename(f), wrong, [k for k in b if k not in a][:5])
for k, v in sorted(res.items()):
    print(f"{k[0]} modo {k[1]}: {len(v)} rescates · abren {sum(x[0] for x in v)}/{len(v)} · conservado medio {100*sum(x[1] for x in v)/len(v):.0f}% · valores ajenos {sum(x[2] for x in v)}")
