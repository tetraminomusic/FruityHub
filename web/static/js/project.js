// Экран проекта: шапка, баннеры, запуск в FL, переименование, переезд папки, подключение папки к GitHub.

const projectLoads = new Map();
function loadProject(path) {
  if (projectLoads.has(path)) return projectLoads.get(path);
  const pending = readProject(path).finally(() => projectLoads.delete(path));
  projectLoads.set(path, pending);
  return pending;
}
async function readProject(path) {
  try {
    const project = await api('/api/project?path=' + enc(path));
    if (S.sel !== path) return;
    S.proj = project;
  } catch (e) {
    if (S.sel !== path) return;
    S.proj = null;
  }
  if (S.proj?.exists) loadSize(path);
  renderMain();
  api('/api/project/analysis?path=' + enc(path))
    .then(a => {
      if (S.sel === path) {
        S.analysis = a;
        renderMain();
      }
    })
    .catch(() => {});
  if ((S.tab === 'changes' || S.tab === 'history') && !S.history) loadHistory();
  if (S.comments === null) loadComments(false);
}
// сколько места занимает проект: всё вместе, аудио и .git (история и вторые копии аудио Git LFS)
function sizeText(path) {
  const z = (S.sizes || {})[path];
  if (!z) return '';
  return `Занимает <b style="color:var(--text)">${fmtSize(z.total)}</b>: аудио ${fmtSize(z.audio)}${z.git ? ` · история и копии аудио (.git) ${fmtSize(z.git)}` : ''} · файлов ${z.files}`;
}
async function loadSize(path, fresh) {
  try {
    const z = await api('/api/project/size?path=' + enc(path) + (fresh ? '&fresh=1' : ''));
    S.sizes = S.sizes || {};
    S.sizes[path] = z;
    const el = $('#psize');
    if (el && S.sel === path) el.innerHTML = sizeText(path);
  } catch (e) {}
}
async function refreshProject() {
  const path = S.sel;
  if (!path || S.refreshing) return;
  S.refreshing = path;
  renderMain();
  let r = {};
  try {
    r = await api('/api/project/refresh', { path });
  } catch (e) {}
  try {
    S.analysis = null;
    S.hidden = null;
    if (S.tab === 'changes' || S.tab === 'history') S.history = null;
    await refreshAll();
    await loadProject(path);
    loadSize(path, true);
    if (S.tab === 'talk') loadComments(true);
  } finally {
    S.refreshing = null;
    if (S.sel === path) renderMain();
  }
  const p = S.proj;
  if (S.sel !== path || !p) return;
  const n = (p.changes || []).filter(c => !c.hidden).length;
  toast(
    `Обновлено: ${n ? 'изменений — ' + n : 'изменений нет'}${p.behind > 0 ? ` · <b>на сервере новое: ${p.behind}</b>` : ''}${r.fetched === false && p.remote ? ' · GitHub сейчас не ответил' : ''}`
  );
}
function keepDrafts(fn) {
  const vals = {},
    act = document.activeElement,
    actId = act && act.id,
    sel = act && act.selectionStart != null ? [act.selectionStart, act.selectionEnd] : null;
  document.querySelectorAll('#main [data-dirty]').forEach(el => {
    const k = el.id ? '#' + el.id : el.dataset.m ? 'm:' + el.dataset.m : '';
    if (k) vals[k] = el.value;
  });
  const proj = S.sel;
  fn();
  if (S.sel !== proj) return;
  for (const [k, v] of Object.entries(vals)) {
    const el = k.startsWith('m:')
      ? document.querySelector(`#main [data-m="${k.slice(2)}"]`)
      : document.getElementById(k.slice(1));
    if (el) {
      el.value = v;
      el.dataset.dirty = 1;
    }
  }
  if (actId) {
    const el = document.getElementById(actId);
    if (el && el.closest('#main')) {
      el.focus();
      if (sel)
        try {
          el.setSelectionRange(sel[0], sel[1]);
        } catch (e) {}
    }
  }
}
function renderMain() {
  keepDrafts(renderMainNow);
  if (S.tab === 'settings' && $('#metaf')) loadMeta();
}
function renderMainNow() {
  const m = $('#main');
  if (!S.sel || !S.proj) {
    m.innerHTML = `<div class="empty"><div class="box"><div style="font-size:18px;color:var(--text)">Выбери или добавь проект</div>
    <div style="margin-top:6px">Один проект = одна папка с .flp и всем аудио.<br>Проекты друзей — во вкладке «Доступные» и в приглашениях.</div></div></div>`;
    return;
  }
  const p = S.proj;
  if (!p.exists) {
    m.innerHTML = `<div class="card phead"><h1>${esc(p.name)}</h1><div class="muted small mono">${esc(p.path)}</div></div>
    <div class="card"><h3 style="margin-top:0">Папки проекта здесь больше нет</h3>
    <div class="muted small">Скорее всего, её перенесли или переименовали. Укажи, где она теперь, — история, участники и настройки проекта сохранятся. Проекты, которые лежат в «папках с проектами» (Настройки → Проекты), находятся сами.</div>
    <div id="loc" class="small" style="margin-top:10px">Ищу, куда переехала папка…</div>
    <div class="row" style="margin-top:12px"><button class="primary" onclick="relocatePick()">Указать новое место…</button><button class="ghost" onclick="openSettings('projects')">Папки с проектами…</button><span class="spacer"></span><button class="ghost" onclick="removeProject()">Убрать из списка</button></div></div>`;
    locateProject(p.path);
    return;
  }
  if (!p.isRepo) return renderSetup();
  if (!p.adopted) return renderAdopt();
  const a = S.analysis?.analysis,
    plugins = S.analysis?.plugins || [];
  const missing = (a?.samples || []).filter(s => s.status === 'missing');
  const external = (a?.samples || []).filter(s => s.status === 'external');
  const pluginProblems = plugins.filter(r => r.problem);
  const visibleChanges = (p.changes || []).filter(c => S.showService || !c.hidden);
  const lock = p.lock,
    mine = lock && lock.user === p.me;
  const st = S.comments?.status;
  const openTodos = (S.comments?.comments || []).filter(c => c.kind === 'todo' && !c.done).length;
  const unread = S.notes[S.sel]?.unread || 0;
  const incoming = S.history?.find(c => c.incoming);
  const issueKey = 'prepare:' + JSON.stringify([...missing.map(x => 's:' + (x.original || x.name || x.local)), ...pluginProblems.map(x => 'p:' + x.name + ':' + x.format)].sort());
  const prepareHidden = !!(S.state.config?.hidePrepareWarnings || dismissed()[(S.sel || '') + '|' + issueKey]);
  const action = projectActionState(p, prepareHidden ? 0 : missing.length, prepareHidden ? 0 : pluginProblems.length);
  const status = action.primary === 'receive' && incoming ? `Новая версия от ${incoming.author}` : action.status;
  const banner = `<div class="banner ${!lock ? 'free' : mine ? 'me' : 'other'}"><div><div class="big">${esc(status)}</div><div class="muted small">${action.primary === 'prepare' && !prepareHidden ? `<a onclick="mainProjectAction('prepare')">Подготовить проект к работе →</a>` : lock ? `${mine ? 'С ' + fmtTime(lock.since) : 'В работе у ' + esc(lock.user)}` : 'Перед открытием можно взять проект в работу.'}</div></div>${action.primary === 'prepare' && !prepareHidden ? `<button class="ghost sm" title="Скрыть до появления новых проблем" onclick="dismissBox('${enc((S.sel || '') + '|' + issueKey)}')">✕</button>` : ''}</div>`;
  const tabs = [
    ['changes', 'Изменения и история', visibleChanges.length, ''],
    ['talk', 'Обсуждение', unread || openTodos || null, unread ? 'hot' : ''],
    ['samples', 'Сэмплы', (a?.samples || []).length, missing.length ? 'bad' : ''],
    ['plugins', 'Плагины', plugins.length, pluginProblems.length ? 'bad' : ''],
    ['settings', 'Настройки', null, ''],
  ];
  m.innerHTML = `<div class="card phead">
    <div class="row"><div class="project-heading"><h1>${esc(p.name)}</h1>${(S.state.projects || []).find(x => x.path === p.path)?.artist ? `<div class="project-artist">${esc((S.state.projects || []).find(x => x.path === p.path).artist)}</div>` : ''}</div><button class="ghost sm" onclick="projectMore(event)">Ещё ▾</button>${st?.stage ? `<span class="stage" title="Этап проекта">● ${esc(st.stage)}</span>` : ''}${p.tempo ? `<span class="tchip" title="${p.tempoFlp ? 'Темп из .flp (в начале проекта). Свой — «Настройки» → «Темп, тональность, длительность»' : 'Темп из «Настройки» → «Темп, тональность, длительность»'}" onclick="setTab('settings')">${esc(p.tempo)} BPM</span>` : ''}${p.key ? `<span class="tchip" title="Тональность" onclick="setTab('settings')">${esc(p.key)}</span>` : ''}${(S.state.projects || []).find(x => x.path === p.path)?.length ? `<span class="tchip" title="Длительность трека" onclick="setTab('settings')">${fmtLen((S.state.projects || []).find(x => x.path === p.path).length)}</span>` : ''}<span class="spacer"></span><div class="project-header-tools">
      ${p.onServer ? `<button class="ghost" onclick="participantsDialog()">Участники</button>` : ''}<button class="ghost" onclick="api('/api/project/open',{path:S.sel})">Открыть папку</button><button class="ghost iconbtn" onclick="refreshProject()" ${S.refreshing === p.path ? 'disabled' : ''} title="Обновить: перечитать проект (изменения в папке, .flp, сэмплы, размер) и проверить, нет ли нового на GitHub">${S.refreshing === p.path ? '<span class="spin"></span>' : ICON_REFRESH}</button><button class="pin-button ${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? 'on' : ''}" title="${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? 'Открепить проект' : 'Закрепить проект'}" onclick="togglePin(S.sel)">${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? '★' : '☆'}</button></div></div>
    </div>
    <div class="top"><div>${banner}
      <div class="actions stable-actions">
        <button class="${action.primary === 'receive' ? 'primary' : 'action-muted'}" onclick="receive()">⤓ Получить</button>
        <button class="action-muted" onclick="${mine ? 'unlock()' : `takeLock(${!!lock})`}">${mine ? 'Освободить' : 'Взять в работу'}</button>
        <button class="${action.primary === 'send' ? 'primary' : 'action-muted'}" onclick="sendDialog()" ${S.sendOpen ? 'disabled' : ''}>${S.sendOpen ? '<span class="spin"></span> ' : ''}⤒ Отправить${visibleChanges.length ? ' (' + visibleChanges.length + ')' : ''}</button>
      </div></div>
      <div class="card launch ${action.primary === 'launch' ? '' : 'launch-muted'}">${launchPanel(p)}</div>
    </div>
    ${layoutBanners(p)}
    ${p.fl?.tooOld ? `<div class="badbox">${esc(p.fl.text)}</div>` : ''}
    ${missing.length ? dbox('missing:' + missing.length, 'badbox', `Не найдено сэмплов: ${missing.length}. Они пропадут в FL — смотри вкладку «Сэмплы».`) : ''}
    ${external.length ? dbox('external:' + external.length, 'warnbox', `Сэмплов вне папки проекта: ${external.length} — при отправке они скопируются в <b>Audio/</b>.`) : ''}
    ${pluginProblems.length ? dbox('plugins:' + pluginProblems.map(r => r.name).join(','), 'warnbox', `Проблемы с плагинами: ${pluginProblems.length} — смотри вкладку «Плагины».`) : ''}
    <div class="tabs">${tabs.map(([k, n, c, cls]) => `<div class="tab ${S.tab === k ? 'sel' : ''}" onclick="setTab('${k}')">${n}${c != null ? `<span class="cnt ${cls}">${c}</span>` : ''}</div>`).join('')}</div>
    <div id="tabbody">${renderTab()}</div>`;
}
async function locateProject(path) {
  let r;
  try {
    r = await api('/api/project/locate', { path });
  } catch (e) {
    const el = $('#loc');
    if (el) el.innerHTML = `<span class="muted">${esc(e.message)}</span>`;
    return;
  }
  const el = $('#loc');
  if (!el || S.sel !== path) return;
  const why = { remote: 'тот же репозиторий на GitHub', flp: 'тот же .flp', name: '.flp с именем папки' };
  el.innerHTML = r.candidates.length
    ? `<b>Похоже, это она:</b>${r.candidates.map((c, i) => `<div class="drow" style="margin-top:6px"><span class="ellip"><span class="mono">${esc(c.path)}</span> <span class="tag ${c.match === 'remote' ? 'ok' : ''}">${why[c.match] || ''}</span></span><button class="sm primary" data-i="${i}">Это она</button></div>`).join('')}`
    : `<span class="muted">Сам не нашёл${r.flp ? ` (искал папку с «${esc(r.flp)}»)` : ''}${r.roots ? '' : ' — добавь папку, где лежат проекты, в Настройки → Проекты, или укажи место вручную'}.</span>`;
  el.querySelectorAll('[data-i]').forEach(b => (b.onclick = () => relocateTo(path, r.candidates[+b.dataset.i].path)));
}
async function relocateTo(path, np) {
  try {
    await api('/api/project/relocate', { path, newPath: np });
    S.sel = np;
    S.analysis = null;
    S.comments = null;
    toast('Путь к проекту обновлён');
    await refreshAll();
    loadProject(np);
  } catch (e) {
    fail(e);
  }
}
async function relocatePick() {
  const r = await api('/api/pickfolder', {}).catch(() => ({}));
  if (r.path) relocateTo(S.sel, r.path);
}
function launchPanel(p) {
  const fl = p.fl || {};
  const info = fl.saved
    ? `Сохранён в FL Studio ${esc(fl.saved.split('.').slice(0, 2).join('.'))}${fl.installed ? ` · у тебя ${esc(fl.installed.split('.').slice(0, 2).join('.'))}` : ''}`
    : '';
  return `<div class="row" style="flex-wrap:nowrap;gap:4px"><button class="primary big" style="flex:1" onclick="launchProject()" oncontextmenu="event.preventDefault();launchMenu(event)" title="Правый клик — выбрать версию FL Studio">Запустить проект</button><button class="primary big" style="padding:12px 10px" onclick="launchMenu(event)" title="В какой версии FL Studio открыть">▾</button></div>
    <div class="muted small">${info || 'Откроется в FL Studio'}${fl.tooOld ? ` · <span style="color:var(--bad)">твоя FL старее</span>` : ''}</div>`;
}
// выбор версии FL Studio для запуска (правый клик по «Запустить проект» или ▾)
async function launchMenu(ev) {
  const x = { clientX: ev.clientX, clientY: ev.clientY };
  let apps = [];
  try {
    apps = await api('/api/flapps');
  } catch (e) {}
  if (!apps.length) {
    popMenu(x, [
      ['Открыть программой по умолчанию', () => launchProject()],
      ['<span class="muted">Другие версии FL Studio не найдены</span>', () => {}],
    ]);
    return;
  }
  popMenu(x, [
    ...apps.map(a => [
      `FL Studio ${esc(a.version)} <span class="muted small">${esc(a.name)}</span>`,
      () => launchProject(a.path),
    ]),
    '-',
    ['Программой по умолчанию', () => launchProject()],
  ]);
}
async function launchProject(app) {
  const p = S.proj;
  if (!p) return;
  if (
    p.fl?.tooOld &&
    !(await uiConfirm(esc(p.fl.text) + '<br><br>Всё равно открыть?', {
      title: 'Старая версия FL Studio',
      ok: 'Открыть',
    }))
  )
    return;
  if (
    p.lock &&
    p.lock.user !== p.me &&
    !(await uiConfirm(
      `Сейчас проектом занят ${esc(p.lock.user)}. Если сохранишь его у себя — получится конфликт. Открыть?`,
      { title: 'Проект в работе у друга', ok: 'Открыть' }
    ))
  )
    return;
  try {
    const r = await api('/api/project/launch', { path: S.sel, app: app || '' });
    toast('Открываю ' + esc(r.flp) + ' в FL Studio');
  } catch (e) {
    fail(e);
  }
}
async function renameProject(path) {
  path = path || S.sel;
  const cur = (S.state.projects || []).find(x => x.path === path);
  if (!cur) return;
  const folder = path.split(/[\\/]/).pop();
  const m =
    modal(`<h2>Переименовать проект</h2><div class="muted small">Название меняется только на этом компьютере — папка, репозиторий на GitHub и сервер остаются как были.</div>
    <label>Название</label><input id="rn" value="${esc(cur.name)}" placeholder="${esc(folder)}">
    <label>Артист</label><input id="ra" value="${esc(cur.artist || '')}" placeholder="кто автор трека">
    <label class="switch"><input type="checkbox" id="rfl" checked> Записать название и артиста и в FL Studio (Project info → Title и Author, русские буквы — латиницей)</label>
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="no">Отмена</button><button class="primary" id="ok">Сохранить</button></div>`);
  const i = m.querySelector('#rn');
  i.focus();
  i.select();
  m.querySelector('#ra').onkeydown = e => {
    if (e.key === 'Enter') go();
  };
  const go = async () => {
    try {
      const fl = m.querySelector('#rfl').checked,
        ar = m.querySelector('#ra').value.trim();
      const r = await api('/api/project/rename', {
        path,
        name: i.value,
        flTitle: fl,
        ...(fl && ar !== (cur.artist || '') ? { artist: ar } : {}),
      });
      m.close();
      if (r.flError) toast('В .flp не записал: ' + esc(r.flError), 6000);
      await refreshAll();
    } catch (e) {
      fail(e);
    }
  };
  m.querySelector('#ok').onclick = go;
  i.onkeydown = e => {
    if (e.key === 'Enter') go();
  };
  m.querySelector('#no').onclick = () => m.close();
}
async function trashProject(path) {
  path = path || S.sel;
  const cur = (S.state.projects || []).find(x => x.path === path);
  if (!cur) return;
  if (
    !(await uiConfirm(
      `Удалить проект <b>${esc(cur.name)}</b> с этого компьютера?<br><br>Папка <span class="mono">${esc(path)}</span> целиком уйдёт в Корзину — оттуда её можно вернуть. Репозиторий на GitHub и проект на сервере остаются: его можно снова скачать из «Доступных».`,
      { title: 'Удалить файлы проекта', ok: 'Удалить в корзину', danger: true }
    ))
  )
    return;
  try {
    await api('/api/project/delete-files', { path });
    if (S.sel === path) {
      S.sel = null;
      S.proj = null;
    }
    toast('Папка проекта в Корзине');
    await refreshAll();
  } catch (e) {
    fail(e);
  }
}
async function loadFLTitle() {
  const el = $('#fltitle');
  if (!el) return;
  try {
    const r = await api('/api/project/fltitle', { path: S.sel });
    el.value = r.title || '';
    el.placeholder = r.title ? '' : 'не задано';
    const ar = $('#flartist');
    if (ar) {
      ar.value = r.artist || '';
      ar.placeholder = r.artist ? '' : 'не задан';
    }
    const n = $('#fltitleNote');
    if (n) n.textContent = r.flp ? 'Файл: ' + r.flp : '';
  } catch (e) {
    el.placeholder = e.message;
  }
}
async function saveFLArtist(v) {
  try {
    const r = await api('/api/project/fltitle', { path: S.sel, artist: v });
    toast(
      r.changed
        ? (r.latin
            ? 'FL не понимает кириллицу — артист записан латиницей: <b>' + esc(r.latin) + '</b>. '
            : 'Артист записан в .flp. ') + 'Если проект открыт в FL — закрой его без сохранения и открой снова.'
        : 'Без изменений',
      6000
    );
    await refreshAll();
    loadProject(S.sel);
  } catch (e) {
    fail(e);
  }
}
function repoWebURL(remote) {
  const m = (remote || '').match(/github\.com[:/]+([^/]+\/[^/]+?)(\.git)?\/?$/);
  return m ? 'https://github.com/' + m[1] : '';
}
async function saveFLTitle(v) {
  try {
    const r = await api('/api/project/fltitle', { path: S.sel, title: v });
    toast(
      r.changed
        ? (r.latin
            ? 'FL не понимает кириллицу — название записано латиницей: <b>' + esc(r.latin) + '</b>. '
            : 'Записал в .flp. ') + 'Если проект открыт в FL — закрой его без сохранения и открой снова.'
        : 'Без изменений',
      6000
    );
    loadProject(S.sel);
  } catch (e) {
    fail(e);
  }
}
function setTab(t) {
  if (t === 'history') t = 'changes';
  S.tab = t;
  if (t === 'plugins') loadReqs();
  if ((t === 'changes' || t === 'history') && !S.history) loadHistory();
  if (t === 'talk') loadComments(true);
  renderMain();
}
// предупреждение с крестиком: закрытое не показывается, пока не изменится (другое число, другие плагины)
function dismissed() {
  try {
    return JSON.parse(localStorage.getItem('dismissed') || '{}');
  } catch (e) {
    return {};
  }
}
function dbox(key, cls, html) {
  const k = (S.sel || '') + '|' + key;
  if (dismissed()[k]) return '';
  return `<div class="${cls} dbox">${html}<button class="ghost sm dx" title="Скрыть, пока ничего не изменится" onclick="dismissBox('${enc(k)}')">✕</button></div>`;
}
function dismissBox(k) {
  const d = dismissed();
  d[decodeURIComponent(k)] = Date.now();
  try {
    localStorage.setItem('dismissed', JSON.stringify(d));
  } catch (e) {}
  renderMain();
}
function layoutBanners(p) {
  let h = '';
  if (p.isRepo && p.adopted && !p.onServer && S.state?.registered)
    h += dbox(
      'noserver',
      'warnbox',
      `Проект не подключён к серверу FruityHub — звать участников и видеть его в «Доступных» у друзей пока нельзя. <button class="sm" style="margin-left:6px" onclick="moveToServer()">Подключить к серверу</button>`
    );
  if ((p.extraFlps || []).length)
    h += dbox(
      'flps:' + p.extraFlps.join(','),
      'warnbox',
      `В папке проекта лежит несколько проектов FL: ${p.extraFlps.map(x => '<b>' + esc(x) + '</b>').join(', ')}. Так их легко перепутать, а у друга всё смешается. <button class="sm" style="margin-left:6px" onclick="splitDialog()">Разобрать по папкам…</button>`
    );
  if (p.rootAudio > 0)
    h += dbox(
      'root:' + p.rootAudio,
      'warnbox',
      `В корне проекта лежит аудио (${p.rootAudio}). Аудио должно жить в папке <b>Audio/</b> — перенесу и поправлю пути в .flp (закрой проект в FL). <button class="sm" style="margin-left:6px" onclick="runJob('/api/project/organize',{path:S.sel},{title:'Навожу порядок в папке'})">Перенести в Audio/</button>`
    );
  return h;
}
function splitDialog() {
  const p = S.proj,
    list = p.extraFlps || [],
    main = p.mainFlp || list[0];
  const m = modal(`<h2>Разобрать проекты по папкам</h2>
    <div class="muted small">Один проект = одна папка. Выбери главный проект этой папки, а для остальных — что с ними сделать. Аудио, которое нужно только вынесенному проекту, переедет вместе с ним; общее — скопируется. <b>Закрой эти проекты в FL Studio.</b></div>
    <div style="margin-top:10px">${list
      .map(
        (f, i) => `<div class="flpchoice"><div><b>${esc(f)}</b></div>
      <select data-f="${esc(f)}" style="width:auto">
        <option value="main" ${f === main ? 'selected' : ''}>главный проект этой папки</option>
        <option value="extract" ${f !== main ? 'selected' : ''}>вынести в отдельный проект</option>
        <option value="keep">это версия — оставить здесь</option></select></div>`
      )
      .join('')}</div>
    <div class="muted small" style="margin-top:8px">Новые проекты появятся рядом: ${esc(p.path.replace(/[\\/][^\\/]+$/, ''))}/&lt;имя проекта&gt; и сразу добавятся в список.</div>
    <div class="row" style="justify-content:flex-end;margin-top:14px"><button id="no">Отмена</button><button class="primary" id="go">Разобрать</button></div>`);
  m.querySelector('#no').onclick = () => m.close();
  m.querySelector('#go').onclick = async () => {
    const sel = [...m.querySelectorAll('select')],
      mains = sel.filter(x => x.value === 'main');
    if (mains.length !== 1) return uiAlert('Главным может быть ровно один проект');
    const extract = sel.filter(x => x.value === 'extract').map(x => x.dataset.f),
      keep = sel.filter(x => x.value === 'keep').map(x => x.dataset.f);
    m.close();
    await runJob('/api/project/split', { path: S.sel, extract, keep }, { title: 'Разделяю проекты' });
  };
}
function renderAdopt() {
  const p = S.proj;
  $('#main').innerHTML =
    `<div class="card phead"><div class="row"><div class="project-heading"><h1>${esc(p.name)}</h1>${(S.state.projects || []).find(x => x.path === p.path)?.artist ? `<div class="project-artist">${esc((S.state.projects || []).find(x => x.path === p.path).artist)}</div>` : ''}</div><button class="ghost sm" onclick="projectMore(event)">Ещё ▾</button><span class="spacer"></span>
    <button class="primary" onclick="launchProject()">Запустить проект</button><button class="ghost" onclick="api('/api/project/open',{path:S.sel})">Открыть папку</button><button class="ghost iconbtn" onclick="refreshProject()" ${S.refreshing === p.path ? 'disabled' : ''} title="Обновить: перечитать проект (изменения в папке, .flp, сэмплы, размер) и проверить, нет ли нового на GitHub">${S.refreshing === p.path ? '<span class="spin"></span>' : ICON_REFRESH}</button><button class="pin-button ${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? 'on' : ''}" title="${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? 'Открепить проект' : 'Закрепить проект'}" onclick="togglePin(S.sel)">${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? '★' : '☆'}</button></div>
    <div class="muted small mono">${esc(p.path)}</div>${p.fl?.saved ? `<div class="muted small">Сохранён в FL Studio ${esc(p.fl.saved)}${p.fl.installed ? ` · у тебя ${esc(p.fl.installed)}` : ''}</div>` : ''}</div>
    ${p.fl?.tooOld ? `<div class="badbox">${esc(p.fl.text)}</div>` : ''}${layoutBanners(p)}
    <div class="card"><h3 style="margin-top:0">В этой папке уже есть git-репозиторий</h3><div id="adopt" class="muted">Смотрю, что в нём…</div></div>`;
  const path = p.path;
  api('/api/project/adopt-info?path=' + enc(path))
    .then(i => {
      if (S.sel !== path) return;
      $('#adopt').innerHTML = `<div style="color:var(--text)">
      ${i.hasFlsync ? '<div class="okbox">Это проект FruityHub — просто подключу его на этом компьютере.</div>' : '<div class="muted small">Обычный git-репозиторий. Подключу как есть: история и сервер сохранятся, добавятся только служебные файлы FruityHub (PLUGINS.txt, .flsync/).</div>'}
      <table style="margin-top:8px"><tr><td class="muted">Сервер</td><td class="mono">${i.remote ? esc(i.remote.replace(/https:\/\/[^@]*@/, 'https://')) : '<span style="color:var(--warn)">не указан — укажешь после подключения</span>'}</td></tr>
      <tr><td class="muted">Ветка</td><td>${esc(i.branch)}</td></tr>
      <tr><td class="muted">История</td><td>${i.commits} коммит(ов)${i.lastCommit ? ` · последний: ${esc(i.lastCommit)} — ${esc(i.lastAuthor)}` : ''}</td></tr>
      <tr><td class="muted">Не отправлено</td><td>${i.dirty ? `<span style="color:var(--warn)">${i.dirty} изменённых файлов</span>` : 'ничего'}</td></tr>
      ${i.remote ? `<tr><td class="muted">На сервере</td><td>${i.fetchError ? `<span style="color:var(--bad)">${esc(i.fetchError)}</span>` : i.behind > 0 ? `<b style="color:var(--accent)">новое: ${i.behind}</b> — после подключения нажми «Получить»` : 'совпадает'}</td></tr>` : ''}
      <tr><td class="muted">Участники</td><td>${i.onServer ? (i.participants || []).map(x => `<span class="row" style="gap:6px;display:inline-flex;margin-right:10px">${avatar(x, 20)} <b>${esc(x.nick)}</b></span>`).join('') : `<span class="muted small">${i.remote ? 'проект не на сервере FruityHub — после подключения его можно туда перенести' : 'нет'}</span>`}</td></tr></table>
      ${(i.nonLfsAudio || []).length ? `<div class="warnbox small">${i.nonLfsAudio.length} аудиофайлов уже лежат в истории git без LFS (${i.nonLfsAudio.slice(0, 3).map(esc).join(', ')}${i.nonLfsAudio.length > 3 ? '…' : ''}). Они останутся как есть; новые и изменённые аудио пойдут через LFS.</div>` : ''}
      ${!i.remote ? `<div class="warnbox small" style="margin-top:10px">У репозитория нет адреса на GitHub — например, создание проекта оборвалось на середине. Создай репозиторий: если он уже был создан в прошлый раз, FruityHub продолжит с ним.</div>` : ''}
      <div class="row" style="margin-top:12px">${!i.remote ? `<button class="primary" onclick="createRepoDialog()">Создать репозиторий на GitHub…</button><button onclick="adopt()">Подключить без GitHub</button>` : `<button class="primary" onclick="adopt()">Подключить как есть</button>`}<button class="ghost" onclick="removeProject()">Не подключать, убрать из списка</button></div></div>`;
    })
    .catch(e => {
      $('#adopt').innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
    });
}
async function adopt() {
  await runJob('/api/project/adopt', { path: S.sel }, { title: 'Подключаю репозиторий' });
}
function renderSetup() {
  const p = S.proj,
    reg = S.state.registered;
  $('#main').innerHTML =
    `<div class="card phead"><div class="row"><div class="project-heading"><h1>${esc(p.name)}</h1>${(S.state.projects || []).find(x => x.path === p.path)?.artist ? `<div class="project-artist">${esc((S.state.projects || []).find(x => x.path === p.path).artist)}</div>` : ''}</div><button class="ghost sm" onclick="projectMore(event)">Ещё ▾</button><span class="spacer"></span>
    <button class="primary" onclick="launchProject()">Запустить проект</button><button class="ghost" onclick="api('/api/project/open',{path:S.sel})">Открыть папку</button><button class="ghost iconbtn" onclick="refreshProject()" ${S.refreshing === p.path ? 'disabled' : ''} title="Обновить: перечитать проект (изменения в папке, .flp, сэмплы, размер) и проверить, нет ли нового на GitHub">${S.refreshing === p.path ? '<span class="spin"></span>' : ICON_REFRESH}</button><button class="pin-button ${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? 'on' : ''}" title="${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? 'Открепить проект' : 'Закрепить проект'}" onclick="togglePin(S.sel)">${((S.state.projects || []).find(x => x.path === p.path)?.pinned) ? '★' : '☆'}</button></div>
    <div class="muted small mono">${esc(p.path)}</div>${p.fl?.saved ? `<div class="muted small">Сохранён в FL Studio ${esc(p.fl.saved)}${p.fl.installed ? ` · у тебя ${esc(p.fl.installed)}` : ''}</div>` : ''}</div>
    ${p.fl?.tooOld ? `<div class="badbox">${esc(p.fl.text)}</div>` : ''}${layoutBanners(p)}
  <div class="card"><h3 style="margin-top:0">Подключить проект к синхронизации</h3>
    <p class="muted">Создастся приватный репозиторий на твоём GitHub — один репозиторий на этот проект. Друзей FruityHub позовёт сам: приглашение придёт им в FruityHub, а доступ к репозиторию на GitHub выдастся и примется автоматически. Аудио идёт через Git LFS, .flp — с историей версий.</p>
    ${reg ? '' : `<div class="warnbox small">Сначала зарегистрируйся — <a onclick="openRegister(true)">регистрация</a>.</div>`}
    <label>Название проекта</label><input id="g_name" value="${esc(p.name)}">
    <label>Кого позвать в проект (им придёт приглашение, проект появится у них)</label><div id="g_pick"></div>
    <div style="margin-top:10px"><button class="primary" ${reg ? '' : 'disabled'} onclick="createOnServer()">Создать репозиторий и отправить</button></div>
    <details style="margin-top:14px"><summary class="muted small" style="cursor:pointer">У меня уже есть репозиторий для этого проекта</summary>
      <label>Ссылка на репозиторий (HTTPS)</label><input id="g_url" placeholder="https://github.com/you/track.git">
      <div style="margin-top:10px"><button onclick="initWith($('#g_url').value)">Подключить и отправить</button></div>
      <div class="muted small" style="margin-top:8px">Если это твой репозиторий на GitHub, потом нажми «Подключить к серверу» — тогда можно будет звать участников через FruityHub.</div></details></div>
  <div class="card small muted">Если друг уже позвал тебя в свой проект — приглашение появится слева сверху, принимать и скачивать его нужно там, а не подключать эту папку.</div>`;
  if (reg) userPicker($('#g_pick'));
}
async function createOnServer() {
  await runJob(
    '/api/project/server-create',
    { path: S.sel, name: $('#g_name').value, invite: pickedNicks($('#g_pick')) },
    { title: 'Создаю репозиторий и отправляю' }
  );
}
async function moveToServer() {
  const p = S.proj;
  if (
    !(await uiConfirm(
      p.remote
        ? `Подключить проект к серверу FruityHub? Репозиторий остаётся тем же, на твоём GitHub. Те, у кого уже есть доступ к нему и кто зарегистрирован в FruityHub, сразу станут участниками.<div class="muted small" style="margin-top:6px">Подключить может только владелец репозитория.</div>`
        : `У проекта ещё нет репозитория — создать приватный репозиторий на твоём GitHub и подключить его к серверу?`,
      { ok: 'Подключить', title: 'Подключение к серверу' }
    ))
  )
    return;
  await runJob('/api/project/move-to-server', { path: S.sel }, { title: 'Подключаю проект к серверу' });
}
async function initWith(url) {
  if (!url.trim()) return uiAlert('Вставь ссылку на репозиторий');
  await runJob(
    '/api/project/init',
    { path: S.sel, remote: url.trim(), push: true },
    { title: 'Подключение и первая отправка' }
  );
}
async function addProject() {
  const r = await api('/api/pickfolder', {}).catch(() => ({ path: '' }));
  const path = r.path;
  if (!path) return;
  try {
    await api('/api/projects/add', { path });
    S.sel = path;
    S.analysis = null;
    S.comments = null;
    S.preview = null;
    await refreshAll();
  } catch (e) {
    fail(e);
  }
}
async function removeProject() {
  if (!(await uiConfirm('Убрать проект из списка? Файлы не удаляются.', { ok: 'Убрать' }))) return;
  await api('/api/projects/remove', { path: S.sel });
  S.sel = null;
  refreshAll();
}

// черновики: при перерисовке (вернулся в окно, пришло новое с сервера) набранный текст и курсор не пропадают
// (сохраняются поля, в которых человек что-то набрал — у них метка data-dirty)
function initDrafts() {
  document.addEventListener(
    'input',
    e => {
      const t = e.target;
      if (t && (t.tagName === 'TEXTAREA' || t.tagName === 'INPUT') && t.closest && t.closest('#main'))
        t.dataset.dirty = 1;
    },
    true
  );
}
