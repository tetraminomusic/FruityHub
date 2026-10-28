// Получить / Взять в работу / Отправить: окно отправки и возврат пропавших сэмплов.

async function receive() {
  const path = S.sel;
  await runJob('/api/project/receive', { path }, { title: 'Получение изменений' });
  if (S.sel === path) {
    S.history = null;
    if (S.tab === 'history') loadHistory();
  }
}
async function takeLock(force) {
  if (
    force &&
    !(await uiConfirm(
      `Проект сейчас у <b>${esc(S.proj.lock.user)}</b>. Забрать принудительно? Его работа не пропадёт, но вы можете получить конфликт.`,
      { ok: 'Забрать', danger: true }
    ))
  )
    return;
  await runJob('/api/project/lock', { path: S.sel, force: !!force }, { title: 'Беру проект в работу' });
}
async function unlock() {
  await runJob('/api/project/unlock', { path: S.sel }, { title: 'Освобождаю проект' });
}
async function sendDialog() {
  // интерфейс подвис, а по «Отправить» нажали много раз — окно одно
  if (S.sendOpen) {
    const ex = document.querySelector('.modal-bg[data-send]');
    if (ex) ex.querySelector('#msg')?.focus();
    return;
  }
  S.sendOpen = true;
  renderMain();
  try {
    await sendDialogNow();
  } catch (e) {
    S.sendOpen = false;
    renderMain();
    fail(e);
  }
}
async function sendDialogNow() {
  const p = S.proj,
    mine = p.lock && p.lock.user === p.me;
  const a = S.analysis?.analysis;
  const flps = (p.changes || []).filter(c => c.isFlp);
  // перед отправкой: не пропали ли сэмплы (FL могла удалить сэмплы синтов как «неиспользуемые»)
  let lost = [];
  try {
    lost = (await api('/api/project/precheck?path=' + enc(S.sel))).lost || [];
  } catch (e) {}
  const back = lost.filter(f => f.from),
    synth = lost.filter(f => f.plugin).length;
  const missing = (a?.samples || []).filter(
    s => s.status === 'missing' && !lost.some(l => l.name.toLowerCase() === s.name.toLowerCase())
  );
  const m = modal(
    `<h2>Отправить изменения</h2>
    <div class="muted small">Перед отправкой: внешние сэмплы скопируются в Audio/, обновится PLUGINS.txt. <b>Сохрани проект в FL (Ctrl/⌘+S)</b>.</div>
    ${p.lock && !mine ? `<div class="badbox small">Проект сейчас у ${esc(p.lock.user)}. Отправка может создать конфликт.</div>` : ''}
    ${
      lost.length
        ? `<div class="badbox small"><b>Пропали файлы, которые нужны проекту: ${lost.length}</b>${synth ? ` (из них сэмплы внутри плагинов: ${synth})` : ''}. Если отправить так — у друга их тоже не будет.
      <div style="margin-top:4px">${lost
        .slice(0, 6)
        .map(f => esc(f.name))
        .join(', ')}${lost.length > 6 ? '…' : ''}</div>
      ${back.length ? `<div class="row" style="margin-top:8px"><button class="sm primary" id="lback">Вернуть (${back.length}) и отправить</button><button class="sm ghost" id="lsee">Подробнее…</button></div>` : '<div class="muted" style="margin-top:4px">Вернуть их неоткуда — ни в прошлых версиях проекта, ни в Корзине их нет.</div>'}</div>`
        : ''
    }
    ${missing.length ? `<div class="warnbox small">Не найдено сэмплов: ${missing.length} — у друга их тоже не будет.</div>` : ''}
    <label>Что сделал</label><textarea id="msg" placeholder="Напр.: «добавил бас и переделал дроп»"></textarea>
    <label class="switch"><input type="checkbox" id="rel" ${mine ? 'checked' : ''}> Освободить проект (друг сможет взять его в работу)</label>
    <div class="muted small">${flps.length ? 'Проекты: ' + flps.map(f => esc(f.path)).join(', ') : ''}</div>
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="cancel">Отмена</button><button class="${lost.length ? '' : 'primary'}" id="go">⤒ Отправить${lost.length ? ' так' : ''}</button></div>`,
    () => {
      S.sendOpen = false;
      renderMain();
    }
  );
  m.dataset.send = '1';
  m.querySelector('#msg').focus();
  m.querySelector('#cancel').onclick = () => m.close();
  const send = async () => {
    const msg = m.querySelector('#msg').value,
      rel = m.querySelector('#rel').checked;
    m.close();
    await runJob('/api/project/send', { path: S.sel, message: msg, release: rel }, { title: 'Отправка' });
  };
  m.querySelector('#go').onclick = send;
  const lb = m.querySelector('#lback');
  if (lb)
    lb.onclick = async () => {
      lb.disabled = true;
      lb.textContent = 'Возвращаю…';
      if (await restoreLost(back)) send();
      else lb.disabled = false;
    };
  const ls = m.querySelector('#lsee');
  if (ls) ls.onclick = () => lostDialog(lost);
}
// вернуть пропавшие файлы: из прошлой версии проекта или из Корзины
async function restoreLost(files) {
  try {
    const r = await api('/api/project/restore-lost', { path: S.sel, files });
    toast(`Возвращено файлов: ${r.n}`, 5000);
    S.analysis = null;
    loadProject(S.sel);
    return true;
  } catch (e) {
    fail(e);
    return false;
  }
}
async function lostDialog(list) {
  if (!list) {
    try {
      list = (await api('/api/project/precheck?path=' + enc(S.sel))).lost || [];
    } catch (e) {
      return fail(e);
    }
  }
  const FROM = { git: 'из прошлой версии проекта', trash: 'из Корзины' };
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2 style="margin:0">Пропавшие файлы</h2><button class="ghost sm" id="x">✕</button></div>
    <div class="muted small">Файлы, которые нужны проекту, а на диске их нет. Чаще всего это сэмплы, загруженные прямо в синт: FL Studio не знает, что синт ими пользуется, и при «удалить неиспользуемое» удаляет их.</div>
    ${
      list.length
        ? `<div style="max-height:46vh;overflow:auto;margin-top:8px"><table>${list
            .map(
              (
                f,
                i
              ) => `<tr><td style="width:24px">${f.from ? `<input type="checkbox" style="width:auto" data-i="${i}" checked>` : ''}</td><td><b>${esc(f.name)}</b>${f.plugin ? ' <span class="tag">в плагине</span>' : ''}<div class="muted small mono">${esc(f.rel)}</div></td>
      <td class="small">${f.kind === 'deleted' ? 'пропал из папки' : 'проект ссылается, файла нет'}</td><td class="small">${f.from ? `<span style="color:var(--ok)">${FROM[f.from]}</span>` : '<span class="muted">вернуть неоткуда</span>'}</td></tr>`
            )
            .join('')}</table></div>
      <div class="row" style="justify-content:flex-end;margin-top:12px"><button class="primary" id="ok">Вернуть выбранные</button></div>`
        : '<div class="okbox" style="margin-top:10px">Всё на месте.</div>'
    }`);
  m.querySelector('.modal').style.width = 'min(820px,94vw)';
  m.querySelector('#x').onclick = () => m.close();
  const ok = m.querySelector('#ok');
  if (ok)
    ok.onclick = async () => {
      const files = [...m.querySelectorAll('input[data-i]')].filter(b => b.checked).map(b => list[+b.dataset.i]);
      if (!files.length) return;
      m.close();
      restoreLost(files);
    };
}
