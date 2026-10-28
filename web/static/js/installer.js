// Установка FruityHub на Windows: exe, запущенный не из папки программы (рабочий стол, «Загрузки»),
// показывает окно установки — куда, ярлыки, убрать ли этот файл; шаги видны на экране. И удаление из «Приложений».

function installerNeeded() {
  const i = S.state?.install;
  return !!(i && (i.show || i.uninstall));
}

function openInstaller() {
  const i = S.state.install;
  S.installer = true;
  let el = $('#installer');
  if (!el) {
    el = document.createElement('div');
    el.id = 'installer';
    document.body.appendChild(el);
  }
  if (i.uninstall) renderUninstall(el, i);
  else renderInstall(el, i);
}

function closeInstaller() {
  S.installer = false;
  $('#installer')?.remove();
  refreshAll().catch(() => {}); // теперь — знакомство, регистрация, как обычно
}

const INSTALL_TEXT = {
  install: [
    'Установка FruityHub',
    'FruityHub встанет как обычная программа: ярлыки на рабочем столе и в меню «Пуск», удалить можно через «Приложения» Windows.',
    'Установить',
  ],
  move: [
    'FruityHub теперь устанавливается как программа',
    'Раньше FruityHub был просто файлом на рабочем столе. Перенесём его в папку программ — вход, проекты и настройки остаются как есть, а обновления больше не будут спрашивать разрешение Windows.',
    'Установить',
  ],
  update: [
    'Обновить установленный FruityHub',
    'FruityHub уже установлен на этом компьютере — этот файл заменит его. Вход, проекты и настройки остаются как есть.',
    'Обновить',
  ],
};

// сравнение версий вида 0.9.13: <0 — a старее b
function verCmp(a, b) {
  const x = String(a).split('.').map(Number), y = String(b).split('.').map(Number);
  for (let k = 0; k < Math.max(x.length, y.length); k++) {
    const d = (x[k] || 0) - (y[k] || 0);
    if (d) return d;
  }
  return 0;
}

function renderInstall(el, i) {
  const [title, sub, btn] = INSTALL_TEXT[i.mode] || INSTALL_TEXT.install;
  const known = i.mode === 'update' && i.installedVersion && i.installedVersion !== '?';
  const cmp = known ? verCmp(i.version, i.installedVersion) : 0;
  el.innerHTML = `<div class="inst-card">
    <img src="/assets/icon?v=__V__" alt="" class="inst-icon">
    <h1>${esc(title)}</h1>
    <div class="muted">${esc(sub)}</div>
    ${known ? `<div class="${cmp < 0 ? 'warnbox' : 'muted'} small" style="margin-top:10px">Установлена версия <b>${esc(i.installedVersion)}</b>, этот файл — версия <b>${esc(i.version)}</b>.${cmp < 0 ? ' Этот файл старее — лучше запустить установленный FruityHub с ярлыка.' : ''}</div>` : ''}
    <div class="inst-form" id="instForm">
      <div class="inst-path"><span class="muted small">Папка программы</span><span class="mono small">${esc(i.dir)}</span></div>
      <label class="switch"><input type="checkbox" id="iDesk" checked> Ярлык на рабочем столе</label>
      <label class="switch"><input type="checkbox" id="iStart" checked> Ярлык в меню «Пуск»</label>
      <label class="switch"><input type="checkbox" id="iOld" checked> Убрать этот файл после установки</label>
      <div class="inst-note mono">${esc(i.exe)}</div>
      ${i.oldProtected ? '<div class="inst-note">Он на общем рабочем столе — Windows один раз спросит разрешение.</div>' : ''}
      <div class="muted small" style="margin-top:8px">Настройки, вход и кеш хранятся отдельно, в папке пользователя (AppData), — переустановка их не трогает. Папки проектов не трогаются.</div>
    </div>
    <div class="inst-steps" id="instSteps" style="display:none"></div>
    <div class="row inst-buttons" id="instBtns">
      <button class="ghost" id="iSkip">${i.mode === 'update' ? 'Запустить без установки' : 'Запускать без установки'}</button>
      <span class="spacer"></span>
      <button class="primary big" id="iGo">${esc(btn)}</button>
    </div>
  </div>`;
  el.querySelector('#iSkip').onclick = async () => {
    if (i.mode !== 'update') {
      if (!(await uiConfirm('FruityHub будет запускаться прямо из этого файла, без ярлыков и без пункта в «Приложениях». Установить можно будет потом — в Настройки → Система.', { title: 'Запускать без установки?', ok: 'Да, без установки' }))) return;
      await api('/api/install/skip', {}).catch(() => {});
    }
    closeInstaller();
  };
  el.querySelector('#iGo').onclick = () => runInstall(el, i);
}

async function runInstall(el, i) {
  const desktop = el.querySelector('#iDesk').checked, startMenu = el.querySelector('#iStart').checked;
  const removeOld = el.querySelector('#iOld').checked;
  el.querySelectorAll('#instForm input').forEach(x => (x.disabled = true));
  const btns = el.querySelector('#instBtns'), steps = el.querySelector('#instSteps');
  btns.innerHTML = '';
  steps.style.display = '';
  const paint = (lines, done, err) => {
    steps.innerHTML =
      lines.map((l, k) => `<div class="inst-step ${done || k < lines.length - 1 ? 'ok' : ''}">${done || k < lines.length - 1 ? '<span class="tick">✓</span>' : '<span class="spin"></span>'}<span>${esc(l.replace(/^✓\s*/, ''))}</span></div>`).join('') +
      (err ? `<div class="badbox small" style="margin-top:8px;text-align:left">${esc(err)}</div>` : '');
  };
  paint(['Начинаю установку'], false);
  let job;
  try {
    job = await api('/api/install', { desktop, startMenu });
  } catch (e) {
    paint(['Начинаю установку'], true, e.message);
    return installRetry(el, i, btns);
  }
  for (;;) {
    let j;
    try {
      j = await api('/api/job?id=' + job.id);
    } catch (e) {
      await new Promise(r => setTimeout(r, 500));
      continue;
    }
    const lines = (j.steps || []).filter(Boolean);
    if (!j.done) {
      paint(lines.length ? lines : ['Начинаю установку'], false);
      await new Promise(r => setTimeout(r, 300));
      continue;
    }
    if (j.error) {
      paint(lines, true, j.error);
      return installRetry(el, i, btns);
    }
    paint(lines, true);
    break;
  }
  el.querySelector('h1').textContent = i.mode === 'update' ? 'FruityHub обновлён' : 'FruityHub установлен';
  if (removeOld) steps.insertAdjacentHTML('beforeend', '<div class="muted small" style="margin-top:8px">Этот файл уберётся, когда FruityHub запустится из папки программы.</div>');
  btns.innerHTML = `<span class="spacer"></span><button class="primary big" id="iRun">Запустить FruityHub</button>`;
  btns.querySelector('#iRun').onclick = async () => {
    btns.querySelector('#iRun').disabled = true;
    try {
      await api('/api/install/launch', { removeOld });
      btns.innerHTML = '<span class="spacer"></span><div class="row small muted"><span class="spin"></span> Запускаю установленный FruityHub…</div>';
    } catch (e) {
      fail(e);
      btns.querySelector('#iRun').disabled = false;
    }
  };
}

function installRetry(el, i, btns) {
  btns.innerHTML = '<button class="ghost" id="iSkip2">Запускать без установки</button><span class="spacer"></span><button class="primary" id="iAgain">Попробовать ещё раз</button>';
  btns.querySelector('#iAgain').onclick = () => renderInstall(el, i);
  btns.querySelector('#iSkip2').onclick = () => closeInstaller();
}

function renderUninstall(el, i) {
  el.innerHTML = `<div class="inst-card">
    <img src="/assets/icon?v=__V__" alt="" class="inst-icon">
    <h1>Удалить FruityHub?</h1>
    <div class="muted">Уберутся программа, ярлыки и пункт в «Приложениях». Папки проектов, их файлы и репозитории на GitHub не трогаются.</div>
    <div class="inst-form">
      <label class="switch"><input type="checkbox" id="uCfg"> Удалить и настройки FruityHub: вход на сервер, список проектов, кеш</label>
      <div class="inst-note">Без этой галочки после повторной установки всё будет как было.</div>
    </div>
    <div class="row inst-buttons" id="uBtns">
      <button class="ghost" id="uNo">Отмена</button><span class="spacer"></span><button class="danger big" id="uGo">Удалить</button>
    </div>
  </div>`;
  el.querySelector('#uNo').onclick = () => api('/api/quit', {}).catch(() => window.close());
  el.querySelector('#uGo').onclick = async () => {
    const settings = el.querySelector('#uCfg').checked;
    const b = el.querySelector('#uBtns');
    b.innerHTML = '<div class="row small muted"><span class="spin"></span> Удаляю…</div>';
    try {
      await api('/api/uninstall', { settings });
      el.querySelector('h1').textContent = 'FruityHub удалён';
      b.innerHTML = '<div class="muted small">Окно закроется само.</div>';
    } catch (e) {
      b.innerHTML = `<div class="badbox small" style="text-align:left">${esc(e.message)}</div>`;
    }
  };
}
