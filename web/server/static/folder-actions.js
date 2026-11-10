// Diálogos para crear, renombrar y borrar carpetas. Los usan el árbol del menú
// lateral y la página del proyecto, para que se comporten igual en los dos.
// Antes de renombrar o borrar, el servidor dice qué pasará: cuántos archivos se
// mueven o van a la papelera, cuáles bloquea otra persona y qué escenas de
// fuera usan algo de dentro (sus enlaces se romperían).

const fmtBytes = (n) => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i ? n.toFixed(n < 10 ? 1 : 0) : n} ${units[i]}`.replace('.', ',');
};
const count = (n, one, many) => `${n} ${n === 1 ? one : many}`;
const parentOf = (path) => (path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '');
const nameOf = (path) => path.slice(path.lastIndexOf('/') + 1);

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === false || v == null) continue;
    node.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) if (c != null && c !== false) node.append(c);
  return node;
}

async function call(method, url, body) {
  const res = await fetch(url, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.message || `Error ${res.status} del servidor.`);
  return data;
}

// dialog muestra un diálogo y resuelve con lo que devuelve onSubmit, o null si
// se cancela. onSubmit puede lanzar un error: se muestra sin cerrar.
function dialog({ title, body, field, submit, danger, blocked, onSubmit }) {
  return new Promise((resolve) => {
    const error = el('p', { class: 'sync-error wide', role: 'alert', hidden: true });
    const ok = el('button', { class: `btn ${danger ? 'danger' : 'primary'}`, type: 'submit', disabled: Boolean(blocked) }, submit);
    const cancel = el('button', { class: 'btn ghost', type: 'button' }, 'Cancelar');
    const form = el('form', { class: 'form-grid' },
      el('h2', { class: 'wide' }, title),
      field && el('div', { class: 'field wide' }, el('label', { for: 'carpeta-nombre' }, field.label), field.input, field.hint && el('span', { class: 'hint' }, field.hint)),
      el('div', { class: 'wide folder-info' }, body),
      error,
      el('div', { class: 'wide form-foot' }, cancel, ok));
    const box = el('dialog', { class: 'panel folder-dialog' }, form);
    const close = (value) => {
      box.close();
      box.remove();
      resolve(value);
    };
    cancel.addEventListener('click', () => close(null));
    box.addEventListener('cancel', (e) => {
      e.preventDefault();
      close(null);
    });
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      ok.disabled = true;
      error.hidden = true;
      try {
        close(await onSubmit());
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
        ok.disabled = false;
      }
    });
    document.body.append(box);
    box.showModal();
    (field?.input || ok).focus();
    if (field?.select) field.input.select();
  });
}

function consequences(info, verb) {
  const out = [];
  if (info.locked.length) {
    out.push(el('p', { class: 'notice' },
      `No se puede ${verb}: ${count(info.locked.length, 'archivo está bloqueado', 'archivos están bloqueados')} por otra persona. Pídele que los libere.`,
      el('ul', { class: 'upload-plan' }, info.locked.slice(0, 5).map((a) => el('li', {}, `${a.path} — ${a.lock.display_name}`)))));
  }
  if (info.linked_from.length) {
    out.push(el('p', { class: 'notice warn' },
      `${count(info.linked_from.length, 'escena de fuera usa', 'escenas de fuera usan')} archivos de esta carpeta. Sus enlaces dejarán de funcionar hasta que alguien los vuelva a enlazar:`,
      el('ul', { class: 'upload-plan' }, info.linked_from.slice(0, 5).map((a) => el('li', {}, a.path)),
        info.linked_from.length > 5 && el('li', {}, `y ${info.linked_from.length - 5} más`))));
  }
  return out;
}

const changed = (project) => document.dispatchEvent(new CustomEvent('3dvault:folders', { detail: { project: String(project) } }));

// newFolder pide un nombre y crea la carpeta dentro de parent ('' = raíz).
export async function newFolder(project, parent = '') {
  const input = el('input', { class: 'input', id: 'carpeta-nombre', maxlength: '500', autocomplete: 'off', required: true });
  const done = await dialog({
    title: 'Nueva carpeta',
    field: { label: 'Nombre', input, hint: parent ? `Dentro de ${parent}. Puedes escribir varias, separadas por «/».` : 'En la raíz del proyecto. Puedes escribir varias, por ejemplo ASSETS/Vehículos.' },
    body: el('p', { class: 'sub' }, 'Aparece al momento en la web y, en unos segundos, en la carpeta de 3D-Vault de cada PC con Mi carpeta abierta.'),
    submit: 'Crear',
    onSubmit: () => {
      const typed = input.value.trim().replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
      return call('POST', `/api/projects/${project}/folders`, { path: parent ? `${parent}/${typed}` : typed });
    },
  });
  if (done) changed(project);
  return done;
}

// renameFolder pide el nombre nuevo de una carpeta y la renombra con todo.
export async function renameFolder(project, path) {
  path = path.replace(/\/+$/, '');
  const info = await call('GET', `/api/projects/${project}/folders/info?path=${encodeURIComponent(path)}`);
  const input = el('input', { class: 'input', id: 'carpeta-nombre', maxlength: '255', autocomplete: 'off', required: true, value: nameOf(path) });
  const parent = parentOf(path);
  const what = info.files
    ? (info.files === 1
      ? 'Su archivo se mueve con ella, con todo su historial. Mi carpeta lo cambia de lugar en cada PC, sin volver a descargarlo.'
      : `Sus ${info.files} archivos se mueven con ella, con todo su historial. Mi carpeta los cambia de lugar en cada PC, sin volver a descargarlos.`)
    : 'La carpeta está vacía.';
  const done = await dialog({
    title: `Renombrar ${nameOf(path)}`,
    field: { label: 'Nombre nuevo', input, select: true, hint: parent ? `Dentro de ${parent}.` : 'En la raíz del proyecto.' },
    body: [el('p', {}, what), ...consequences(info, 'renombrarla')],
    submit: 'Renombrar',
    blocked: info.locked.length > 0,
    onSubmit: () => {
      const name = input.value.trim();
      if (!name || /[\\/]/.test(name)) throw new Error('Escribe solo el nombre, sin barras.');
      return call('POST', `/api/projects/${project}/folders/rename`, { from: path, to: parent ? `${parent}/${name}` : name });
    },
  });
  if (done) changed(project);
  return done;
}

// deleteFolder borra una carpeta. Si tiene archivos, van a la papelera.
export async function deleteFolder(project, path) {
  path = path.replace(/\/+$/, '');
  const info = await call('GET', `/api/projects/${project}/folders/info?path=${encodeURIComponent(path)}`);
  const body = info.files
    ? [el('p', {}, `${info.files === 1 ? 'Se mandará 1 archivo' : `Se mandarán ${info.files} archivos`} (${fmtBytes(info.bytes)}) a la `, el('a', { href: `/projects/${project}/papelera` }, 'Papelera del proyecto'), '. Desde ahí se pueden recuperar con todo su historial. Mi carpeta los quita de cada PC, salvo los que tengan cambios sin subir.')]
    : [el('p', {}, 'La carpeta está vacía. Se quita de la web y de cada PC.')];
  const done = await dialog({
    title: `Borrar ${nameOf(path)}`,
    body: [...body, ...consequences(info, 'borrarla')],
    submit: info.files ? 'Mandar a la papelera' : 'Borrar carpeta',
    danger: true,
    blocked: info.locked.length > 0,
    onSubmit: () => {
      return call('DELETE', `/api/projects/${project}/folders?path=${encodeURIComponent(path)}&files=1`);
    },
  });
  if (done) changed(project);
  return done;
}
