#!/bin/sh
# Arma la carpeta «Herramientas Delio» lista para entregar: los cuatro
# programas, el manual en PDF y el LEEME. Uso (desde la raíz del proyecto):
#     ./empaquetar.sh
# Requiere Go; para el manual, pandoc y Node con Playwright (si faltan, se
# arma la carpeta sin el PDF).
set -e
cd "$(dirname "$0")"
DIST="Herramientas Delio"
VERSION=$(sed -n 's/^const version = "\(.*\)"/\1/p' main.go)
rm -rf "$DIST"
mkdir -p "$DIST/Programas por separado"
build() { GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui -X main.appMode=$1" -o "$2" .; }
build ""          "$DIST/herramientas.exe"
build recuperar   "$DIST/Programas por separado/recovery.exe"
build reparar     "$DIST/Programas por separado/reparador.exe"
build respaldo    "$DIST/Programas por separado/respaldo.exe"
{ printf '\357\273\277'; sed "s/{{VERSION}}/$VERSION/" empaquetado/LEEME.txt | sed 's/$/\r/'; } > "$DIST/LEEME.txt"

# Manual: el README sin la parte técnica de compilación, en PDF.
if command -v pandoc >/dev/null && command -v node >/dev/null; then
	TMP=$(mktemp -d)
	awk '/^## Compilar y probar/{skip=1} /^## /&&!/^## Compilar y probar/{skip=0} !skip' README.md |
		grep -v 'go test -run TestRepairMeasure' > "$TMP/manual.md"
	pandoc "$TMP/manual.md" -f gfm -t html5 -o "$TMP/cuerpo.html"
	{
		printf '<!doctype html><html lang="es"><head><meta charset="utf-8"><title>Herramientas Delio · Manual de uso</title>'
		printf '<base href="file://%s/"><link rel="stylesheet" href="empaquetado/manual.css"></head><body>' "$(pwd)"
		printf '<div class="portada"><img src="marca/logotipo-tinta.png" alt="Delio"><h1>Herramientas Delio</h1>'
		printf '<p class="sub">Recuperar · Reparar · Respaldar</p><div class="onda"></div>'
		printf '<p class="sub">Manual de uso · versión %s</p><p class="pie">delioph.tech</p></div>' "$VERSION"
		cat "$TMP/cuerpo.html"
		printf '</body></html>'
	} > "$TMP/manual.html"
	node empaquetado/manual.js "$TMP/manual.html" "$DIST/Manual de uso.pdf"
	rm -rf "$TMP"
else
	echo "Aviso: sin pandoc o Node no se genera «Manual de uso.pdf»."
fi
echo "Listo: $DIST"
find "$DIST" -type f | sort
