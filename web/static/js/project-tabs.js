// Вкладки проекта: «Изменения», «Сэмплы», «История», «Настройки» (метаданные трека, общие правила).

async function loadHistory() {
  const path = S.sel;
  const h = await api('/api/project/history?path=' + enc(path)).catch(() => []);
  if (S.sel !== path) return;
  S.history = h;
  S.histFetching = true;
  renderMain();
  // заодно — что нового на сервере (появится сверху, пометкой «на сервере»)
  const h2 = await api('/api/project/history?fetch=1&path=' + enc(path)).catch(() => null);
  S.histFetching = false;
  if (S.sel !== path) return;
  if (h2) S.history = h2;
  if (S.tab === 'history') renderMain();
}
async function loadMeta() {
  const path = S.sel,
    f = $('#metaf');
  if (!f) return;
  let r;
  try {
    r = await api('/api/project/meta?path=' + enc(path));
  } catch (e) {
    return;
  }
  if (S.sel !== path || !$('#metaf')) return;
  S.meta = r;
  const ml = $('#mlength'),
    ln = $('#lengthNote');
  if (ml) {
    ml.placeholder = r.meta?.flpLength || 'напр. 3:12';
    if (ln)
      ln.innerHTML = r.meta?.flpLength
        ? `Длительность ${esc(r.meta.flpLength)} — ${r.meta.flpLengthBy === 'selection' ? 'по красному выделению в плейлисте' : 'до конца последнего клипа в плейлисте (выдели трек красной полосой сверху в плейлисте FL — посчитаю по ней)'}. Впиши своё, если нужно иначе.`
        : '';
  }
  const tt = $('#mtempo');
  if (tt) {
    tt.placeholder = r.meta?.flpTempo || 'напр. 140';
    const n = $('#tempoNote');
    if (n)
      n.textContent = r.meta?.flpTempo
        ? `В .flp темп в начале проекта — ${r.meta.flpTempo} BPM. Если темп по ходу трека меняется (автоматизация), впиши основной сам.`
        : '';
  }
  document.querySelectorAll('#metaf [data-m], #musf [data-m]').forEach(i => {
    if (!i.dataset.dirty) i.value = r.meta?.[i.dataset.m] || '';
    if (i.placeholder === 'загружаю…') i.placeholder = S.proj?.name || '';
  });
  drawCover();
}
function drawCover() {
  const c = $('#mcover');
  if (!c) return;
  const r = S.meta || {};
  c.innerHTML = r.cover
    ? `<img src="${r.cover}" alt="">`
    : `<div class="muted small" style="text-align:center;padding:10px">Обложки нет<br><a onclick="pickCover()">выбрать картинку</a></div>`;
  const b = $('#mcrm');
  if (b) b.style.display = r.cover ? '' : 'none';
}
function metaFromForm() {
  const m = {};
  document.querySelectorAll('#metaf [data-m], #musf [data-m]').forEach(i => (m[i.dataset.m] = i.value.trim()));
  return m;
}
async function saveMeta() {
  try {
    const r = await api('/api/project/meta', { path: S.sel, meta: metaFromForm(), flp: true });
    toast(
      r.flError
        ? 'Сохранено, но в .flp не записал: ' + esc(r.flError)
        : (r.latin ? 'Сохранено. В FL Studio — латиницей: <b>' + esc(r.latin) + '</b>. ' : 'Сохранено. ') +
            'Если проект открыт в FL — закрой его без сохранения и открой снова.',
      6000
    );
    await refreshAll();
    loadProject(S.sel);
  } catch (e) {
    fail(e);
  }
}
async function pickCover() {
  try {
    const r = await api('/api/project/meta/cover', { path: S.sel, action: 'pick' });
    if (r.ok) {
      toast('Обложка сохранена в Metadata/');
      loadMeta();
    }
  } catch (e) {
    fail(e);
  }
}
async function removeCover() {
  try {
    await api('/api/project/meta/cover', { path: S.sel, action: 'remove' });
    loadMeta();
  } catch (e) {
    fail(e);
  }
}
async function tagRendersNow() {
  await api('/api/project/meta', { path: S.sel, meta: metaFromForm(), flp: false }).catch(() => {});
  runJob('/api/project/meta/tag', { path: S.sel }, { title: 'Записываю метаданные в рендеры' });
}
async function makeReadme() {
  await api('/api/project/meta', { path: S.sel, meta: metaFromForm(), flp: false }).catch(() => {});
  try {
    await api('/api/project/readme', { path: S.sel });
    toast('README.md собран — уйдёт на GitHub со следующей отправкой');
    loadProject(S.sel);
  } catch (e) {
    fail(e);
  }
}
// one persistent <audio>: it is moved between re-renders so playback never stops
function renderTab() {
  const p = S.proj,
    a = S.analysis?.analysis;
  if (S.tab === 'changes') {
    const ch = (p.changes || []).filter(c => S.showService || !c.hidden);
    const kinds = {
      untracked: ['новый', 'blue'],
      added: ['новый', 'blue'],
      modified: ['изменён', 'warn'],
      deleted: ['удалён', 'bad'],
      conflict: ['конфликт', 'bad'],
    };
    return `<div class="row" style="margin-top:10px"><span class="muted small">Что уйдёт при следующей отправке${a ? ` · неиспользуемых аудио не отправляется: ${(a.audio || []).filter(x => x.ignored).length}` : ''}</span><span class="spacer"></span>
      <label class="switch small"><input type="checkbox" ${S.showService ? 'checked' : ''} onchange="S.showService=this.checked;renderMain()"> служебные файлы</label></div>
      ${
        ch.length
          ? `<table><tr><th>Файл</th><th>Статус</th><th style="text-align:right">Размер</th></tr>${ch.map(c => `<tr><td class="mono">${esc(c.path)}</td><td><span class="tag ${kinds[c.kind]?.[1] || ''}">${kinds[c.kind]?.[0] || c.kind}</span></td><td style="text-align:right" class="muted">${fmtSize(c.size)}</td></tr>`).join('')}</table>`
          : `<div class="okbox" style="margin-top:12px">Локальных изменений нет.</div>`
      }`;
  }
  if (S.tab === 'talk') return renderTalk();
  if (S.tab === 'samples') {
    if (!a) return `<p class="muted">Анализирую .flp…</p>`;
    const st = {
      project: ['в проекте', 'ok'],
      external: ['снаружи → скопируется', 'warn'],
      factory: ['из библиотеки FL', 'blue'],
      missing: ['НЕ НАЙДЕН', 'bad'],
    };
    const unused = (a.audio || []).filter(x => !x.used);
    return `<h3>Используются в проекте (${a.samples.length})</h3>
      <div class="muted small">Ищутся в каналах, аудиоклипах и внутри плагинов (Slicex, DirectWave, FPC…). Совпадение по имени файла.</div>
      ${a.samples.length ? `<table><tr><th>Файл</th><th>Статус</th><th>Где лежит</th></tr>${a.samples.map(s => `<tr><td>${esc(s.name)}${s.embedded ? ' <span class="tag" title="найден внутри данных плагина">в плагине</span>' : ''}</td><td><span class="tag ${st[s.status][1]}">${st[s.status][0]}</span></td><td class="mono muted ellip" title="${esc(s.original)}">${esc(s.local || s.original)}</td></tr>`).join('')}</table>` : '<p class="muted">Сэмплов нет.</p>'}
      <div class="row" style="margin-top:10px"><button onclick="runJob('/api/project/prepare',{path:S.sel},{title:'Собрать сэмплы в папку проекта'})">Собрать внешние сэмплы сейчас</button>
      <button onclick="runJob('/api/project/relink',{path:S.sel})">Направить пути в .flp на папку проекта</button><button onclick="lostDialog()" title="Вернуть сэмплы, которые пропали с диска (например, FL удалила сэмплы синтов как «неиспользуемые»)">Вернуть пропавшие…</button></div>
      ${S.hidden === undefined || S.hidden === null ? (loadHidden(), '') : S.hidden.length ? `<div class="okbox small" style="margin-top:10px">Убрано только с этого компьютера (на GitHub и у друзей есть): ${S.hidden.length} — ${S.hidden.slice(0, 4).map(esc).join(', ')}${S.hidden.length > 4 ? '…' : ''} <button class="sm" style="margin-left:6px" onclick="restoreHidden(S.hidden)">Вернуть все</button></div>` : ''}
      <h3>Аудио в папке, которое проект не использует (${unused.length})</h3>
      <div class="muted small">Старые дубли, неудачные записи, рендеры. По умолчанию такие файлы не отправляются; «Отправлять» — отправить всё равно (и защитить от удаления — отметь так сэмплы, которые загружены прямо в синты). Ненужное можно выделить и удалить в корзину.</div>
      ${
        unused.length
          ? `<div class="row" style="margin-top:8px"><label class="switch small" style="margin:0"><input type="checkbox" onchange="selAllUnused(this.checked)"> выделить всё</label><span class="spacer"></span>
        ${S.trashing === S.sel ? '<span class="row small muted" style="gap:6px"><span class="spin"></span> Удаляю…</span>' : `<button class="sm danger" ${Object.values(S.trashSel).some(Boolean) ? '' : 'disabled'} onclick="trashSelected()">В корзину (${Object.values(S.trashSel).filter(Boolean).length})</button>`}</div>
        <table><tr><th></th><th>Отправлять</th><th>Файл</th><th style="text-align:right">Размер</th></tr>${unused
          .map(
            f => `<tr><td><input type="checkbox" style="width:auto" ${S.trashSel[f.rel] ? 'checked' : ''} onchange="S.trashSel[decodeURIComponent('${enc(f.rel)}')]=this.checked;renderMain()"></td>
        <td>${f.tracked ? '<span class="tag" title="уже на сервере">уже на сервере</span>' : `<input type="checkbox" style="width:auto" ${!f.ignored ? 'checked' : ''} onchange="toggleInclude('${enc(f.rel)}',this.checked)">`}</td><td class="mono">${esc(f.rel)}</td><td style="text-align:right" class="muted">${fmtSize(f.size)}</td></tr>`
          )
          .join('')}</table>`
          : '<p class="muted">Лишнего аудио нет </p>'
      }`;
  }
  if (S.tab === 'plugins') return renderPlugins();
  if (S.tab === 'history') {
    if (!S.history) return `<p class="muted">Загружаю…</p>`;
    if (!S.history.length) return `<p class="muted">Коммитов пока нет.</p>`;
    const other = c =>
      (c.files || []).filter(f => !/\.flp$/i.test(f) && !f.startsWith('.flsync/') && f !== 'PLUGINS.txt').length;
    const got = (S.restored || {})[S.sel] || {};
    const inc = S.history.filter(c => c.incoming).length,
      firstLocal = S.history.findIndex(c => !c.incoming);
    return `<div class="muted small" style="margin-bottom:8px"><b>↺ достать копию</b> — старый .flp встанет рядом с текущим, ничего не меняя. <b>⟲ Откатить сюда</b> — весь проект (.flp и аудио) станет как в этой версии; текущее состояние останется в истории, так что откат можно отменить.${S.histFetching ? ' <span class="spin" style="display:inline-block;vertical-align:middle;width:11px;height:11px"></span> проверяю сервер…' : ''}</div>
    ${inc ? `<div class="row" style="margin:0 0 8px;gap:10px"><span class="tag incoming">⤓ на сервере новое: ${inc}</span><span class="muted small">эти версии ещё не у тебя — они придут после «Получить»</span><span class="spacer"></span><button class="sm primary" onclick="receive()">⤓ Получить</button></div>` : ''}
    <table><tr><th>Когда</th><th>Кто</th><th>Что</th><th>Файлы</th><th></th></tr>${S.history
      .map(
        (
          c,
          i
        ) => `<tr class="${c.incoming ? 'incoming' : ''}"><td class="muted" style="white-space:nowrap">${fmtTime(c.time)}</td><td>${esc(c.author)}</td><td>${esc(c.subject)}</td>
      <td>${(c.files || [])
        .filter(f => /\.flp$/i.test(f))
        .map(f => {
          const cp = got[c.hash + '|' + f];
          return `<div class="row" style="gap:6px"><span class="mono">${esc(f)}</span>${cp ? `<button class="sm primary" title="${esc(cp)}" onclick="launchCopy('${enc(cp)}')">▶ Запустить копию</button>` : `<button class="ghost sm" title="Сохранить эту версию рядом копией" onclick="restore('${c.hash}','${enc(f)}')">↺ достать копию</button>`}</div>`;
        })
        .join('')}
      ${other(c) ? `<span class="muted small">+ ${other(c)} файл(ов)</span>` : ''}</td>
      <td style="white-space:nowrap;text-align:right">${c.incoming ? '<span class="tag incoming" title="Есть на сервере, ещё не получено">на сервере</span>' : i === firstLocal ? '<span class="tag ok" title="Проект сейчас в этой версии (плюс неотправленные правки, если есть)">сейчас</span>' : `<button class="ghost sm" onclick="rollbackTo('${c.hash}')" title="Сделать весь проект таким, как в этой версии">⟲ Откатить сюда</button>`}</td></tr>`
      )
      .join('')}</table>`;
  }
  if (S.tab === 'settings') {
    const sh = p.shared;
    return `<div class="grid2"><div class="card"><h3 style="margin-top:0">Общие правила (одинаковые у вас обоих)</h3>
      <label class="switch"><input type="checkbox" id="s_ign" ${sh.ignoreUnused ? 'checked' : ''}> Не отправлять неиспользуемое аудио</label>
      <label class="switch"><input type="checkbox" id="s_col" ${sh.collectSamples ? 'checked' : ''}> Копировать внешние сэмплы в Audio/</label>
      <label class="switch"><input type="checkbox" id="s_org" ${sh.organizeAudio ? 'checked' : ''}> Держать аудио в папке Audio/ (корень — только .flp и служебное)</label>
      <label class="switch"><input type="checkbox" id="s_bak" ${sh.sendBackup ? '' : 'checked'}> Не отправлять папку Backup (автосохранения FL — много весят)</label>
      <label>Всегда отправлять (папки с «/» на конце, по одной в строке)</label><textarea id="s_inc" placeholder="Stems/&#10;Vocals/take3.wav">${esc((sh.alwaysInclude || []).join('\n'))}</textarea>
      <div class="muted small" style="margin-top:6px">Какие файлы идут через Git LFS — общая настройка для всех проектов: Настройки → GitHub.</div>
      <div style="margin-top:12px"><button class="primary" onclick="saveShared()">Сохранить</button></div></div>
      <div class="card"><h3 style="margin-top:0">Трек: название, артист, обложка</h3>
      <div class="muted small">Название и артист записываются и в FL Studio (Project info → Title и Author; русские буквы FL не понимает — там будут латиницей). Всё вместе с обложкой лежит в папке <b>Metadata/</b> проекта, уходит другу с отправкой, само записывается в каждый новый рендер трека и в README проекта на GitHub.</div>
      <div class="metagrid" id="metaf">
        <div class="mcover" id="mcover" title="Обложка (JPG или PNG, лучше квадрат 1500×1500)"><span class="muted small">загружаю…</span></div>
        <div><label>Название</label><input data-m="title" placeholder="загружаю…">
        <label>Артист</label><input data-m="artist">
        <div class="grid3"><div><label>Альбом</label><input data-m="album"></div><div><label>Год</label><input data-m="year" placeholder="${new Date().getFullYear()}"></div><div><label>№ трека</label><input data-m="track"></div></div>
        <div class="grid3" style="grid-template-columns:1fr 1fr"><div><label>Жанр</label><input data-m="genre" placeholder="напр. Phonk"></div><div><label>Комментарий</label><input data-m="comment" placeholder="prod. by …"></div></div></div></div>
      <div class="row" style="margin-top:10px"><button class="primary" onclick="saveMeta()">Сохранить</button><button onclick="pickCover()">Обложка…</button><button class="ghost" id="mcrm" onclick="removeCover()" style="display:none">Убрать обложку</button><span class="spacer"></span>
        <button class="ghost sm" onclick="tagRendersNow()" title="Записать название, артиста и обложку во все рендеры в папке проекта">В рендеры сейчас</button><button class="ghost sm" onclick="makeReadme()">Собрать README</button></div>
      <div class="muted small" id="metaNote" style="margin-top:6px">${p.fl?.saved ? `Проект сохранён в FL Studio ${esc(p.fl.saved)}${p.fl.installed ? `, у тебя установлена ${esc(p.fl.installed)}` : ''}.` : ''}</div></div>
      <div class="card" id="musf"><h3 style="margin-top:0">Темп, тональность, длительность</h3>
      <div class="muted small">Видны в списке проектов и в шапке проекта, уходят другу с отправкой, записываются в рендеры (BPM и тональность — в теги файла) и в README. Пустое поле — значение берётся из .flp само.</div>
      <div class="grid3" style="grid-template-columns:1fr 1fr 1fr"><div><label>Темп, BPM</label><input data-m="tempo" inputmode="decimal" id="mtempo"></div><div><label>Тональность</label><input data-m="key" list="keylist" placeholder="напр. Am"></div><div><label>Длительность, м:сс</label><input data-m="length" id="mlength" placeholder="напр. 3:12"></div></div>
      <datalist id="keylist">${KEYS.map(k => `<option value="${k}">`).join('')}</datalist>
      <div class="muted small" id="tempoNote" style="margin-top:6px"></div><div class="muted small" id="lengthNote" style="margin-top:4px"></div>
      <div class="row" style="margin-top:10px"><button class="primary" onclick="saveMeta()">Сохранить</button></div></div>
      <div class="card"><h3 style="margin-top:0">Этот компьютер</h3>
      <label class="switch"><input type="checkbox" ${p.relinkOnReceive ? 'checked' : ''} onchange="api('/api/project/settings',{path:S.sel,relinkOnReceive:this.checked}).then(()=>toast('Сохранено'))"> После получения направлять пути сэмплов в .flp на папку проекта</label>
      <div class="muted small">Резервная копия .flp перед правкой лежит в .flsync-local/backup.</div>
      <label>Сервер (git remote)</label><input id="s_remote" value="${esc(p.remote)}" placeholder="https://github.com/you/track.git">
      <div class="row" style="margin-top:8px"><button onclick="api('/api/project/settings',{path:S.sel,remote:$('#s_remote').value}).then(()=>{toast('Сохранено');refreshAll()}).catch(fail)">Сохранить адрес</button>
        ${!p.remote ? `<button class="primary" onclick="createRepoDialog()">Создать репозиторий на GitHub…</button>` : ''}
        ${repoWebURL(p.remote) ? `<button onclick="api('/api/openurl',{url:repoWebURL(S.proj.remote)})">Открыть репозиторий на GitHub ↗</button>` : ''}</div>
      ${!p.remote ? '<div class="muted small">Репозитория у проекта нет (или его создание не дошло до конца) — можно указать адрес существующего или создать новый.</div>' : ''}
      <div style="margin-top:16px"><button class="ghost" onclick="renameProject()">Переименовать…</button> <button class="ghost" onclick="removeProject()">Убрать проект из списка</button><div class="muted small">«Убрать из списка» не трогает файлы и репозиторий.</div></div>
      ${p.remote ? `<div style="margin-top:14px"><button class="danger" onclick="deleteRepoDialog()">Удалить репозиторий с GitHub…</button><div class="muted small">Только владелец. Репозиторий исчезнет с GitHub и проект — с сервера FruityHub; файлы на компьютерах останутся.</div></div>` : ''}
      <div style="margin-top:14px"><button class="danger" onclick="trashProject()">Удалить файлы проекта…</button><div class="muted small">Вся папка проекта уйдёт в Корзину. На GitHub и сервере проект останется.</div></div></div></div>`;
  }
  return '';
}
async function restore(hash, f) {
  const path = S.sel,
    file = decodeURIComponent(f);
  const j = await runJob('/api/project/restore', { path, hash, file }, { title: 'Достаю старую версию' });
  if (!j || j.error || !j.result) return;
  S.restored = S.restored || {};
  (S.restored[path] = S.restored[path] || {})[hash + '|' + file] = j.result;
  if (S.sel === path) {
    renderMain();
    toast(`Копия готова: <b>${esc(j.result)}</b> — кнопка «▶ Запустить копию» в истории`, 5000);
  }
}
async function launchCopy(rel) {
  rel = decodeURIComponent(rel);
  try {
    await api('/api/project/launch-file', { path: S.sel, rel });
    toast('Открываю ' + esc(rel.split('/').pop()) + ' в FL Studio');
  } catch (e) {
    fail(e);
  }
}
async function rollbackTo(hash) {
  const p = S.proj,
    c = (S.history || []).find(x => x.hash === hash);
  if (!p || !c) return;
  const lockOther = p.lock && p.lock.user !== p.me;
  if (
    !(await uiConfirm(
      `Весь проект станет таким, как в версии от <b>${fmtTime(c.time)}</b> (${esc(c.author)}: «${esc(c.subject)}»): .flp и аудио — как тогда, файлы, появившиеся позже, уберутся.<br><br>
    Ничего не пропадёт: текущее состояние (и несохранённые правки) сначала запишется в историю — откат можно отменить, откатившись обратно. Не отправлявшиеся файлы, которые мешают, уйдут в папку <b>Backup</b>.<br><br>
    <b>Закрой проект в FL Studio</b> перед откатом.${p.behind ? `<div class="warnbox" style="margin-top:10px">На сервере есть новое (${p.behind}) — лучше сначала «Получить», иначе при отправке оно сольётся с откатом.</div>` : ''}${lockOther ? `<div class="warnbox" style="margin-top:10px">Сейчас проектом занят ${esc(p.lock.user)}.</div>` : ''}
    <div class="muted small" style="margin-top:10px">Откат делается у тебя; чтобы у друга стало так же — потом «Отправить».</div>`,
      { title: 'Откатить проект к этой версии', ok: 'Откатить' }
    ))
  )
    return;
  const path = S.sel;
  const j = await runJob('/api/project/rollback', { path, hash }, { title: 'Откат проекта к версии' });
  if (S.sel === path) {
    S.history = null;
    S.analysis = null;
    if (S.tab === 'history') loadHistory();
    loadSize(path, true);
  }
  if (!j || j.error || !j.result) return;
  const r = j.result;
  if (r.nothing) {
    toast('Проект и так в этой версии');
    return;
  }
  if (
    S.sel === path &&
    (await uiConfirm(
      `Проект как в версии ${esc(r.to)}: вернулось файлов — ${r.changed}, убрано появившихся позже — ${r.removed}.${r.moved ? `<br>Мешавшие файлы (${r.moved}) — в <span class="mono">${esc(r.backupTo)}</span>.` : ''}<br><br>Отправить откат, чтобы у друга стало так же? Передумаешь — откатись обратно к версии ${esc(r.before)} (она в «Истории»).`,
      { title: 'Откат готов', ok: 'Отправить…', cancel: 'Позже' }
    ))
  )
    sendDialog();
}
function selAllUnused(v) {
  S.trashSel = {};
  if (v) for (const f of (S.analysis?.analysis?.audio || []).filter(x => !x.used)) S.trashSel[f.rel] = true;
  renderMain();
}
async function trashSelected() {
  const files = Object.entries(S.trashSel)
    .filter(([, v]) => v)
    .map(([k]) => k);
  if (!files.length) return;
  const audio = S.analysis?.analysis?.audio || [],
    sel = audio.filter(x => files.includes(x.rel)),
    onServer = sel.filter(x => x.tracked).length,
    size = sel.reduce((a, x) => a + (x.size || 0), 0);
  const m =
    modal(`<h2>Удалить неиспользуемое аудио</h2><div>Выбрано файлов: <b>${files.length}</b> (${fmtSize(size)}). Проект их не использует.</div>
    ${
      onServer
        ? `<div style="margin-top:10px"><label class="radio on"><input type="radio" name="tm" value="local" checked><span><b>Только у меня</b><div class="muted small">Файлы уйдут в Корзину и с этого компьютера пропадут совсем (вместе с копией в .git) — освободится место. На GitHub и у друзей они останутся, вернуть можно в любой момент.</div></span></label>
      <label class="radio"><input type="radio" name="tm" value="all"><span><b>Везде</b><div class="muted small">${onServer} из них уже на GitHub — после твоей отправки они удалятся и у друзей (в истории останутся).</div></span></label></div>`
        : '<div class="muted small" style="margin-top:6px">Их нет на GitHub — они просто уйдут в Корзину на этом компьютере.</div>'
    }
    <div class="row" style="justify-content:flex-end;margin-top:14px"><button id="no">Отмена</button><button class="danger" id="ok">В корзину</button></div>`);
  m.querySelector('#no').onclick = () => m.close();
  m.querySelectorAll('input[name=tm]').forEach(
    i =>
      (i.onchange = () =>
        m.querySelectorAll('.radio').forEach(l => l.classList.toggle('on', l.querySelector('input').checked)))
  );
  m.querySelector('#ok').onclick = async () => {
    const mode = (m.querySelector('input[name=tm]:checked') || {}).value || 'all',
      path = S.sel;
    m.close();
    S.trashSel = {};
    S.trashing = path;
    renderMain();
    // задачей с плашкой: на больших файлах удаление идёт заметное время, а в списке это не видно
    const j = await runJob(
      '/api/project/trash',
      { path, files, mode },
      {
        title:
          mode === 'local'
            ? `Убираю аудио с этого компьютера (${files.length})`
            : `Удаляю аудио в корзину (${files.length})`,
      }
    );
    S.trashing = null;
    const r = j && !j.error && j.result;
    if (r)
      toast(
        mode === 'local'
          ? `Убрано только у тебя: ${r.n}, освободилось ${fmtSize(r.freed)}`
          : `Удалено в корзину: ${r.n}`,
        5000
      );
    if (S.sel === path) {
      S.analysis = null;
      S.hidden = null;
      loadProject(path);
      loadSize(path, true);
    }
  };
}
async function loadHidden() {
  const path = S.sel;
  S.hidden = [];
  try {
    const l = await api('/api/project/hidden?path=' + enc(path));
    if (S.sel === path) {
      S.hidden = l;
      if (S.tab === 'samples') renderMain();
    }
  } catch (e) {
    S.hidden = [];
  }
}
async function restoreHidden(files) {
  const j = await runJob('/api/project/restore-hidden', { path: S.sel, files }, { title: 'Возвращаю аудио' });
  S.hidden = null;
  S.analysis = null;
  loadProject(S.sel);
  loadSize(S.sel, true);
}
async function toggleInclude(f, inc) {
  await api('/api/project/include', { path: S.sel, file: decodeURIComponent(f), include: inc });
  loadProject(S.sel);
}
async function rescan() {
  toast('Сканирую…');
  await api('/api/rescan', {});
  S.analysis = null;
  loadProject(S.sel);
}
async function saveShared() {
  const sh = {
    ignoreUnused: $('#s_ign').checked,
    collectSamples: $('#s_col').checked,
    organizeAudio: $('#s_org').checked,
    sendBackup: !$('#s_bak').checked,
    alwaysInclude: $('#s_inc')
      .value.split('\n')
      .map(s => s.trim())
      .filter(Boolean),
    lfsExtensions: S.proj.shared.lfsExtensions || [],
  };
  try {
    await api('/api/project/settings', { path: S.sel, shared: sh });
    toast('Сохранено — правила уйдут другу со следующей отправкой');
  } catch (e) {
    return fail(e);
  }
  S.analysis = null;
  loadProject(S.sel);
}
