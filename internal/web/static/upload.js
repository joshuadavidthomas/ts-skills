const form = document.getElementById('upload-form');
const zone = document.getElementById('dropzone');
const archiveInput = document.getElementById('archive');
const summary = document.getElementById('source-summary');
const status = document.getElementById('upload-status');

let selection = null;

function formatSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

function reject(message) {
  selection = null;
  summary.textContent = 'Nothing selected yet.';
  status.textContent = message;
}

function selectZIP(file) {
  if (!file.name.toLowerCase().endsWith('.zip')) {
    reject('That is not a ZIP archive or a directory.');
    return;
  }
  selection = {kind: 'zip', file: file};
  status.textContent = '';
  summary.textContent = 'ZIP archive: ' + file.name + ' (' + formatSize(file.size) + ')';
}

function selectDirectory(root, files) {
  if (files.length === 0) {
    reject('That directory is empty.');
    return;
  }
  files.sort((a, b) => a.path.localeCompare(b.path));
  selection = {kind: 'directory', files: files};
  status.textContent = '';
  summary.textContent = 'Directory: ' + root + '/ (' + files.length + (files.length === 1 ? ' file)' : ' files)');
}

function walkEntry(entry, prefix, out) {
  if (entry.isFile) {
    return new Promise((resolve, reject) => entry.file(resolve, reject))
      .then((file) => { out.push({path: prefix + entry.name, file: file}); });
  }
  if (!entry.isDirectory) return Promise.resolve();
  const reader = entry.createReader();
  const readAll = () => new Promise((resolve, reject) => reader.readEntries(resolve, reject))
    .then((batch) => {
      if (batch.length === 0) return;
      return batch.reduce(
        (chain, child) => chain.then(() => walkEntry(child, prefix + entry.name + '/', out)),
        Promise.resolve(),
      ).then(readAll);
    });
  return readAll();
}

zone.addEventListener('click', () => archiveInput.click());

archiveInput.addEventListener('change', () => {
  if (archiveInput.files.length === 1) selectZIP(archiveInput.files[0]);
  archiveInput.value = '';
});

['dragover', 'dragenter'].forEach((name) => zone.addEventListener(name, (event) => {
  event.preventDefault();
  zone.classList.add('border-blue-500', 'bg-gray-100');
}));
['dragleave', 'drop'].forEach((name) => zone.addEventListener(name, () => {
  zone.classList.remove('border-blue-500', 'bg-gray-100');
}));

zone.addEventListener('drop', (event) => {
  event.preventDefault();
  const items = event.dataTransfer.items;
  if (items.length !== 1) {
    reject('Drop one ZIP archive or one directory.');
    return;
  }
  const item = items[0];
  const entry = item.webkitGetAsEntry ? item.webkitGetAsEntry() : null;
  if (entry && entry.isDirectory) {
    const files = [];
    walkEntry(entry, '', files)
      .then(() => selectDirectory(entry.name, files))
      .catch(() => reject('That directory could not be read.'));
    return;
  }
  const file = item.getAsFile();
  if (!file) {
    reject('Drop one ZIP archive or one directory.');
    return;
  }
  selectZIP(file);
});

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  if (!selection) {
    status.textContent = 'Choose a ZIP archive or drop a directory first.';
    return;
  }
  const data = new FormData();
  data.append('namespace', form.elements.namespace.value);
  if (selection.kind === 'zip') {
    data.append('archive', selection.file, selection.file.name);
  } else {
    const manifest = selection.files.map((file, index) => ({index: index, path: file.path, size: file.file.size}));
    data.append('manifest', JSON.stringify(manifest));
    selection.files.forEach((file, index) => data.append('file-' + index, file.file, file.file.name));
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
