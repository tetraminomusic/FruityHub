// Вкладка «Доступные»: проекты с сервера, которых ещё нет на этом компьютере; приглашения.

function sideTab(t) {
  S.side = t;
  $('#st_my').classList.toggle('sel', t === 'my');
  $('#st_av').classList.toggle('sel', t === 'av');
  $('#plist').style.display = t === 'my' ? '' : 'none';
  $('#alist').style.display = t === 'av' ? '' : 'none';
  $('#sortbar').style.display = $('#legend').style.display = t === 'my' ? '' : 'none'; // сортировка и цвета — только для своих проектов
  if (t === 'av') loadAvailable();
}
async function loadAvailable(silent) {
  const el = $('#alist');
  if (!S.state?.registered) {
    el.innerHTML =
      '<div class="muted small" style="padding:10px">Зарегистрируйся, и здесь появятся проекты, в которых ты участвуешь.</div>';
    return;
  }
  if (!silent) el.innerHTML = '<div class="muted small" style="padding:10px">Смотрю, что есть на сервере…</div>';
  const seq = (S.availSeq = (S.availSeq || 0) + 1);
  let res;
  try {
    res = (await api('/api/available' + (silent ? '' : '?force=1'))) || [];
  } catch (e) {
    if (seq === S.availSeq) el.innerHTML = `<div class="badbox small" style="margin:8px">${esc(e.message)}</div>`;
    return;
  }
  if (seq !== S.availSeq) return; // пришёл ответ на старый запрос — более новый уже в пути
  S.avail = res;
  renderAvailable();
}
function renderAvailable() {
  const el = $('#alist'),
    list = S.avail || [];
  const badge = $('#avcnt');
  badge.textContent = list.length;
  badge.style.display = list.length ? '' : 'none';
  el.innerHTML =
    `<div class="muted small" style="padding:4px 4px 6px">Проекты, где ты участник, и все проекты, к которым у твоего GitHub-аккаунта есть доступ (свои с других устройств и где ты коллаборатор), — которых ещё нет на этом компьютере.</div>` +
    (list.length
      ? list
          .map(
            a => `<div class="avail"><div class="an">${esc(a.name)}</div>
      <div class="muted small">${a.isOwner ? 'твой проект' : 'от ' + esc(a.owner.nick)}</div>
      <div style="margin-top:6px">${a.downloading ? '<span class="tag blue">скачивается…</span>' : `<button class="sm primary" onclick="downloadAvailable('${a.id}')">Скачать</button>`}</div></div>`
          )
          .join('')
      : `<div class="muted small" style="padding:10px">Здесь пусто: всё уже скачано или тебя ещё никуда не позвали. Проект друга появится здесь, когда он позовёт тебя: в проекте Участники → ник или «Позвать всех».</div>`) +
    `<div style="padding:4px"><a class="small" onclick="loadAvailable()">↻ обновить</a></div>`;
}
function pickParentDialog(title, text, onGo, okText = 'Скачать') {
  const def = S.state?.config?.lastParent || '';
  const m = modal(`<h2>${title}</h2><div>${text}</div>
    <label>Куда скачать (папка, в которой появится папка проекта)</label><div class="row" style="flex-wrap:nowrap"><input id="cp" value="${esc(def)}" placeholder="напр. Музыка/Проекты"><button id="pick">Выбрать…</button></div>
    <div class="row" style="justify-content:flex-end;margin-top:14px"><button id="later" class="ghost">Позже</button><button class="primary" id="go">${okText}</button></div>`);
  m.querySelector('#later').onclick = () => m.close();
  m.querySelector('#pick').onclick = async () => {
    const r = await api('/api/pickfolder', {}).catch(() => ({}));
    if (r.path) m.querySelector('#cp').value = r.path;
  };
  m.querySelector('#go').onclick = () => {
    const parent = m.querySelector('#cp').value.trim();
    if (!parent) return uiAlert('Выбери папку, куда скачать проект');
    m.close();
    onGo(parent);
  };
}
async function openDownloaded(j) {
  if (j && !j.error && j.result && j.result.path) {
    S.sel = j.result.path;
    S.comments = null;
    S.preview = null;
    sideTab('my');
    await refreshAll();
  }
}
function downloadAvailable(id) {
  const a = (S.avail || []).find(x => x.id === id);
  if (!a) return;
  if (a.downloading) return toast('«' + esc(a.name) + '» уже скачивается');
  pickParentDialog(
    `⤓ Скачать «${esc(a.name)}»`,
    `<div class="muted small">${a.isOwner ? 'Твой проект' : 'Проект ' + esc(a.owner.nick)}. Аудио докачается автоматически.</div>`,
    async parent => {
      a.downloading = true;
      renderAvailable();
      const j = await runJob('/api/available/clone', { id, parent }, { title: 'Скачиваю «' + a.name + '»' });
      await openDownloaded(j);
      loadAvailable(true);
    }
  );
}
function acceptInvite(id) {
  const inv = (S.invites || []).find(x => x.id === id);
  if (!inv) return;
  pickParentDialog(
    'Приглашение в проект',
    `<b>${esc(inv.from.nick)}</b> приглашает тебя в проект <b>«${esc(inv.name)}»</b>.`,
    async parent => {
      S.invites = (S.invites || []).filter(x => x.id !== id);
      renderInvites();
      setTimeout(() => loadAvailable(true), 1500);
      const j = await runJob('/api/invitations/accept', { id, parent }, { title: 'Скачиваю «' + inv.name + '»' });
      await openDownloaded(j);
      pollNotes();
      loadAvailable(true);
    },
    'Принять и скачать'
  );
}
