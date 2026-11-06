// Imprime docs/guia-modeladores/guia.html a PDF con Chromium. Uso: make guia-pdf
import { chromium } from 'playwright-core';
import { readdirSync } from 'node:fs';
import { resolve } from 'node:path';

const here = resolve(import.meta.dirname, '../../docs/guia-modeladores');
const out = process.argv[2] || resolve(here, '../3D-Vault-guia-modeladores.pdf');
const pw = `${process.env.HOME}/.cache/ms-playwright`;
const chrome = process.env.CHROME || `${pw}/${readdirSync(pw).filter((d) => /^chromium-\d+$/.test(d)).sort().pop()}/chrome-linux64/chrome`;

const browser = await chromium.launch({ executablePath: chrome });
const page = await browser.newPage();
await page.goto('file://' + resolve(here, 'guia.html'));
await page.evaluate(() => document.fonts.ready);
await page.pdf({
  path: out,
  format: 'A4',
  printBackground: true,
  preferCSSPageSize: true,
  displayHeaderFooter: true,
  headerTemplate: '<span></span>',
  footerTemplate: '<div style="width:100%;font:8px sans-serif;text-align:center;color:#000">3D-Vault · guía para modeladores · <span class="pageNumber"></span>/<span class="totalPages"></span></div>',
});
await browser.close();
console.log(out);
