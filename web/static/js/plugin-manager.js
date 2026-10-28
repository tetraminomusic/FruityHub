// Окно «Плагины»: мои плагины, люди, передачи, настройки обмена.

const PM_TABS = [
  ['mine', 'Мои плагины'],
  ['people', 'Люди'],
  ['transfers', 'Передачи'],
  ['settings', 'Настройки'],
];
async function openPluginManager(tab) {
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2 style="margin:0">Плагины</h2><button class="ghost sm" id="x">✕</button></div>
    <div class="seg" style="margin:10px 0 6px;flex-wrap:wrap">${PM_TABS.map(([k, t]) => `<button data-pt="${k}">${t}</button>`).join('')}</div><div id="pmb"></div>`);
  m.querySelector('.modal').style.width = 'min(980px,95vw)';
  m.querySelector('#x').onclick = () => m.close();
  const body = m.querySelector('#pmb');
  let timer = null;
  const show = k => {
    clearInterval(timer);
    m.querySelectorAll('[data-pt]').forEach(b => b.classList.toggle('on', b.dataset.pt === k));
    try {
      localStorage.setItem('pmTab', k);
    } catch (e) {}
    body.innerHTML = '<p class="muted">Загружаю…</p>';
    if (k === 'mine') pmMine(body, m);
    else if (k === 'people') pmPeople(body, m);
    else if (k === 'settings') pmSettings(body, m);
    else {
      const load = async () => {
        if (!document.body.contains(m)) return clearInterval(timer);
        await loadReqs();
        if (!body.isConnected) return;
        body.innerHTML = (S.reqs || []).length
          ? reqTable(S.reqs.slice(0, 100))
          : '<p class="muted">Пока ничего не передавали. Плагины можно скачать у друга во вкладке «Плагины» проекта или во вкладке «Люди».</p>';
      };
      S.trRefresh = load;
      load();
      timer = setInterval(load, 4000);
    }
  };
  m.querySelectorAll('[data-pt]').forEach(b => (b.onclick = () => show(b.dataset.pt)));
  let t0 = tab;
  if (!t0) {
    try {
      t0 = localStorage.getItem('pmTab');
    } catch (e) {}
  }
  if (!PM_TABS.some(([k]) => k === t0)) t0 = 'mine';
  if (!S.state.registered && (t0 === 'people' || t0 === 'transfers')) t0 = 'mine';
  show(t0);
}
async function pmMine(el, m, force) {
  let list;
  try {
    list = (await api('/api/plugins/installed' + (force ? '?force=1' : ''))) || [];
  } catch (e) {
    el.innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
    return;
  }
  const fmts = [...new Set(list.map(p => p.format))].sort();
  el.innerHTML = `<div class="row"><input id="pmq" placeholder="поиск: название или производитель" style="flex:1;min-width:180px"><select id="pmf" style="width:auto"><option value="">все форматы</option>${fmts.map(f => `<option>${esc(f)}</option>`).join('')}</select><button class="sm" id="pmr" title="Найти плагины заново">↻ Пересканировать</button></div>
    <div class="muted small" style="margin:6px 0">Найдено плагинов: ${list.length}. Нажми на плагин — проверка, зависимости, «без пресетов» при передаче и полное удаление вместе с данными.</div>
    <div style="max-height:56vh;overflow:auto"><table id="pmt"><tr><th>Плагин</th><th>Формат</th><th>Версия</th><th>Производитель</th></tr>${list.map((p, i) => `<tr class="prow" data-i="${i}" data-q="${esc((p.name + ' ' + (p.vendor || '')).toLowerCase())}" data-f="${esc(p.format)}"><td><b class="pname">${esc(p.name)}</b>${S.state.config.litePlugins?.some(n => n.toLowerCase() === p.name.toLowerCase()) ? ' <span class="tag" title="При передаче библиотека пресетов не тянется">без пресетов</span>' : ''}${p.copies > 1 ? ` <span class="tag warn" title="Плагин лежит в нескольких местах">копий: ${p.copies}</span>` : ''}</td><td><span class="tag">${esc(p.format)}</span></td><td class="muted small">${esc(p.version || '')}</td><td class="muted small">${esc(p.vendor || '')}</td></tr>`).join('')}</table></div>`;
  const flt = () => {
    const q = el.querySelector('#pmq').value.toLowerCase(),
      f = el.querySelector('#pmf').value;
    el.querySelectorAll('tr[data-i]').forEach(
      tr => (tr.style.display = (!q || tr.dataset.q.includes(q)) && (!f || tr.dataset.f === f) ? '' : 'none')
    );
  };
  el.querySelector('#pmq').oninput = flt;
  el.querySelector('#pmf').onchange = flt;
  el.querySelector('#pmr').onclick = () => {
    el.innerHTML = '<p class="muted">Сканирую плагины…</p>';
    pmMine(el, m, true);
  };
  el.querySelectorAll('tr[data-i]').forEach(
    tr => (tr.onclick = () => pmCard(list[+tr.dataset.i], () => pmMine(el, m)))
  );
}
function isLite(name) {
  return (S.state.config.litePlugins || []).some(n => n.toLowerCase() === name.toLowerCase());
}
async function setLite(name, on) {
  try {
    await api('/api/settings', { lite: { plugin: name, on } });
    const l = (S.state.config.litePlugins || []).filter(n => n.toLowerCase() !== name.toLowerCase());
    if (on) l.push(name);
    S.state.config.litePlugins = l;
    toast(on ? `${esc(name)}: при передаче — без библиотеки пресетов` : `${esc(name)}: при передаче — с пресетами`);
  } catch (e) {
    fail(e);
  }
}
function pmCard(p, after) {
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2 style="margin:0">${esc(p.name)}</h2><button class="ghost sm" id="x">✕</button></div>
    <div class="muted small">${esc(p.vendor || '')} ${p.vendor ? '·' : ''} <span class="tag">${esc(p.format)}</span> ${p.version ? '· версия ' + esc(p.version) : ''}</div>
    <h3>Проверка</h3><div class="row"><button class="sm" data-a="check">Проверить</button><button class="sm" data-a="deps">Зависимости</button><button class="sm" data-a="diag">Диагностика</button></div>
    <div class="muted small">«Проверить» — загружается ли плагин, хватает ли библиотек и папок, нужна ли активация.</div>
    <h3>Передача</h3>
    <label class="switch"><input type="checkbox" id="lite" ${isLite(p.name) ? 'checked' : ''}> Когда скачиваю этот плагин у друга — не тянуть библиотеку пресетов</label>
    <div class="muted small">Придёт только сам плагин и то, без чего он не запустится. Пригодится для плагинов с огромными банками пресетов, которые у тебя уже есть или не нужны.</div>
    ${p.uninstall ? `<h3>Удаление</h3><div class="row"><button class="sm danger" data-a="uninstall">Удалить с этого компьютера…</button></div><div class="muted small">Сам плагин (все копии), его данные (пресеты, ресурсы) и запись в базе плагинов FL — галочками, что именно. Всё уходит в Корзину.</div>` : ''}`);
  m.querySelector('.modal').style.width = 'min(640px,94vw)';
  m.querySelector('#x').onclick = () => m.close();
  m.querySelector('#lite').onchange = e => setLite(p.name, e.target.checked);
  m.querySelectorAll('[data-a]').forEach(
    b =>
      (b.onclick = () => {
        m.close();
        const a = b.dataset.a;
        if (a === 'check') checkPlugin(enc(p.name), p.format);
        else if (a === 'deps') openDeps(enc(p.name), p.format);
        else if (a === 'diag') pluginDiag(p.name, p.format);
        else if (a === 'uninstall') uninstallPlugin(p.name, p.format, after);
      })
  );
}
async function pmPeople(el, m) {
  if (!S.state.registered) {
    el.innerHTML =
      '<p class="muted">Зарегистрируйся (Настройки → Профиль), чтобы видеть людей и скачивать у них плагины.</p>';
    return;
  }
  let us;
  try {
    us = await getUsers(true);
  } catch (e) {
    el.innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
    return;
  }
  const shareTxt = {
    off: 'не делится плагинами',
    ask: 'спрашивает перед отправкой',
    members: 'делится с участниками своих проектов',
    all: 'делится плагинами со всеми',
  };
  el.innerHTML =
    `<div class="muted small">Все, кто зарегистрирован на сервере FruityHub. Можно посмотреть, каких плагинов друга нет у тебя, и скачать их все разом.</div>` +
    us
      .map(
        u => `<div class="person">${avatar(u)}<div style="flex:1"><b>${esc(u.nick)}</b>${u.id === S.state.userId ? ' <span class="muted small">(ты)</span>' : ''}
      <div class="muted small">GitHub @${esc(u.github || '?')} · ${esc(u.os || '')} · ${u.online ? '<span style="color:var(--ok)">в сети</span>' : 'был ' + fmtTime(u.lastSeen)} · ${shareTxt[u.sharing] || ''}</div></div>
      ${u.id !== S.state.userId && u.sharing !== 'off' ? `<button class="sm" onclick="openMissing('${u.id}')">Чего у меня нет…</button><button class="sm primary" onclick="getAllFrom('${u.id}')">Скачать всё, чего нет</button>` : ''}</div>`
      )
      .join('');
}
// все плагины друга, которых у меня нет (без «других версий» — их лучше выбирать руками)
async function getAllFrom(id) {
  let r;
  try {
    r = await api('/api/user/missing?id=' + enc(id));
  } catch (e) {
    return fail(e);
  }
  const u = r.user,
    list = r.missing || [];
  if (!r.updated)
    return uiAlert(
      `${u.nick} ещё ни разу не присылал список своих плагинов — он появится, когда ${u.nick} откроет FruityHub.`,
      'Пока нечего скачать'
    );
  if (!list.length) return uiAlert(`У тебя уже есть всё, что есть у ${u.nick}.`, 'Всё уже есть');
  const auto = u.sharing === 'members' || u.sharing === 'all';
  if (
    !(await uiConfirm(
      `Скачать у <b>${esc(u.nick)}</b> все плагины, которых у тебя нет: <b>${list.length}</b>?<div class="muted small" style="margin-top:6px">${list
        .slice(0, 12)
        .map(p => esc(p.name))
        .join(
          ', '
        )}${list.length > 12 ? '…' : ''}</div>${auto ? '' : `<div class="muted small" style="margin-top:6px">${esc(u.nick)} увидит запросы у себя и разрешит их.</div>`}`,
      { title: 'Все плагины друга', ok: `Скачать (${list.length})` }
    ))
  )
    return;
  try {
    const res = await api('/api/plugin/request', {
      to: u.nick,
      items: list.map(p => ({ plugin: p.name, format: p.format, vendor: p.vendor || '' })),
    });
    toast(`Запрошено у ${esc(u.nick)}: ${res.length}. Ход — во вкладке «Передачи».`, 6000);
    loadReqs();
  } catch (e) {
    fail(e);
  }
}
async function pmSettings(el, m) {
  const st = S.state,
    c = st.config;
  el.innerHTML = `<h3 style="margin-top:4px">Кто может скачивать плагины с моего компьютера</h3>
    <div id="share">${Object.entries(SHARE)
      .map(
        ([k, [t1, d]]) =>
          `<label class="radio ${st.sharing === k ? 'on' : ''}"><input type="radio" name="sh" value="${k}" ${st.sharing === k ? 'checked' : ''} ${st.registered ? '' : 'disabled'}><span><b>${t1}</b><div class="muted small">${d}</div></span></label>`
      )
      .join('')}</div>
    <div class="muted small">Плагин идёт напрямую с твоего компьютера на компьютер друга, когда у обоих открыт FruityHub. Без вопросов отправляется только то, что подойдёт на систему друга как есть. Если нужен установщик — FruityHub всё равно спросит. Передавай только бесплатные плагины или те, на которые у друга есть лицензия.</div>
    <h3>Недостающие плагины</h3>
    <label class="switch"><input type="checkbox" id="autoplug" ${c.autoFetchPlugins ? 'checked' : ''}> Когда скачиваю проект — сразу запрашивать у участников плагины, которых у меня нет</label>
    <div class="muted small">Запрос уходит тому, у кого плагин есть (лучше с той же системой). Если он разрешил передачу без вопросов — плагин придёт и встанет сам.</div>
    ${(c.litePlugins || []).length ? `<h3>Без библиотеки пресетов</h3><div class="small">${c.litePlugins.map(n => `<span class="tag" style="margin:2px">${esc(n)} <a data-unlite="${esc(n)}" title="Снова тянуть пресеты">✕</a></span>`).join('')}</div>` : ''}
    <h3>Где ищу плагины</h3>
    <div class="muted small">Папки берутся из базы плагинов FL Studio (там все папки, которые ты добавлял в FL), из настроек FL, системного пути VST и стандартных мест.</div>
    <div id="pfolders" class="small" style="margin-top:6px">Сканирую…</div>
    <label>Свои папки</label>
    <div id="pdlist" class="droplist"></div>
    <div class="row" style="margin-top:6px"><button class="sm" id="pdadd">＋ Добавить папку…</button><span class="muted small">или перетащи папку сюда из Finder / Проводника</span></div>`;
  el.querySelector('#autoplug').onchange = async e => {
    try {
      await api('/api/settings', { autoFetchPlugins: e.target.checked });
      S.state.config.autoFetchPlugins = e.target.checked;
      toast('Сохранено');
    } catch (er) {
      fail(er);
    }
  };
  el.querySelectorAll('[data-unlite]').forEach(
    a =>
      (a.onclick = async () => {
        await setLite(a.dataset.unlite, false);
        pmSettings(el, m);
      })
  );
  el.querySelectorAll('input[name=sh]').forEach(
    i =>
      (i.onchange = async () => {
        try {
          await api('/api/account/update', { sharing: i.value });
          S.state.sharing = i.value;
          el.querySelectorAll('.radio').forEach(l => l.classList.toggle('on', l.querySelector('input').checked));
          toast('Сохранено');
        } catch (e) {
          fail(e);
        }
      })
  );
  api('/api/pluginfolders')
    .then(f => renderFolders(el.querySelector('#pfolders'), f))
    .catch(() => {});
  let dirs = [...(c.extraPluginDirs || [])];
  const saveDirs = async () => {
    try {
      await api('/api/settings', { extraPluginDirs: dirs });
      S.state.config.extraPluginDirs = dirs;
      drawDirs();
      api('/api/pluginfolders', {})
        .then(f => renderFolders(el.querySelector('#pfolders'), f))
        .catch(() => {});
    } catch (e) {
      fail(e);
    }
  };
  const drawDirs = () => {
    el.querySelector('#pdlist').innerHTML = dirs.length
      ? dirs
          .map(
            (d, i) =>
              `<div class="drow"><span class="mono ellip" title="${esc(d)}">${esc(d)}</span><button class="sm ghost" data-i="${i}" title="Убрать">−</button></div>`
          )
          .join('')
      : '<div class="muted small">Своих папок нет</div>';
    el.querySelectorAll('#pdlist [data-i]').forEach(
      b =>
        (b.onclick = () => {
          dirs.splice(+b.dataset.i, 1);
          saveDirs();
        })
    );
  };
  drawDirs();
  const addDir = d => {
    if (d && !dirs.includes(d)) {
      dirs.push(d);
      saveDirs();
    }
  };
  el.querySelector('#pdadd').onclick = async () => {
    const r = await api('/api/pickfolder', {}).catch(() => ({}));
    if (r.path) addDir(r.path);
  };
  bindFolderDrop(m.querySelector('.modal'), true, paths => paths.forEach(addDir));
}
function renderFolders(el, f) {
  if (!el) return;
  el.innerHTML = `<table><tr><th>Папка</th><th>Откуда</th><th style="text-align:right">Плагинов</th></tr>${(
    f.folders || []
  )
    .filter(x => x.exists)
    .map(
      x =>
        `<tr><td class="mono">${esc(x.path)}</td><td><span class="tag ${/FL/.test(x.source) ? 'v' : ''}">${esc(x.source)}</span></td><td style="text-align:right">${x.count}</td></tr>`
    )
    .join('')}</table>
  <div class="muted" style="margin-top:4px">Всего найдено плагинов: ${f.count}. ${f.dbCount ? `В базе плагинов FL: ${f.dbCount}.` : 'База плагинов FL не найдена (' + esc(f.db) + ') — открой в FL «Manage plugins» и нажми «Find installed plugins».'} <a onclick="rescanFolders(this)">↻ пересканировать</a></div>`;
}
async function rescanFolders(a) {
  a.textContent = 'сканирую…';
  const r = await api('/api/pluginfolders', {}).catch(() => null);
  if (r) renderFolders(a.closest('#pfolders'), r);
}
