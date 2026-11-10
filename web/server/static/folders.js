// Crear y borrar carpetas desde la página de un proyecto. Las mismas acciones
// están en el árbol del menú lateral; esta sirve también en el teléfono.
const form = document.getElementById('web-upload');
const dialog = document.getElementById('folder-dialog');
const project = form?.dataset.project;

if (project && dialog) {
  const name = document.getElementById('folder-name');
  const where = document.getElementById('folder-where');
  const error = document.getElementById('folder-error');
  let parent = '';

  const done = () => {
    document.dispatchEvent(new CustomEvent('3dvault:folders', { detail: { project } }));
    location.reload();
  };

  document.addEventListener('click', async (e) => {
    const add = e.target.closest('[data-page-new-folder]');
    const del = e.target.closest('[data-page-delete-folder]');
    if (add) {
      parent = add.dataset.pageNewFolder.replace(/\/+$/, '');
      where.textContent = parent
        ? `Dentro de ${parent}. Puedes escribir varias, separadas por «/».`
        : 'En la raíz del proyecto. Puedes escribir varias, por ejemplo ASSETS/Vehículos.';
      name.value = '';
      error.hidden = true;
      dialog.showModal();
      name.focus();
    } else if (del) {
      const path = del.dataset.pageDeleteFolder.replace(/\/+$/, '');
      if (!confirm(`¿Borrar la carpeta «${path}»? Está vacía. También se quitará de Mi carpeta en cada PC.`)) return;
      const res = await fetch(`/api/projects/${project}/folders?path=${encodeURIComponent(path)}`, { method: 'DELETE' });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        alert(body.message || `No se pudo borrar (error ${res.status}).`);
        return;
      }
      done();
    }
  });

  dialog.querySelector('[data-close]').addEventListener('click', () => dialog.close());
  dialog.querySelector('form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const typed = name.value.trim().replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
    if (!typed) return;
    const res = await fetch(`/api/projects/${project}/folders`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ path: parent ? `${parent}/${typed}` : typed }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      error.textContent = body.message || `No se pudo crear (error ${res.status}).`;
      error.hidden = false;
      return;
    }
    dialog.close();
    done();
  });
}
