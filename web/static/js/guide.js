// Знакомство с программой (кнопка «?») и окно «Что нового».

const GUIDE = [
  [
    'Что такое FruityHub',
    `Программа для совместной работы над проектами FL Studio. Каждый проект хранится в приватном репозитории на твоём GitHub со всей историей версий, а друзья получают его одной кнопкой. Плагины друг другу можно передавать прямо из программы.`,
  ],
  [
    '1. Регистрация',
    `Придумай ник и вставь GitHub-токен — по нему FruityHub отправляет и получает проекты (пароль и SSH-ключ не нужны). Нет аккаунта — зарегистрируйся на <a href="https://github.com/signup">github.com/signup</a>, токен создаётся <a href="${TOKEN_URL}">по этой ссылке</a> (галочки уже стоят).`,
  ],
  [
    '2. Проект',
    `«＋ Добавить проект» — выбери папку с .flp (или перетащи её в окно). Затем «Создать репозиторий и отправить» и отметь, кого позвать: друзьям придёт приглашение, проект появится у них во вкладке «Доступные».`,
  ],
  [
    '3. Рабочий цикл',
    `<b>Получить</b> (забрать свежую версию) → <b>Взять в работу</b> (друзья увидят, что ты занят) → <b>Запустить проект</b> (правый клик или ▾ — выбрать версию FL) → работаешь и сохраняешь в FL (Ctrl/⌘+S) → <b>Отправить</b> с галочкой «освободить». Сэмплы сами копируются в папку Audio проекта.`,
  ],
  [
    '4. Цвета в списке',
    `<span class="dot ok" style="display:inline-block"></span> актуально · <span class="dot local" style="display:inline-block"></span> есть твои неотправленные изменения · <span class="dot behind" style="display:inline-block"></span> на GitHub новое — нажми «Получить» · <span class="dot other" style="display:inline-block"></span> проект в работе у друга · <span class="dot none" style="display:inline-block"></span> без GitHub. Под названием — артист из Project info.`,
  ],
  [
    '5. Если вы оба поменяли проект',
    `Ничего не теряется: FruityHub предложит, какую версию оставить — с GitHub, свою или обе (вторая ляжет рядом копией).`,
  ],
  [
    '6. Плагины',
    `Вкладка «Плагины» проекта показывает, у кого какие плагины есть. Нет плагина — «Скачать у …», он придёт с компьютера друга вместе со своими данными и встанет сам. Нажми на плагин — проверка, зависимости, переустановка, удаление. Если вы в одной сети — всё идёт напрямую, быстрее. Лицензии не переносятся: платные плагины активируй своей лицензией (Arturia Software Center, Native Access и т. п.).`,
  ],
  [
    '7. Один раз в FL Studio',
    `<i>Options → File settings → Browser extra search folders</i> — добавь папку со всеми проектами. В проекте: <i>Project → General settings → Data folder</i> = папка <b>Audio</b> проекта — тогда записи сразу падают туда.`,
  ],
  [
    'Если что-то не так',
    `Красная плашка задачи → «Подробности». В «Настройках» есть проверка системы, у плагина — «Диагностика». Ошибки автоматически попадают в журнал сервера, по нему проще найти причину.`,
  ],
];
function openGuide(first) {
  return new Promise(res => {
    let i = 0;
    const m =
      modal(`<div class="row" style="justify-content:space-between"><h2 style="margin:0">${first ? 'Добро пожаловать в FruityHub' : 'Как пользоваться FruityHub'}</h2><button class="ghost sm" id="x">✕</button></div>
      <div class="muted small" id="gi"></div><h3 id="gt" style="margin-top:6px"></h3><div id="gb" style="line-height:1.55;min-height:110px"></div>
      <div class="row" style="margin-top:12px"><span class="muted small" id="gn"></span><span class="spacer"></span><button id="gp">Назад</button><button class="primary" id="gx">Дальше</button></div>`);
    m.querySelector('.modal').style.width = 'min(620px,94vw)';
    const draw = () => {
      const [t, b] = GUIDE[i];
      m.querySelector('#gt').textContent = t;
      m.querySelector('#gb').innerHTML = b;
      m.querySelector('#gn').textContent = i + 1 + ' из ' + GUIDE.length;
      m.querySelector('#gp').disabled = i === 0;
      m.querySelector('#gx').textContent = i === GUIDE.length - 1 ? (first ? 'Начать' : 'Готово') : 'Дальше';
    };
    const done = () => {
      m.close();
      res();
    };
    m.querySelector('#x').onclick = done;
    m.querySelector('#gp').onclick = () => {
      if (i > 0) {
        i--;
        draw();
      }
    };
    m.querySelector('#gx').onclick = () => {
      if (i < GUIDE.length - 1) {
        i++;
        draw();
      } else done();
    };
    draw();
  });
}
function openWhatsNew(auto) {
  const cur = S.state?.version || '';
  const m =
    modal(`<div class="row" style="justify-content:space-between"><h2 style="margin:0">${auto ? 'FruityHub обновился до ' + esc(cur) : 'Что нового'}</h2><button class="ghost sm" id="x">✕</button></div>
    ${CHANGES.map(([v, l], k) => `<h3 style="margin-bottom:4px">${esc(v)}${v === cur ? ' <span class="tag ok">у тебя</span>' : ''}</h3><ul class="small" style="margin:0 0 0 18px;line-height:1.6">${l.map(x => `<li>${esc(x)}</li>`).join('')}</ul>`).join('')}
    <div class="row" style="justify-content:flex-end;margin-top:12px"><button class="primary" id="ok">Понятно</button></div>`);
  m.querySelector('.modal').style.width = 'min(680px,94vw)';
  m.querySelector('#x').onclick = m.querySelector('#ok').onclick = () => m.close();
}
