// Состояние приложения (refreshAll) и опрос уведомлений с сервера (pollNotes): кто на связи, приглашения.

async function refreshAll() {
  S.state = await api('/api/state');
  const st = S.state;
  renderWho();
  setDim(st.config.bgDim || 50);
  renderDevBtn();
  $('#quitBtn').style.display = st.windowed ? 'none' : '';
  $('#helpBtn').style.display = st.config.hideHelp ? 'none' : '';
  const t = st.tools,
    tc = $('#toolsChip');
  if (!t.gitOk || !t.lfsOk) {
    tc.style.display = '';
    tc.innerHTML = `<span style="color:var(--warn)">${!t.gitOk ? 'нет git' : 'нет git-lfs'}</span> <a onclick="installTools()">установить</a>`;
  } else tc.style.display = 'none';
  renderList();
  renderInvites();
  if (S.side === 'av' || (st.registered && !S.avail)) loadAvailable(true);
  if (S.sel) await loadProject(S.sel);
  else renderMain();
  if (!S.introShown) {
    S.introShown = true;
    if (!st.config.guideSeen) {
      api('/api/settings', { guideSeen: true, seenVersion: st.version }).catch(() => {});
      st.config.guideSeen = true;
      st.config.seenVersion = st.version;
      await openGuide(true);
    } else if (st.version && st.version !== 'dev' && st.config.seenVersion !== st.version) {
      api('/api/settings', { seenVersion: st.version }).catch(() => {});
      st.config.seenVersion = st.version;
      openWhatsNew(true);
    }
  }
  if (!st.registered && !S.askedName) {
    S.askedName = true;
    openRegister(true);
  }
}
async function pollNotes() {
  if (document.hidden) return; // окно свёрнуто — не опрашиваем (догоним, когда развернут)
  let r;
  try {
    r = await api('/api/notifications');
  } catch (e) {
    return;
  }
  S.updating = r.updating || '';
  renderUpdateBar(r.update);
  S.notes = r.projects || {};
  S.invites = r.invites || [];
  S.incoming = r.incoming || [];
  S.ready = r.ready || [];
  S.toSend = r.toSend || [];
  S.busy = r.busy || {};
  S.serverError = r.serverError || '';
  renderList();
  renderInvites();
  renderWho();
  for (const q of S.incoming)
    if (!S.shownReq[q.id] && S.busy[q.id] !== 'approving') {
      S.shownReq[q.id] = 1;
      askPermission(q);
    }
  for (const it of r.installers || [])
    if (!S.shownReq['inst' + it.id]) {
      S.shownReq['inst' + it.id] = 1;
      offerInstaller(it);
    }
  for (const rep of r.reports || [])
    if (!S.shownReq['rep' + rep.id]) {
      S.shownReq['rep' + rep.id] = 1;
      showReport(rep);
    }
  for (const inv of S.invites)
    if (!S.shownReq['inv' + inv.id]) {
      S.shownReq['inv' + inv.id] = 1;
      acceptInvite(inv.id);
    }
  // background jobs (plugins sent/installed automatically) get their own cards
  api('/api/jobs')
    .then(js => {
      for (const j of js)
        if (!j.done && !S.followed[j.id])
          followJob(j.id, j.title, () => {
            S.analysis = null;
            if (S.sel) loadProject(S.sel);
            if (S.tab === 'plugins') loadReqs();
          });
    })
    .catch(() => {});
  const n = S.notes[S.sel];
  if (n && S.proj && n.behind !== S.proj.behind && !document.querySelector('.modal-bg')) loadProject(S.sel);
  if (n && S.tab === 'talk' && n.unread) loadComments(false);
}
function renderWho() {
  const st = S.state;
  if (!st) return;
  const c = st.config,
    w = $('#who');
  if (!st.registered) {
    w.innerHTML = 'не зарегистрирован';
    w.onclick = () => openRegister(true);
    return;
  }
  const ok = !S.serverError;
  w.innerHTML = `<span class="dot ${ok ? 'ok' : 'other'}"></span> ${esc(c.userName)}`;
  w.title = ok ? 'Сервер на связи' : 'Сервер недоступен, проверь интернет';
  w.onclick = () => openSettings();
}
function renderInvites() {
  const el = $('#invites');
  if (!el) return;
  el.innerHTML =
    (S.invites || [])
      .map(
        i =>
          `<div class="invite"><b>${esc(i.from.nick)}</b> приглашает в проект «${esc(i.name)}»<div class="row" style="margin-top:6px;gap:6px"><button class="sm primary" onclick="acceptInvite('${i.id}')">Принять и скачать</button><button class="sm ghost" onclick="declineInvite('${i.id}')">Отказаться</button></div></div>`
      )
      .join('') +
    (S.serverError && S.state?.registered
      ? `<div class="badbox small" style="margin:8px">Сервер недоступен, проверь интернет</div>`
      : '');
}
async function declineInvite(id) {
  const inv = (S.invites || []).find(x => x.id === id);
  if (!inv) return;
  if (
    !(await uiConfirm(`Отказаться от приглашения в «${esc(inv.name)}» от ${esc(inv.from.nick)}?`, { ok: 'Отказаться' }))
  )
    return;
  try {
    await api('/api/invitations/decline', { id });
    pollNotes();
  } catch (e) {
    fail(e);
  }
}
