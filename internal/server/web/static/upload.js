const form = document.getElementById('upload-form');
const zone = document.getElementById('dropzone');
const directoryInput = document.getElementById('directory');
const summary = document.getElementById('source-summary');
const status = document.getElementById('upload-status');

let selection = null;

function reject(message) {
  selection = null;
  summary.textContent = 'Nothing selected yet.';
  status.textContent = message;
}

function selectDirectory(root, files) {
  if (!root || files.length === 0) {
    reject('That directory is empty.');
    return;
  }
  if (files.some((file) => !file.path || file.path.split('/')[0] !== root)) {
    reject('That directory could not be read.');
    return;
  }
  files.sort((a, b) => a.path.localeCompare(b.path));
  selection = {files: files};
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

zone.addEventListener('click', () => directoryInput.click());

directoryInput.addEventListener('change', () => {
  const files = Array.from(directoryInput.files, (file) => ({path: file.webkitRelativePath, file: file}));
  const root = files.length === 0 ? '' : files[0].path.split('/')[0];
  selectDirectory(root, files);
  directoryInput.value = '';
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
    reject('Drop one skill directory.');
    return;
  }
  const item = items[0];
  const entry = item.webkitGetAsEntry ? item.webkitGetAsEntry() : null;
  if (!entry || !entry.isDirectory) {
    reject('Drop a skill directory (not a ZIP).');
    return;
  }
  const files = [];
  walkEntry(entry, '', files)
    .then(() => selectDirectory(entry.name, files))
    .catch(() => reject('That directory could not be read.'));
});

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  if (!selection) {
    status.textContent = 'Choose or drop a skill directory first.';
    return;
  }
  const data = new FormData();
  data.append('namespace', form.elements.namespace.value);
  const manifest = selection.files.map((file, index) => ({index: index, path: file.path, size: file.file.size}));
  data.append('manifest', JSON.stringify(manifest));
  selection.files.forEach((file, index) => data.append('file-' + index, file.file, file.file.name));
  status.textContent = 'Uploading…';
  const response = await fetch('/candidates', {
    method: 'POST',
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
