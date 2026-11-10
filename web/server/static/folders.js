// Carpetas desde la página de un proyecto: crear, renombrar y borrar. Los
// diálogos son los mismos que en el árbol del menú (folder-actions.js).
import { deleteFolder, newFolder, renameFolder } from './folder-actions.js';

const project = document.getElementById('web-upload')?.dataset.project;

if (project) {
  document.addEventListener('click', async (e) => {
    const add = e.target.closest('[data-page-new-folder]');
    const ren = e.target.closest('[data-rename-folder]');
    const del = e.target.closest('[data-delete-folder]');
    if (!add && !ren && !del) return;
    try {
      const done = add ? await newFolder(project, add.dataset.pageNewFolder.replace(/\/+$/, ''))
        : ren ? await renameFolder(project, ren.dataset.renameFolder)
          : await deleteFolder(project, del.dataset.deleteFolder);
      if (done) location.reload();
    } catch (err) {
      alert(err.message);
    }
  });
}
