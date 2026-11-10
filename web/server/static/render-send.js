// «Enviar a render»: manda la escena con todo lo que usa a la página de render.
// Antes dice qué se manda y qué falta. Allí se eligen frames y máquinas: desde
// aquí nunca se alquila nada.

const fmtBytes = (n) => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i ? n.toFixed(n < 10 ? 1 : 0) : n} ${units[i]}`.replace('.', ',');
};

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === false || v == null) continue;
    node.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) if (c != null && c !== false) node.append(c);
  return node;
}

async function call(method, url) {
  const res = await fetch(url, { method });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.message || `Error ${res.status} del servidor.`);
  return data;
}

const KINDS = { image: 'Textura', sound: 'Sonido', movie: 'Video', font: 'Fuente', volume: 'Volumen', cache: 'Caché', library: 'Archivo enlazado' };
const issueLine = (i) => el('li', {}, `${KINDS[i.kind] || i.kind}: ${i.path}`);
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

function open(assetId, plan) {
  const body = el('div', { class: 'wide folder-info' });
  const error = el('p', { class: 'sync-error wide', role: 'alert', hidden: true });
  const cancel = el('button', { class: 'btn ghost', type: 'button' }, 'Cancelar');
  const blocked = plan.pending || plan.blocking.length > 0;
  const ok = el('button', { class: 'btn primary', type: 'submit', disabled: blocked }, 'Enviar');
  const form = el('form', { class: 'form-grid' }, el('h2', { class: 'wide' }, 'Enviar a render'), body, error, el('div', { class: 'wide form-foot' }, cancel, ok));
  const box = el('dialog', { class: 'panel folder-dialog' }, form);
  let sending = false;
  const close = () => {
    box.close();
    box.remove();
  };
  cancel.addEventListener('click', close);
  box.addEventListener('cancel', (e) => {
    e.preventDefault();
    if (!sending) close();
  });

  if (plan.pending) {
    body.append(el('p', { class: 'notice warn' }, 'Todavía se está revisando qué archivos usa esta escena. Inténtalo en un momento.'));
  } else if (plan.blocking.length) {
    body.append(el('p', { class: 'notice' }, 'No se puede enviar: faltan archivos enlazados en 3D-Vault. Sin ellos, esos objetos no saldrían en el render. Súbelos y vuelve a intentarlo.',
      el('ul', { class: 'upload-plan' }, plan.blocking.slice(0, 8).map(issueLine))));
  } else {
    const others = plan.files.length - 1;
    body.append(el('p', {}, others
      ? `Se manda la escena con ${others === 1 ? '1 archivo' : `${others} archivos`} que usa (${fmtBytes(plan.bytes)} en total).`
      : `Se manda la escena (${fmtBytes(plan.bytes)}). No usa otros archivos.`));
    body.append(el('p', { class: 'sub' }, 'Después se abre la página de render, donde eliges los frames y las máquinas. No se alquila nada hasta que lo confirmes allí.'));
  }
  if (plan.warnings.length) {
    body.append(el('p', { class: 'notice warn' }, 'Estos archivos no están en 3D-Vault. Se puede renderizar igual, pero no van a estar (una textura que falta se ve rosa):',
      el('ul', { class: 'upload-plan' }, plan.warnings.slice(0, 8).map(issueLine))));
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    ok.disabled = true;
    cancel.disabled = true;
    sending = true;
    error.hidden = true;
    const bar = el('progress', { max: '100', value: '0' });
    const text = el('p', { role: 'status' }, 'Preparando el envío…');
    body.replaceChildren(text, bar);
    const fail = (message) => {
      sending = false;
      error.textContent = message;
      error.hidden = false;
      cancel.disabled = false;
      ok.disabled = false;
    };
    try {
      const { id } = await call('POST', `/api/assets/${assetId}/render`);
      for (;;) {
        await wait(1000);
        const s = await call('GET', `/api/render/${id}`);
        if (s.state === 'error') return fail(s.message);
        if (s.state === 'listo') {
          text.textContent = 'Listo. Abriendo la página de render…';
          bar.value = 100;
          location.href = s.url;
          return;
        }
        const pct = s.total ? Math.min(100, Math.floor((100 * s.done) / s.total)) : 0;
        bar.value = pct;
        text.textContent = `Enviando: ${pct} % (${fmtBytes(s.done)} de ${fmtBytes(s.total)}). No cierres esta página.`;
      }
    } catch (err) {
      fail(err.message);
    }
  });

  document.body.append(box);
  box.showModal();
  ok.focus();
}

document.addEventListener('click', async (e) => {
  const btn = e.target.closest('[data-render-send]');
  if (!btn) return;
  btn.disabled = true;
  try {
    open(btn.dataset.renderSend, await call('GET', `/api/assets/${btn.dataset.renderSend}/render`));
  } catch (err) {
    alert(err.message);
  } finally {
    btn.disabled = false;
  }
});
