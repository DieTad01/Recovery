#!/usr/bin/env python3
"""Genera un árbol de archivos de prueba de muchos tipos (fotos, videos,
documentos, comprimidos...) para comprobar el recuperador.

Uso: gen_muestras.py CARPETA
Requiere: Pillow, ffmpeg, ImageMagick (convert), 7z, rar (los que falten se omiten).
"""
import os, random, sqlite3, struct, subprocess, sys, zipfile, zlib

random.seed(1234)
out = sys.argv[1]


def d(*p):
    path = os.path.join(out, *p)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    return path


def run(cmd):
    try:
        subprocess.run(cmd, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return True
    except Exception as e:
        print("  (omitido:", cmd[0], e, ")")
        return False


def noise(n):
    return random.randbytes(n)


# ---------- imágenes
from PIL import Image

def picture(w, h, seed):
    rnd = random.Random(seed)
    img = Image.new("RGB", (w, h))
    px = img.load()
    for y in range(h):
        for x in range(w):
            px[x, y] = ((x * 255 // w + rnd.randrange(40)) % 256, (y * 255 // h + rnd.randrange(40)) % 256, (x + y + rnd.randrange(60)) % 256)
    return img

fotos = "Users/Ana María/Pictures/Vacaciones 2023"
for i in range(6):
    img = picture(900 + 60 * i, 700, i)
    exif = Image.Exif()
    exif[0x0132] = "2023:07:%02d 10:20:30" % (i + 1)
    exif[0x010F] = "Camara"
    img.save(d(fotos, "IMG_%04d.jpg" % i), quality=92, exif=exif)
picture(1200, 900, 50).save(d(fotos, "progresiva.jpg"), quality=90, progressive=True)
picture(640, 480, 51).save(d(fotos, "captura.png"))
picture(300, 200, 52).save(d(fotos, "logo ñandú.gif"))
picture(400, 300, 53).save(d(fotos, "mapa.bmp"))
picture(800, 600, 54).save(d(fotos, "escaneo.tif"))
picture(800, 600, 55).save(d(fotos, "escaneo_lzw.tiff"), compression="tiff_lzw")
picture(500, 400, 56).save(d(fotos, "foto.webp"), quality=90)
frames = [picture(120, 90, 60 + k).convert("P") for k in range(5)]
frames[0].save(d(fotos, "animado.gif"), save_all=True, append_images=frames[1:], duration=80, loop=0)
run(["convert", d(fotos, "captura.png"), d("Users/Ana María/Documents/Diseño", "cartel.psd")])

# ---------- audio y video
vid = "Users/Ana María/Videos"
mus = "Users/Ana María/Music"
base = ["ffmpeg", "-y", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25",
        "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "6"]
run(base + ["-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", d(vid, "clip.mp4")])
run(base + ["-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "+faststart", d(vid, "clip_faststart.mp4")])
run(base + ["-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", d(vid, "Proyecto Filmora", "toma.mov")])
run(base + ["-c:v", "mpeg4", "-c:a", "mp3", d(vid, "antiguo.avi")])
run(base + ["-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", d(vid, "pelicula.mkv")])
run(base + ["-c:v", "libvpx", "-b:v", "300k", "-c:a", "libvorbis", d(vid, "web.webm")])
run(base + ["-c:v", "wmv2", "-c:a", "wmav2", d(vid, "viejo.wmv")])
run(base + ["-c:v", "mpeg2video", "-c:a", "mp2", "-f", "mpegts", d(vid, "camara.ts")])
run(base + ["-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-f", "mpegts", "-mpegts_m2ts_mode", "1", d(vid, "AVCHD", "00001.MTS")])
run(base + ["-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "frag_keyframe+empty_moov", d(vid, "fragmentado.mp4")])
aud = ["ffmpeg", "-y", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=44100", "-t", "20"]
run(aud + ["-c:a", "libmp3lame", "-b:a", "128k", "-id3v2_version", "3", "-metadata", "title=Canción", d(mus, "cancion.mp3")])
run(aud + ["-c:a", "libmp3lame", "-q:a", "4", "-id3v2_version", "0", "-write_xing", "0", d(mus, "sin_etiqueta.mp3")])
run(aud + [d(mus, "grabacion.wav")])
run(aud + ["-c:a", "libvorbis", d(mus, "tema.ogg")])
run(aud + ["-c:a", "aac", d(mus, "tema.m4a")])
run(aud + ["-c:a", "wmav2", d(mus, "tema.wma")])
run(aud + ["-c:a", "flac", d(mus, "tema.flac")])
run(aud + ["-c:a", "libopus", d(mus, "voz.opus")])

# ---------- documentos
doc = "Users/Ana María/Documents"

def pdf(path, pages, extra=b""):
    objs = []
    kids = " ".join("%d 0 R" % (3 + 2 * i) for i in range(pages))
    objs.append(b"<< /Type /Catalog /Pages 2 0 R >>")
    objs.append(("<< /Type /Pages /Kids [%s] /Count %d >>" % (kids, pages)).encode())
    for i in range(pages):
        stream = zlib.compress(("BT /F1 24 Tf 72 700 Td (Pagina %d) Tj ET\n" % i).encode() + noise(3000))
        objs.append(("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R >>" % (4 + 2 * i)).encode())
        objs.append(b"<< /Length %d /Filter /FlateDecode >>\nstream\n" % len(stream) + stream + b"\nendstream")
    body = b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n"
    offs = []
    for n, o in enumerate(objs, 1):
        offs.append(len(body))
        body += b"%d 0 obj\n" % n + o + b"\nendobj\n"
    xref = len(body)
    body += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)
    for o in offs:
        body += b"%010d 00000 n \n" % o
    body += b"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, xref)
    open(path, "wb").write(body + extra)

pdf(d(doc, "Factura 001.pdf"), 3)
pdf(d(doc, "Tesis", "Tesis final.pdf"), 40)
# PDF con actualización incremental (dos %%EOF)
pdf(d(doc, "Contrato firmado.pdf"), 5, b"12 0 obj\n<< /Firma (si) >>\nendobj\nxref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 13 /Prev 100 >>\nstartxref\n999\n%%EOF\n")

def ooxml(path, kind, parts):
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>')
        z.writestr("_rels/.rels", "<Relationships/>")
        for i in range(parts):
            z.writestr("%s/parte%d.xml" % (kind, i), "<x>" + "texto de prueba %d " % i * 400 + "</x>")
        z.writestr("%s/media/image1.bin" % kind, noise(20000))

ooxml(d(doc, "Carta.docx"), "word", 3)
ooxml(d(doc, "Presupuesto 2024.xlsx"), "xl", 5)
ooxml(d(doc, "Presentación final.pptx"), "ppt", 8)
try:
    import docx
    dd = docx.Document()
    for i in range(30):
        dd.add_paragraph("Párrafo de prueba número %d. " % i * 5)
    dd.save(d(doc, "Informe real.docx"))
except Exception:
    pass
try:
    import openpyxl
    wb = openpyxl.Workbook()
    ws = wb.active
    for i in range(200):
        ws.append([i, "fila %d" % i, i * 3.14])
    wb.save(d(doc, "Datos reales.xlsx"))
except Exception:
    pass
try:
    import xlwt
    wb = xlwt.Workbook()
    ws = wb.add_sheet("Hoja")
    for i in range(500):
        ws.write(i, 0, "celda %d" % i)
        ws.write(i, 1, i)
    wb.save(d(doc, "Antiguo.xls"))
except Exception:
    pass
# ODT mínimo (mimetype sin comprimir como primera entrada)
with zipfile.ZipFile(d(doc, "Notas.odt"), "w") as z:
    z.writestr(zipfile.ZipInfo("mimetype"), "application/vnd.oasis.opendocument.text")
    z.writestr("content.xml", "<office>" + "hola " * 3000 + "</office>", zipfile.ZIP_DEFLATED)
# ZIP escrito en flujo (tamaños en descriptor de datos)
class NoSeek:
    def __init__(self, f): self.f = f
    def write(self, b): return self.f.write(b)
    def flush(self): self.f.flush()
    def tell(self): raise OSError()
with open(d(doc, "en_flujo.zip"), "wb") as raw:
    with zipfile.ZipFile(NoSeek(raw), "w", zipfile.ZIP_DEFLATED) as z:
        for i in range(4):
            with z.open("dato%d.bin" % i, "w") as e:
                e.write(noise(15000) + b"PK" * 50)
open(d(doc, "notas.rtf"), "w").write("{\\rtf1\\ansi{\\fonttbl{\\f0 Arial;}}\\f0\\fs24 " + "Hola mundo \\{llaves\\} \\par " * 300 + "}")
open(d(doc, "léeme.txt"), "w", encoding="utf-8").write("Texto sencillo con acentos: áéíóú ñ\n" * 2000)
open(d(doc, "pequeño.txt"), "w", encoding="utf-8").write("archivo diminuto (residente en la MFT)\n")
open(d(doc, "datos.csv"), "w").write("".join("%d;valor %d\n" % (i, i * 7) for i in range(5000)))
open(d(doc, "Proyecto Filmora", "mi video.wfp"), "wb").write(b"WFPROJ" + noise(70000))
open(d(doc, "binario.dat"), "wb").write(noise(300000))
open(d(doc, "a" * 120 + " nombre muy largo.txt"), "w").write("largo\n" * 3000)
con = sqlite3.connect(d(doc, "base.sqlite"))
con.execute("create table t(a,b)")
con.executemany("insert into t values(?,?)", [(i, "x" * 100) for i in range(3000)])
con.commit(); con.close()

# ---------- comprimidos
comp = "Users/Ana María/Downloads"
os.makedirs(os.path.join(out, comp), exist_ok=True)
tmp = d("tmp_src", "x")
os.makedirs(os.path.dirname(tmp), exist_ok=True)
for i in range(5):
    open(os.path.join(out, "tmp_src", "f%d.bin" % i), "wb").write(noise(40000) + b"A" * 20000)
srcs = [os.path.join(out, "tmp_src", "f%d.bin" % i) for i in range(5)]
with zipfile.ZipFile(d(comp, "copia.zip"), "w", zipfile.ZIP_DEFLATED) as z:
    for s_ in srcs:
        z.write(s_, os.path.basename(s_))
run(["7z", "a", "-bd", d(comp, "copia.7z")] + srcs)
run(["rar", "a", "-idq", "-ep", d(comp, "copia.rar")] + srcs)
run(["rar", "a", "-idq", "-ep", "-ma4", d(comp, "copia_v4.rar")] + srcs)
for s_ in srcs:
    os.remove(s_)
os.rmdir(os.path.join(out, "tmp_src"))

# ---------- escritorio y muchos archivos pequeños
for i in range(40):
    open(d("Users/Ana María/Desktop/Apuntes", "tema %02d" % (i // 10), "apunte %03d.txt" % i), "w").write("apunte %d\n" % i * (50 + i * 37))
open(d("Users/Ana María/Desktop", "vacío.txt"), "w").close()
print("muestras generadas en", out)
