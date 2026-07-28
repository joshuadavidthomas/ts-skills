const form = document.getElementById('upload-form');
const archiveInput = document.getElementById('archive');
const directoryInput = document.getElementById('directory');
const summary = document.getElementById('source-summary');
const status = document.getElementById('upload-status');

function formatSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

function clearSelection() {
  archiveInput.value = '';
  directoryInput.value = '';
  summary.textContent = 'Nothing selected yet.';
}

document.getElementById('choose-zip').addEventListener('click', () => archiveInput.click());
document.getElementById('choose-directory').addEventListener('click', () => directoryInput.click());

archiveInput.addEventListener('change', () => {
  const file = archiveInput.files[0];
  if (!file) return;
  if (!file.name.toLowerCase().endsWith('.zip')) {
    clearSelection();
    status.textContent = 'That file is not a ZIP archive.';
    return;
  }
  directoryInput.value = '';
  status.textContent = '';
  summary.textContent = 'ZIP archive: ' + file.name + ' (' + formatSize(file.size) + ')';
});

directoryInput.addEventListener('change', () => {
  const files = directoryInput.files;
  if (files.length === 0) return;
  archiveInput.value = '';
  status.textContent = '';
  const root = files[0].webkitRelativePath.split('/')[0];
  summary.textContent = 'Directory: ' + root + '/ (' + files.length + (files.length === 1 ? ' file)' : ' files)');
});

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  const data = new FormData();
  data.append('namespace', form.elements.namespace.value);
  if (archiveInput.files.length === 1) {
    const archive = archiveInput.files[0];
    data.append('kind', 'zip');
    data.append('archive', archive, archive.name);
  } else if (directoryInput.files.length > 0) {
    const files = Array.from(directoryInput.files)
      .sort((a, b) => a.webkitRelativePath.localeCompare(b.webkitRelativePath));
    data.append('kind', 'directory');
    const manifest = files.map((file, index) => ({index: index, path: file.webkitRelativePath, size: file.size}));
    data.append('manifest', JSON.stringify(manifest));
    files.forEach((file, index) => data.append('file-' + index, file, file.name));
  } else {
    status.textContent = 'Choose a ZIP archive or a directory first.';
    return;
  }
  status.textContent = 'Uploading…';
  const response = await fetch('/candidates', {
    method: 'POST',
    headers: {'X-CSRF-Token': document.querySelector('meta[name="csrf-token"]').content},
    body: data,
  });
  if (response.redirected) {
    window.location = response.url;
    return;
  }
  document.open();
  document.write(await response.text());
  document.close();
});
