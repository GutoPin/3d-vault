// Ponerme al día: marca cada paso apenas el servidor lo detecta (Mi carpeta
// abierta con la versión nueva, Blender conectado con el complemento nuevo) y
// genera el código de conexión de Blender sin pasar por Mi carpeta.

const $ = (id) => document.getElementById(id);

const STATES = {
  done: 'Listo',
  old: 'Versión vieja',
  todo: 'Pendiente',
};

function paint(st) {
  const steps = { web: st.web, blender: st.blender, ack: st.ack };
  for (const [name, ok] of Object.entries(steps)) {
    const li = document.querySelector(`[data-step="${name}"]`);
    if (!li) continue;
    li.classList.toggle('done', ok);
    li.classList.toggle('old', name === 'blender' && !ok && st.blender_old);
    const state = li.querySelector('[data-state]');
    const kind = ok ? 'done' : name === 'blender' && st.blender_old ? 'old' : 'todo';
    state.textContent = STATES[kind];
    state.className = `setup-state pill ${kind === 'done' ? 'pos' : 'warn'}`;
    const detail = li.querySelector('[data-detail]');
    if (detail && name !== 'ack') detail.textContent = name === 'web' ? st.web_detail : st.blender_detail;
  }
  $('ack').hidden = st.ack;
  $('setup-count').textContent = st.done ? 'Todo listo' : `${3 - st.left} de 3 listos`;
  $('setup-done').hidden = !st.done;
}

async function refresh() {
  try {
    const res = await fetch('/api/setup', { cache: 'no-store' });
    if (res.ok) paint(await res.json());
  } catch { /* sin red: se reintenta */ }
}

// Cada 4 s mientras la página se ve: así el paso se marca al momento.
refresh();
setInterval(() => document.visibilityState === 'visible' && refresh(), 4000);
document.addEventListener('visibilitychange', () => document.visibilityState === 'visible' && refresh());

$('ack').addEventListener('click', async () => {
  $('ack').disabled = true;
  await fetch('/api/setup/entendido', { method: 'POST' }).catch(() => {});
  $('ack').disabled = false;
  refresh();
});

// El código usa la identidad de Mi carpeta en este navegador, igual que el botón
// de Mi carpeta: así un bloqueo hecho en Blender vale también allí.
function read(key) {
  try {
    return JSON.parse(localStorage.getItem(key));
  } catch {
    return null;
  }
}

$('gen-code').addEventListener('click', async () => {
  const msg = $('code-msg');
  const clientId = read('3dvault-client-id');
  if (!clientId) {
    msg.textContent = 'Primero abre Mi carpeta en este navegador (paso 1) y vuelve a pulsar Generar código.';
    return;
  }
  const platform = navigator.userAgentData?.platform || navigator.platform || 'PC';
  const pc = read('3dvault-client-name') || `Navegador en ${platform}`;
  $('gen-code').disabled = true;
  try {
    const res = await fetch('/api/tokens', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: `Blender · ${pc}` }) });
    if (!res.ok) throw new Error((await res.json().catch(() => ({}))).message || `error ${res.status}`);
    const { token } = await res.json();
    const data = { s: location.origin, t: token, c: clientId, n: pc };
    const raw = btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(data))));
    $('code').value = `3DV1:${raw.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`;
    $('code').hidden = $('copy-code').hidden = false;
    $('code').select();
    msg.textContent = 'Código listo. Cópialo y pégalo en Blender (siguiente punto).';
  } catch (err) {
    msg.textContent = `No se pudo generar el código: ${err.message}. Recarga la página e inténtalo de nuevo.`;
  } finally {
    $('gen-code').disabled = false;
  }
});

$('copy-code').addEventListener('click', async () => {
  await navigator.clipboard.writeText($('code').value).catch(() => $('code').select());
  $('code-msg').textContent = 'Copiado. Ahora pégalo en Blender: Complementos → 3D-Vault → Código de conexión → Probar conexión.';
});

// Instalar Mi carpeta como aplicación, si el navegador lo ofrece.
let installPrompt = null;
addEventListener('beforeinstallprompt', (e) => {
  e.preventDefault();
  installPrompt = e;
  $('install-app').hidden = false;
});
$('install-app').addEventListener('click', async () => {
  if (!installPrompt) return;
  installPrompt.prompt();
  await installPrompt.userChoice.catch(() => {});
  installPrompt = null;
  $('install-app').hidden = true;
});
