// Участники проекта, приглашения, репозитории на GitHub (создание, удаление).

async function participantsDialog() {
  const path = S.sel;
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2>Участники проекта</h2><button class="ghost sm" id="x">✕</button></div>
    <div id="plistp" class="muted small">Загружаю…</div>
    <label>Позвать в проект</label>
    <div id="ppick"></div>
    <div class="row" style="margin-top:6px"><button class="primary" id="pinv">Пригласить выбранных</button></div>
    <div style="margin-top:8px"><button id="pall">Позвать всех пользователей FruityHub</button></div>
    <div class="muted small" style="margin-top:8px">Приглашение придёт прямо в FruityHub, доступ к репозиторию на GitHub выдастся сам. Звать и убирать участников может владелец проекта.</div>`);
  m.querySelector('#x').onclick = () => m.close();
  const load = async () => {
    let r;
    try {
      r = await api('/api/project/participants?path=' + enc(path));
    } catch (e) {
      m.querySelector('#plistp').innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
      return;
    }
    const p = r.project,
      me = r.me;
    userPicker(m.querySelector('#ppick'), {
      exclude: [...(p.members || []).map(u => u.id), ...(p.invited || []).map(i => i.to.id)],
    });
    const rows = [
      ...(p.members || []).map(u => ({ u, pending: false })),
      ...(p.invited || []).map(i => ({ u: i.to, pending: true })),
    ];
    m.querySelector('#plistp').innerHTML = rows
      .map(
        ({ u, pending }) => `<div class="person" style="color:var(--text)">${avatar(u)}
      <div style="flex:1"><b>${esc(u.nick)}</b>${u.id === me ? ' <span class="muted small">(ты)</span>' : ''} <span class="muted small">GitHub @${esc(u.github || '?')} · ${esc(u.os || '')}</span><div>${u.id === p.owner.id ? '<span class="tag">владелец</span> ' : ''}${pending ? '<span class="tag warn">приглашён, ещё не принял</span>' : ''}</div></div>
      ${p.isOwner && u.id !== me ? `<button class="sm ghost" data-u="${u.id}" data-n="${esc(u.nick)}" data-p="${pending ? 1 : 0}">${pending ? 'отменить приглашение' : 'убрать'}</button>` : ''}
      ${!p.isOwner && u.id === me ? `<button class="sm ghost" data-u="${u.id}" data-n="${esc(u.nick)}" data-p="0">выйти из проекта</button>` : ''}</div>`
      )
      .join('');
    m.querySelectorAll('button[data-u]').forEach(
      b =>
        (b.onclick = async () => {
          const pend = b.dataset.p === '1',
            self = b.dataset.u === me;
          if (
            !(await uiConfirm(
              self
                ? 'Выйти из проекта? Файлы на компьютере останутся, но получать и отправлять изменения ты больше не сможешь.'
                : `${pend ? 'Отменить приглашение для' : 'Убрать из проекта'} <b>${esc(b.dataset.n)}</b>?${pend ? '' : ' Файлы у него останутся, но получать и отправлять изменения он больше не сможет.'}`,
              { ok: 'Да', danger: true }
            ))
          )
            return;
          try {
            await api('/api/project/remove-participant', { path, userId: b.dataset.u, name: b.dataset.n });
            load();
          } catch (e) {
            fail(e);
          }
        })
    );
  };
  m.querySelector('#pall').onclick = async () => {
    try {
      const r = await api('/api/project/invite-all', { path });
      const n = (r.invited || []).length;
      toast(n ? `Приглашения отправлены: ${esc(r.invited.join(', '))}` : 'Все уже участники или приглашены');
      if ((r.failed || []).length) uiAlert('Не получилось позвать:<br>' + r.failed.map(esc).join('<br>'));
      load();
    } catch (e) {
      fail(e);
    }
  };
  m.querySelector('#pinv').onclick = async () => {
    const nicks = pickedNicks(m.querySelector('#ppick'));
    if (!nicks.length) return toast('Отметь, кого позвать');
    await inviteMany(path, nicks);
    load();
  };
  load();
}
// выбор людей из всех пользователей этого сервера — вместо ввода ника руками
async function userPicker(el, { exclude = [] } = {}) {
  if (!el) return;
  el.innerHTML = '<div class="muted small">Загружаю пользователей сервера…</div>';
  let us = [];
  try {
    us = await getUsers(true);
  } catch (e) {
    el.innerHTML = `<div class="badbox small">${esc(e.message)}</div>`;
    return;
  }
  const list = us
    .filter(u => u.id !== S.state.userId && !exclude.includes(u.id))
    .sort((a, b) => a.nick.localeCompare(b.nick, 'ru', { sensitivity: 'base' }));
  if (!list.length) {
    el.innerHTML = `<div class="muted small">${exclude.length ? 'Все пользователи сервера уже в проекте или приглашены.' : 'Кроме тебя на сервере пока никого нет — другу нужно установить FruityHub и зарегистрироваться.'}</div>`;
    return;
  }
  el.innerHTML = `<div class="upick">${list.map(u => `<label class="urow"><input type="checkbox" value="${esc(u.nick)}">${avatar(u, 24)}<b>${esc(u.nick)}</b><span class="muted small">@${esc(u.github || '?')} · ${esc(u.os || '')}${u.online ? ' · в сети' : ''}</span></label>`).join('')}</div>
    ${list.length > 1 ? '<a class="small" data-all>выбрать всех</a>' : ''}`;
  const all = el.querySelector('[data-all]');
  if (all)
    all.onclick = () => {
      const bs = [...el.querySelectorAll('input[type=checkbox]')];
      const on = bs.some(b => !b.checked);
      bs.forEach(b => (b.checked = on));
      all.textContent = on ? 'снять всех' : 'выбрать всех';
    };
}
function pickedNicks(el) {
  return el ? [...el.querySelectorAll('input[type=checkbox]:checked')].map(i => i.value) : [];
}
async function inviteMany(path, nicks) {
  const ok = [],
    bad = [];
  for (const nick of nicks) {
    try {
      await api('/api/project/invite', { path, nick });
      ok.push(nick);
    } catch (e) {
      bad.push(nick + ': ' + e.message);
    }
  }
  if (ok.length) toast('Приглашения отправлены: ' + esc(ok.join(', ')));
  if (bad.length) uiAlert('Не получилось позвать:<br>' + bad.map(esc).join('<br>'));
}
// создать репозиторий на GitHub для папки (в том числе если прошлая попытка не дошла до конца)
// удалить репозиторий проекта с GitHub (и с сервера); подтверждение — вводом названия
function deleteRepoDialog() {
  const p = S.proj;
  if (!p?.remote) return;
  const repo = p.remote.replace(/^.*github\.com[:/]+/, '').replace(/\.git$/, '');
  const m = modal(`<h2>Удалить репозиторий с GitHub</h2>
    <div>Репозиторий <b class="mono">${esc(repo)}</b> будет удалён с GitHub <b>навсегда</b> вместе со всей историей версий, а проект — с сервера FruityHub (у участников он пропадёт из «Доступных»). Файлы проекта на компьютерах останутся.</div>
    <label class="switch"><input type="checkbox" id="dru" checked> Убрать проект из списка FruityHub</label>
    <label class="switch"><input type="checkbox" id="drt"> Удалить папку проекта с этого компьютера (в Корзину — оттуда можно вернуть)</label>
    <label>Чтобы подтвердить, впиши название проекта: <b>${esc(p.name)}</b></label><input id="drn" autocomplete="off">
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="no">Отмена</button><button class="danger" id="ok" disabled>Удалить навсегда</button></div>`);
  const inp = m.querySelector('#drn'),
    ok = m.querySelector('#ok');
  inp.focus();
  inp.oninput = () => {
    ok.disabled = inp.value.trim() !== p.name.trim();
  };
  m.querySelector('#no').onclick = () => m.close();
  ok.onclick = async () => {
    ok.disabled = true;
    ok.textContent = 'Удаляю…';
    const trash = m.querySelector('#drt').checked,
      unlist = m.querySelector('#dru').checked || trash;
    try {
      const r = await api('/api/github/delete-repo', { path: S.sel, trash, unlist });
      m.close();
      toast(
        'Репозиторий ' +
          esc(r.deleted) +
          ' удалён' +
          (r.trashError ? '. Папку в корзину не убрал: ' + esc(r.trashError) : ''),
        6000
      );
      if (unlist && !r.trashError) {
        S.sel = null;
        S.proj = null;
      }
      await refreshAll();
    } catch (e) {
      m.close();
      fail(e);
    }
  };
}
// все репозитории проектов на моём GitHub: где используются, удалить ненужные
async function openRepos() {
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2>Репозитории проектов на GitHub</h2><button class="ghost sm" id="x">✕</button></div>
    <div class="muted small">Репозитории на твоём GitHub, которые создал FruityHub. «Не используется» — его нет ни в одной папке на этом компьютере и ни в одном проекте на сервере: например, остался после оборванного создания.</div>
    <div id="rl" style="margin-top:8px" class="muted">Загружаю…</div>`);
  m.querySelector('.modal').style.width = 'min(900px,94vw)';
  m.querySelector('#x').onclick = () => m.close();
  const load = async () => {
    let rs;
    try {
      rs = await api('/api/github/repos');
    } catch (e) {
      m.querySelector('#rl').innerHTML = `<div class="badbox">${esc(e.message)}</div>`;
      return;
    }
    if (!rs.length) {
      m.querySelector('#rl').innerHTML = '<div class="muted">Репозиториев проектов нет.</div>';
      return;
    }
    m.querySelector('#rl').innerHTML =
      `<table><tr><th>Репозиторий</th><th>Где используется</th><th style="text-align:right">Размер</th><th>Изменён</th><th></th></tr>
      ${rs
        .map(
          r => `<tr><td class="mono small">${esc(r.fullName)}</td>
        <td class="small">${r.local ? `на этом компьютере: <span class="mono">${esc(r.local.split(/[\\/]/).pop())}</span><br>` : ''}${r.server ? `на сервере: ${esc(r.server)} (${r.members} уч.)` : ''}${!r.local && !r.server ? '<span style="color:var(--warn)">не используется</span>' : ''}</td>
        <td class="muted small" style="text-align:right">${fmtSize((r.sizeKb || 0) * 1024)}</td><td class="muted small">${r.pushedAt ? esc(new Date(r.pushedAt).toLocaleDateString('ru-RU')) : ''}</td>
        <td><button class="sm danger" data-r="${esc(r.fullName)}" data-u="${r.server || r.local ? 1 : 0}">Удалить</button></td></tr>`
        )
        .join('')}</table>`;
    m.querySelectorAll('[data-r]').forEach(
      b =>
        (b.onclick = async () => {
          const used = b.dataset.u === '1';
          if (
            !(await uiConfirm(
              `Удалить репозиторий <b class="mono">${esc(b.dataset.r)}</b> с GitHub навсегда, со всей историей?${used ? '<br><br><b>Он ещё используется</b> — проект уйдёт и с сервера FruityHub; файлы на компьютерах останутся.' : ''}`,
              { title: 'Удалить репозиторий', ok: 'Удалить навсегда', danger: true }
            ))
          )
            return;
          b.disabled = true;
          b.textContent = 'удаляю…';
          try {
            await api('/api/github/delete-repo', { repo: b.dataset.r });
            toast('Удалён: ' + esc(b.dataset.r));
            load();
            refreshAll();
          } catch (e) {
            fail(e);
            b.disabled = false;
            b.textContent = 'Удалить';
          }
        })
    );
  };
  load();
}
function createRepoDialog() {
  const p = S.proj;
  if (!p) return;
  if (!S.state.registered) return openRegister(true);
  const m = modal(`<h2>Создать репозиторий на GitHub</h2>
    <div class="muted small">Приватный репозиторий на твоём GitHub для этого проекта, запись на сервере FruityHub и первая отправка. Если прошлая попытка создать его оборвалась, FruityHub продолжит с уже созданным репозиторием, а не сделает второй.</div>
    <label>Название</label><input id="crn" value="${esc(p.name)}">
    <label>Кого позвать в проект</label><div id="crp"></div>
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="no">Отмена</button><button class="primary" id="ok">Создать и отправить</button></div>`);
  userPicker(m.querySelector('#crp'));
  m.querySelector('#no').onclick = () => m.close();
  m.querySelector('#ok').onclick = async () => {
    const name = m.querySelector('#crn').value,
      invite = pickedNicks(m.querySelector('#crp'));
    m.close();
    await runJob(
      '/api/project/server-create',
      { path: S.sel, name, invite },
      { title: 'Создаю репозиторий и отправляю' }
    );
    refreshAll();
  };
}
async function getUsers(force) {
  if (!S.users || force || Date.now() - S.usersAt > 30000) {
    const r = await api('/api/users');
    S.users = r.users || [];
    S.usersAt = Date.now();
  }
  return S.users;
}
function bindLookup(input, out, datalist) {
  getUsers()
    .then(us => {
      if (datalist)
        datalist.innerHTML = us
          .filter(u => u.id !== S.state.userId)
          .map(u => `<option value="${esc(u.nick)}">`)
          .join('');
    })
    .catch(() => {});
  let t;
  input.addEventListener('input', () => {
    clearTimeout(t);
    const v = input.value
      .split(/[,;]/)
      .map(x => x.trim())
      .filter(Boolean);
    if (!v.length) {
      out.innerHTML = '';
      return;
    }
    t = setTimeout(async () => {
      let us = [];
      try {
        us = await getUsers();
      } catch (e) {}
      out.innerHTML = v
        .map(n => {
          const u = us.find(x => x.nick.toLowerCase() === n.toLowerCase());
          return u
            ? `<span style="color:var(--ok)">✓ ${esc(u.nick)} <span class="muted">(${esc(u.os || '?')})</span></span>`
            : `<span style="color:var(--bad)">✗ «${esc(n)}» не зарегистрирован в FruityHub</span>`;
        })
        .join('<br>');
    }, 300);
  });
}
