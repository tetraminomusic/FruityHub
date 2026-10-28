// Обновления приложения: полоска «Доступно обновление».

function renderUpdateBar(v) {
  let bar = $('#upbar');
  if (!v || S.upHidden === v) {
    if (bar) bar.remove();
    return;
  }
  if (!bar) {
    bar = document.createElement('div');
    bar.id = 'upbar';
    bar.className = 'upbar';
    document.querySelector('header').after(bar);
  }
  bar.innerHTML = `<b>Доступно обновление FruityHub ${esc(v)}</b><span class="muted small">у тебя ${esc(S.state?.version || '')}</span><span class="spacer"></span>
    ${S.updating ? '<span class="small"><span class="spin" style="display:inline-block;vertical-align:-2px;margin-right:6px"></span>Обновляется — FruityHub перезапустится сам</span>' : `<button class="sm primary" onclick="applyUpdate()">Обновить и перезапустить</button><button class="sm ghost" onclick="S.upHidden='${esc(v)}';renderUpdateBar()">Позже</button>`}`;
  bar.dataset.v = v;
}
async function applyUpdate() {
  if (S.updating) return toast('Обновление уже идёт — FruityHub перезапустится сам');
  if (!(await confirmNoTransfers('обновить FruityHub сейчас (он перезапустится)'))) return;
  if (S.updating) return;
  S.updating = '…';
  const bar = $('#upbar');
  if (bar) renderUpdateBar(bar.dataset.v);
  let job;
  try {
    job = await api('/api/update/apply', {});
  } catch (e) {
    S.updating = '';
    return fail(e);
  }
  S.updating = job.id;
  if (S.followed[job.id]) return; // эту задачу уже показываем (автообновление)
  const j = await followJob(job.id, 'Обновляю FruityHub');
  if (j && !j.error) {
    document.body.innerHTML =
      '<div class="empty"><div class="box"><div style="font-size:18px">Перезапускаю FruityHub…</div><div class="muted" style="margin-top:6px">Новое окно откроется само через несколько секунд.</div></div></div>';
  }
}
async function checkUpdate() {
  try {
    const r = await api('/api/update/check', {});
    if (r.newer) {
      S.upHidden = null;
      renderUpdateBar(r.latest);
      toast('Есть новая версия: ' + esc(r.latest));
    } else
      uiAlert(
        r.latest ? `У тебя последняя версия (${esc(r.current)}).` : 'На сервере пока нет выложенных версий.',
        'Обновления'
      );
  } catch (e) {
    fail(e);
  }
}
