// Общее: состояние S, мелкие помощники (esc, fmtSize…), запросы к приложению (api), уведомления (toast) и свои окна (modal, uiAlert, uiConfirm, uiPrompt).

const TOKEN_URL = 'https://github.com/settings/tokens/new?scopes=repo,delete_repo&description=FruityHub';
const TOKEN = window.FH_TOKEN; // ключ доступа к приложению — страница получает его от приложения
const STAGES = ['Идея', 'Набросок', 'Аранжировка', 'Запись', 'Сведение', 'Мастеринг', 'Готово'];
let S = {
  state: null,
  sel: null,
  proj: null,
  analysis: null,
  history: null,
  comments: null,
  preview: null,
  previewErr: null,
  previewProgress: null,
  tab: 'changes',
  showService: false,
  notes: {},
  invites: [],
  incoming: [],
  ready: [],
  busy: {},
  followed: {},
  shownReq: {},
  trashSel: {},
  ckind: 'note',
  cfilter: 'all',
};
const BG = new Image();
const $ = s => document.querySelector(s);
const esc = s =>
  String(s ?? '').replace(
    /[&<>"']/g,
    c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]
  );
const fmtSize = n =>
  n == null
    ? ''
    : n < 1024
      ? n + ' Б'
      : n < 1048576
        ? (n / 1024).toFixed(0) + ' КБ'
        : n < 1073741824
          ? (n / 1048576).toFixed(1) + ' МБ'
          : (n / 1073741824).toFixed(2) + ' ГБ';
const fmtTime = t =>
  new Date(t * 1000).toLocaleString('ru-RU', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' });
const enc = x => encodeURIComponent(x).replace(/'/g, '%27');
const color = name => {
  let h = 0;
  for (const c of name || '') h = (h * 31 + c.charCodeAt(0)) % 360;
  return `hsl(${h} 70% 62%)`;
};

// окно выбора папки/файла — только одно за раз: пока открыто, повторные нажатия ничего не открывают
const isPicker = (path, body) => /\/api\/pick/.test(path) || (body && body.action === 'pick');
async function api(path, body) {
  if (isPicker(path, body)) {
    if (S.picking) {
      toast('Окно выбора уже открыто — оно может быть за окном FruityHub');
      return {};
    }
    S.picking = true;
    document.body.classList.add('picking');
    try {
      return await apiRaw(path, body);
    } finally {
      S.picking = false;
      document.body.classList.remove('picking');
    }
  }
  return apiRaw(path, body);
}
async function apiRaw(path, body) {
  const opt = { method: body === undefined ? 'GET' : 'POST', headers: { 'X-FLSync': TOKEN } };
  if (body !== undefined) {
    opt.body = JSON.stringify(body);
    opt.headers['Content-Type'] = 'application/json';
  }
  const r = await fetch(path, opt);
  const j = await r.json().catch(() => ({ error: 'bad response' }));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}
// сколько держать уведомления (Настройки → Вид): 0 — пока не закрою
function noteSec() {
  const v = S.state?.config?.toastSec;
  return v == null || v === '' ? 4 : +v;
}
function toast(msg, ms = 3500) {
  const d = document.createElement('div');
  d.className = 'toast';
  d.innerHTML = msg;
  $('#toasts').prepend(d);
  const sec = noteSec(),
    t = sec === 0 ? Math.max(ms, 10000) : Math.max(ms, sec * 1000);
  setTimeout(() => {
    d.remove();
    stackFit();
  }, t);
  stackFit();
  return d;
}
// не больше 3 уведомлений на экране: новые сверху, остальные — прокруткой вниз
function stackFit() {
  let fitting = false;
  const scheduleFit = () => {
    if (fitting) return;
    fitting = true;
    requestAnimationFrame(() => { fitting = false; stackFit(); });
  };
  for (const id of ['tasks', 'toasts']) {
    const box = $('#' + id);
    if (!box) continue;
    const kids = [...box.children];
    let h = 0;
    kids.slice(0, 3).forEach((k, i) => (h += k.offsetHeight + (i ? (id === 'tasks' ? 8 : 6) : 0)));
    box.style.maxHeight = kids.length > 3 ? h + 2 + 'px' : 'calc(100vh - 32px)';
    let more = document.getElementById(id + 'More');
    if (kids.length > 3) {
      if (!more) {
        more = document.createElement('div');
        more.id = id + 'More';
        more.className = 'stackmore';
        document.body.appendChild(more);
      }
      more.textContent = 'ещё ' + (kids.length - 3) + ' ↓ прокрути';
      const r = box.getBoundingClientRect();
      if (id === 'toasts') {
        more.style.right = 'auto';
        more.style.left = r.left + r.width / 2 - 40 + 'px';
      }
      more.style.top = r.top - 20 + 'px';
    } else if (more) more.remove();
  }
}
// загрузка на весь экран (перетащили папку — видно, что она добавляется)
function busyOverlay(text, sub = '') {
  const d = document.createElement('div');
  d.className = 'busy';
  d.innerHTML = `<span class="spin"></span><div class="bt">${esc(text)}</div><div class="sub">${esc(sub)}</div>`;
  document.body.appendChild(d);
  return {
    set: (t, s2 = '') => {
      d.querySelector('.bt').textContent = t;
      d.querySelector('.sub').textContent = s2;
    },
    hide: () => (d.style.display = 'none'),
    show: () => (d.style.display = ''),
    close: () => d.remove(),
  };
}

function modal(html, onclose) {
  const bg = document.createElement('div');
  bg.className = 'modal-bg';
  bg.innerHTML = `<div class="modal">${html}</div>`;
  bg.close = () => {
    bg.remove();
    onclose && onclose();
  };
  bg.addEventListener('mousedown', e => {
    if (e.target === bg && !bg.busy && !bg.sticky) bg.close();
  });
  $('#modals').appendChild(bg);
  return bg;
}
function uiAlert(msg, title = 'Ошибка') {
  return new Promise(res => {
    const m = modal(
      `<h2>${esc(title)}</h2><div class="err">${esc(msg)}</div><div class="row" style="justify-content:flex-end;margin-top:14px"><button class="primary" id="ok">Понятно</button></div>`,
      res
    );
    m.querySelector('#ok').onclick = () => m.close();
    m.querySelector('#ok').focus();
  });
}
function uiConfirm(msg, { title = 'Точно?', ok = 'Да', cancel = 'Отмена', danger = false } = {}) {
  return new Promise(res => {
    let v = false;
    const m = modal(
      `<h2>${esc(title)}</h2><div>${msg}</div><div class="row" style="justify-content:flex-end;margin-top:14px"><button id="no">${esc(cancel)}</button><button class="${danger ? 'danger' : 'primary'}" id="yes">${esc(ok)}</button></div>`,
      () => res(v)
    );
    m.querySelector('#no').onclick = () => m.close();
    m.querySelector('#yes').onclick = () => {
      v = true;
      m.close();
    };
  });
}
function uiPrompt(title, { label = '', value = '', placeholder = '' } = {}) {
  return new Promise(res => {
    let v = null;
    const m = modal(
      `<h2>${esc(title)}</h2>${label ? `<label>${esc(label)}</label>` : ''}<input id="pv" value="${esc(value)}" placeholder="${esc(placeholder)}"><div class="row" style="justify-content:flex-end;margin-top:14px"><button id="no">Отмена</button><button class="primary" id="yes">OK</button></div>`,
      () => res(v)
    );
    const i = m.querySelector('#pv');
    i.focus();
    const go = () => {
      v = i.value;
      m.close();
    };
    m.querySelector('#yes').onclick = go;
    i.onkeydown = e => {
      if (e.key === 'Enter') go();
    };
    m.querySelector('#no').onclick = () => m.close();
  });
}
const fail = e => uiAlert(e.message || String(e));
function avatar(u, size = 30) {
  const n = u.nick || '?';
  return `<span class="ava" style="width:${size}px;height:${size}px;background:${color(n)}">${esc(n[0].toUpperCase())}${u.online ? '<i class="on" title="в сети"></i>' : ''}</span>`;
}
function projName(path) {
  return (S.state?.projects || []).find(p => p.path === path)?.name || path;
}
async function installTools() {
  await runJob('/api/tools/install', {}, { title: 'Установка git / git-lfs' });
}
async function quitApp(ask = true) {
  if (!(await confirmNoTransfers('закрыть FruityHub'))) return;
  if (!ask || (await uiConfirm('Закрыть FruityHub?', { ok: 'Закрыть' }))) {
    await api('/api/quit', {}).catch(() => {});
    document.body.innerHTML = '<div class="empty"><div>FruityHub закрыт. Вкладку можно закрыть.</div></div>';
  }
}
const ICON_REFRESH =
  '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" style="display:block"><path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/></svg>';
const REPO_URL = 'https://github.com/tetraminomusic/FruityHub';

// запуск: фон, высота стопок уведомлений, «я жив» для сервера, внешние ссылки — в браузер
function initCore() {
  BG.onerror = () => document.body.classList.add('nobg');
  BG.src = '/assets/bg?v=__V__';
  let fitting = false;
  const scheduleFit = () => {
    if (fitting) return;
    fitting = true;
    requestAnimationFrame(() => { fitting = false; stackFit(); });
  };
  for (const id of ['tasks', 'toasts'])
    new MutationObserver(scheduleFit).observe(document.getElementById(id), {
      childList: true,
      subtree: true,
      characterData: true,
    });
  window.addEventListener('resize', scheduleFit);
  setInterval(() => api('/api/ping', {}).catch(() => {}), 20000);
  api('/api/ping', {}).catch(() => {});

  // External links open in the system browser (inside the app window they would go nowhere)
  document.addEventListener('click', e => {
    const a = e.target.closest('a[href]');
    if (!a) return;
    const h = a.getAttribute('href');
    if (/^https?:\/\//.test(h)) {
      e.preventDefault();
      api('/api/openurl', { url: h }).catch(() => {});
    }
  });
}
