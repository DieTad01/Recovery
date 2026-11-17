// Convierte el manual (HTML hecho con pandoc desde el README) a PDF con Chromium.
// Uso: node empaquetado/manual.js entrada.html salida.pdf
const { chromium } = require('playwright');
(async () => {
  const [inp, out] = process.argv.slice(2);
  const b = await chromium.launch();
  const p = await b.newPage();
  await p.goto('file://' + require('path').resolve(inp), { waitUntil: 'load' });
  await p.pdf({
    path: out, format: 'A4', printBackground: true, preferCSSPageSize: true,
    displayHeaderFooter: true,
    headerTemplate: '<span></span>',
    footerTemplate: '<div style="font-size:8px;width:100%;text-align:center;color:#555;font-family:Arial">' +
      'Herramientas Delio · Manual de uso · página <span class="pageNumber"></span> de <span class="totalPages"></span></div>',
  });
  await b.close();
})();
