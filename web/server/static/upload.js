// Subir desde la ficha de un archivo o desde un proyecto, en cualquier navegador
// y sin carpeta conectada. Solo envía los trozos que el servidor no tiene.
import { Api, pushFile } from './sync/engine.js';

const form = document.getElementById('web-upload');

function stored(key) {
  try {
    return JSON.parse(localStorage.getItem(key)) || '';
  } catch {
    return '';
  }
}

if (form) {
  const ds = form.dataset;
  // Si el bloqueo es de Mi carpeta en este navegador, se sube como esa PC. Si no,
  // como la web, igual que los botones Bloquear y Liberar de esta página.
  const clientId = stored('3dvault-client-id');
  const api = new Api(clientId && clientId === ds.lockClient ? clientId : '', stored('3dvault-client-name'));
  const status = form.querySelector('[data-status]');
  const button = form.querySelector('button[type=submit]');

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const files = [...form.elements.file.files];
    const comment = form.elements.comment.value.trim();
    const folder = (form.elements.folder?.value || '').trim().replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
    button.disabled = true;
    status.classList.remove('sync-error');
    try {
      for (const [i, file] of files.entries()) {
        const label = files.length > 1 ? `${file.name} (${i + 1} de ${files.length})` : file.name;
        await pushFile(api, file, {
          assetId: ds.asset ? Number(ds.asset) : undefined,
          baseVersion: Number(ds.base),
          projectId: Number(ds.project),
          path: folder ? `${folder}/${file.name}` : file.name,
          comment,
          progress: (action, done, total) => {
            status.textContent = `${label}: ${action.toLowerCase()} ${total ? Math.floor((100 * done) / total) : 100} %`;
          },
        });
      }
      status.textContent = 'Listo.';
      location.reload();
    } catch (err) {
      status.textContent = err.message;
      status.classList.add('sync-error');
      button.disabled = false;
    }
  });
}
