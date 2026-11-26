// Tooltips: al pasar el ratón o llegar con Tab a algo con data-tip, se explica
// qué es y para qué sirve. También toma los title de siempre, para que todos se
// vean igual y salgan rápido (el del navegador tarda y no se ve con el teclado).
// Funciona con lo que se agrega después (Mi carpeta, htmx) porque escucha en el
// documento.

const tip = document.createElement('div');
tip.className = 'tip';
tip.id = 'tip';
tip.setAttribute('role', 'tooltip');
tip.hidden = true;

let target = null;
let timer = 0;

function textOf(node) {
  if (node.hasAttribute('title')) {
    // El title pasa a data-tip para que el navegador no muestre el suyo encima.
    node.dataset.tip = node.getAttribute('title');
    node.removeAttribute('title');
  }
  return node.dataset.tip?.trim() || '';
}

function place() {
  const r = target.getBoundingClientRect();
  const t = tip.getBoundingClientRect();
  const gap = 8;
  let top = r.top - t.height - gap;
  if (top < 4) top = r.bottom + gap; // sin sitio arriba: debajo
  let left = r.left + r.width / 2 - t.width / 2;
  left = Math.max(8, Math.min(left, innerWidth - t.width - 8));
  tip.style.top = `${Math.round(top)}px`;
  tip.style.left = `${Math.round(left)}px`;
}

function show(node) {
  const text = textOf(node);
  if (!text) return;
  target = node;
  tip.textContent = text;
  if (!tip.isConnected) document.body.append(tip);
  tip.hidden = false;
  node.setAttribute('aria-describedby', 'tip');
  place();
}

function hide() {
  clearTimeout(timer);
  if (target?.getAttribute('aria-describedby') === 'tip') target.removeAttribute('aria-describedby');
  target = null;
  tip.hidden = true;
}

const owner = (e) => e.target instanceof Element ? e.target.closest('[data-tip], [title]') : null;

document.addEventListener('pointerover', (e) => {
  if (e.pointerType === 'touch') return;
  const node = owner(e);
  if (node === target) return;
  hide();
  if (!node || node.tagName === 'svg' || node.closest('svg')) return;
  textOf(node);
  timer = setTimeout(() => show(node), 250);
});
document.addEventListener('pointerout', (e) => {
  const node = owner(e);
  if (node && !node.contains(e.relatedTarget)) hide();
});
document.addEventListener('focusin', (e) => {
  const node = owner(e);
  if (node && node.matches(':focus-visible')) show(node);
});
document.addEventListener('focusout', hide);
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') hide(); });
document.addEventListener('scroll', hide, true);
document.addEventListener('pointerdown', hide);
