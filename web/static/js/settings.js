// Окно «Настройки» и регистрация.

const SHARE = {
  off: ['Никому', 'С твоего компьютера плагины не скачать.'],
  ask: ['Спрашивать каждый раз', 'У тебя всплывает запрос — разрешить или отказать.'],
  members: ['Участникам моих проектов — без вопросов', 'Тем, с кем у тебя общий проект, плагины уходят автоматически.'],
  all: [
    'Всем пользователям FruityHub — без вопросов',
    'Любой зарегистрированный может скачать плагин, ты ничего не нажимаешь.',
  ],
};
const SET_TABS = [
  ['profile', 'Профиль'],
  ['projects', 'Проекты'],
  ['github', 'GitHub'],
  ['net', 'Сеть'],
  ['meta', 'Метаданные'],
  ['look', 'Вид'],
  ['system', 'Система'],
  ['updates', 'Обновления'],
];
// как получить токен — с прямыми ссылками; пароль и SSH-ключ не нужны
function githubHelpHtml() {
  return `<details style="margin-top:10px"><summary class="small" style="cursor:pointer">Нет аккаунта на GitHub или не знаешь, где взять токен?</summary>
    <ol class="small" style="margin:6px 0 0 18px;line-height:1.6">
      <li>Зарегистрируйся на GitHub (бесплатно): <a href="https://github.com/signup">github.com/signup</a>. Подтверди почту.</li>
      <li>Открой <a href="${TOKEN_URL}">страницу создания токена</a> — нужные галочки <b>repo</b> и <b>delete_repo</b> уже стоят. В «Expiration» выбери <b>No expiration</b> (или подольше), внизу нажми <b>Generate token</b>.</li>
      <li>Скопируй токен (начинается с <span class="mono">ghp_</span>) — GitHub показывает его один раз — и вставь в FruityHub.</li>
    </ol>
    <div class="muted small">SSH-ключ и пароль от GitHub не нужны: FruityHub сам подставляет токен при отправке и получении. Токен хранится только на этом компьютере. Если GitHub открывается только через VPN — во вкладке «GitHub» есть прокси.</div></details>`;
}
function changeTokenDialog() {
  const m = modal(`<h2>Сменить GitHub-токен</h2>
    <div class="small">1. Открой <a href="${TOKEN_URL}">страницу создания токена на GitHub ↗</a> — галочки <b>repo</b> и <b>delete_repo</b> уже стоят.<br>2. Срок — <b>No expiration</b>, внизу <b>Generate token</b>.<br>3. Скопируй токен и вставь сюда:</div>
    <input id="tk" type="password" placeholder="ghp_…" style="margin-top:8px">
    ${githubHelpHtml()}
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button id="no">Отмена</button><button class="primary" id="ok">Сохранить</button></div>`);
  m.querySelector('#tk').focus();
  m.querySelector('#no').onclick = () => m.close();
  m.querySelector('#ok').onclick = async () => {
    const t = m.querySelector('#tk').value.trim();
    if (!t) return;
    try {
      const u = await api('/api/account/update', { token: t });
      m.close();
      toast('Токен обновлён — GitHub @' + esc(u.github));
      refreshAll();
    } catch (e) {
      fail(e);
    }
  };
}
async function openSettings(tab) {
  const st = S.state,
    c = st.config,
    t = st.tools,
    me = c.meta || {},
    mexts = me.exts && me.exts.length ? me.exts : ['wav', 'mp3', 'flac'];
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2>Настройки</h2><button class="ghost sm" id="x">✕</button></div>
    <div class="seg stabsx" style="margin-bottom:6px;flex-wrap:wrap">${SET_TABS.map(([k, t]) => `<button data-st="${k}">${t}</button>`).join('')}</div>
    <div data-sp="profile">
    <h3>Профиль</h3>
    ${
      st.registered
        ? `<div class="row">${avatar({ nick: c.userName }, 34)}<div><b style="font-size:16px">${esc(c.userName)}</b><div class="muted small">GitHub @${esc(st.githubLogin || '?')} · сервер ${esc(st.server)} ${S.serverError ? `<span style="color:var(--bad)">· ${esc(S.serverError)}</span>` : '<span style="color:var(--ok)">· на связи</span>'}</div></div><span class="spacer"></span><button class="sm" id="rn">Сменить ник</button><button class="sm" id="rt">Сменить GitHub-токен</button></div>
      <div class="muted small" style="margin-top:6px">Одно устройство — один пользователь: этот компьютер и есть твой аккаунт.</div>`
        : `<div class="row"><span class="muted">Не зарегистрирован</span><button class="sm primary" id="regb">Зарегистрироваться</button></div>`
    }
    <h3>Время в FL Studio</h3>
    <div id="flstats"></div>
    <div class="row" style="margin-top:10px"><button class="sm" onclick="openGuide()">Как пользоваться FruityHub</button><button class="sm" onclick="openWhatsNew()">Что нового</button></div>
    <div class="muted small" style="margin-top:10px">Плагины — кто может скачивать их с твоего компьютера, где FruityHub их ищет, люди и передачи — в окне <a onclick="openPluginManager('settings')">«Плагины»</a> (кнопка вверху).</div>
    </div>
    <div data-sp="projects">
    <h3>Папки с проектами</h3>
    <div class="muted small">Папки, где лежат твои проекты FL Studio (можно несколько). Если папку проекта переименуют или перенесут внутри них, FruityHub найдёт её сам — по имени .flp и репозиторию — и поправит путь. Здесь же видно проекты, которых ещё нет в списке.</div>
    <div id="prlist" class="droplist" style="margin-top:8px"></div>
    <div class="row" style="margin-top:6px"><button class="sm" id="pradd">＋ Добавить папку…</button><span class="muted small">или перетащи папку сюда из Finder / Проводника</span></div>
    <div id="prfound" style="margin-top:10px"></div>
    </div>
    <div data-sp="github">
    <h3>GitHub-токен</h3>
    ${
      st.registered
        ? `<div class="small">Сейчас: GitHub <b>@${esc(st.githubLogin || '?')}</b>. Токен хранится только на этом компьютере — им FruityHub отправляет и получает проекты, пароль и SSH-ключ не нужны.</div>
      <div class="row" style="margin-top:6px"><button class="sm" id="rt2">Сменить GitHub-токен…</button><a class="small" href="${TOKEN_URL}">создать новый токен на GitHub ↗</a></div>`
        : '<div class="muted small">Сначала зарегистрируйся (вкладка «Профиль»).</div>'
    }
    ${githubHelpHtml()}
    <h3>Git LFS — большие файлы</h3>
    <div class="muted small">Файлы с этими расширениями хранятся через Git LFS (аудио, видео, архивы, пресеты) — так репозиторий не раздувается. Действует во всех проектах; новое расширение начнёт работать со следующей отправки.</div>
    <input id="lfsx" value="${esc((c.lfsExtensions && c.lfsExtensions.length ? c.lfsExtensions : st.defaultLfs || []).join(', '))}">
    <div class="row" style="margin-top:6px"><button class="sm" id="lfss">Сохранить</button><a class="small" id="lfsd">вернуть стандартный список</a></div>
    <h3>Мои репозитории на GitHub</h3>
    <div class="row"><button class="sm" id="repos">Показать репозитории проектов…</button><span class="muted small">все, что создал FruityHub, — можно удалить ненужные</span></div>
    </div>
    <div data-sp="net">
    <h3>Сервер FruityHub</h3>
    <div class="small">${esc(st.server || 'не указан')} ${S.serverError ? `<span style="color:var(--bad)">· ${esc(S.serverError)}</span>` : ''}</div>
    <div class="row" style="margin-top:6px"><button class="sm" id="srvt">Проверить связь с сервером</button><button class="sm" id="spd">Проверить скорость</button><span class="small" id="srvr"></span></div>
    <h3>Прокси для GitHub</h3>
    <div class="muted small">Нужен, если GitHub у тебя открывается только через VPN. FruityHub сам берёт «системный прокси» Windows, если он включён в VPN-программе; иначе впиши адрес сюда.</div>
    <input id="px" placeholder="${esc(st.proxyHint ? 'например ' + st.proxyHint : 'http://127.0.0.1:10809')}" value="${esc(st.proxy || '')}">
    <div class="row" style="margin-top:6px"><button class="sm" id="pxs">Сохранить</button><button class="sm" id="pxt">Проверить связь с GitHub</button><span class="small" id="pxr"></span></div>
    <h3>Передача между участниками</h3>
    <label class="switch"><input type="checkbox" id="lanx" ${c.noLan ? '' : 'checked'}> Если мы в одной сети (дома, в студии) — передавать напрямую, мимо сервера</label>
    <div class="muted small">Плагины и аудио идут по локальной сети на её скорости. Windows один раз спросит разрешение для FruityHub в брандмауэре — разреши для частных сетей. Не получилось напрямую — передача сама пойдёт через сервер.</div>
    <label class="switch"><input type="checkbox" id="peerx" ${c.noPeerFetch ? '' : 'checked'}> Аудио проектов брать у участников, а не только с GitHub</label>
    <div class="muted small">Когда скачиваешь проект или жмёшь «Получить», FruityHub сначала просит недостающие wav у участников в сети (это быстрее GitHub и не тратит его лимит), остальное докачивает с GitHub. Своё аудио так же отдаётся участникам.</div>
    </div>
    <div data-sp="meta">
    <h3>Метаданные рендеров</h3>
    <label class="switch"><input type="checkbox" id="mon" ${me.off ? '' : 'checked'}> Записывать название, артиста, альбом и обложку в рендеры трека</label>
    <div class="muted small">Когда FL Studio рендерит трек в папку проекта, FruityHub сам подписывает новый файл тем, что указано во вкладке «Настройки» проекта → «Трек». Сэмплы проекта (папка Audio/) не трогаются.</div>
    <label>В какие файлы</label>
    <div class="row">${['wav', 'mp3', 'flac'].map(x => `<label class="switch" style="margin:0"><input type="checkbox" data-mx="${x}" ${mexts.includes(x) ? 'checked' : ''}> ${x.toUpperCase()}</label>`).join('')}</div>
    <label class="switch"><input type="checkbox" id="mcov" ${me.noCover ? '' : 'checked'}> Вшивать обложку (Metadata/cover.jpg или .png)</label>
    <label class="switch"><input type="checkbox" id="mall" ${me.all ? 'checked' : ''}> Подписать и старые рендеры, которые уже лежат в папках проектов</label>
    <h3>README проекта на GitHub</h3>
    <label class="switch"><input type="checkbox" id="mrd" ${me.noReadme ? '' : 'checked'}> Собирать README.md с обложкой, названием, артистом и списком плагинов при каждой отправке</label>
    <div class="muted small">Если в проекте уже есть свой README.md (написанный не FruityHub), он не трогается.</div>
    </div>
    <div data-sp="look">
    <h3>Язык</h3>
    <select id="lang" style="width:auto"><option value="" ${c.lang ? '' : 'selected'}>Русский</option><option value="en" ${c.lang === 'en' ? 'selected' : ''}>English</option></select>
    <h3>Список проектов</h3>
    <label>Порядок</label>
    <select id="sortm" style="width:auto">${SORTS.map(([k, t]) => `<option value="${k}" ${sortMode() === k ? 'selected' : ''}>${t}</option>`).join('')}</select>
    <div class="muted small">Цвет точки у проекта: зелёный — актуально, синий — есть твои изменения, жёлтый — на GitHub новое, красный — занят другом, пустой — без GitHub. Подсказка — при наведении на точку.</div>
    <h3>Качество графики</h3>
    <div id="gfx">${[
      ['high', 'Высокое', 'Размытие, прозрачные панели, фоновая картинка и плавные анимации. Для мощных компьютеров.'],
      [
        'lite',
        'Простое',
        'Без размытия, прозрачности и лишних анимаций. Фоновая картинка сохраняется.',
      ],
    ]
      .map(
        ([k, t1, d]) =>
          `<label class="radio ${(c.liteUi ? 'lite' : 'high') === k ? 'on' : ''}"><input type="radio" name="gfx" value="${k}" ${(c.liteUi ? 'lite' : 'high') === k ? 'checked' : ''}><span><b>${t1}</b><div class="muted small">${d}</div></span></label>`
      )
      .join('')}</div>
    <div class="muted small">Меняется сразу. Аппаратное ускорение прокрутки работает в обоих режимах.</div>
    <h3>Фон</h3>
    <div class="row"><button class="sm" id="bgpick">Выбрать картинку…</button><button class="sm ghost" id="bgreset" ${st.customBg ? '' : 'disabled'}>Вернуть стандартный</button></div>
    <label>Затемнение фона</label><input type="range" id="bgdim" min="0" max="90" step="1" value="${c.bgDim || 50}" style="padding:0">
    <h3>Иконка приложения</h3>
    <div id="icst" class="row" style="gap:8px;align-items:stretch">${[
      ['', 'Без фона', 'icon-clear'],
      ['tile', 'На тёмной плитке', 'icon-tile'],
    ]
      .map(
        ([k, t1, img]) =>
          `<label class="radio ${(c.iconStyle || '') === k ? 'on' : ''}" style="flex:1;align-items:center;margin:0"><input type="radio" name="icst" value="${k}" ${(c.iconStyle || '') === k ? 'checked' : ''}><img src="/assets/${img}?v=__V__" alt="" style="width:44px;height:44px;flex:none"><span><b>${t1}</b></span></label>`
      )
      .join('')}</div>
    <div class="muted small">${/mac/i.test(S.state.os || navigator.platform) ? 'Меняется в Доке и у FruityHub.app в Finder.' : 'Меняется у окна и на панели задач, и у самого файла FruityHub.exe — если он лежит в защищённой папке (общий рабочий стол), Windows один раз спросит разрешение. Значок, закреплённый на панели задач, Windows может обновить не сразу.'}</div>
    <h3>Уведомления</h3>
    <label>Сколько держать на экране сообщения о готовых задачах (справа снизу) и подсказки</label>
    <select id="tsec" style="width:auto">${[
      [3, '3 секунды'],
      [4, '4 секунды (обычно)'],
      [6, '6 секунд'],
      [10, '10 секунд'],
      [20, '20 секунд'],
      [60, 'минуту'],
      [0, 'пока не закрою'],
    ]
      .map(([v, t]) => `<option value="${v}" ${noteSec() === v ? 'selected' : ''}>${t}</option>`)
      .join('')}</select>
    <div class="muted small">Ошибки висят, пока их не закроешь. На экране не больше трёх сообщений — остальные прокручиваются вниз.</div>
    <h3>Подсказки</h3>
    <label class="switch"><input type="checkbox" id="helpb" ${c.hideHelp ? '' : 'checked'}> Показывать кнопку «?» (знакомство с программой) в шапке</label>
    <div class="muted small">Знакомство всегда можно открыть во вкладке «Профиль». Скрытые крестиком предупреждения в проектах: <a id="undis">показать снова</a>.</div>
    </div>
    <div data-sp="system">
    ${st.install?.supported ? `<h3>Установка</h3>${st.install.installed ? `<div class="small">FruityHub установлен в <span class="mono">${esc(st.install.dir)}</span>. Удалить — Параметры Windows → Приложения → FruityHub.</div>` : `<div class="small">FruityHub запущен прямо из файла <span class="mono">${esc(st.install.exe)}</span>, без установки.</div><div style="margin-top:8px"><button onclick="startInstallFromSettings()">Установить как программу…</button></div>`}` : ''}
    <h3>Компоненты</h3>
    <div class="small">${t.gitOk ? esc(t.git) : 'git не найден'}<br>${t.lfsOk ? esc(t.lfs) : 'git-lfs не найден'}</div>
    ${!t.gitOk || !t.lfsOk ? `<div style="margin-top:8px"><button onclick="installTools()">Установить автоматически</button></div>` : ''}
    <div id="syscheck" class="small" style="margin-top:6px"></div>
    <div class="card small muted">Один раз в FL Studio: <i>Options → File settings → Browser extra search folders</i> — добавь папку, где лежат все проекты. И в каждом проекте: <i>Project → General settings → Data folder</i> = папка <b>Audio</b> проекта, тогда записи сразу будут падать туда.</div>
    <h3>Для разработчика</h3>
    <label class="switch"><input type="checkbox" id="devm" ${c.devMode ? 'checked' : ''}> Режим разработчика — журнал команд и управление сервером (кнопка «Разработчик» вверху)</label>
    <div class="muted small">Настройки: <span class="mono">${esc(st.configDir)}</span></div>
    </div>
    <div data-sp="updates">
    <h3>Обновления</h3>
    <label class="switch"><input type="checkbox" id="autoup" ${c.autoUpdate ? 'checked' : ''}> Автоматически обновлять FruityHub, когда выходит новая версия</label>
    <div class="muted small">Обновление ставится, только когда ничего не передаётся и не отправляется: если идёт передача плагина или синхронизация проекта, FruityHub дождётся её конца и перезапустится сам.</div>
    <label class="switch"><input type="checkbox" id="errrep" ${c.noErrorReports ? '' : 'checked'}> Отправлять мои ошибки в журнал сервера (их видит администратор — так проще разобраться, что сломалось)</label>
    <div class="row" style="margin-top:8px"><span class="small">Версия <b>${esc(st.version)}</b></span><button class="sm" onclick="checkUpdate()">Проверить обновления</button><button class="sm" onclick="openWhatsNew()">Что нового</button></div>
    </div>
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button class="primary" id="cancel">Готово</button></div>`);
  m.querySelector('#x').onclick = m.querySelector('#cancel').onclick = () => m.close();
  m.querySelector('.modal').style.width = 'min(860px,94vw)';
  // вкладки настроек (последняя открытая запоминается)
  const showSet = k => {
    m.querySelectorAll('[data-sp]').forEach(d => (d.style.display = d.dataset.sp === k ? '' : 'none'));
    m.querySelectorAll('[data-st]').forEach(b => b.classList.toggle('on', b.dataset.st === k));
    try {
      localStorage.setItem('setTab', k);
    } catch (e) {}
  };
  m.querySelectorAll('[data-st]').forEach(b => (b.onclick = () => showSet(b.dataset.st)));
  loadFlStats(m.querySelector('#flstats'));
  let st0 = tab || 'profile';
  if (!tab) {
    try {
      st0 = localStorage.getItem('setTab') || 'profile';
    } catch (e) {}
  }
  if (!SET_TABS.some(([k]) => k === st0)) st0 = 'profile';
  showSet(st0);
  const set = async (o, after) => {
    try {
      await api('/api/settings', o);
      after && after();
      toast('Сохранено');
    } catch (er) {
      fail(er);
    }
  };
  m.querySelector('#lanx').onchange = e =>
    set({ lan: e.target.checked }, () => (S.state.config.noLan = !e.target.checked));
  m.querySelector('#peerx').onchange = e =>
    set({ peerFetch: e.target.checked }, () => (S.state.config.noPeerFetch = !e.target.checked));
  m.querySelector('#spd').onclick = async () => {
    const j = await runJob('/api/system/speedtest', {}, { title: 'Проверяю скорость связи с сервером' });
    if (j && !j.error && j.result) uiAlert(j.result, 'Скорость связи с сервером');
  };
  m.querySelector('#srvt').onclick = async () => {
    const o = m.querySelector('#srvr');
    o.textContent = 'Проверяю…';
    try {
      const r = await api('/api/server/test', {});
      o.innerHTML = r.ok
        ? `<span style="color:var(--ok)">✓ сервер отвечает, ${r.ms} мс</span>`
        : `<span style="color:var(--bad)">✗ ${esc(r.error)}</span>`;
    } catch (e) {
      o.textContent = String(e.message || e);
    }
  };
  m.querySelector('#tsec').onchange = e => {
    const v = +e.target.value;
    set({ toastSec: v }, () => (S.state.config.toastSec = v));
  };
  m.querySelector('#helpb').onchange = async e => {
    try {
      await api('/api/settings', { hideHelp: !e.target.checked });
      S.state.config.hideHelp = !e.target.checked;
      $('#helpBtn').style.display = e.target.checked ? '' : 'none';
    } catch (er) {
      fail(er);
    }
  };
  m.querySelector('#undis').onclick = () => {
    try {
      localStorage.removeItem('dismissed');
    } catch (e) {}
    renderMain();
    toast('Предупреждения снова видны');
  };
  m.querySelector('#sortm').onchange = e => setSort(e.target.value);
  m.querySelectorAll('input[name=gfx]').forEach(
    i =>
      (i.onchange = async () => {
        const on = i.value === 'lite';
        m.querySelectorAll('#gfx .radio').forEach(l => l.classList.toggle('on', l.querySelector('input').checked));
        try {
          await api('/api/settings', { liteUi: on });
          S.state.config.liteUi = on;
          document.body.classList.toggle('lite', on);
          if (!on && !BG.src) BG.src = '/assets/bg?v=__V__';
          toast(
            on
              ? 'Простая графика включена'
              : 'Высокое качество графики включено',
            7000
          );
        } catch (er) {
          fail(er);
        }
      })
  );
  m.querySelector('#lang').onchange = async e => {
    try {
      await api('/api/settings', { lang: e.target.value });
      S.state.config.lang = e.target.value;
      m.close();
      applyLang(true);
    } catch (er) {
      fail(er);
    }
  };
  const rt2 = m.querySelector('#rt2');
  if (rt2)
    rt2.onclick = () => {
      m.close();
      changeTokenDialog();
    };
  const rb = m.querySelector('#regb');
  if (rb)
    rb.onclick = () => {
      m.close();
      openRegister(true);
    };
  m.querySelector('#autoup').onchange = async e => {
    try {
      await api('/api/settings', { autoUpdate: e.target.checked });
      S.state.config.autoUpdate = e.target.checked;
      toast(e.target.checked ? 'Буду обновляться сам' : 'Автообновление выключено');
    } catch (er) {
      fail(er);
    }
  };
  m.querySelector('#errrep').onchange = e =>
    set({ errorReports: e.target.checked }, () => (S.state.config.noErrorReports = !e.target.checked));
  const rn = m.querySelector('#rn');
  if (rn)
    rn.onclick = async () => {
      const n = await uiPrompt('Новый ник', { value: c.userName });
      if (!n || !n.trim() || n.trim() === c.userName) return;
      try {
        await api('/api/account/update', { nick: n.trim() });
        toast('Ник изменён');
        m.close();
        refreshAll();
      } catch (e) {
        fail(e);
      }
    };
  const rt = m.querySelector('#rt');
  if (rt)
    rt.onclick = () => {
      m.close();
      changeTokenDialog();
    };
  // папки с проектами
  let roots = [...(c.projectRoots || [])];
  const drawFound = r => {
    const el = m.querySelector('#prfound');
    const moved = (r.found || []).filter(f => !f.known && f.moved),
      fresh = (r.found || []).filter(f => !f.known && !f.moved);
    el.innerHTML = !roots.length
      ? ''
      : `<h3>Проекты в этих папках</h3><div class="muted small">Найдено: ${(r.found || []).length}, ещё не в списке: ${fresh.length}.</div>` +
        moved
          .map(
            (f, i) =>
              `<div class="drow"><span class="ellip"><b>${esc(f.name)}</b> <span class="muted small">— похоже, сюда переехал «${esc(f.movedName)}»</span></span><button class="sm primary" data-mv="${i}">Обновить путь</button></div>`
          )
          .join('') +
        (fresh.length
          ? `<div class="row" style="margin:6px 0"><button class="sm primary" id="praall">Добавить все (${fresh.length})</button></div>` +
            fresh
              .map(
                (f, i) =>
                  `<div class="drow"><span class="ellip"><b>${esc(f.name)}</b> <span class="muted small mono">${esc(f.path)}</span></span><button class="sm" data-pa="${i}">Добавить</button></div>`
              )
              .join('')
          : '');
    const add = async l => {
      try {
        for (const f of l) await api('/api/projects/add', { path: f.path });
        toast('Добавлено проектов: ' + l.length);
        await refreshAll();
        scanRoots();
      } catch (e) {
        fail(e);
      }
    };
    el.querySelectorAll('[data-pa]').forEach(b => (b.onclick = () => add([fresh[+b.dataset.pa]])));
    el.querySelectorAll('[data-mv]').forEach(
      b =>
        (b.onclick = async () => {
          const f = moved[+b.dataset.mv];
          try {
            await api('/api/project/relocate', { path: f.moved, newPath: f.path });
            if (S.sel === f.moved) S.sel = f.path;
            toast('Путь к проекту обновлён');
            await refreshAll();
            if (S.sel) loadProject(S.sel);
            scanRoots();
          } catch (e) {
            fail(e);
          }
        })
    );
    const aa = el.querySelector('#praall');
    if (aa) aa.onclick = () => add(fresh);
  };
  const scanRoots = async save => {
    const el = m.querySelector('#prfound');
    if (roots.length) el.innerHTML = '<div class="muted small">Ищу проекты…</div>';
    try {
      const r = await api('/api/projectroots', save ? { roots } : undefined);
      S.state.config.projectRoots = r.roots || [];
      drawFound(r);
    } catch (e) {
      fail(e);
    }
  };
  const drawRoots = () => {
    m.querySelector('#prlist').innerHTML = roots.length
      ? roots
          .map(
            (d, i) =>
              `<div class="drow"><span class="mono ellip" title="${esc(d)}">${esc(d)}</span><button class="sm ghost" data-ri="${i}" title="Убрать">−</button></div>`
          )
          .join('')
      : '<div class="muted small">Папок пока нет</div>';
    m.querySelectorAll('#prlist [data-ri]').forEach(
      b =>
        (b.onclick = () => {
          roots.splice(+b.dataset.ri, 1);
          drawRoots();
          scanRoots(true);
        })
    );
  };
  const addRoot = d => {
    if (d && !roots.includes(d)) {
      roots.push(d);
      drawRoots();
      scanRoots(true);
    }
  };
  drawRoots();
  if (roots.length) scanRoots();
  m.querySelector('#pradd').onclick = async () => {
    const r = await api('/api/pickfolder', {}).catch(() => ({}));
    if (r.path) addRoot(r.path);
  };
  bindFolderDrop(m.querySelector('[data-sp=projects]'), true, paths => paths.forEach(addRoot));
  // Git LFS
  const lfsList = () =>
    m
      .querySelector('#lfsx')
      .value.split(/[,\s]+/)
      .map(x => x.replace(/^\./, '').trim().toLowerCase())
      .filter(Boolean);
  m.querySelector('#lfss').onclick = () =>
    set({ lfsExtensions: lfsList() }, () => (S.state.config.lfsExtensions = lfsList()));
  m.querySelector('#lfsd').onclick = () => {
    m.querySelector('#lfsx').value = (st.defaultLfs || []).join(', ');
    set({ lfsExtensions: st.defaultLfs || [] }, () => (S.state.config.lfsExtensions = st.defaultLfs));
  };
  // метаданные
  const saveMetaSet = () => {
    const o = {
      off: !m.querySelector('#mon').checked,
      exts: [...m.querySelectorAll('[data-mx]')].filter(x => x.checked).map(x => x.dataset.mx),
      noCover: !m.querySelector('#mcov').checked,
      noReadme: !m.querySelector('#mrd').checked,
      all: m.querySelector('#mall').checked,
    };
    if (!o.exts.length) {
      o.exts = ['wav'];
      m.querySelector('[data-mx=wav]').checked = true;
    }
    set({ meta: o }, () => (S.state.config.meta = o));
  };
  m.querySelectorAll('#mon,#mcov,#mrd,#mall,[data-mx]').forEach(x => (x.onchange = saveMetaSet));
  // прокси
  m.querySelector('#pxs').onclick = () =>
    set({ proxy: m.querySelector('#px').value.trim() }, () => (S.state.proxy = m.querySelector('#px').value.trim()));
  m.querySelector('#pxt').onclick = async () => {
    const o = m.querySelector('#pxr');
    o.textContent = 'Проверяю…';
    try {
      const r = await api('/api/github/test', { proxy: m.querySelector('#px').value.trim() });
      o.innerHTML = r.ok
        ? `<span style="color:var(--ok)">✓ GitHub отвечает ${esc(r.via)}, ${r.ms} мс</span>`
        : `<span style="color:var(--bad)">✗ ${esc(r.via)}: ${esc(r.error)}</span>`;
    } catch (e) {
      o.textContent = String(e.message || e);
    }
  };
  // проверка системы: показываем только то, что мешает плагинам (Windows)
  api('/api/system/check')
    .then(c => {
      const el = m.querySelector('#syscheck');
      const rows = [];
      if (c.os === 'Windows') {
        if (c.vcredistMissing)
          rows.push(
            `<div style="color:var(--bad)">✗ Нет Microsoft Visual C++ — многие плагины не загрузятся. <button class="sm primary" id="vcb">Установить</button></div>`
          );
        if ((c.gpus || []).length && c.basicGpu)
          rows.push(
            `<div style="color:var(--bad)">✗ Видеокарта на стандартном драйвере (${esc(c.gpus.join(', '))}) — у плагинов может быть серое окно. Поставь драйвер с сайта Intel / AMD / NVIDIA.</div>`
          );
      }
      if (c.gitCrashes >= 5)
        rows.push(`<div style="color:var(--bad)">✗ git на этом компьютере часто не запускается (${c.gitCrashes} раз с запуска FruityHub, сбой 0xc0000005 или «Отказано в доступе»). FruityHub повторяет попытки, но работа идёт медленно и иногда с ошибками. Обычно виноват антивирус или защита Windows.
      ${c.systemGit ? '' : '<b>Поставь Git for Windows</b> с сайта git-scm.com (обычный установщик, всё по умолчанию) — FruityHub сам начнёт им пользоваться вместо встроенного MinGit. '}Если не поможет: «Безопасность Windows» → «Управление приложениями/браузером» → «Защита от эксплойтов» → «Параметры системы» → «Принудительный случайный выбор для образов (обязательный ASLR)» = «По умолчанию (выкл.)», и добавь папку FruityHub в исключения антивируса.</div>`);
      el.innerHTML = rows.join('');
      const vb = el.querySelector('#vcb');
      if (vb)
        vb.onclick = () => {
          m.close();
          runJob('/api/system/install-vcredist', {}, { title: 'Ставлю Microsoft Visual C++' });
        };
    })
    .catch(() => {});
  // фон
  const bgApply = r => {
    S.state.customBg = r.custom;
    m.querySelector('#bgreset').disabled = !r.custom;
    reloadBg(r.stamp);
  };
  m.querySelectorAll('input[name=icst]').forEach(
    i =>
      (i.onchange = async () => {
        m.querySelectorAll('#icst .radio').forEach(l => l.classList.toggle('on', l.querySelector('input').checked));
        const j = await runJob('/api/appicon', { style: i.value }, { title: 'Меняю иконку приложения' });
        if (j) {
          S.state.config.iconStyle = i.value;
          const lg = document.querySelector('.logo img');
          if (lg) lg.src = '/assets/icon?v=' + Date.now();
          document.querySelectorAll('link[rel=icon]').forEach(l => (l.href = '/assets/icon?v=' + Date.now()));
        }
      })
  );
  m.querySelector('#bgpick').onclick = async () => {
    try {
      const r = await api('/api/background', { action: 'pick' });
      if (r.ok) bgApply(r);
    } catch (e) {
      fail(e);
    }
  };
  m.querySelector('#bgreset').onclick = async () => {
    try {
      bgApply(await api('/api/background', { action: 'reset' }));
    } catch (e) {
      fail(e);
    }
  };
  const dim = m.querySelector('#bgdim');
  let dimT;
  dim.oninput = () => {
    setDim(+dim.value);
    clearTimeout(dimT);
    dimT = setTimeout(() => {
      api('/api/background', { dim: +dim.value })
        .then(() => {
          S.state.config.bgDim = +dim.value;
        })
        .catch(() => {});
    }, 300);
  };
  m.querySelector('#repos').onclick = () => {
    m.close();
    openRepos();
  };
  m.querySelector('#devm').onchange = async e => {
    try {
      await api('/api/dev/settings', { devMode: e.target.checked });
      S.state.config.devMode = e.target.checked;
      renderDevBtn();
    } catch (er) {
      fail(er);
    }
  };
}
function openRegister(first) {
  const st = S.state;
  if (st.registered) return openSettings();
  const m = modal(`<h2>${first ? 'Привет! Давай зарегистрируемся' : 'Регистрация'}</h2>
    <div class="muted small">Одно устройство — один пользователь: аккаунт привязывается к этому компьютеру. Друзья зовут тебя в проекты по нику, через него же вы обмениваетесь плагинами.</div>
    <label>Ник</label><input id="un" placeholder="напр. tetramino" maxlength="32" value="${esc(st.config.userName || '')}">
    <label>GitHub-токен ${st.hasToken ? '<span class="tag ok">уже сохранён</span>' : ''}</label><input id="tk" type="password" placeholder="${st.hasToken ? 'оставь пустым, чтобы использовать сохранённый' : 'ghp_…'}">
    <div class="muted small">Проекты лежат на твоём GitHub — по репозиторию на проект. <a href="${TOKEN_URL}"><b>Создать токен на GitHub ↗</b></a> — нужные галочки (<b>repo</b> и <b>delete_repo</b>, чтобы можно было удалять ненужные репозитории прямо из FruityHub) уже стоят; срок — No expiration. Токен хранится только на этом компьютере; сервер лишь один раз сверяет, чей он.</div>
    ${githubHelpHtml()}
    <details style="margin-top:10px" ${st.serverDefault ? '' : 'open'}><summary class="muted small" style="cursor:pointer">Сервер</summary>
      <div class="muted small" style="margin-top:6px">${st.serverDefault ? 'Адрес встроен в FruityHub — менять не нужно.' : 'Адрес и код сервера даёт тот, кто его поднял.'}</div>
      <label>Адрес</label><input id="srv" value="${esc(st.server || '')}" placeholder="https://1-2-3-4.sslip.io">
      ${st.serverCodeBuiltIn ? '' : '<label>Код сервера</label><input id="code" placeholder="напр. 3f9a1c7b">'}</details>
    <div class="row" style="justify-content:flex-end;margin-top:14px">${first ? '<button id="later" class="ghost">Позже</button>' : ''}<button class="primary" id="ok">Зарегистрироваться</button></div>`);
  const l = m.querySelector('#later');
  if (l) l.onclick = () => m.close();
  const un = m.querySelector('#un');
  un.focus();
  const go = async () => {
    const nick = un.value.trim();
    if (!nick) return uiAlert('Придумай ник');
    const token = m.querySelector('#tk').value.trim();
    if (!token && !st.hasToken) return uiAlert('Нужен GitHub-токен — проекты хранятся на твоём GitHub');
    const ce = m.querySelector('#code');
    const btn = m.querySelector('#ok');
    btn.disabled = true;
    btn.textContent = 'Регистрирую…';
    try {
      const u = await api('/api/account/register', {
        nick,
        token,
        server: m.querySelector('#srv').value.trim(),
        code: ce ? ce.value.trim() : '',
      });
      m.close();
      toast(`Готово! Ты — ${esc(u.nick)} (GitHub @${esc(u.github)})`);
      await refreshAll();
      pollNotes();
      loadAvailable(true);
    } catch (e) {
      btn.disabled = false;
      btn.textContent = 'Зарегистрироваться';
      fail(e);
    }
  };
  m.querySelector('#ok').onclick = go;
  un.onkeydown = e => {
    if (e.key === 'Enter') go();
  };
}

// «Установить как программу…» (Настройки → Система) — для тех, кто раньше выбрал «без установки»
function startInstallFromSettings() {
  document.querySelectorAll('.modal-bg').forEach(m => m.close());
  S.state.install.show = true;
  openInstaller();
}
