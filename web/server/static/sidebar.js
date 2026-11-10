// Árbol de carpetas y archivos de cada proyecto en el menú lateral, como el
// explorador de un editor de código. Se carga al desplegar un proyecto, desde la
// misma foto que usa Mi carpeta (/api/projects/{id}/snapshot), y permite crear y
// borrar carpetas. Mi carpeta las crea o borra también en el disco de cada PC.
//
// Qué está desplegado se recuerda en este navegador (localStorage).

import { deleteFolder, renameFolder } from './folder-actions.js';

const STORE = '3dvault-arbol';
const REFRESH_MS = 30000;
const collator = new Intl.Collator('es', { sensitivity: 'base', numeric: true });

const keyOf = (path) => path.normalize('NFC').toLowerCase();

function loadOpen() {
  try {
    return new Set(JSON.parse(localStorage.getItem(STORE)) || []);
  } catch {
    return new Set();
  }
}
const open = loadOpen();
function saveOpen() {
  try {
    localStorage.setItem(STORE, JSON.stringify([...open]));
  } catch {
    // Sin almacenamiento: el árbol funciona, solo no se recuerda.
  }
}
const openKey = (project, folderKey) => `${project}:${folderKey}`;

const svg = (paths) => {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  s.setAttribute('viewBox', '0 0 16 16');
  s.setAttribute('aria-hidden', 'true');
  for (const d of paths) {
    const p = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    p.setAttribute('d', d);
    s.append(p);
  }
  return s;
};
const ICON = {
  twisty: ['M6 4l4 4-4 4'],
  folder: ['M2 4.5h4l1.5 1.5H14v6.5H2z'],
  file: ['M4 2h5l3 3v9H4z', 'M9 2v3h3'],
  add: ['M2 4.5h4l1.5 1.5H14v6.5H2z', 'M8 7.5v4M6 9.5h4'],
  trash: ['M3 4.5h10', 'M6.5 4.5V3h3v1.5', 'M4.5 4.5l.7 9h5.6l.7-9'],
  rename: ['M3 13l.6-2.6L10.5 3.5l2 2-6.9 6.9z', 'M9.5 4.5l2 2'],
};

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === false || v == null) continue;
    // La CSP no deja usar el atributo style; la profundidad va por CSSOM.
    if (k === 'depth') node.style.setProperty('--d', v);
    else node.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) if (c != null && c !== false) node.append(c);
  return node;
}

// buildTree arma las carpetas: las creadas a propósito y las que salen de las
// rutas de los archivos.
function buildTree(snap) {
  const root = { name: '', path: '', key: '', folders: new Map(), files: [], count: 0 };
  const folderAt = (path) => {
    let node = root;
    for (const part of path.split('/')) {
      const k = keyOf(part);
      if (!node.folders.has(k)) {
        const p = node.path ? `${node.path}/${part}` : part;
        node.folders.set(k, { name: part, path: p, key: keyOf(p), folders: new Map(), files: [], count: 0, parent: node });
      }
      node = node.folders.get(k);
    }
    return node;
  };
  for (const f of snap.folders || []) folderAt(f.path);
  for (const a of snap.assets) {
    const cut = a.path.lastIndexOf('/');
    const node = cut < 0 ? root : folderAt(a.path.slice(0, cut));
    node.files.push(a);
    for (let n = node; n; n = n.parent) n.count++;
  }
  return root;
}

class ProjectTree {
  constructor(box) {
    this.box = box;
    this.id = box.dataset.project;
    this.twisty = box.querySelector('.tree-twisty');
    this.list = box.querySelector('.nav-tree');
    this.root = null;
    this.loadedAt = 0;
    this.twisty.addEventListener('click', () => this.toggle());
  }

  get expanded() {
    return this.twisty.getAttribute('aria-expanded') === 'true';
  }

  async toggle(force) {
    const want = force ?? !this.expanded;
    this.twisty.setAttribute('aria-expanded', String(want));
    this.list.hidden = !want;
    if (want) open.add(openKey(this.id, '')); else open.delete(openKey(this.id, ''));
    saveOpen();
    if (want && !this.root) await this.load();
  }

  async load() {
    if (!this.root) this.list.replaceChildren(el('p', { class: 'tree-note' }, 'Cargando…'));
    try {
      const res = await fetch(`/api/projects/${this.id}/snapshot`, { headers: { Accept: 'application/json' } });
      if (!res.ok) throw new Error(`Error ${res.status}`);
      this.root = buildTree(await res.json());
      this.loadedAt = Date.now();
      this.render();
    } catch (err) {
      if (!this.root) this.list.replaceChildren(el('p', { class: 'tree-note' }, `No se pudo cargar: ${err.message}`));
    }
  }

  render() {
    const focused = document.activeElement?.closest?.('[data-path]')?.dataset.path;
    const ul = el('ul', { class: 'tree-list', role: 'group' });
    this.fill(ul, this.root, 1);
    if (!this.root.folders.size && !this.root.files.length) ul.append(el('li', { class: 'tree-note' }, 'Sin archivos todavía.'));
    this.list.replaceChildren(ul);
    if (focused != null) this.list.querySelector(`[data-path="${CSS.escape(focused)}"] .tree-name`)?.focus();
  }

  fill(ul, node, depth) {
    const folders = [...node.folders.values()].sort((a, b) => collator.compare(a.name, b.name));
    const files = [...node.files].sort((a, b) => collator.compare(a.path, b.path));
    for (const f of folders) ul.append(this.folderRow(f, depth));
    const current = currentAsset();
    for (const a of files) {
      const name = a.path.slice(a.path.lastIndexOf('/') + 1);
      ul.append(el('li', {},
        el('a', {
          class: 'tree-row tree-file', href: `/assets/${a.id}`, depth: String(depth), title: a.path,
          'aria-current': String(a.id) === current?.id ? 'page' : false,
        }, svg(ICON.file), el('span', { class: 'tree-label' }, name), a.lock ? el('span', { class: 'tree-lock', title: `Bloqueado por ${a.lock.display_name}` }, '●') : null)));
    }
  }

  folderRow(f, depth) {
    const isOpen = open.has(openKey(this.id, f.key));
    const children = el('ul', { class: 'tree-list', role: 'group', hidden: !isOpen });
    const name = el('button', { class: 'tree-name', type: 'button', 'aria-expanded': String(isOpen), title: f.path },
      el('span', { class: 'tree-twisty-icon' }, f.folders.size || f.files.length ? svg(ICON.twisty) : null), svg(ICON.folder), el('span', { class: 'tree-label' }, f.name),
      f.count ? el('span', { class: 'tree-count' }, String(f.count)) : el('span', { class: 'tree-count' }, 'vacía'));
    const actions = el('span', { class: 'tree-actions' },
      el('button', { class: 'tree-act', type: 'button', 'data-new-folder': f.path, title: `Nueva carpeta dentro de ${f.name}`, 'aria-label': `Nueva carpeta dentro de ${f.name}` }, svg(ICON.add)),
      el('button', { class: 'tree-act', type: 'button', 'data-rename-folder': f.path, title: `Renombrar ${f.name}`, 'aria-label': `Renombrar la carpeta ${f.name}` }, svg(ICON.rename)),
      el('button', { class: 'tree-act', type: 'button', 'data-delete-folder': f.path, title: `Borrar ${f.name}`, 'aria-label': `Borrar la carpeta ${f.name}` }, svg(ICON.trash)));
    const li = el('li', { 'data-path': f.path }, el('div', { class: 'tree-row tree-folder', depth: String(depth) }, name, actions), children);
    let filled = false;
    const fillOnce = () => {
      if (!filled) this.fill(children, f, depth + 1);
      filled = true;
    };
    if (isOpen) fillOnce();
    name.addEventListener('click', () => {
      const now = name.getAttribute('aria-expanded') !== 'true';
      name.setAttribute('aria-expanded', String(now));
      children.hidden = !now;
      if (now) {
        fillOnce();
        open.add(openKey(this.id, f.key));
      } else {
        open.delete(openKey(this.id, f.key));
      }
      saveOpen();
    });
    return li;
  }

  // newFolder muestra un campo para el nombre dentro de parent ('' = raíz).
  async newFolder(parent) {
    await this.toggle(true);
    if (parent) {
      for (let p = parent; p; p = p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '') open.add(openKey(this.id, keyOf(p)));
      saveOpen();
      this.render();
    }
    this.list.querySelector('.tree-new')?.remove();
    const host = parent ? this.list.querySelector(`[data-path="${CSS.escape(parent)}"] > ul`) : this.list.querySelector('.tree-list');
    if (!host) return;
    const depth = parent ? parent.split('/').length + 1 : 1;
    const input = el('input', { class: 'input tree-input', name: 'nombre', 'aria-label': parent ? `Nombre de la carpeta nueva en ${parent}` : 'Nombre de la carpeta nueva', placeholder: 'Nombre de la carpeta', maxlength: '255', autocomplete: 'off' });
    const msg = el('p', { class: 'tree-error', role: 'alert', hidden: true });
    const form = el('form', { class: 'tree-row', depth: String(depth) }, svg(ICON.folder), input);
    const li = el('li', { class: 'tree-new' }, form, msg);
    host.hidden = false;
    host.prepend(li);
    input.focus();
    const cancel = () => li.remove();
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') cancel();
    });
    input.addEventListener('blur', () => {
      if (!input.value.trim()) setTimeout(cancel, 150);
    });
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const name = input.value.trim();
      if (!name) return cancel();
      if (/[\\/]/.test(name)) {
        msg.textContent = 'Escribe solo el nombre, sin barras.';
        msg.hidden = false;
        return;
      }
      input.disabled = true;
      const path = parent ? `${parent}/${name}` : name;
      try {
        const res = await fetch(`/api/projects/${this.id}/folders`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ path }),
        });
        const body = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(body.message || `Error ${res.status}`);
        open.add(openKey(this.id, keyOf(body.folder.path)));
        saveOpen();
        await this.load();
        this.list.querySelector(`[data-path="${CSS.escape(body.folder.path)}"] .tree-name`)?.focus();
        document.dispatchEvent(new CustomEvent('3dvault:folders', { detail: { project: this.id } }));
      } catch (err) {
        msg.textContent = err.message;
        msg.hidden = false;
        input.disabled = false;
        input.focus();
      }
    });
  }

  // afterChange vuelve a cargar el árbol y, si la página muestra este proyecto,
  // también la página: una carpeta renombrada cambia sus rutas.
  async afterChange() {
    if (this.box.hasAttribute('data-active')) {
      location.reload();
      return;
    }
    await this.load();
  }
}

function currentAsset() {
  const h = document.querySelector('[data-current-asset]');
  return h ? { id: h.dataset.currentAsset, path: h.dataset.currentPath } : null;
}

const trees = new Map();
for (const box of document.querySelectorAll('.nav-project')) trees.set(box.dataset.project, new ProjectTree(box));

// Al abrir: los proyectos que estaban desplegados, y el actual con la carpeta
// del archivo que se está viendo.
const current = currentAsset();
for (const [id, tree] of trees) {
  const active = tree.box.hasAttribute('data-active');
  if (active && current) {
    const parts = current.path.split('/').slice(0, -1);
    for (let i = 1; i <= parts.length; i++) open.add(openKey(id, keyOf(parts.slice(0, i).join('/'))));
  }
  if (open.has(openKey(id, '')) || (active && current)) tree.toggle(true);
}

document.querySelector('.sidebar')?.addEventListener('click', async (e) => {
  const add = e.target.closest('[data-new-folder]');
  const ren = e.target.closest('[data-rename-folder]');
  const del = e.target.closest('[data-delete-folder]');
  const tree = trees.get((add || ren || del)?.closest('.nav-project')?.dataset.project);
  if (!tree) return;
  e.preventDefault();
  e.stopPropagation();
  if (add) return tree.newFolder(add.dataset.newFolder);
  try {
    const done = ren ? await renameFolder(tree.id, ren.dataset.renameFolder) : await deleteFolder(tree.id, del.dataset.deleteFolder);
    if (done) await tree.afterChange();
  } catch (err) {
    alert(err.message);
  }
});

// Lo que cambian otros (o Mi carpeta en otra PC) aparece al volver a la pestaña
// y cada medio minuto.
const refresh = (minAge) => {
  if (document.hidden) return;
  for (const tree of trees.values()) {
    if (tree.expanded && tree.root && Date.now() - tree.loadedAt > minAge && !tree.list.querySelector('.tree-new')) tree.load();
  }
};
document.addEventListener('visibilitychange', () => refresh(5000));
setInterval(() => refresh(REFRESH_MS - 1000), REFRESH_MS);

// La página del proyecto también puede crear y borrar carpetas.
document.addEventListener('3dvault:folders', (e) => trees.get(String(e.detail.project))?.load());
export { trees };
