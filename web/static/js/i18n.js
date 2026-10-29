// Язык интерфейса.
//
// Тексты интерфейса написаны прямо в коде по-русски — это «ключи». Два словаря подменяют их на экране:
//   static/i18n/ru.js — русский текст: правая часть — как фраза показывается (по умолчанию совпадает с ключом);
//   static/i18n/en.js — английский перевод.
// Подмена идёт кусками (поэтому фразы с числами и именами тоже работают) — и для того, что уже на экране,
// и для всего, что появляется потом. Проверка, что словари полные: python3 scripts/i18n_check.py
const I18N = { on: false, lang: 'ru', re: null, dict: null, obs: null, done: new WeakMap() };
const LETTER = /[A-Za-zА-Яа-яЁё0-9]/;
const I18N_ATTRS = ['placeholder', 'title', 'alt'];

function trText(t) {
  if (!t) return t;
  const translated = !I18N.re || !/[А-Яа-яЁё]/.test(t) ? t : t.replace(I18N.re, (m, _, off, str) => {
    // кусок внутри слова (например «Отмена» в «Отменами») не трогаем
    if (LETTER.test(m[0]) && off > 0 && LETTER.test(str[off - 1])) return m;
    const e = off + m.length;
    if (LETTER.test(m[m.length - 1]) && e < str.length && LETTER.test(str[e])) return m;
    return I18N.dict[m] ?? m;
  });
  return platformShortcuts(translated);
}

// уже подменённое не трогаем повторно (иначе «Отправить» → «Отправить!» превращалось бы в «Отправить!!»)
function trAttrs(el) {
  let seen = I18N.done.get(el);
  for (const a of I18N_ATTRS) {
    const v = el.getAttribute && el.getAttribute(a);
    if (!v || (seen && seen[a] === v)) continue;
    const t = trText(v);
    if (t !== v) el.setAttribute(a, t);
    if (!seen) I18N.done.set(el, (seen = {}));
    seen[a] = t;
  }
}

function trNode(n) {
  if (n.nodeType === 3) {
    const p = n.parentNode;
    if (p && (p.nodeName === 'SCRIPT' || p.nodeName === 'STYLE' || p.nodeName === 'TEXTAREA')) return;
    const v = n.nodeValue;
    if (I18N.done.get(n) === v) return;
    const t = trText(v);
    if (t !== v) n.nodeValue = t;
    I18N.done.set(n, t);
    return;
  }
  if (n.nodeType !== 1 || n.nodeName === 'SCRIPT' || n.nodeName === 'STYLE') return;
  trAttrs(n);
  const w = document.createTreeWalker(n, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
  let x;
  while ((x = w.nextNode())) {
    if (x.nodeType === 3) trNode(x);
    else trAttrs(x);
  }
}

// словарь языка: файл static/i18n/<lang>.js кладёт его в window.I18N_RU / window.I18N_EN
function loadDict(lang) {
  const name = 'I18N_' + lang.toUpperCase();
  if (window[name]) return Promise.resolve(window[name]);
  return new Promise(res => {
    const sc = document.createElement('script');
    sc.src = '/static/i18n/' + lang + '.js?v=__V__';
    sc.onload = () => res(window[name] || {});
    sc.onerror = () => res({});
    document.head.appendChild(sc);
  });
}

function startI18n(d, lang) {
  // в работу идут только фразы, которые что-то меняют (в русском словаре обычно — те, что ты переписал)
  const keys = Object.keys(d).filter(k => d[k] && d[k] !== k);

  keys.sort((a, b) => b.length - a.length);
  I18N.dict = d;
  I18N.re = keys.length ? new RegExp('(' + keys.map(k => k.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|') + ')', 'g') : null;
  I18N.on = true;
  I18N.lang = lang;
  document.documentElement.lang = lang;
  trNode(document.body);
  I18N.obs = new MutationObserver(ms => {
    const roots = new Set();
    for (const m of ms) {
      if (m.type === 'childList') m.addedNodes.forEach(n => roots.add(n));
      else roots.add(m.target);
    }
    for (const n of roots) {
      let parent = n.parentNode;
      while (parent && !roots.has(parent)) parent = parent.parentNode;
      if (!parent && n.isConnected) trNode(n);
    }

  });
  I18N.obs.observe(document.body, {
    subtree: true,
    childList: true,
    characterData: true,
    attributes: true,
    attributeFilter: I18N_ATTRS,
  });
}

function applyLang() {
  const lang = S.state?.config?.lang === 'en' ? 'en' : 'ru';
  if (I18N.on) {
    if (I18N.lang !== lang) location.reload(); // сменили язык — проще всего перерисовать заново
    return;
  }
  if (I18N.loading === lang) return;
  I18N.loading = lang;
  loadDict(lang).then(d => {
    if (!I18N.on) startI18n(d, lang);
  });
}

function platformShortcuts(text) {
 const mac = S.state?.platform?.startsWith('darwin') || S.state?.os === 'macOS';
 return text.replace(/(?:⌘|cmd|command)\s*\/\s*ctrl|ctrl\s*\/\s*(?:⌘|cmd|command)/gi, mac ? '⌘' : 'Ctrl')
    .replace(/\b(?:Ctrl|Control)(?=\s*\+)/gi, mac ? '⌘' : 'Ctrl')
    .replace(/(?:⌘|Cmd|Command)(?=\s*\+)/gi, mac ? '⌘' : 'Ctrl');
}
