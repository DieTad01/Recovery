# Recovery 2 — recuperador de archivos con explorador

Programa para **recuperar archivos de un equipo con Windows que fue restaurado,
formateado o del que se borraron archivos**, con un explorador para **ver lo
encontrado antes de recuperarlo**, como los programas comerciales:

- Busca los archivos **con su nombre y su carpeta original** (la tabla de
  archivos de NTFS actual y los restos de la que había antes de restaurar).
- Encuentra además **por su contenido** fotos, videos, documentos, música y
  comprimidos cuyo nombre ya no existe.
- Muestra todo en un **árbol de carpetas** con **vista previa** de fotos y
  textos, un **buscador** y **casillas** para elegir qué recuperar.
- Indica el **estado de cada archivo** (bueno, dañado en parte, no recuperable).
- Incluye dos herramientas: **estado de los discos** y **programas que más ocupan**.
- **Solo lee** el disco afectado. Nunca escribe en él.

Es un único archivo (`recovery.exe`), no se instala y no necesita nada más.

![El explorador de resultados](docs/explorador.png)

---

## Antes de empezar (importante)

1. **Usa el equipo afectado lo menos posible.** Cada archivo nuevo que se guarda
   en ese disco puede sobrescribir para siempre algo recuperable.
2. **Necesitas otro disco** (externo o USB) para guardar lo recuperado. El
   programa no deja guardar en la misma unidad que se analiza.
3. **No descargues `recovery.exe` en el disco afectado**: cópialo a la memoria
   USB o al disco externo desde otro equipo y ejecútalo desde ahí.

## Cómo usarlo

1. Haz doble clic en `recovery.exe` y acepta el permiso de administrador (hace
   falta para leer el disco). Si aparece «Windows protegió su PC», pulsa
   **Más información → Ejecutar de todas formas** (sale porque el programa no
   está firmado por una empresa).
2. En la pantalla de inicio elige la unidad donde estaban los archivos
   (normalmente **C:**) y pulsa **Buscar archivos**.
   - Deja **sin marcar** «Análisis rápido» si el equipo se formateó o
     restauró: así se analiza el disco entero (puede tardar horas).

   ![Pantalla de inicio](docs/inicio.png)

3. Los archivos **van apareciendo mientras se analiza**: ya puedes explorarlos.
   Pulsa **Detener** cuando quieras para quedarte con lo encontrado.

   ![Análisis en curso](docs/analizando.png)

4. Explora a la izquierda y selecciona un archivo para ver su **vista previa**
   y su estado a la derecha. Usa el **buscador** de arriba a la derecha.
5. **Marca las casillas** de los archivos o carpetas que quieres (marcar una
   carpeta marca todo su contenido) y pulsa **Recuperar**. Elige una carpeta en
   **otro disco** y, al terminar, el programa te ofrece abrirla.

### Las secciones del explorador

| Sección | Qué contiene |
|---------|--------------|
| **Con su ubicación original** | Archivos con su nombre y su ruta, por ejemplo `\Users\Ana\Documents\...` |
| **Carpetas sin ubicación** | Carpetas completas cuya carpeta superior ya no existe (`carpeta_1234`) |
| **Por tipo de archivo** | Todo lo recuperable agrupado: fotos, videos, audio, documentos… y por extensión |
| **Sin nombre (por contenido)** | Archivos encontrados por su contenido, sin nombre original, agrupados por tipo |
| **Encontrados pero no recuperables** | Archivos que existían pero su contenido ya fue sobrescrito (solo informativo) |

La casilla **«Ocultar archivos de Windows y de programas»** (marcada por
defecto) deja solo los archivos personales; desmárcala para verlo todo.

### El buscador

Escribe en la casilla de búsqueda:

| Ejemplo | Encuentra |
|---------|-----------|
| `tesis` | archivos o carpetas cuyo nombre contiene «tesis» |
| `*.jpg` | todas las fotos JPG (también vale `*.docx`, `*.mp4`…) |
| `Vacaciones 2023` | todo lo que está dentro de esa carpeta |
| `tesis, *.mp4` | varias cosas a la vez, separadas por comas |

No distingue mayúsculas ni acentos («mama» encuentra «Mamá»).

![Resultados de una búsqueda](docs/busqueda.png)

### Estado de cada archivo

- **Bueno**: se puede recuperar entero.
- **Dañado en parte**: parte de su contenido fue reutilizado por archivos nuevos;
  puede abrir a medias.
- **Puede estar incompleto**: encontrado por su contenido pero estaba
  fragmentado.
- **No recuperable**: el archivo existía pero su contenido ya no está.

## Herramientas

**Estado de los discos** muestra cada disco con su tipo (SSD/HDD), conexión,
salud según Windows, temperatura, desgaste del SSD, horas de uso y errores de
lectura, con un veredicto (**Bueno / Atención / Mal**) y el espacio libre de
cada unidad.

![Estado de los discos](docs/discos.png)

**Programas que más ocupan** lista los programas instalados de mayor a menor,
midiendo lo que ocupa realmente su carpeta (con «~» cuando solo se conoce el
tamaño que declaró el instalador). Haz clic en una columna para ordenar.

![Programas que más ocupan](docs/programas.png)

## Dónde quedan los archivos recuperados

Dentro de la carpeta que elijas se crean:

| Carpeta | Contenido |
|---------|-----------|
| `Archivos` | Archivos con su ruta original |
| `Carpetas_sueltas` | Carpetas cuya carpeta superior ya no existía |
| `Por_firma` | Archivos sin nombre, por tipo (las fotos y videos se nombran con su fecha cuando la tienen) |
| `Dudosos` | Archivos dañados en parte o incompletos: puede que no abran |

## Preguntas frecuentes

**¿Por qué aparece un archivo pero sale «dañado» o «no recuperable»?** El nombre
y el contenido se guardan en sitios distintos del disco. Tras una restauración
suele sobrevivir la «ficha» del archivo pero no su contenido, si el Windows nuevo
reutilizó ese espacio.

**Mi disco es un SSD.** En los SSD, Windows suele vaciar físicamente (TRIM) el
contenido de lo borrado: quedan los nombres pero el contenido son ceros, y
**ningún programa** puede recuperarlo. El programa lo detecta y no guarda
archivos en blanco.

**La unidad está cifrada con BitLocker.** Elige la **letra de unidad** (`C:`)
con la unidad desbloqueada, no el «Disco físico».

**¿Tenía archivos en OneDrive?** Los que estaban «solo en la nube» no ocupaban
sitio en el disco y siguen en tu cuenta de OneDrive.

**¿Existe `C:\Windows.old`?** Mira primero en `C:\Windows.old\Users`: ahí
Windows deja a veces los archivos anteriores tal cual.

**No aparece mi unidad o se cambiaron las particiones.** Elige «Disco físico …»:
el programa localiza por sí mismo dónde empezaba el volumen antiguo.

## Uso avanzado (línea de comandos)

Con opciones, el programa trabaja por consola en vez de abrir la ventana. Abre
una consola **como administrador**:

```
recovery.exe -origen C: -destino E:\Recuperado -buscar "tesis, *.jpg"
recovery.exe -estado-disco
recovery.exe -programas
```

| Opción | Efecto |
|--------|--------|
| `-origen X` | `C:`, `disco0`, `\\.\PhysicalDrive0`, un archivo de imagen (`disco.img`) o en Linux `/dev/sdb1` |
| `-destino DIR` | Carpeta de destino (en otra unidad) |
| `-buscar TEXTO` | Recupera solo lo que coincida (mismas reglas que el buscador) |
| `-modo todo` | Incluye también archivos de Windows y de programas |
| `-rapido` | Solo archivos borrados hace poco |
| `-sin-firmas` / `-solo-firmas` | Solo con nombre / solo por contenido |
| `-solo-listar` | Analiza sin recuperar todavía |
| `-ext jpg,mp4` | Solo esas extensiones |
| `-estado-disco` | Muestra el estado de los discos |
| `-programas` | Muestra los programas que más ocupan |
| `-nuevo` | No reutiliza un análisis anterior |
| `-si` | No pide confirmaciones |
| `-listar` | Muestra las unidades |

## Cómo funciona

1. **Sistema de archivos actual.** Lee la MFT de NTFS: los registros libres que
   conservan datos son archivos borrados, y el mapa de clusters indica qué
   zonas ocupan los archivos actuales (ahí no hay nada perdido).
2. **Análisis completo.** Recorre el espacio libre buscando registros MFT de
   antes de la restauración, sectores de arranque antiguos y cabeceras de
   archivos conocidos. Los resultados se publican por etapas.
3. **Reconstrucción.** Une los fragmentos de los archivos muy fragmentados,
   rehace el árbol de carpetas y averigua dónde empezaba el volumen antiguo.
4. **Archivos sin nombre.** Calcula el tamaño real de cada archivo encontrado
   por su firma analizando su estructura interna.
5. **Recuperación.** Copia lo marcado siguiendo su lista de fragmentos (admite
   archivos fragmentados, dispersos y con compresión NTFS) y comprueba cada uno.

Formatos reconocidos por contenido: JPG, PNG, GIF, BMP, TIFF y RAW, HEIC, WEBP,
PSD · MP4, MOV, 3GP, AVI, MKV, WEBM, WMV, TS, MTS · MP3, WAV, M4A, WMA, OGG,
OPUS · PDF, DOCX, XLSX, PPTX, DOC, XLS, PPT, ODT, RTF, EPUB · ZIP, RAR, 7Z ·
SQLite, PST. Los archivos que conservan su registro NTFS se recuperan sean del
tipo que sean.

## Limitaciones

- No puede recuperar lo físicamente sobrescrito ni lo vaciado por TRIM.
- Los archivos sin nombre muy fragmentados salen incompletos (`Dudosos`) o no salen.
- En unidades FAT32/exFAT solo funciona la búsqueda por contenido.
- No descifra BitLocker ni EFS.
- La vista previa muestra fotos JPG, PNG y GIF y archivos de texto.
- El estado de los discos usa la información de Windows (Windows 8 o posterior);
  en discos USB suele no estar disponible.
- Verificado con imágenes NTFS reales y ejecutando el `.exe` en Wine (la
  ventana, la recuperación y las herramientas). **No se ha podido probar en un
  Windows real con un disco físico.**

## Compilar y probar

Hace falta [Go](https://go.dev/dl/) 1.21 o posterior. No tiene dependencias.
El icono y el manifiesto de Windows están en `rsrc_windows_amd64.syso`
(generado desde `winres/icon.png` con `go-winres`).

```
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o recovery.exe .
go test ./...
```

Pruebas de extremo a extremo (Linux, como root; requieren `ntfs-3g`, `ffmpeg`,
ImageMagick, `7z`, `rar` y Pillow):

```
go build -o /tmp/recovery .
python3 pruebas/gen_muestras.py /tmp/muestras
python3 pruebas/prueba_firmas.py /tmp/recovery /tmp/muestras /tmp/trabajo
python3 pruebas/prueba_ntfs.py   /tmp/recovery /tmp/muestras /tmp/trabajo
RECOVERY_TEST_IMG=/tmp/trabajo/borrado_win.img RECOVERY_TEST_SRC=/tmp/muestras go test -run TestSessionImage -v .
```
