// Режим разработчика и «Журнал».

async function openJournal() {
  const m = modal(
    `<div class="row" style="justify-content:space-between"><h2>Журнал</h2><button class="ghost sm" id="x">✕</button></div><div class="muted small">Что происходило: отправки, получения, приглашения, плагины.</div><div id="acts" style="margin-top:8px">Загружаю…</div>`
  );
  m.querySelector('#x').onclick = () => m.close();
  const list = await api('/api/activity').catch(() => []);
  const ic = { ok: '✓', error: '!', info: '•' },
    col = { ok: 'var(--ok)', error: 'var(--bad)', info: 'var(--blue)' };
  m.querySelector('#acts').innerHTML = list.length
    ? list
        .slice(0, 200)
        .map(
          a =>
            `<div class="act"><span class="tm">${fmtTime(a.time)}</span><span style="color:${col[a.kind]}">${ic[a.kind] || '•'}</span><span style="flex:1">${a.project ? `<b>${esc(a.project)}</b> — ` : ''}${esc(a.text)}</span></div>`
        )
        .join('')
    : '<p class="muted">Пока пусто.</p>';
}

function renderDevBtn() {
  let b = $('#devBtn');
  if (!S.state?.config?.devMode) {
    if (b) b.remove();
    return;
  }
  if (!b) {
    b = document.createElement('button');
    b.id = 'devBtn';
    b.className = 'ghost';
    b.textContent = 'Разработчик';
    b.onclick = openDev;
    $('#quitBtn').before(b);
  }
}
const KINDS = { git: 'git', http: 'сеть', ui: 'клик', job: 'задача', info: 'инфо', error: 'ошибка' };
async function openDev() {
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2>Режим разработчика</h2><button class="ghost sm" id="x">✕</button></div>
    <div class="seg" style="margin-bottom:10px"><button class="on" data-t="log">Журнал команд</button><button data-t="server">Сервер</button><button data-t="slog">Логи сервера</button><button data-t="sys">Система</button></div>
    <div id="devbody"></div>`);
  m.querySelector('.modal').style.width = 'min(1100px,94vw)';
  m.querySelector('#x').onclick = () => m.close();
  let timer = null,
    last = 0,
    entries = [];
  const stop = () => {
    clearInterval(timer);
    timer = null;
  };
  const old = m.close;
  m.close = () => {
    stop();
    old();
  };
  const tabs = m.querySelectorAll('.seg button');
  tabs.forEach(
    b =>
      (b.onclick = () => {
        tabs.forEach(x => x.classList.toggle('on', x === b));
        show(b.dataset.t);
      })
  );
  const body = m.querySelector('#devbody');
  const drawLog = () => {
    const f = body.querySelector('#df')?.value || '',
      q = (body.querySelector('#dq')?.value || '').toLowerCase(),
      onlyErr = body.querySelector('#de')?.checked;
    const list = entries.filter(
      x => (!f || x.kind === f) && (!q || x.text.toLowerCase().includes(q)) && (!onlyErr || x.err)
    );
    const el = body.querySelector('#dlog');
    if (!el) return;
    const atEnd = el.scrollTop + el.clientHeight >= el.scrollHeight - 20;
    el.innerHTML =
      list
        .slice(-1500)
        .map(
          x =>
            `<div class="e ${x.err ? 'err' : ''}"><span class="t">${new Date(x.t).toLocaleTimeString('ru-RU')}</span> <span class="k">${KINDS[x.kind] || x.kind}</span> ${esc(x.text)}${x.ms ? ` <span class="t">${x.ms} мс</span>` : ''}</div>`
        )
        .join('') || '<div class="muted">Пока пусто — сделай что-нибудь в приложении.</div>';
    if (atEnd) el.scrollTop = el.scrollHeight;
  };
  const poll = async () => {
    try {
      const r = await api('/api/dev/logs?since=' + last);
      if (r.entries.length) {
        entries = entries.concat(r.entries).slice(-3000);
        last = r.entries[r.entries.length - 1].n;
        drawLog();
      }
    } catch (e) {}
  };
  async function show(t) {
    stop();
    if (t === 'log') {
      const r = await api('/api/dev/logs?since=0').catch(() => ({ entries: [], file: '' }));
      entries = r.entries;
      last = entries.length ? entries[entries.length - 1].n : 0;
      body.innerHTML = `<div class="row" style="margin-bottom:8px"><select id="df" style="width:auto"><option value="">все</option>${Object.entries(
        KINDS
      )
        .map(([k, v]) => `<option value="${k}">${v}</option>`)
        .join('')}</select>
        <input id="dq" placeholder="поиск" style="width:220px"><label class="switch" style="margin:0"><input type="checkbox" id="de"> только ошибки</label>
        <label class="switch" style="margin:0"><input type="checkbox" id="dv" ${r.verbose ? 'checked' : ''}> служебные git-команды</label><span class="spacer"></span>
        <button class="sm" id="dcopy">Копировать</button><button class="sm" id="dopen">Файл журнала</button><button class="sm ghost" id="dclr">Очистить</button></div>
        <div class="devlog" id="dlog"></div><div class="muted small" style="margin-top:6px">Пишется и в файл: <span class="mono">${esc(r.file)}</span>. Токены и ключи в журнале скрыты.</div>`;
      ['#df', '#dq', '#de'].forEach(s => (body.querySelector(s).oninput = drawLog));
      body.querySelector('#dv').onchange = e => api('/api/dev/settings', { verbose: e.target.checked }).catch(fail);
      body.querySelector('#dclr').onclick = async () => {
        await api('/api/dev/clear', {});
        entries = [];
        drawLog();
      };
      body.querySelector('#dopen').onclick = () => api('/api/dev/openlogs', {}).catch(fail);
      body.querySelector('#dcopy').onclick = () => {
        const txt = entries
          .map(
            x =>
              `${new Date(x.t).toISOString()} [${x.kind}]${x.err ? ' ОШИБКА' : ''} ${x.text}${x.ms ? ' (' + x.ms + ' мс)' : ''}`
          )
          .join('\n');
        navigator.clipboard
          ?.writeText(txt)
          .then(() => toast('Скопировано'))
          .catch(() => uiAlert('Не получилось скопировать — открой файл журнала', 'Журнал'));
      };
      drawLog();
      const el = body.querySelector('#dlog');
      el.scrollTop = el.scrollHeight;
      timer = setInterval(poll, 1000);
    } else if (t === 'slog') {
      if (!S.state.hasAdminKey) {
        show('server');
        return;
      }
      let sl = [],
        slast = 0;
      const LV = { info: 'инфо', error: 'ошибка', client: 'приложение' };
      body.innerHTML = `<div class="row" style="margin-bottom:8px"><select id="sf" style="width:auto"><option value="">все</option><option value="error">только ошибки сервера</option><option value="client">ошибки из приложений</option><option value="bad">все ошибки</option><option value="info">инфо</option></select>
        <input id="sq" placeholder="поиск (ник, плагин, текст)" style="width:240px"><span class="spacer"></span>
        <button class="sm" id="scopy">Копировать</button><button class="sm" id="ssave">Скачать файл</button></div>
        <div class="devlog" id="slog"></div><div class="muted small" style="margin-top:6px">Здесь видно запросы с ошибками, все передачи плагинов (скорость, обрывы) и ошибки, которые присылают приложения участников (их можно отключить в настройках у себя). Обновляется само. Чтобы показать мне ошибку — нажми «Копировать» и вставь в чат.</div>`;
      const fmtE = x => `${new Date(x.t).toLocaleString('ru-RU')} [${LV[x.level] || x.level}] ${x.src}: ${x.text}`;
      const flt = () => {
        const f = body.querySelector('#sf').value,
          q = body.querySelector('#sq').value.toLowerCase();
        return sl.filter(
          x =>
            (!f || (f === 'bad' ? x.level !== 'info' : x.level === f)) &&
            (!q || (x.src + ' ' + x.text).toLowerCase().includes(q))
        );
      };
      const draw = () => {
        const el = body.querySelector('#slog');
        if (!el) return;
        const atEnd = el.scrollTop + el.clientHeight >= el.scrollHeight - 20;
        el.innerHTML =
          flt()
            .slice(-1500)
            .map(
              x =>
                `<div class="e ${x.level !== 'info' ? 'err' : ''}" style="white-space:pre-wrap"><span class="t">${new Date(x.t).toLocaleString('ru-RU')}</span> <span class="k">${LV[x.level] || x.level}</span> <b>${esc(x.src)}</b> ${esc(x.text)}</div>`
            )
            .join('') || '<div class="muted">Пусто.</div>';
        if (atEnd) el.scrollTop = el.scrollHeight;
      };
      const spoll = async () => {
        try {
          const r = await api('/api/admin/logs?since=' + slast);
          if (r.entries.length) {
            sl = sl.concat(r.entries).slice(-5000);
            slast = r.entries[r.entries.length - 1].n;
            draw();
          }
        } catch (e) {
          const el = body.querySelector('#slog');
          if (el && !sl.length)
            el.innerHTML = `<div class="badbox">${esc(e.message)}${/404/.test(e.message) ? ' — обнови сервер: ./deploy/server-up.sh' : ''}</div>`;
        }
      };
      body.querySelector('#sf').onchange = draw;
      body.querySelector('#sq').oninput = draw;
      body.querySelector('#scopy').onclick = () => {
        navigator.clipboard
          ?.writeText(flt().map(fmtE).join('\n'))
          .then(() => toast('Скопировано'))
          .catch(() => uiAlert('Не получилось скопировать — нажми «Скачать файл»', 'Логи'));
      };
      body.querySelector('#ssave').onclick = async () => {
        try {
          const p = await api('/api/admin/logs/save', {});
          toast('Сохранено: ' + esc(p), 6000);
        } catch (e) {
          fail(e);
        }
      };
      await spoll();
      const el = body.querySelector('#slog');
      if (el) el.scrollTop = el.scrollHeight;
      timer = setInterval(spoll, 2000);
    } else if (t === 'server') {
      if (!S.state.hasAdminKey) {
        body.innerHTML = `<div class="muted">Чтобы управлять сервером, нужен ключ администратора. Его печатает <span class="mono">deploy/server-up.sh</span> и сохраняет в <span class="mono">server.txt</span>.</div>
          <label>Ключ администратора</label><div class="row" style="flex-wrap:nowrap"><input id="ak" type="password"><button class="primary" id="aks">Сохранить</button></div>`;
        body.querySelector('#aks').onclick = async () => {
          try {
            await api('/api/dev/settings', { adminKey: body.querySelector('#ak').value });
            S.state.hasAdminKey = true;
            show('server');
          } catch (e) {
            fail(e);
          }
        };
        return;
      }
      body.innerHTML = '<div class="muted">Загружаю…</div>';
      let o;
      try {
        o = await api('/api/admin/overview');
      } catch (e) {
        body.innerHTML = `<div class="badbox">${esc(e.message)}</div><button class="sm" id="akr">Сменить ключ</button>`;
        body.querySelector('#akr').onclick = async () => {
          await api('/api/dev/settings', { adminKey: '' });
          S.state.hasAdminKey = false;
          show('server');
        };
        return;
      }
      const up = o.update;
      body.innerHTML = `<div class="muted small">${esc(S.state.server)} · данные: <span class="mono">${esc(o.dataDir)}</span> · база ${fmtSize(o.dbSize)} · запущен ${fmtTime(o.started)} · код регистрации <span class="mono">${esc(o.code)}</span></div>
        <h3>Пользователи (${o.users.length})</h3>
        <table><tr><th>Ник</th><th>GitHub</th><th>Система</th><th>Был в сети</th><th>Проекты</th><th></th></tr>
        ${o.users
          .map(
            u => `<tr><td><b>${esc(u.nick)}</b>${u.online ? ' <span class="tag ok">в сети</span>' : ''}</td><td>@${esc(u.github)}</td><td>${esc(u.os)} <span class="muted small">${esc(u.device || '')}</span></td><td class="muted">${u.lastSeen ? fmtTime(u.lastSeen) : '—'}</td><td class="small">${esc((u.projects || []).join(', '))}</td>
          <td><button class="sm danger" data-du="${esc(u.id)}" data-n="${esc(u.nick)}">Удалить</button></td></tr>`
          )
          .join('')}</table>
        <h3>Проекты (${o.projects.length})</h3>
        <table><tr><th>Название</th><th>Репозиторий</th><th>Владелец</th><th>Участники</th><th></th></tr>
        ${o.projects
          .map(
            p => `<tr><td><b>${esc(p.name)}</b></td><td class="mono small">${esc(p.repo)}</td><td>${esc(p.owner)}</td><td class="small">${esc(p.members.join(', '))}</td>
          <td><button class="sm danger" data-dp="${esc(p.id)}" data-n="${esc(p.name)}">Удалить</button></td></tr>`
          )
          .join('')}</table>
        <div class="muted small">Удаление убирает запись только с сервера: репозитории на GitHub и файлы у людей остаются. Удалённый пользователь сможет зарегистрироваться заново.</div>
        <h3>Передачи плагинов</h3><div class="row"><span class="muted small">всего ${o.requests}, активных ${o.activeRequests}</span><button class="sm" id="clr">Очистить историю</button></div>
        <h3>Бэкап и переезд</h3>
        <div class="row"><button class="sm" id="bk">Скачать бэкап</button><span class="muted small">Всё о пользователях, проектах и приглашениях + код и ключ сервера, одним файлом. Сервер ещё сам делает копию раз в сутки.</div>
        <label>Сервер переехал — новый адрес (приложения переключатся сами, если туда восстановлен бэкап)</label>
        <div class="row" style="flex-wrap:nowrap"><input id="mv" value="${esc(o.moveTo || '')}" placeholder="https://5-6-7-8.sslip.io:47943"><button class="sm" id="mvs">Сохранить</button></div>
        <h3>Обновления приложения</h3>
        <div class="small">${
          up
            ? `На сервере версия <b>${esc(up.version)}</b>: ${Object.keys(up.files || {})
                .map(esc)
                .join(', ')}`
            : '<span class="muted">На сервере обновлений нет.</span>'
        }</div>
        <div class="muted small" style="margin-top:4px">Удобнее всего выкладывать сборку командой <span class="mono">./deploy/server-up.sh</span> — она отправляет на сервер и сам сервер, и приложения из dist. Или вручную:</div>
        <div class="row" style="margin-top:6px"><input id="uv" placeholder="версия, напр. 0.7.1" style="width:160px"><select id="up" style="width:auto"><option value="windows-amd64">Windows (exe)</option><option value="darwin-arm64">Mac M-чип (zip)</option><option value="darwin-amd64">Mac Intel (zip)</option></select>
          <input id="un" placeholder="что нового (необязательно)" style="flex:1;min-width:160px"><button class="sm primary" id="ups">Выбрать файл и выложить…</button></div>`;
      body.querySelectorAll('[data-du]').forEach(
        b =>
          (b.onclick = async () => {
            if (
              !(await uiConfirm(
                `Удалить пользователя <b>${esc(b.dataset.n)}</b> с сервера? Его проекты без других участников тоже удалятся с сервера.`,
                { ok: 'Удалить', danger: true }
              ))
            )
              return;
            try {
              await api('/api/admin/delete-user', { id: b.dataset.du });
              toast('Удалён: ' + esc(b.dataset.n));
              show('server');
            } catch (e) {
              fail(e);
            }
          })
      );
      body.querySelectorAll('[data-dp]').forEach(
        b =>
          (b.onclick = async () => {
            if (
              !(await uiConfirm(
                `Удалить проект <b>${esc(b.dataset.n)}</b> с сервера? Репозиторий на GitHub останется.`,
                { ok: 'Удалить', danger: true }
              ))
            )
              return;
            try {
              await api('/api/admin/delete-project', { id: b.dataset.dp });
              toast('Удалён: ' + esc(b.dataset.n));
              show('server');
            } catch (e) {
              fail(e);
            }
          })
      );
      body.querySelector('#clr').onclick = async () => {
        try {
          const r = await api('/api/admin/clear-requests', {});
          toast('Удалено записей: ' + r.deleted);
          show('server');
        } catch (e) {
          fail(e);
        }
      };
      body.querySelector('#bk').onclick = async () => {
        try {
          const p = await api('/api/admin/backup', {});
          toast('Бэкап сохранён: ' + esc(p), 6000);
        } catch (e) {
          fail(e);
        }
      };
      body.querySelector('#mvs').onclick = async () => {
        try {
          await api('/api/admin/move', { url: body.querySelector('#mv').value });
          toast('Сохранено');
        } catch (e) {
          fail(e);
        }
      };
      body.querySelector('#ups').onclick = async () => {
        const v = body.querySelector('#uv').value.trim();
        if (!v) return uiAlert('Укажи версию');
        const j = await runJob(
          '/api/admin/publish',
          { version: v, platform: body.querySelector('#up').value, notes: body.querySelector('#un').value },
          { title: 'Выкладываю обновление' }
        );
        if (j && !j.error) show('server');
      };
    } else {
      const i = await api('/api/dev/info', {}).catch(e => ({ error: e.message }));
      body.innerHTML = `<table>${Object.entries(i)
        .map(
          ([k, v]) =>
            `<tr><td class="muted">${esc(k)}</td><td class="mono small">${esc(typeof v === 'object' ? JSON.stringify(v) : v)}</td></tr>`
        )
        .join('')}</table>
        <div class="row" style="margin-top:10px"><button class="sm" onclick="checkUpdate()">Проверить обновления</button><button class="sm" onclick="openRepos()">Мои репозитории на GitHub (удалить ненужные)…</button><button class="sm ghost" id="akc">Забыть ключ администратора</button></div>`;
      body.querySelector('#akc').onclick = async () => {
        await api('/api/dev/settings', { adminKey: '' });
        S.state.hasAdminKey = false;
        toast('Ключ удалён');
      };
    }
  }
  show('log');
}
