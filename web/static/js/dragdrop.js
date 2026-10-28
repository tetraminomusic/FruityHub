// Перетаскивание папок проектов из Finder / Проводника в окно.

// Окно не знает, откуда папку перетащили, поэтому FruityHub ищет её на диске по имени и содержимому.
async function readDropped(dt) {
  const out = [];
  for (const it of [...(dt.items || [])]) {
    const en = it.webkitGetAsEntry ? it.webkitGetAsEntry() : null;
    if (!en) continue;
    if (en.isDirectory) {
      const names = await new Promise(res => {
        en.createReader().readEntries(
          es => res(es.map(e => e.name)),
          () => res([])
        );
      });
      out.push({ name: en.name, children: names.slice(0, 200), dir: true });
    } else out.push({ name: en.name, dir: false });
  }
  return out;
}
function hasFiles(e) {
  return [...(e.dataTransfer?.types || [])].includes('Files');
}
function bindFolderDrop(el, plugins, onPaths) {
  if (!el) return;
  // папку проекта можно бросить в любое место окна — рамка на всё окно, пока тащишь
  let hint = null,
    hintT = 0;
  const showHint = () => {
    if (plugins) return;
    if (!hint) {
      hint = document.createElement('div');
      hint.className = 'dragall';
      hint.textContent = 'Отпусти — папка добавится как проект';
      document.body.appendChild(hint);
    }
    clearTimeout(hintT);
    hintT = setTimeout(hideHint, 250);
  };
  const hideHint = () => {
    if (hint) {
      hint.remove();
      hint = null;
    }
  };
  el.addEventListener('dragover', e => {
    if (hasFiles(e) && (plugins || !topModal())) {
      e.preventDefault();
      e.stopPropagation();
      el.classList.add('dropping');
      showHint();
    }
  });
  el.addEventListener('dragleave', e => {
    if (e.target === el) el.classList.remove('dropping');
  });
  el.addEventListener('drop', async e => {
    if (!hasFiles(e) || (!plugins && topModal())) return;
    e.preventDefault();
    e.stopPropagation();
    el.classList.remove('dropping');
    hideHint();
    const items = await readDropped(e.dataTransfer);
    const dirs = items.filter(x => x.dir);
    if (!dirs.length) {
      toast('Перетащи папку (не файл)');
      return;
    }
    const busy = plugins ? null : busyOverlay('Добавляю «' + dirs[0].name + '»…', 'Ищу папку на диске');
    const t = plugins ? toast('Ищу папку «' + esc(dirs[0].name) + '» на диске…', 15000) : null;
    const paths = [];
    try {
      for (const d of dirs) {
        busy && busy.set('Добавляю «' + d.name + '»…', 'Ищу папку на диске');
        let found = [];
        try {
          found = await api('/api/locate-dropped', { name: d.name, children: d.children, plugins });
        } catch (er) {}
        if (found.length === 1) paths.push(found[0]);
        else if (found.length > 1) {
          busy && busy.hide();
          const p = await choosePath(d.name, found);
          busy && busy.show();
          if (p) paths.push(p);
        } else {
          t && t.remove();
          busy && busy.hide();
          if (
            await uiConfirm(`Не нашёл папку «${esc(d.name)}» на диске. Выбрать её вручную?`, {
              title: 'Папка не найдена',
              ok: 'Выбрать…',
            })
          ) {
            const r = await api('/api/pickfolder', {}).catch(() => ({}));
            if (r.path) paths.push(r.path);
          }
          busy && busy.show();
        }
      }
      t && t.remove();
      if (paths.length) {
        busy &&
          busy.set(
            paths.length > 1
              ? `Добавляю проекты (${paths.length})…`
              : 'Добавляю «' + paths[0].split(/[\\/]/).pop() + '»…',
            'Разбираю .flp, сэмплы и плагины'
          );
        await onPaths(paths);
      }
    } finally {
      busy && busy.close();
    }
  });
}
function choosePath(name, list) {
  return new Promise(res => {
    let v = null;
    const m = modal(
      `<h2>Какая из папок «${esc(name)}»?</h2>${list.map((p, i) => `<label class="radio"><input type="radio" name="cp" value="${i}" ${i ? '' : 'checked'}><span class="mono">${esc(p)}</span></label>`).join('')}
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="no">Отмена</button><button class="primary" id="ok">Эта</button></div>`,
      () => res(v)
    );
    m.querySelector('#ok').onclick = () => {
      v = list[+m.querySelector('input[name=cp]:checked').value];
      m.close();
    };
    m.querySelector('#no').onclick = () => m.close();
  });
}
async function addProjectPaths(paths) {
  let last = null;
  for (const path of paths) {
    try {
      await api('/api/projects/add', { path });
      last = path;
    } catch (e) {
      fail(e);
    }
  }
  if (last) {
    S.sel = last;
    S.analysis = null;
    S.comments = null;
    await refreshAll();
    toast(paths.length > 1 ? `Добавлено проектов: ${paths.length}` : 'Проект добавлен');
  }
}

function initDragDrop() {
  bindFolderDrop(document.querySelector('.wrap'), false, addProjectPaths);
  // чтобы случайно брошенный файл не открылся в окне вместо приложения
  window.addEventListener('dragover', e => {
    if (hasFiles(e)) e.preventDefault();
  });
  window.addEventListener('drop', e => {
    if (hasFiles(e)) e.preventDefault();
  });
}
