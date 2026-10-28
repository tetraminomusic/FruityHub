// Список проектов слева: цвета, сортировка, папки, контекстные меню, выбор проекта.

// цвет точки: занят ли другом, есть ли новое на GitHub, есть ли твои неотправленные изменения, всё ли актуально
function projState(p) {
  const n = S.notes[p.path] || {};
  if (p.lock && p.lock.user !== S.state.me) return ['other', 'в работе у ' + p.lock.user + ' — не открывай для правки'];
  const mine = p.lock && p.lock.user === S.state.me;
  if (!p.isRepo || n.remote === false) return ['none', 'не синхронизируется (нет репозитория на GitHub)'];
  const local = n.changed > 0 || n.ahead > 0,
    behind = n.behind > 0;
  const tail = mine ? ' · в работе у тебя' : '';
  if (local && behind)
    return ['both', 'есть и твои неотправленные изменения, и новое на GitHub — сначала «Получить»' + tail];
  if (behind) return ['behind', 'на GitHub есть новое — нажми «Получить»' + tail];
  if (local)
    return ['local', 'у тебя есть неотправленные изменения' + (n.changed ? ' (' + n.changed + ' файлов)' : '') + tail];
  if (n.checked === undefined) return ['ok', 'свободен' + tail];
  return ['ok', 'актуально: у тебя последняя версия' + tail];
}
function renderLegend() {
  const el = $('#legend');
  if (!el) return;
  el.innerHTML = [
    ['ok', 'актуально'],
    ['local', 'есть твои изменения'],
    ['behind', 'на GitHub новое'],
    ['both', 'и то и другое'],
    ['other', 'занят другом'],
    ['none', 'без GitHub'],
  ]
    .map(([c, t]) => `<span><span class="dot ${c}"></span>${t}</span>`)
    .join('');
}
function projItem(p) {
  const [cls, tip] = projState(p),
    mine = p.lock && p.lock.user === S.state.me;
  const n = S.notes[p.path] || {};
  const badges =
    (n.unread ? `<span class="badge" title="новые комментарии">${n.unread}</span>` : '') +
    (n.behind > 0 ? `<span class="badge" title="на сервере новое">⤓</span>` : '');
  return `<div class="pitem ${S.sel === p.path ? 'sel' : ''}" draggable="true" data-path="${esc(p.path)}" title="${esc(p.path)}${p.modified ? '\nизменён ' + esc(fmtTime(p.modified)) : ''}" onclick="selectProject('${enc(p.path)}')">
    <span class="dot ${cls}${mine ? ' mine' : ''}" title="${esc(tip)}"></span><span class="nn"><span class="n">${esc(p.name)}</span>${p.artist ? `<span class="art">${esc(p.artist)}</span>` : ''}</span>${badges}${p.exists ? '' : '<span class="tag bad">нет папки</span>'}
    <span class="more" onclick="event.stopPropagation();projMenu(event,'${enc(p.path)}')">⋯</span></div>`;
}
const KEYS = [
  'C',
  'Cm',
  'C#',
  'C#m',
  'D',
  'Dm',
  'D#',
  'D#m',
  'E',
  'Em',
  'F',
  'Fm',
  'F#',
  'F#m',
  'G',
  'Gm',
  'G#',
  'G#m',
  'A',
  'Am',
  'A#',
  'A#m',
  'B',
  'Bm',
];
const SORTS = [
  ['manual', 'как добавлены'],
  ['name', 'по названию А–Я'],
  ['name-desc', 'по названию Я–А'],
  ['modified', 'по дате изменения — новые сверху'],
  ['modified-asc', 'по дате изменения — старые сверху'],
];
const SORT_SHORT = {
  manual: 'как добавлены',
  name: 'А–Я',
  'name-desc': 'Я–А',
  modified: 'сначала свежие',
  'modified-asc': 'сначала давние',
};
function sortMode() {
  return S.state?.config?.sortMode || 'manual';
}
function sortProjects(list) {
  const m = sortMode(),
    byName = (a, b) => a.name.localeCompare(b.name, 'ru', { sensitivity: 'base', numeric: true });
  const l = [...list];
  if (m === 'name') l.sort(byName);
  else if (m === 'name-desc') l.sort((a, b) => byName(b, a));
  else if (m === 'modified') l.sort((a, b) => (b.modified || 0) - (a.modified || 0) || byName(a, b));
  else if (m === 'modified-asc') l.sort((a, b) => (a.modified || 0) - (b.modified || 0) || byName(a, b));
  return l;
}
// папки: по названию — по своему имени; по дате — по самому свежему (или самому старому) проекту внутри; пустые — в конце
function sortGroups(groups, ps) {
  const m = sortMode(),
    byName = (a, b) => a.name.localeCompare(b.name, 'ru', { sensitivity: 'base', numeric: true });
  const l = [...groups];
  if (m === 'name') l.sort(byName);
  else if (m === 'name-desc') l.sort((a, b) => byName(b, a));
  else if (m.startsWith('modified')) {
    const key = g => {
      const t = ps.filter(p => p.group === g.id).map(p => p.modified || 0);
      return t.length ? (m === 'modified' ? Math.max(...t) : Math.min(...t)) : null;
    };
    l.sort((a, b) => {
      const ka = key(a),
        kb = key(b);
      if (ka === null || kb === null) return (ka === null) - (kb === null) || byName(a, b);
      return (m === 'modified' ? kb - ka : ka - kb) || byName(a, b);
    });
  }
  return l;
}
function renderSortBar() {
  const el = $('#sortbar');
  if (!el) return;
  const ps = S.state?.projects || [];
  if (ps.length < 2) {
    el.innerHTML = '';
    return;
  }
  el.innerHTML = `Сортировка: <a onclick="sortMenu(event)">${esc(SORT_SHORT[sortMode()] || SORT_SHORT.manual)}</a>`;
}
function sortMenu(ev) {
  popMenu(
    ev,
    SORTS.map(([k, t]) => [(k === sortMode() ? '✓ ' : '') + t, () => setSort(k)])
  );
}
async function setSort(k) {
  S.state.config.sortMode = k;
  renderList();
  try {
    await api('/api/settings', { sortMode: k });
  } catch (e) {
    fail(e);
  }
}
// общая длительность треков в папке списка (у кого она указана во вкладке «Трек»)
function fmtLen(sec) {
  const h = Math.floor(sec / 3600),
    m = Math.floor((sec % 3600) / 60),
    s = sec % 60;
  return h ? `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}` : `${m}:${String(s).padStart(2, '0')}`;
}
function groupLen(list) {
  const known = list.filter(p => p.length > 0);
  if (!known.length) return '';
  const total = known.reduce((a, p) => a + p.length, 0),
    miss = list.length - known.length;
  return `<span class="glen" title="Общая длительность треков${miss ? ` — у ${miss} из ${list.length} она не указана (Настройки проекта → «Темп, тональность, длительность»)` : ''}">${fmtLen(total)}${miss ? '+' : ''}</span>`;
}
function renderList() {
  renderSortBar();
  const ps = sortProjects(S.state?.projects || []),
    groups = sortGroups(S.state?.groups || [], S.state?.projects || []);
  const ids = new Set(groups.map(g => g.id));
  let h = groups
    .map(g => {
      const inG = ps.filter(p => p.group === g.id);
      return `<div class="ghead" data-group="${g.id}" onclick="toggleGroup('${g.id}',${!g.collapsed})"><span>${g.collapsed ? '▸' : '▾'}</span><span class="gn">${esc(g.name)}</span>${groupLen(inG)}<span class="muted">${inG.length}</span><span class="more" onclick="event.stopPropagation();groupMenu(event,'${g.id}')">⋯</span></div>
      ${g.collapsed ? '' : `<div class="gbody" data-group="${g.id}">${inG.map(projItem).join('') || '<div class="muted small" style="padding:6px 10px">перетащи сюда проект</div>'}</div>`}`;
    })
    .join('');
  const top = ps.filter(p => !p.group || !ids.has(p.group));
  h += `<div data-group="" class="topzone" style="min-height:${groups.length ? 24 : 0}px">${top.map(projItem).join('')}</div>`;
  if (!ps.length)
    h =
      `<div class="muted small" style="padding:10px">Проектов пока нет. Добавь папку с .flp или прими приглашение друга.</div>` +
      (groups.length ? h : '');
  $('#plist').innerHTML = h;
  renderLegend();
  // drag & drop projects between folders
  document.querySelectorAll('.pitem[draggable]').forEach(
    el =>
      (el.ondragstart = e => {
        e.dataTransfer.setData('text/x-project', el.dataset.path);
        e.dataTransfer.effectAllowed = 'move';
      })
  );
  document.querySelectorAll('[data-group]').forEach(z => {
    z.ondragover = e => {
      if ([...e.dataTransfer.types].includes('text/x-project')) {
        e.preventDefault();
        z.classList.add('drop');
      }
    };
    z.ondragleave = () => z.classList.remove('drop');
    z.ondrop = e => {
      e.preventDefault();
      z.classList.remove('drop');
      const p = e.dataTransfer.getData('text/x-project');
      if (p) moveProject(p, z.dataset.group);
    };
  });
}
async function moveProject(path, group) {
  try {
    await api('/api/projects/move', { path, group });
    await refreshAll();
  } catch (e) {
    fail(e);
  }
}
async function toggleGroup(id, collapsed) {
  await api('/api/groups/update', { id, collapsed }).catch(() => {});
  refreshAll();
}
async function newGroup() {
  const n = await uiPrompt('Новая папка в списке', {
    label: 'Название',
    placeholder: 'напр. «Альбом 2027» или «Биты на продажу»',
  });
  if (!n || !n.trim()) return;
  try {
    await api('/api/groups/create', { name: n });
    refreshAll();
  } catch (e) {
    fail(e);
  }
}
function popMenu(ev, items) {
  document.querySelectorAll('.menu').forEach(m => m.remove());
  const m = document.createElement('div');
  m.className = 'menu';
  m.innerHTML = items
    .map((it, i) => (it === '-' ? '<div class="sep"></div>' : `<div data-i="${i}">${it[0]}</div>`))
    .join('');
  document.body.appendChild(m);
  const r = m.getBoundingClientRect();
  m.style.left = Math.min(ev.clientX, innerWidth - r.width - 8) + 'px';
  m.style.top = Math.min(ev.clientY, innerHeight - r.height - 8) + 'px';
  m.onclick = e => {
    const d = e.target.closest('[data-i]');
    if (!d) return;
    m.remove();
    items[+d.dataset.i][1]();
  };
  setTimeout(
    () =>
      document.addEventListener('mousedown', function h(e) {
        if (!m.contains(e.target)) {
          m.remove();
          document.removeEventListener('mousedown', h);
        }
      }),
    0
  );
}
function groupMenu(ev, id) {
  const g = (S.state.groups || []).find(x => x.id === id);
  popMenu(ev, [
    [
      '✎ Переименовать',
      async () => {
        const n = await uiPrompt('Переименовать папку', { value: g.name });
        if (n && n.trim()) {
          await api('/api/groups/update', { id, name: n });
          refreshAll();
        }
      },
    ],
    [
      'Удалить папку (проекты останутся)',
      async () => {
        if (
          await uiConfirm(
            `Удалить папку «${esc(g.name)}» из списка? Проекты из неё переместятся наверх, файлы не трогаются.`,
            { ok: 'Удалить' }
          )
        ) {
          await api('/api/groups/delete', { id });
          refreshAll();
        }
      },
    ],
  ]);
}
function projMenu(ev, encPath) {
  const path = decodeURIComponent(encPath);
  const groups = S.state.groups || [],
    cur = (S.state.projects || []).find(p => p.path === path)?.group || '';
  const items = groups
    .filter(g => g.id !== cur)
    .map(g => [`Переместить в «${esc(g.name)}»`, () => moveProject(path, g.id)]);
  if (cur) items.push(['Убрать из папки', () => moveProject(path, '')]);
  items.push([
    '＋ В новую папку…',
    async () => {
      const n = await uiPrompt('Новая папка в списке', { label: 'Название' });
      if (n && n.trim()) {
        const g = await api('/api/groups/create', { name: n });
        moveProject(path, g.id);
      }
    },
  ]);
  items.push(
    '-',
    [
      'Запустить в FL Studio',
      () => {
        S.sel === path ? launchProject() : api('/api/project/launch', { path }).catch(fail);
      },
    ],
    ['Переименовать…', () => renameProject(path)],
    ['Открыть в Finder/Проводнике', () => api('/api/project/open', { path })],
    [
      '✕ Убрать из списка',
      async () => {
        if (await uiConfirm('Убрать проект из списка? Файлы не удаляются.', { ok: 'Убрать' })) {
          await api('/api/projects/remove', { path });
          if (S.sel === path) S.sel = null;
          refreshAll();
        }
      },
    ],
    ['Удалить файлы проекта…', () => trashProject(path)]
  );
  popMenu(ev, items);
}
async function selectProject(e) {
  S.sel = decodeURIComponent(e);
  S.analysis = null;
  S.hidden = null;
  S.history = null;
  S.comments = null;
  renderList();
  scrollSelIntoView();
  await loadProject(S.sel);
  if (S.tab === 'talk') loadComments(true);
}
