// Задачи: карточки с прогрессом справа внизу (runJob, followJob), выбор версии и окно конфликта .flp.

function taskCard(title) {
  const el = document.createElement('div');
  el.className = 'task';
  el.innerHTML = `<div class="t"><span class="spin"></span><span class="tt">${esc(title)}</span></div><div class="s">Начинаю…</div><div class="bar ind"><i style="width:0"></i></div>`;
  $('#tasks').prepend(el);
  return el;
}
function paintTask(el, j) {
  const last = (j.steps || [])[(j.steps || []).length - 1] || '';
  // «Отменить» — у скачивания проекта
  let cb = el.querySelector('.tcancel');
  if (j.cancellable && !cb) {
    cb = document.createElement('button');
    cb.className = 'ghost sm tcancel';
    cb.textContent = 'Отменить';
    cb.title = 'Остановить скачивание — недокачанная папка удалится';
    cb.onclick = async () => {
      if (
        !(await uiConfirm(
          'Остановить скачивание? Недокачанная папка проекта удалится — скачать можно будет заново из «Доступных».',
          { title: 'Отменить скачивание', ok: 'Остановить', danger: true }
        ))
      )
        return;
      cb.disabled = true;
      cb.textContent = 'Отменяю…';
      try {
        await api('/api/job/cancel', { id: j.id });
      } catch (e) {
        fail(e);
      }
    };
    const t = el.querySelector('.t');
    const sp = document.createElement('span');
    sp.className = 'spacer';
    t.append(sp, cb);
  } else if (!j.cancellable && cb) {
    cb.previousElementSibling?.remove();
    cb.remove();
  }
  const p = j.progress;
  el.querySelector('.s').textContent = p ? p.label + (p.pct >= 0 ? ` — ${p.pct}%` : '') : last || 'Работаю…';
  const bar = el.querySelector('.bar');
  if (p && p.pct >= 0) {
    bar.classList.remove('ind');
    bar.querySelector('i').style.width = p.pct + '%';
  } else bar.classList.add('ind');
}
function finishTask(el, j) {
  const bar = el.querySelector('.bar');
  bar.remove();
  const tt = el.querySelector('.t');
  if (j.cancelled) {
    tt.innerHTML = `<span class="tt">${esc(j.title)} — отменено</span><span class="spacer"></span><button class="ghost sm" onclick="this.closest('.task').remove()">✕</button>`;
    el.querySelector('.s').textContent = 'Недокачанная папка удалена.';
    setTimeout(
      () => {
        el.style.transition = 'opacity .4s';
        el.style.opacity = 0;
        setTimeout(() => el.remove(), 400);
      },
      Math.max(3, noteSec() || 4) * 1000
    );
    return;
  }
  if (j.error) {
    el.classList.add('err');
    tt.innerHTML = `<span class="tt">${esc(j.title)} — не получилось</span><span class="spacer"></span><button class="ghost sm" onclick="this.closest('.task').remove()">✕</button>`;
    const fx = j.fix;
    el.querySelector('.s').innerHTML =
      `<div style="color:#ffc4c4;font-size:13px;margin-top:2px">${esc(j.error)}</div>` +
      (fx && fx.kind === 'exists'
        ? `<div class="row" style="gap:6px;margin:8px 0 4px">${fx.same ? '<button class="sm primary" data-fx="add">Открыть его</button>' : ''}<button class="sm ${fx.same ? '' : 'primary'}" data-fx="trash">Удалить папку и скачать заново</button><button class="sm ghost" data-fx="open">Показать папку</button></div>`
        : '') +
      (j.details
        ? `<a class="small muted" onclick="this.nextElementSibling.hidden=!this.nextElementSibling.hidden">Подробности для знающих</a><div class="details" hidden>${esc(j.details)}</div>`
        : '');
    el.querySelectorAll('[data-fx]').forEach(
      b =>
        (b.onclick = async () => {
          const k = b.dataset.fx;
          if (k === 'open') return api('/api/project/open', { path: fx.path }).catch(fail);
          if (k === 'add') {
            try {
              await api('/api/projects/add', { path: fx.path });
              el.remove();
              S.sel = fx.path;
              S.comments = null;
              sideTab('my');
              await refreshAll();
            } catch (e) {
              fail(e);
            }
            return;
          }
          if (
            !(await uiConfirm(
              `Папка <span class="mono">${esc(fx.path)}</span> уйдёт в Корзину целиком (оттуда можно вернуть)${fx.same ? '' : ' — проверь, что там нет ничего нужного'}, и проект скачается заново.`,
              { title: 'Удалить папку и скачать заново', ok: 'Удалить и скачать', danger: true }
            ))
          )
            return;
          try {
            await api('/api/clone/trash-existing', { path: fx.path });
            el.remove();
            el.retry && el.retry();
          } catch (e) {
            fail(e);
          }
        })
    );
  } else {
    el.classList.add('ok');
    tt.innerHTML = `<span style="color:var(--ok)">✓</span><span class="tt">${esc(j.title)}</span><span class="spacer"></span><button class="ghost sm" onclick="this.closest('.task').remove()">✕</button>`;
    const st = (j.steps || []).filter(x => !/^✓/.test(x)).slice(-3);
    el.querySelector('.s').innerHTML = st.length ? st.map(x => `<div>${esc(x)}</div>`).join('') : 'Готово';
    const sec = noteSec();
    if (sec > 0)
      setTimeout(
        () => {
          el.style.transition = 'opacity .4s';
          el.style.opacity = 0;
          setTimeout(() => el.remove(), 400);
        },
        st.some(x => x.startsWith('Внимание')) ? Math.max(9000, sec * 2000) : sec * 1000
      );
  }
}
async function followJob(id, title, onDone, retry) {
  S.followed[id] = 1;
  const el = taskCard(title);
  el.retry = retry;
  for (;;) {
    let j;
    try {
      j = await api('/api/job?id=' + id);
    } catch (e) {
      await new Promise(r => setTimeout(r, 1000));
      continue;
    }
    if (title !== j.title) el.querySelector('.tt').textContent = j.title;
    if (!j.done) {
      paintTask(el, j);
      await new Promise(r => setTimeout(r, 400));
      continue;
    }
    finishTask(el, j);
    const r = j.result || {};
    if (r.already) toast('Этот проект уже скачивается — смотри соседнюю задачу');
    if (!j.error && r.conflicts && r.conflicts.length) showConflicts(r.conflicts);
    onDone && onDone(j);
    refreshAll();
    return j;
  }
}
async function runJob(path, body, { title, onDone } = {}) {
  let job;
  try {
    job = await api(path, body);
  } catch (e) {
    fail(e);
    return null;
  }
  const j = await followJob(job.id, title || job.title, onDone, () => runJob(path, body, { title, onDone }));
  // проект здесь и на GitHub начаты отдельно — спрашиваем, какую версию оставить, и повторяем
  if (j && !j.error && j.result && j.result.needChoice === 'unrelated' && body && !body.resolve) {
    const how = await chooseVersion();
    if (!how) return j;
    return runJob(path, { ...body, resolve: how }, { title, onDone });
  }
  return j;
}
function chooseVersion() {
  return new Promise(res => {
    const m = modal(`<h2>Какую версию оставить?</h2>
      <div class="muted small">Проект на этом компьютере и на GitHub начаты отдельно (например, папку скопировали и подключили заново) — у них разные истории, и git не может их просто сложить.</div>
      <label class="radio on"><input type="radio" name="vv" value="theirs" checked><span><b>Только версию с GitHub</b><div class="muted small">Проект станет как на GitHub. Твои отличающиеся файлы не пропадут — сохранятся в папку <b>Backup</b> проекта.</div></span></label>
      <label class="radio"><input type="radio" name="vv" value="mine"><span><b>Только мою версию</b><div class="muted small">Твоя версия останется и при отправке заменит ту, что на GitHub (история на GitHub сохранится — старое можно будет вернуть из «Истории»).</div></span></label>
      <label class="radio"><input type="radio" name="vv" value="both"><span><b>Обе</b><div class="muted small">Одинаковые файлы сольются; где .flp разные — с GitHub под исходным именем, твой рядом копией. Потом выберешь, какой оставить.</div></span></label>
      <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="no">Отмена</button><button class="primary" id="yes">Продолжить</button></div>`);
    m.querySelectorAll('input[name=vv]').forEach(
      i =>
        (i.onchange = () =>
          m.querySelectorAll('.radio').forEach(l => l.classList.toggle('on', l.querySelector('input').checked)))
    );
    m.querySelector('#no').onclick = () => {
      m.close();
      res(null);
    };
    m.querySelector('#yes').onclick = () => {
      const v = m.querySelector('input[name=vv]:checked').value;
      m.close();
      res(v);
    };
  });
}
// после конфликта: по каждому файлу — оставить версию с GitHub, свою или обе
function showConflicts(list) {
  const path = S.sel;
  const m = modal(`<h2>Вы оба поменяли одно и то же</h2>
    <div class="muted small">Сейчас под исходным именем лежит версия с GitHub, твоя — копией «(конфликт — …)» в папке проекта <span class="mono">.flsync/conflicts</span>. Выбери по каждому файлу, что оставить, или оставь обе и сравни в FL Studio.</div>
    <table style="margin-top:8px">${list
      .map(
        (
          c,
          i
        ) => `<tr data-i="${i}"><td class="small"><b>${esc(c.replace(/^\.flsync\/conflicts\//, '').replace(/ \(конфликт — [^)]*\)(\.[^.]*)$/, '$1'))}</b><div class="muted">${esc(c)}</div></td>
      <td style="white-space:nowrap"><button class="sm ghost" data-open="1" title="Открыть твою версию в FL Studio, чтобы сравнить">Открыть мою</button> <button class="sm" data-k="theirs">С GitHub</button> <button class="sm" data-k="mine">Мою</button> <button class="sm ghost" data-k="both">Обе</button></td></tr>`
      )
      .join('')}</table>
    <div class="muted small" style="margin-top:6px">Выбор «Мою» уйдёт на GitHub при следующей отправке.</div>
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button class="primary" id="ok">Готово</button></div>`);
  m.querySelector('#ok').onclick = () => m.close();
  m.querySelectorAll('tr[data-i] [data-open]').forEach(
    b => (b.onclick = () => api('/api/project/open-file', { path, rel: list[+b.closest('tr').dataset.i] }).catch(fail))
  );
  m.querySelectorAll('tr[data-i] button[data-k]').forEach(
    b =>
      (b.onclick = async () => {
        const tr = b.closest('tr'),
          c = list[+tr.dataset.i],
          k = b.dataset.k;
        if (k !== 'both') {
          try {
            await api('/api/project/conflict-keep', { path, copy: c, keep: k });
          } catch (e) {
            return fail(e);
          }
        }
        tr.querySelector('td:last-child').innerHTML =
          `<span class="tag ok">${k === 'theirs' ? 'оставлена с GitHub' : k === 'mine' ? 'оставлена твоя' : 'оставлены обе'}</span>`;
        if (S.sel) loadProject(S.sel);
      })
  );
}
// job without a modal (progress shown inline)
async function silentJob(path, body, onTick) {
  const job = await api(path, body);
  for (;;) {
    const j = await api('/api/job?id=' + job.id);
    onTick && onTick(j);
    if (j.done) {
      if (j.error) throw new Error(j.error);
      return j;
    }
    await new Promise(r => setTimeout(r, 400));
  }
}
