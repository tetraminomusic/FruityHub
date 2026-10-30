// Вкладка «Плагины» проекта: чего не хватает, запросы плагинов у друзей, проверка и переустановка.

async function askPermission(r) {
  const m = modal(`<h2>${esc(r.fromNick)} просит плагин</h2>
    <div class="card" style="margin:8px 0"><b style="font-size:16px">${esc(r.plugin)}</b> <span class="tag">${esc(r.format)}</span>${r.vendor ? ` <span class="muted">${esc(r.vendor)}</span>` : ''}<div class="muted small">у ${esc(r.fromNick)} ${esc(r.fromOs)}${r.fromOnline ? ' · в сети' : ' · сейчас не в сети — плагин уйдёт, когда он появится'}</div></div>
    <div id="insp" class="muted small">Проверяю, что можно отправить…</div>
    <div class="warnbox small" style="margin-top:10px">Передавай только бесплатные плагины или те, на которые у ${esc(r.fromNick)} есть своя лицензия. Данные плагина (пресеты, ресурсы и файлы лицензий) уйдут вместе с ним.</div>
    <div class="muted small" style="margin-top:6px">Чтобы не нажимать каждый раз — Плагины → Настройки → «Кто может скачивать плагины с моего компьютера».</div>
    <label>Причина отказа (если отказываешь)</label><input id="why" placeholder="напр.: он платный, купи на сайте">
    <div class="row" style="justify-content:flex-end;margin-top:14px"><button id="later" class="ghost">Позже</button><button id="no" class="danger">Отклонить</button><button id="yes" class="primary" disabled>Разрешить и отправить</button></div>`);
  m.sticky = true;
  m.querySelector('#later').onclick = () => m.close();
  m.querySelector('#no').onclick = async () => {
    try {
      await api('/api/plugin/decline', { id: r.id, reason: m.querySelector('#why').value });
      m.close();
      toast('Запрос отклонён');
      pollNotes();
    } catch (e) {
      fail(e);
    }
  };
  let insp;
  try {
    insp = await api('/api/plugin/inspect?id=' + enc(r.id));
  } catch (e) {
    m.querySelector('#insp').innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
    return;
  }
  const opts = [];
  if (insp.compatible)
    opts.push({
      v: '',
      t: `Сам плагин${insp.version ? ' (версия ' + esc(insp.version) + ')' : ''} — ${fmtSize(insp.size)}`,
      d: esc(insp.reason),
    });
  for (const c of insp.installers || [])
    opts.push({
      v: c.path,
      t: `Установщик ${esc(c.name)} — ${fmtSize(c.size)}`,
      d: 'нашёл в «Загрузках»/на рабочем столе',
    });
  opts.push({
    v: '__pick__',
    t: 'Выбрать файл установщика…',
    d: `например, скачанный с сайта производителя для ${esc(r.fromOs)}`,
  });
  const dataInfo =
    (insp.data || []).length || (insp.dataSkipped || []).length
      ? `<div class="small" style="margin-top:8px;color:var(--text)"><b>Данные плагина</b> (пресеты, ресурсы, файлы лицензий):
      ${(insp.data || []).map(d => `<div>✓ ${esc(d.rel)} <span class="muted">${fmtSize(d.size)} — ${esc(d.why || 'уйдёт вместе с плагином')}</span></div>`).join('')}
      ${(insp.dataSkipped || []).map(d => `<div>— ${esc(d.rel)} <span class="muted">${fmtSize(d.size)}: ${esc(d.note)}</span></div>`).join('')}
      <div class="muted">Найденные файлы лицензий передаются вместе с данными. Может потребоваться повторная активация.</div></div>`
      : '';
  m.querySelector('#insp').innerHTML =
    dataInfo +
    `${insp.compatible ? '' : `<div class="warnbox small" style="color:var(--text)">${esc(insp.reason)}</div>`}
    <div style="margin-top:6px;color:var(--text)"><b>Что отправить:</b>${opts.map((o, i) => `<label class="switch" style="align-items:flex-start"><input type="radio" name="what" value="${esc(o.v)}" ${i === 0 ? 'checked' : ''} style="margin-top:3px"><span>${o.t}<div class="muted small">${o.d}</div></span></label>`).join('')}</div>`;
  const yes = m.querySelector('#yes');
  yes.disabled = false;
  yes.onclick = async () => {
    let file = m.querySelector('input[name=what]:checked').value;
    if (file === '__pick__') {
      const p = await api('/api/pickanyfile', {}).catch(() => ({}));
      if (!p.path) return;
      file = p.path;
    }
    m.close();
    try {
      await api('/api/plugin/approve', { id: r.id, file });
      toast(
        r.fromOnline
          ? `Разрешено — ${esc(r.plugin)} уже уходит к ${esc(r.fromNick)}`
          : `Разрешено — ${esc(r.plugin)} уйдёт к ${esc(r.fromNick)}, как только вы оба будете в сети`,
        5000
      );
    } catch (e) {
      fail(e);
    }
    pollNotes();
    if (S.tab === 'plugins') loadReqs();
  };
}
// что нужно плагину кроме его файла: библиотеки, папки, реестр
const DEP_ST = {
  take: ['уйдёт с плагином', 'ok'],
  inside: ['внутри плагина', ''],
  system: ['системная', ''],
  runtime: ['Visual C++', 'warn'],
  missing: ['не найдено', 'bad'],
  skip: ['не уйдёт', 'warn'],
};
const DEP_KIND = { lib: 'Библиотека', path: 'Путь в плагине', registry: 'Реестр' };
async function openDeps(encName, format) {
  const name = decodeURIComponent(encName);
  const m = modal(
    `<h2>Зависимости: ${esc(name)}</h2><div id="dp" class="muted">Разбираю файл плагина…</div><div class="row" style="justify-content:flex-end;margin-top:12px"><button class="primary" id="ok">Закрыть</button></div>`
  );
  m.querySelector('#ok').onclick = () => m.close();
  let d;
  try {
    d = await api('/api/plugin/deps?name=' + enc(name) + '&format=' + enc(format));
  } catch (e) {
    m.querySelector('#dp').innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
    return;
  }
  const items = (d.items || []).filter(x => x.status !== 'system');
  const sys = (d.items || []).length - items.length;
  m.querySelector('#dp').innerHTML =
    `<div class="small mono" style="color:var(--text);word-break:break-all">${esc(d.binary)}${(d.arch || []).length ? ` <span class="tag">${esc(d.arch.join(', '))}</span>` : ''}</div>
    <div class="small" style="margin-top:10px;color:var(--text)"><b>Уйдёт вместе с плагином</b>${(d.data || []).length ? (d.data || []).map(x => `<div>✓ ${esc(x.root === 'PLUGINDIR' ? x.rel + ' (рядом с плагином)' : x.rel)} <span class="muted">${fmtSize(x.size)} — ${esc(x.why || '')}</span></div>`).join('') : '<div class="muted">только сам плагин — ничего снаружи ему не нужно</div>'}
      ${(d.dataSkip || []).map(x => `<div>— ${esc(x.rel)} <span class="muted">${fmtSize(x.size)}: ${esc(x.note)}</span></div>`).join('')}
      ${(d.registry || []).length ? `<div style="margin-top:6px"><b>Пути в реестре</b> (запишутся у друга, если у него их нет):</div>${d.registry.map(r => `<div class="mono small">${esc(r.hive + '\\' + r.key)} · ${esc(r.name || '(по умолчанию)')} = ${esc(r.data)}</div>`).join('')}` : ''}</div>
    ${items.length ? `<table style="margin-top:10px"><tr><th>Что</th><th>Нашлось</th><th></th></tr>${items.map(x => `<tr><td><span class="muted small">${DEP_KIND[x.kind] || x.kind}</span><div class="mono small" style="word-break:break-all">${esc(x.name)}</div></td><td class="mono small" style="word-break:break-all">${esc(x.path || '')}${x.note ? `<div class="muted" style="font-family:inherit">${esc(x.note)}</div>` : ''}</td><td><span class="tag ${DEP_ST[x.status]?.[1] || ''}">${DEP_ST[x.status]?.[0] || x.status}</span></td></tr>`).join('')}</table>` : ''}
    <div class="muted small" style="margin-top:8px">${sys ? `Системных библиотек: ${sys} — они есть на любом компьютере. ` : ''}Файлы лицензий переносятся с данными. Активацию с привязкой к компьютеру или аккаунту может потребоваться выполнить повторно.</div>`;
}
// итог установки полученного плагина: что разложено и что стоит проверить
function showReport(rep, again, check) {
  const w = rep.warnings || [];
  const m =
    modal(`<h2>${check ? 'Проверка: ' + esc(rep.plugin) + (w.length ? ' — есть проблемы' : ' — всё в порядке') : esc(rep.plugin) + ' установлен' + (w.length ? ' — есть что проверить' : '')}</h2>
    <div class="muted small">от ${esc(rep.from || '?')} · <span class="mono">${esc(rep.path)}</span></div>
    ${(rep.data || []).length ? `<div class="small" style="margin-top:10px"><b>Данные плагина разложены:</b>${rep.data.map(d => `<div class="mono">✓ ${esc(d)}</div>`).join('')}</div>` : ''}
    ${w.map((x, i) => `<div class="warnbox small" style="margin-top:10px;color:var(--text);white-space:pre-line">${esc(x.text)}${x.action === 'vcredist' ? `<div style="margin-top:6px"><button class="sm primary" data-act="vcredist">Установить Visual C++</button></div>` : ''}${x.action === 'retrydata' ? `<div style="margin-top:6px"><button class="sm primary" data-act="retrydata" data-arg="${esc(x.arg || '')}">Попробовать ещё раз</button></div>` : ''}</div>`).join('')}
    <div class="card small muted" style="margin-top:10px">Если FL был открыт — перезапусти его. Если плагин всё равно открывается серым окном: в окне плагина ▾ → <i>Wrapper settings → Troubleshooting</i> → включи <b>Make bridged</b> и загрузи плагин заново.</div>
    <div class="row" style="justify-content:flex-end;margin-top:12px">${!check && rep.id && !String(rep.id).startsWith('check-') ? '<button class="ghost" id="rei" title="Плагин и все его данные придут заново">Переустановить начисто</button>' : ''}<button class="primary" id="ok">Понятно</button></div>`);
  m.querySelector('#ok').onclick = () => m.close();
  const rei = m.querySelector('#rei');
  if (rei)
    rei.onclick = () => {
      m.close();
      reinstallReq(rep.id);
    };
  m.querySelectorAll('[data-act=vcredist]').forEach(
    b =>
      (b.onclick = () => {
        m.close();
        runJob('/api/system/install-vcredist', {}, { title: 'Ставлю Microsoft Visual C++' });
      })
  );
  m.querySelectorAll('[data-act=retrydata]').forEach(
    b =>
      (b.onclick = () => {
        m.close();
        runJob('/api/plugin/retry-data', { id: b.dataset.arg }, { title: 'Кладу данные ' + rep.plugin });
      })
  );
  if (!again) api('/api/plugin/report-seen', { id: rep.id }).catch(() => {});
}
async function openReport(id) {
  try {
    const all = await api('/api/plugin/reports');
    if (all[id]) showReport(all[id], true);
  } catch (e) {
    fail(e);
  }
}
function offerInstaller(it) {
  const m = modal(`<h2>${esc(it.from)} прислал установщик</h2><div><b>${esc(it.plugin)}</b></div>
    <div class="muted small" style="margin-top:8px">Файл уже в «Загрузках»: <span class="mono">${esc(it.path)}</span>. Запусти и пройди установку как обычно. После установки в FL: Options → Manage plugins → Find installed plugins.</div>
    <div class="row" style="justify-content:flex-end;margin-top:14px"><button id="later" class="ghost">Потом сам</button><button id="yes" class="primary">Запустить установщик</button></div>`);
  m.querySelector('#later').onclick = () => {
    api('/api/plugin/run-installer', { id: it.id, skip: '1' }).catch(() => {});
    m.close();
  };
  m.querySelector('#yes').onclick = async () => {
    m.close();
    try {
      await api('/api/plugin/run-installer', { id: it.id });
    } catch (e) {
      fail(e);
    }
  };
}

async function loadReqs() {
  try {
    const r = await api('/api/plugin/requests');
    S.reqs = r.requests || [];
    S.busy = r.busy || S.busy;
    if (S.tab === 'plugins' && S.sel) renderMain();
  } catch (e) {
    S.reqs = S.reqs || [];
  }
}
const RS = {
  pending: ['ждёт ответа', 'warn'],
  approved: ['разрешён — ждёт, когда оба в сети', 'blue'],
  streaming: ['передаётся…', 'blue'],
  declined: ['отказ', 'bad'],
  installed: ['установлен', 'ok'],
  failed: ['ошибка', 'bad'],
  cancelled: ['отменён', ''],
};
function renderPlugins() {
  if (!S.analysis) return `<p class="muted">Сканирую плагины… (первый раз может занять до минуты)</p>`;
  if (!S.reqs) {
    loadReqs();
  }
  const rows = S.analysis.plugins,
    me = S.state.me,
    myId = S.state.userId;
  const reqs = S.reqs || [];
  const machines = S.proj.machines || [];
  const osOf = u => (machines.find(m => m.user === u) || {}).os || (u === me ? S.state.os : '?');
  const stTag = { ok: 'ok', missing: 'bad', 'other-format': 'warn', unknown: '' };
  const stName = { ok: 'есть', missing: 'нет', 'other-format': 'другой формат', unknown: '?' };
  const users = [...new Set(rows.flatMap(r => r.machines.map(m => m.user)))];
  const myReq = name =>
    reqs.find(
      r =>
        r.from === myId &&
        r.plugin.toLowerCase() === name.toLowerCase() &&
        ['pending', 'approved', 'streaming'].includes(r.status)
    );
  const needs = r => {
    const m = r.machines.find(x => x.user === me);
    return r.format !== 'FL' && (!m || m.state !== 'ok') && !myReq(r.name);
  };
  // per friend: everything this project needs that I don't have and he does
  const bulk = {};
  for (const r of rows)
    if (needs(r))
      for (const o of r.machines) if (o.user !== me && o.state === 'ok') (bulk[o.user] = bulk[o.user] || []).push(r);
  // в таблице только состояние и главное действие; остальное — в карточке плагина (нажать на плагин)
  const cell = (r, u) => {
    const m = r.machines.find(x => x.user === u) || { state: 'unknown' };
    let h = `<span class="tag ${stTag[m.state]}">${stName[m.state]}${m.version ? ' ' + esc(m.version) : ''}</span>${m.note && m.state !== 'unknown' ? `<div class="muted small">${esc(m.note)}</div>` : ''}`;
    if (u !== me || r.format === 'FL') return h;
    const q = myReq(r.name);
    if (q)
      return (
        h +
        `<div style="margin-top:4px"><span class="tag blue">${q.status === 'streaming' ? 'передаётся…' : q.status === 'approved' ? 'разрешён' + (q.toOnline ? '' : ' — ждём ' + esc(q.toNick)) : 'запрошен у ' + esc(q.toNick)}</span></div>`
      );
    const lq = lastReq(r.name);
    if (lq && lq.status === 'installed' && lq.partial)
      h += `<div style="margin-top:4px"><span class="tag warn" title="Плагин встал, но не всё из его данных легло — открой карточку">установлен частично</span></div>`;
    if (m.state !== 'ok') {
      const donors = (r.machines || []).filter(x => x.user !== me && x.state === 'ok');
      if (donors.length === 1)
        h += `<div style="margin-top:4px"><button class="sm" onclick="event.stopPropagation();requestPlugins('${enc(donors[0].user)}',[['${enc(r.name)}','${r.format}','${enc(r.vendor || '')}']])">Скачать у ${esc(donors[0].user)}</button></div>`;
      else if (donors.length > 1)
        h += `<div style="margin-top:4px"><button class="sm" onclick="event.stopPropagation();openPluginCard('${enc(r.name)}','${r.format}')">Скачать…</button></div>`;
    }
    return h;
  };
  const lastReq = name =>
    reqs
      .filter(r => r.from === myId && r.plugin.toLowerCase() === name.toLowerCase())
      .sort((a, b) => b.updated - a.updated)[0];
  S.pluginRows = {};
  for (const r of rows) S.pluginRows[r.name + '|' + r.format] = r;
  S.bulk = bulk;
  // только передачи, запрошенные из этого проекта (тот же плагин для другого проекта — в окне «Передачи»)
  const projReqs = reqs.filter(q => q.project && q.project === S.proj.reqKey);
  return `<div class="muted small" style="margin:10px 0">Список собирается из .flp и записывается в <b>PLUGINS.txt</b> в корне проекта. Каждый участник при отправке публикует, какие плагины (и каких версий) стоят у него.
      ${S.analysis.machines < 2 ? '<br><span style="color:var(--warn)">Пока известен только твой компьютер — данные друга появятся после его первой отправки.</span>' : ''}</div>
    <div class="row"><button onclick="rescan()">Пересканировать мои плагины</button>
      ${Object.keys(bulk).length ? `<button class="primary" onclick="bulkDonorMenu(event)">Скачать недостающие плагины ▾</button>` : ''}
      <a class="small" onclick="openPluginManager('settings')">где я ищу плагины →</a></div>
    ${
      rows.length
        ? `<table><tr><th>Плагин</th><th>Формат</th>${users.map(u => `<th>${esc(u)}${u === me ? ' (ты)' : ''}</th>`).join('')}<th>Где взять</th></tr>
    ${rows
      .map(
        r => `<tr class="prow" onclick="openPluginCard('${enc(r.name)}','${r.format}')" title="Нажми — проверка, зависимости, переустановка, ссылка и файлы"><td><b class="pname">${esc(r.name)}</b>${r.vendor ? `<div class="muted small">${esc(r.vendor)}</div>` : ''}${r.problem ? `<div class="small" style="color:var(--warn)">${esc(r.problem)}</div>` : ''}</td>
      <td><span class="tag">${esc(r.format)}</span></td>${users.map(u => `<td>${cell(r, u)}</td>`).join('')}
      <td class="small">${r.link ? `<a href="${esc(r.link)}" onclick="event.stopPropagation()">ссылка</a>` : ''}${(r.files || []).length ? ` <span class="muted">файлов: ${r.files.length}</span>` : ''}</td></tr>`
      )
      .join('')}</table>
      <div class="muted small" style="margin-top:4px">Нажми на плагин — проверка, зависимости, переустановка, ссылка и файлы.</div>`
        : '<p class="muted">Плагинов не найдено.</p>'
    }
    ${projReqs.length ? `<h3>Передачи плагинов этого проекта</h3>${reqTable(projReqs.slice(0, 30))}` : ''}
    <div class="card small muted">Плагин идёт с компьютера друга на твой: если вы в одной сети — напрямую по ней, иначе потоком через сервер FruityHub (там ничего не хранится). Поэтому оба компьютера должны быть включены и FruityHub открыт. Плагин сам ложится в папку, где у тебя лежат плагины этого формата, и добавляется в базу плагинов FL. Между одинаковыми системами плагин переносится и ставится сам. Между Mac и Windows — если в пакете плагина есть версия для обеих систем (так бывает у VST3), иначе друг пришлёт установщик, и FruityHub его запустит. Вместе с плагином переносятся его данные (пресеты, ресурсы и файлы лицензий из «Документов», ProgramData, AppData), если они не больше 2 ГБ. Большие библиотеки сэмплов не переносятся. Файла лицензии может быть недостаточно для активации на другом компьютере — после установки FruityHub покажет, что ещё нужно сделать.</div>`;
}
function reqTable(list) {
  const myId = S.state.userId;
  return `<table><tr><th>Когда</th><th>Плагин</th><th>Кто → у кого</th><th>Статус</th><th></th></tr>${list
    .map(
      r => `<tr><td class="muted">${fmtTime(r.created)}</td><td>${esc(r.plugin)} <span class="tag">${esc(r.format)}</span></td><td>${esc(r.fromNick)} → ${esc(r.toNick)}</td>
      <td>${r.status === 'installed' && r.partial ? '<span class="tag warn" title="Плагин встал, но не всё из его данных легло">установлен частично</span>' : `<span class="tag ${RS[r.status]?.[1] || ''}">${RS[r.status]?.[0] || r.status}</span>`}${r.note ? `<div class="muted small">${esc(r.note)}</div>` : ''}${r.status === 'installed' && r.from === myId ? `<div><a class="small" onclick="openReport('${r.id}')">что установлено и проверено</a></div>` : ''}</td>
      <td>${r.to === myId && r.status === 'pending' ? (S.busy?.[r.id] === 'approving' ? '<span class="tag blue">разрешаю…</span>' : `<button class="sm primary" onclick="reviewReq('${r.id}')">Ответить</button>`) : ''}
        ${r.status === 'approved' ? `<span class="muted small">${r.to === myId ? (r.fromOnline ? 'сейчас уйдёт' : esc(r.fromNick) + ' не в сети') : r.toOnline ? 'сейчас придёт' : esc(r.toNick) + ' не в сети'}</span>` : ''}
        ${['pending', 'approved', 'streaming'].includes(r.status) ? ` <a class="small muted" onclick="cancelReq('${r.id}')">${r.status === 'streaming' ? 'прервать' : 'отменить'}</a>` : ''}
        ${['failed', 'cancelled'].includes(r.status) ? ` <button class="sm" onclick="retryReq('${r.id}')">Повторить</button>` : ''}
        ${r.status === 'installed' && r.from === myId ? ` <a class="small muted" onclick="reinstallReq('${r.id}')" title="Плагин и все его данные придут заново и заменят твои">переустановить</a>` : ''}
        ${r.status === 'installed' && r.from !== myId ? ` <a class="small muted" onclick="retryReq('${r.id}')" title="Передать этот плагин ещё раз">ещё раз</a>` : ''}</td></tr>`
    )
    .join('')}</table>`;
}
// items: [[encName, format, encVendor], ...]
async function requestPlugins(encTo, items) {
  const to = decodeURIComponent(encTo);
  const list = items.map(([n, f, v]) => ({ plugin: decodeURIComponent(n), format: f, vendor: decodeURIComponent(v) }));
  const what =
    list.length === 1
      ? `плагин <b>${esc(list[0].plugin)}</b>`
      : `плагины (${list.length}): ${list
          .slice(0, 8)
          .map(x => '<b>' + esc(x.plugin) + '</b>')
          .join(', ')}${list.length > 8 ? '…' : ''}`;
  // друг заранее разрешил скачивать у него плагины — лишний вопрос не задаём
  let donor = null;
  try {
    donor = (await getUsers()).find(u => u.nick === to);
  } catch (e) {}
  const auto = donor && (donor.sharing === 'all' || donor.sharing === 'members');
  if (
    !auto &&
    !(await uiConfirm(
      `Скачать ${what} с компьютера <b>${esc(to)}</b>? ${esc(to)} увидит запрос у себя и разрешит или откажет.`,
      { ok: 'Отправить запрос', title: 'Плагины от друга' }
    ))
  )
    return;
  try {
    const r = await api('/api/plugin/request', { to, items: list, path: S.sel || '' });
    toast(
      r.length
        ? auto
          ? `${esc(to)} разрешил передачу заранее — ${r.length === 1 ? 'плагин придёт' : 'плагины придут'} и ${r.length === 1 ? 'установится' : 'установятся'} сами`
          : `Запрошено у ${esc(to)}: ${r.length}`
        : 'Эти плагины уже запрошены — ждём ответа',
      5000
    );
    loadReqs();
  } catch (e) {
    fail(e);
  }
}
function requestBulk(encU) {
  const list = (S.bulk || {})[decodeURIComponent(encU)] || [];
  if (list.length)
    requestPlugins(
      encU,
      list.map(r => [enc(r.name), r.format, enc(r.vendor || '')])
    );
}
async function cancelReq(id) {
  const r = (S.reqs || []).find(x => x.id === id);
  if (
    r?.status === 'streaming' &&
    !(await uiConfirm(`Прервать передачу <b>${esc(r.plugin)}</b>? Её можно будет повторить.`, {
      ok: 'Прервать',
      danger: true,
    }))
  )
    return;
  try {
    await api('/api/plugin/cancel', { id });
    toast('Передача отменена');
    loadReqs();
    if (document.querySelector('#tl') && S.trRefresh) S.trRefresh();
  } catch (e) {
    fail(e);
  }
}
// карточка плагина: всё, что можно с ним сделать
function openPluginCard(encName, format) {
  const name = decodeURIComponent(encName),
    r = (S.pluginRows || {})[name + '|' + format];
  if (!r) return;
  const me = S.state.me,
    myId = S.state.userId,
    machines = S.proj?.machines || [];
  const osOf = u => (machines.find(m => m.user === u) || {}).os || (u === me ? S.state.os : '?');
  const mine = r.machines.find(x => x.user === me) || { state: 'unknown' };
  const reqs = (S.reqs || []).filter(q => q.plugin.toLowerCase() === name.toLowerCase());
  const active = reqs.find(q => q.from === myId && ['pending', 'approved', 'streaming'].includes(q.status));
  const others = r.machines.filter(x => x.user !== me && x.state === 'ok');
  const sameOS = others.filter(o => osOf(o.user) === S.state.os);
  const stName = { ok: 'есть', missing: 'нет', 'other-format': 'другой формат', unknown: 'неизвестно' },
    stTag = { ok: 'ok', missing: 'bad', 'other-format': 'warn', unknown: '' };
  const act = [];
  if (r.format !== 'FL') {
    if (active)
      act.push(
        `<div><span class="tag blue">${active.status === 'streaming' ? 'передаётся…' : active.status === 'approved' ? 'разрешён — придёт, когда вы оба в сети' : 'запрошен у ' + esc(active.toNick)}</span> <button class="sm ghost" data-a="cancel">Отменить</button></div>`
      );
    else if (mine.state === 'ok') {
      act.push(`<div class="row"><button class="sm" data-a="check">Проверить</button><button class="sm" data-a="deps">Зависимости</button><button class="sm" data-a="diag" title="Подробный отчёт: где лежат файлы плагина, что уйдёт при передаче — чтобы сравнить два компьютера">Диагностика</button>
        ${sameOS.map(o => `<button class="sm" data-a="reinstall" data-u="${esc(o.user)}" title="Плагин и все его данные придут заново и заменят твои">Переустановить у ${esc(o.user)}</button>`).join('')}<span class="spacer"></span><button class="sm danger" data-a="uninstall" title="Убрать плагин и его данные с этого компьютера (в Корзину)">Удалить у себя</button></div>`);
      const diff = others.filter(o => o.version && mine.version && o.version !== mine.version);
      if (diff.length)
        act.push(
          `<div class="row">${diff.map(o => `<button class="sm ghost" data-a="get" data-u="${esc(o.user)}" title="Твоя версия заменится, старая уйдёт в резервную копию">Взять версию ${esc(o.version)} у ${esc(o.user)}</button>`).join('')}</div>`
        );
      act.push(
        `<div class="muted small">«Проверить» — загружается ли плагин, хватает ли библиотек и папок, нужна ли активация. «Переустановить» — плагин и все его данные придут заново и заменят то, что есть.</div>`
      );
    } else if (others.length) {
      act.push(`<div class="row"><button class="sm primary" id="donor-menu">Скачать у участника ▾</button></div>
        <label class="switch small"><input type="checkbox" id="lite" ${isLite(name) ? 'checked' : ''}> Без библиотеки пресетов — только сам плагин и то, без чего он не запустится</label>`);
    } else
      act.push(
        '<div class="muted small">Ни у кого из участников этого плагина нет — возьми по ссылке ниже или у производителя.</div>'
      );
  }
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2 style="margin:0">${esc(name)}</h2><button class="ghost sm" id="x">✕</button></div>
    <div class="muted small">${esc(r.vendor || '')} ${r.vendor ? '·' : ''} <span class="tag">${esc(r.format)}</span>${r.problem ? ` <span style="color:var(--warn)">${esc(r.problem)}</span>` : ''}</div>
    <table style="margin-top:8px">${r.machines.map(x => `<tr><td>${esc(x.user)}${x.user === me ? ' (ты)' : ''} <span class="muted small">${esc(osOf(x.user))}</span></td><td><span class="tag ${stTag[x.state] || ''}">${stName[x.state] || x.state}${x.version ? ' ' + esc(x.version) : ''}</span>${x.note ? ` <span class="muted small">${esc(x.note)}</span>` : ''}</td></tr>`).join('')}</table>
    ${act.length ? `<h3>Действия</h3>${act.join('')}` : ''}
    <h3>Ссылка и файлы</h3>
    <div class="small">${r.link ? `<a href="${esc(r.link)}">${esc(r.link)}</a>` : '<span class="muted">Ссылки нет</span>'}${r.linkNote ? `<div class="muted">${esc(r.linkNote)}</div>` : ''}
      ${(r.files || []).map(f => `<div><a data-f="${esc(f.name)}" title="Скачать в «Загрузки»">${esc(f.name)}</a> <span class="muted">${fmtSize(f.size)}</span></div>`).join('')}</div>
    <div style="margin-top:6px"><button class="sm ghost" data-a="link">${r.link || (r.files || []).length ? 'Изменить ссылку и файлы…' : '+ Ссылка / файл…'}</button></div>
    ${reqs.length ? `<h3>Передачи этого плагина</h3>${reqTable(reqs.slice(0, 10))}` : ''}`);
  m.querySelector('.modal').style.width = 'min(820px,94vw)';
  m.querySelector('#x').onclick = () => m.close();
  const donorButton = m.querySelector('#donor-menu');
  if (donorButton) donorButton.onclick = async e => {
    const point = { clientX: e.clientX, clientY: e.clientY };
    let users = [];
    try { users = (await api('/api/users')).users || []; } catch (_) {}
    const online = new Map(users.map(u => [u.nick, !!u.online]));
    const donors = [...others].sort((a, b) => Number(online.get(b.user)) - Number(online.get(a.user)) || a.user.localeCompare(b.user));
    if (!m.isConnected) return;
    popMenu(point, donors.map(o => [
      `${online.get(o.user) ? '<span class="tag ok">в сети</span> ' : ''}${esc(o.user)}${osOf(o.user) !== S.state.os ? ` <span class="muted small">${esc(osOf(o.user))}</span>` : ''}`,
      () => { m.close(); requestPlugins(enc(o.user), [[enc(name), format, enc(r.vendor || '')]]); },
    ]));
  };
  const lt = m.querySelector('#lite');
  if (lt) lt.onchange = e => setLite(name, e.target.checked);
  const go = fn => () => {
    m.close();
    fn();
  };
  m.querySelectorAll('[data-a]').forEach(
    b =>
      (b.onclick = go(() => {
        const u = b.dataset.u ? enc(b.dataset.u) : '';
        switch (b.dataset.a) {
          case 'check':
            return checkPlugin(enc(name), format);
          case 'deps':
            return openDeps(enc(name), format);
          case 'diag':
            return pluginDiag(name, format);
          case 'reinstall':
            return reinstallPlugin(u, enc(name), format, enc(r.vendor || ''));
          case 'get':
            return requestPlugins(u, [[enc(name), format, enc(r.vendor || '')]]);
          case 'cancel':
            return cancelReq(active.id);
          case 'link':
            return editLink(enc(name), enc(r.link || ''), enc(r.linkNote || ''));
          case 'uninstall':
            return uninstallPlugin(name, format);
        }
      }))
  );
  m.querySelectorAll('[data-f]').forEach(a => (a.onclick = () => getLinkFile(enc(name), enc(a.dataset.f))));
}
const REINSTALL_TXT =
  'Плагин и <b>все</b> его данные (ресурсы, библиотеки, пути в реестре) придут заново и заменят то, что у тебя сейчас — даже то, что уже есть. Твои файлы с теми же именами уйдут в резервную копию (в системных папках просто заменятся).';
async function reinstallPlugin(encU, encName, format, encVendor) {
  const u = decodeURIComponent(encU),
    name = decodeURIComponent(encName);
  if (
    !(await uiConfirm(`Переустановить <b>${esc(name)}</b> с компьютера <b>${esc(u)}</b>?<br><br>${REINSTALL_TXT}`, {
      ok: 'Переустановить',
      title: 'Переустановка плагина',
    }))
  )
    return;
  try {
    const r = await api('/api/plugin/request', {
      to: u,
      path: S.sel || '',
      items: [{ plugin: name, format, vendor: decodeURIComponent(encVendor), reinstall: true }],
    });
    toast(
      r.length
        ? `Запрошено у ${esc(u)} — придёт и переустановится`
        : 'Этот плагин уже запрошен — дождись или отмени тот запрос'
    );
    loadReqs();
  } catch (e) {
    fail(e);
  }
}
async function reinstallReq(id) {
  const r = (S.reqs || []).find(x => x.id === id);
  if (
    !(await uiConfirm(`Переустановить <b>${esc(r?.plugin || 'плагин')}</b>?<br><br>${REINSTALL_TXT}`, {
      ok: 'Переустановить',
      title: 'Переустановка плагина',
    }))
  )
    return;
  try {
    await api('/api/plugin/retry', { id, reinstall: true });
    toast('Переустановлю, как только вы оба будете в сети');
    loadReqs();
    if (document.querySelector('#tl') && S.trRefresh) S.trRefresh();
  } catch (e) {
    fail(e);
  }
}
// удалить плагин у себя: что именно — галочками (сам плагин, его данные, запись в базе FL)
async function uninstallPlugin(name, format, after) {
  const m = modal(`<h2>Удалить ${esc(name)}</h2><div id="ul" class="muted">Ищу файлы плагина…</div>
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="no">Отмена</button><button class="danger" id="ok" disabled>Удалить</button></div>`);
  m.querySelector('.modal').style.width = 'min(780px,94vw)';
  m.querySelector('#no').onclick = () => m.close();
  let plan;
  try {
    plan = await api('/api/plugin/uninstall-plan', { name, format });
  } catch (e) {
    m.querySelector('#ul').innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
    return;
  }
  const KIND = { plugin: 'Плагин', data: 'Данные', fldb: 'База FL' };
  m.querySelector('#ul').innerHTML =
    `<div class="small" style="color:var(--text)">Отмеченное уйдёт в Корзину — оттуда можно вернуть${plan.items.some(i => i.admin) ? ' (из защищённых папок — в резервную папку FruityHub, понадобятся права администратора)' : ''}. Закрой FL Studio.</div>
    <div style="max-height:46vh;overflow:auto;margin-top:8px"><table>${plan.items
      .map(
        (
          it,
          i
        ) => `<tr><td style="width:24px"><input type="checkbox" style="width:auto" data-i="${i}" ${it.checked ? 'checked' : ''}></td>
      <td><span class="tag ${it.kind === 'plugin' ? 'bad' : ''}">${KIND[it.kind] || it.kind}</span></td><td class="mono small" style="word-break:break-all">${esc(it.path)}${it.why ? `<div class="muted" style="font-family:inherit">${esc(it.why)}</div>` : ''}</td>
      <td class="muted small" style="text-align:right;white-space:nowrap">${it.size ? fmtSize(it.size) : ''}</td></tr>`
      )
      .join('')}</table></div>
    <div class="muted small" style="margin-top:6px">Отдельные файлы лицензий и большие библиотеки — без галочки. Данные внутри выбранных папок удаляются вместе с ними. Пути в реестре не трогаются.</div>`;
  const boxes = [...m.querySelectorAll('input[data-i]')],
    ok = m.querySelector('#ok');
  const upd = () => {
    const n = boxes.filter(b => b.checked).length;
    ok.disabled = !n;
    ok.textContent = n ? `Удалить (${n})` : 'Удалить';
  };
  boxes.forEach(b => (b.onchange = upd));
  upd();
  ok.onclick = async () => {
    const paths = boxes.filter(b => b.checked).map(b => plan.items[+b.dataset.i].path);
    m.close();
    const j = await runJob('/api/plugin/uninstall', { name, format, paths }, { title: 'Удаляю ' + name });
    if (j && !j.error) {
      S.analysis = null;
      if (S.sel) loadProject(S.sel);
      after && after();
    }
  };
}
async function pluginDiag(name, format) {
  const j = await runJob('/api/plugin/diag', { name, format }, { title: 'Диагностика ' + name });
  if (!j || j.error || !j.result) return;
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2 style="margin:0">Диагностика: ${esc(name)}</h2><button class="ghost sm" id="x">✕</button></div>
    <div class="muted small">Сделай такую же диагностику на втором компьютере и пришли оба отчёта — по ним видно, чего не хватает.</div>
    <textarea id="dt" readonly style="width:100%;height:55vh;margin-top:8px;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12px;white-space:pre"></textarea>
    <div class="row" style="justify-content:flex-end;margin-top:8px"><button class="sm" id="snd" title="Появится в «Логах сервера» у администратора">Отправить в журнал сервера</button><button class="primary sm" id="cp">Копировать</button></div>`);
  m.querySelector('.modal').style.width = 'min(1000px,94vw)';
  const ta = m.querySelector('#dt');
  ta.value = j.result;
  m.querySelector('#x').onclick = () => m.close();
  m.querySelector('#cp').onclick = () => {
    ta.select();
    (navigator.clipboard ? navigator.clipboard.writeText(ta.value) : Promise.reject())
      .then(() => toast('Скопировано'))
      .catch(() => {
        try {
          document.execCommand('copy');
          toast('Скопировано');
        } catch (e) {
          uiAlert('Выдели текст и нажми Ctrl+C', 'Копирование');
        }
      });
  };
  m.querySelector('#snd').onclick = async () => {
    try {
      await api('/api/plugin/diag/send', { text: ta.value });
      toast('Отправлено в журнал сервера');
    } catch (e) {
      fail(e);
    }
  };
}
async function checkPlugin(encName, format) {
  const name = decodeURIComponent(encName);
  const j = await runJob('/api/plugin/check', { name, format }, { title: 'Проверяю ' + name });
  if (j && !j.error && j.result) showReport(j.result, true, true);
}
async function retryReq(id) {
  try {
    await api('/api/plugin/retry', { id });
    toast('Передам ещё раз, как только вы оба будете в сети');
    loadReqs();
    if (document.querySelector('#tl') && S.trRefresh) S.trRefresh();
  } catch (e) {
    fail(e);
  }
}
// передачи идут? — предупредить, прежде чем закрывать или обновлять приложение
async function confirmNoTransfers(what) {
  let a = [];
  try {
    a = await api('/api/transfers/active');
  } catch (e) {}
  if (!a.length) return true;
  return uiConfirm(
    `Сейчас идёт передача плагинов: ${a.map(x => '<b>' + esc(x.title) + '</b>').join(', ')}.<br><br>Если ${what}, передача оборвётся — её придётся повторить.`,
    { title: 'Идёт передача данных', ok: 'Всё равно', danger: true }
  );
}
function reviewReq(id) {
  const r = (S.reqs || []).find(x => x.id === id);
  if (r) askPermission(r);
}
function editLink(n, u, no) {
  n = decodeURIComponent(n);
  const row = (S.analysis?.plugins || []).find(r => r.name === n) || {};
  const m =
    modal(`<h2>Где взять «${esc(n)}»</h2><div class="muted small">Ссылка и файлы сохранятся в проекте и будут видны другу после отправки (и в PLUGINS.txt).</div>
    <label>Ссылка</label><input id="lu" value="${esc(decodeURIComponent(u))}" placeholder="https://…"><label>Заметка</label><input id="ln" value="${esc(decodeURIComponent(no))}" placeholder="бесплатный / версия 1.36 / лежит на диске">
    <label>Файлы (до 50 МБ: пресеты, небольшой установщик, инструкция…)</label>
    <div class="droplist">${(row.files || []).map(f => `<div class="drow"><span class="ellip">${esc(f.name)} <span class="muted small">${fmtSize(f.size)}${f.by ? ' · ' + esc(f.by) : ''}</span></span><button class="sm ghost" onclick="getLinkFile('${enc(n)}','${enc(f.name)}')">Скачать</button><button class="sm ghost" data-rm="${esc(f.name)}" title="Удалить">−</button></div>`).join('') || '<div class="muted small">Файлов нет</div>'}</div>
    <div style="margin-top:6px"><button class="sm" id="lf">＋ Прикрепить файл…</button></div>
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="ok" class="primary">Сохранить</button></div>`);
  m.querySelector('#ok').onclick = async () => {
    await api('/api/project/catalog', {
      Path: S.sel,
      Name: n,
      URL: m.querySelector('#lu').value,
      Note: m.querySelector('#ln').value,
    });
    m.close();
    S.analysis = null;
    loadProject(S.sel);
  };
  m.querySelector('#lf').onclick = async () => {
    await api('/api/project/catalog', {
      Path: S.sel,
      Name: n,
      URL: m.querySelector('#lu').value,
      Note: m.querySelector('#ln').value,
    }).catch(() => {});
    m.close();
    const j = await runJob(
      '/api/plugin/linkfile/add',
      { path: S.sel, plugin: n },
      { title: 'Прикрепляю файл к «' + n + '»' }
    );
    S.analysis = null;
    await loadProject(S.sel);
  };
  m.querySelectorAll('[data-rm]').forEach(
    b =>
      (b.onclick = async () => {
        if (!(await uiConfirm(`Удалить файл ${esc(b.dataset.rm)}?`, { ok: 'Удалить', danger: true }))) return;
        try {
          await api('/api/plugin/linkfile/remove', { path: S.sel, plugin: n, name: b.dataset.rm });
          m.close();
          S.analysis = null;
          await loadProject(S.sel);
          toast('Файл удалён');
        } catch (e) {
          fail(e);
        }
      })
  );
}
async function getLinkFile(n, f) {
  await runJob(
    '/api/plugin/linkfile/get',
    { path: S.sel, plugin: decodeURIComponent(n), name: decodeURIComponent(f) },
    { title: 'Скачиваю ' + decodeURIComponent(f) }
  );
}
async function openMissing(id) {
  const m = modal(
    `<div class="row" style="justify-content:space-between"><h2>Плагины, которых у тебя нет или другие версии</h2><button class="ghost sm" id="x">✕</button></div><div id="ml">Сравниваю…</div>`
  );
  m.querySelector('.modal').classList.add('wide');
  m.querySelector('#x').onclick = () => m.close();
  let r;
  try {
    r = await api('/api/user/missing?id=' + enc(id));
  } catch (e) {
    m.querySelector('#ml').innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
    return;
  }
  const u = r.user,
    list = (r.missing || []).concat((r.versions || []).map(v => ({ ...v, diff: true })));
  if (!r.updated) {
    m.querySelector('#ml').innerHTML =
      `<p class="muted">${esc(u.nick)} ещё ни разу не присылал список своих плагинов (он появится, когда ${esc(u.nick)} откроет FruityHub).</p>`;
    return;
  }
  if (!list.length) {
    m.querySelector('#ml').innerHTML =
      `<div class="okbox">У тебя есть всё, что есть у ${esc(u.nick)} (${r.total} плагинов), и тех же версий.</div>`;
    return;
  }
  m.querySelector('#ml').innerHTML =
    `<div class="muted small">У ${esc(u.nick)} (${esc(u.os)}) ${r.total} плагинов, у тебя нет ${(r.missing || []).length}. Отметь нужные. ${(r.versions || []).length ? ` Ещё ${r.versions.length} есть у вас обоих, но в разных версиях — они внизу списка; скачанная версия заменит твою (старая уйдёт в резервную копию).` : ''} ${u.os !== S.state.os ? 'Системы разные: придёт версия для твоей системы, если она есть в пакете плагина, иначе — установщик.' : ''}</div>
    <div class="row" style="margin-top:8px"><label class="switch small" style="margin:0"><input type="checkbox" id="all"> выделить всё</label><span class="spacer"></span><input id="flt" placeholder="поиск" style="width:200px"></div>
    <div style="max-height:46vh;overflow:auto;margin-top:6px"><table><tr><th></th><th>Плагин</th><th>Формат</th><th>Производитель</th></tr>${list.map((p, i) => `<tr data-n="${esc(p.name.toLowerCase())}"><td><input type="checkbox" style="width:auto" data-i="${i}"></td><td><b>${esc(p.name)}</b>${p.diff ? ` <span class="tag ${p.newer === 'true' ? 'ok' : 'warn'}">у него ${esc(p.version)}, у тебя ${esc(p.mine)}${p.newer === 'true' ? ' — новее' : ''}</span>` : p.version ? ` <span class="muted small">${esc(p.version)}</span>` : ''}</td><td><span class="tag">${esc(p.format)}</span></td><td class="muted small">${esc(p.vendor || '')}</td></tr>`).join('')}</table></div>
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button class="primary" id="go" disabled>Скачать выбранные</button></div>`;
  const boxes = [...m.querySelectorAll('input[data-i]')],
    go = m.querySelector('#go');
  const upd = () => {
    const n = boxes.filter(b => b.checked).length;
    go.disabled = !n;
    go.textContent = `Скачать выбранные (${n})`;
  };
  boxes.forEach(b => (b.onchange = upd));
  m.querySelector('#all').onchange = e => {
    boxes.forEach(b => {
      if (b.closest('tr').style.display !== 'none') b.checked = e.target.checked;
    });
    upd();
  };
  m.querySelector('#flt').oninput = e => {
    const q = e.target.value.toLowerCase();
    m.querySelectorAll('tr[data-n]').forEach(tr => (tr.style.display = tr.dataset.n.includes(q) ? '' : 'none'));
  };
  go.onclick = async () => {
    const items = boxes
      .filter(b => b.checked)
      .map(b => list[+b.dataset.i])
      .map(p => ({ plugin: p.name, format: p.format, vendor: p.vendor || '' }));
    try {
      const res = await api('/api/plugin/request', { to: u.nick, items });
      m.close();
      uiAlert(
        `Запрошено у ${u.nick}: ${res.length}.${u.sharing === 'ask' ? ` ${u.nick} увидит запросы у себя в FruityHub.` : ' Он разрешил это заранее — плагины придут и установятся сами, когда его FruityHub будет открыт.'}\n\nХод передачи — в окне «Плагины» → «Передачи».`,
        'Запросы отправлены'
      );
    } catch (e) {
      fail(e);
    }
  };
}

async function bulkDonorMenu(event) {
  const point = { clientX: event.clientX, clientY: event.clientY };
  let people = [];
  try { people = await api('/api/users'); } catch (_) {}
  const online = new Map((Array.isArray(people) ? people : people.users || []).map(u => [u.nick, !!u.online]));
  popMenu(point, Object.entries(S.bulk || {}).sort(([a], [b]) => Number(online.get(b)) - Number(online.get(a)) || a.localeCompare(b)).map(([u, list]) => [
    `${online.get(u) ? '<span class="tag ok">в сети</span> ' : ''}${esc(u)} <span class="muted small">${list.length} плагин(ов)</span>`,
    () => requestBulk(enc(u)),
  ]));
}
