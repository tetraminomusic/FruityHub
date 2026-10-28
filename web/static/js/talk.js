// Вкладка «Обсуждение»: комментарии, задачи, статус проекта.

async function loadComments(fetch) {
  const path = S.sel;
  if (!path) return;
  try {
    const c = await api('/api/project/comments?path=' + enc(path) + (fetch ? '&fetch=1' : ''));
    if (S.sel === path) {
      S.comments = c;
      renderMain();
    }
  } catch (e) {}
  if (S.tab === 'talk')
    api('/api/project/seen', { path })
      .then(pollNotes)
      .catch(() => {});
}

function renderTalk() {
  const c = S.comments;
  if (!c) return `<p class="muted">Загружаю…</p>`;
  const st = c.status || {};
  let list = c.comments || [];
  if (S.cfilter === 'todo') list = list.filter(x => x.kind === 'todo' && !x.done);
  const todoCnt = (c.comments || []).filter(x => x.kind === 'todo' && !x.done).length;
  return `<div class="grid2" style="margin-top:12px;grid-template-columns:2fr 1fr">
    <div>
      <div class="card" style="margin-top:0">
        <div class="row" style="justify-content:space-between"><div class="seg"><button class="${S.ckind === 'note' ? 'on' : ''}" onclick="S.ckind='note';renderMain()">Заметка</button><button class="${S.ckind === 'todo' ? 'on' : ''}" onclick="S.ckind='todo';renderMain()">Задача</button></div>
        <span class="muted small">⌘/Ctrl + Enter — отправить</span></div>
        <textarea id="ctext" style="margin-top:8px" oninput="S.drafts=S.drafts||{};S.drafts[S.sel]=this.value" placeholder="${S.ckind === 'todo' ? 'Что нужно сделать: «дописать бридж», «перезаписать вокал во 2 куплете»…' : 'Что сейчас с проектом, идеи, что попробовать…'}" onkeydown="if((event.metaKey||event.ctrlKey)&&event.key==='Enter')postComment()">${esc((S.drafts || {})[S.sel] || '')}</textarea>
        <div class="row" style="justify-content:flex-end;margin-top:8px"><button class="primary" id="cpost" onclick="postComment()">Отправить</button></div>
      </div>
      <div class="row"><div class="seg"><button class="${S.cfilter === 'all' ? 'on' : ''}" onclick="S.cfilter='all';renderMain()">Всё</button><button class="${S.cfilter === 'todo' ? 'on' : ''}" onclick="S.cfilter='todo';renderMain()">Открытые задачи (${todoCnt})</button></div>
        <span class="spacer"></span><button class="ghost sm" onclick="loadComments(true)">↻ обновить</button></div>
      ${
        list.length
          ? list
              .map(
                x => `<div class="cm ${x.done ? 'done' : ''}">
        ${x.kind === 'todo' ? `<input type="checkbox" ${x.done ? 'checked' : ''} title="${x.done ? 'Сделано ' + esc(x.doneBy) : 'Отметить выполненной'}" onchange="toggleTodo('${x.id}',this.checked)">` : `<div class="av" style="background:${color(x.author)}">${esc((x.author || '?')[0].toUpperCase())}</div>`}
        <div class="body"><div class="small"><b>${esc(x.author)}</b> <span class="muted">${fmtTime(x.time)}</span> ${x.kind === 'todo' ? `<span class="tag ${x.done ? 'ok' : 'warn'}">${x.done ? 'сделано · ' + esc(x.doneBy) : 'задача'}</span>` : ''}
          ${x.author === c.me ? `<a class="muted small" style="float:right" onclick="delComment('${x.id}')">удалить</a>` : ''}</div>
          <div class="text">${esc(x.text)}</div></div></div>`
              )
              .join('')
          : `<p class="muted">${S.cfilter === 'todo' ? 'Открытых задач нет' : 'Пока тихо. Напиши, что сейчас с треком и что нужно доделать.'}</p>`
      }
    </div>
    <div><div class="card" style="margin-top:0"><b>Состояние проекта</b>
      <label>Этап</label><select id="sstage">${['', ...STAGES].map(s => `<option ${s === st.stage ? 'selected' : ''} value="${esc(s)}">${s || '— не указан —'}</option>`).join('')}</select>
      <label>Коротко</label><textarea id="stext" placeholder="напр.: «дроп готов, нужен бридж и вокал»">${esc(st.text || '')}</textarea>
      <div class="row" style="justify-content:space-between;margin-top:8px"><span class="muted small">${st.by ? `обновил ${esc(st.by)}, ${fmtTime(st.time)}` : ''}</span><button onclick="saveStatus()">Сохранить</button></div></div>
      <div class="muted small">Комментарии и статус уходят на сервер сразу и не трогают файлы проекта — можно писать, даже когда трек открыт в FL.</div></div>
  </div>`;
}
async function postComment() {
  const t = $('#ctext').value.trim();
  if (!t) return;
  $('#cpost').disabled = true;
  try {
    await api('/api/project/comment', { path: S.sel, kind: S.ckind, text: t });
    $('#ctext').value = '';
    if (S.drafts) delete S.drafts[S.sel];
    await loadComments(false);
  } catch (e) {
    fail(e);
  }
  const b = $('#cpost');
  if (b) b.disabled = false;
}
async function toggleTodo(id, done) {
  try {
    await api('/api/project/comment/update', { path: S.sel, id, done });
    loadComments(false);
  } catch (e) {
    fail(e);
    loadComments(false);
  }
}
async function delComment(id) {
  if (!(await uiConfirm('Удалить комментарий?', { ok: 'Удалить', danger: true }))) return;
  try {
    await api('/api/project/comment/update', { path: S.sel, id, delete: true });
    loadComments(false);
  } catch (e) {
    fail(e);
  }
}
async function saveStatus() {
  try {
    await api('/api/project/status', { path: S.sel, stage: $('#sstage').value, text: $('#stext').value });
    toast('Статус обновлён');
    loadComments(false);
  } catch (e) {
    fail(e);
  }
}
