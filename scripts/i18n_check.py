"""Тексты интерфейса FruityHub: проверка словарей и добавление новых фраз.

Интерфейс написан в коде по-русски (web/index.html и web/static/js/*.js) — эти фразы служат «ключами».
Словари:
  web/static/i18n/ru.js — русский текст: «фраза из кода»: «как её показывать» (меняй правую часть);
  web/static/i18n/en.js — английский перевод.

Запуск из корня репозитория:
  python3 scripts/i18n_check.py          — что не переведено на английский, каких фраз нет в ru.js,
                                            какие фразы в ru.js больше не встречаются в коде;
  python3 scripts/i18n_check.py --sync   — дописать в ru.js новые фразы из кода (правая часть = как в коде);
  python3 scripts/i18n_check.py --sync --prune — заодно убрать из ru.js фразы, которых в коде больше нет.
"""
import re, sys, json, os

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..')
WEB = os.path.join(ROOT, 'web')
JS_DIR = os.path.join(WEB, 'static', 'js')
RU = os.path.join(WEB, 'static', 'i18n', 'ru.js')
EN = os.path.join(WEB, 'static', 'i18n', 'en.js')

# порядок разделов в ru.js = порядок подключения скриптов в index.html
def script_order():
    html = open(os.path.join(WEB, 'index.html'), encoding='utf-8').read()
    return re.findall(r'/static/js/([\w-]+)\.js', html)

TITLES = {
    'index': 'Шапка и левая панель', 'core': 'Общее: окна, уведомления', 'tasks': 'Задачи и конфликты',
    'state': 'Связь с сервером, приглашения', 'changelog': '«Что нового» — список изменений',
    'guide': 'Знакомство с программой', 'project-list': 'Список проектов', 'available': 'Вкладка «Доступные»',
    'people': 'Участники и репозитории', 'project': 'Экран проекта', 'project-tabs': 'Вкладки проекта',
    'talk': 'Обсуждение', 'sync': 'Получить и отправить', 'project-plugins': 'Плагины проекта',
    'settings': 'Настройки и регистрация', 'plugin-manager': 'Окно «Плагины»', 'background': 'Фон',
    'dragdrop': 'Перетаскивание папок', 'keyboard': 'Клавиши', 'updates': 'Обновления',
    'devmode': 'Режим разработчика и журнал', 'i18n': 'Язык', 'about': '«О программе» и заставка', 'boot': 'Запуск',
}

def extract(text, is_html):
    """Русские фрагменты текста: куски между тегами, подстановками ${…} и кавычками."""
    frags = []
    def addtext(t):
        for p in re.split(r'<[^>]*>|&[a-z]+;|&#\d+;', t):
            for q in p.split('\n'):
                q = q.strip()
                if re.search('[А-Яа-яЁё]', q):
                    frags.append(q)
    def attrs(t):
        for m in re.finditer(r'(?:title|placeholder|alt)="([^"$]*)"', t):
            if re.search('[А-Яа-яЁё]', m.group(1)):
                frags.append(m.group(1).strip())
    if is_html:
        body = text[text.index('<body>'):text.index('<script')]
        body = re.sub(r'<!--.*?-->', '', body, flags=re.S)
        addtext(body); attrs(body)
        return frags
    js = text; n = len(js)
    def handle(x):
        x = x.replace("\\'", "'").replace('\\"', '"').replace('\\n', '\n')
        addtext(x); attrs(x)
    def read_str(i):
        q = js[i]; i += 1; buf = ''
        while i < n and js[i] != q:
            if js[i] == '\\':
                buf += js[i:i + 2]; i += 2; continue
            buf += js[i]; i += 1
        return i + 1, buf
    def read_template(i):
        i += 1; buf = ''; pieces = []
        while i < n:
            c = js[i]
            if c == '\\':
                buf += js[i:i + 2]; i += 2; continue
            if c == '`':
                pieces.append(buf); return i + 1, pieces
            if c == '$' and js[i + 1] == '{':
                pieces.append(buf); buf = ''
                i = skip_expr(i + 2); continue
            buf += c; i += 1
        return i, pieces
    def skip_expr(i):
        depth = 1
        while i < n and depth > 0:
            c = js[i]
            if c in '\'"':
                j, b = read_str(i); handle(b); i = j; continue
            if c == '`':
                j, p = read_template(i)
                for x in p: handle(x)
                i = j; continue
            if c == '{': depth += 1
            elif c == '}': depth -= 1
            i += 1
        return i
    i = 0
    while i < n:
        c = js[i]
        if js.startswith('//', i):
            j = js.find('\n', i); i = n if j < 0 else j; continue
        if js.startswith('/*', i):
            i = js.find('*/', i) + 2; continue
        if c in '\'"':
            j, b = read_str(i); handle(b); i = j; continue
        if c == '`':
            j, p = read_template(i)
            for x in p: handle(x)
            i = j; continue
        if c == '/':
            prev = js[:i].rstrip()[-1:] or ';'
            if prev in '=(,:!&|?{};[' or js[:i].rstrip().endswith('return'):
                j = i + 1
                while j < n and js[j] != '/':
                    if js[j] == '\\': j += 1
                    elif js[j] == '[':
                        while js[j] != ']':
                            if js[j] == '\\': j += 1
                            j += 1
                    j += 1
                i = j + 1; continue
        i += 1
    return frags

def phrases():
    """[(раздел, фраза)] в порядке появления, без повторов."""
    out, seen = [], set()
    srcs = [('index', open(os.path.join(WEB, 'index.html'), encoding='utf-8').read(), True)]
    for f in script_order():
        srcs.append((f, open(os.path.join(JS_DIR, f + '.js'), encoding='utf-8').read(), False))
    for sec, text, is_html in srcs:
        for f in extract(text, is_html):
            parts = [f] if not re.search(r'data-|class=|onclick|="|\$|\{|\}|<|"', f) else re.split(r'"|<[^>]*>|>|<|\(|\)', f)
            for p in parts:
                p = p.lstrip('>').strip().strip("'").strip()
                if re.search('[А-Яа-яЁё]', p) and not re.search(r'[{}=]', p) and p not in seen:
                    seen.add(p); out.append((sec, p))
    return out

def load_dict(path):
    """Словарь из .js: убираем строки-комментарии и висячие запятые — дальше это JSON."""
    if not os.path.exists(path):
        return {}
    s = open(path, encoding='utf-8').read()
    s = s[s.index('{'):s.rindex('}') + 1]
    s = '\n'.join(l for l in s.split('\n') if not l.strip().startswith('//'))
    s = re.sub(r',\s*}$', '}', s.strip())
    return json.loads(s)

RU_HEAD = '''// Русский текст интерфейса FruityHub.
//
// Слева — фраза, как она написана в коде: её НЕ меняй, по ней программа находит текст на экране.
// Справа — как фразу показывать: меняй только правую часть (в кавычках; кавычка внутри текста — \\").
// • Одна и та же фраза может стоять на нескольких экранах — замена подействует везде.
// • Многие фразы — кусочки предложений: программа подставляет между ними числа, имена, ссылки.
//   Поэтому сохраняй знаки по краям: скобку, двоеточие, тире (например, «Удалено в корзину:» перед числом).
// • Пустая правая часть или совпадающая с левой — фраза показывается как в коде.
// После правки пересобери приложение (./build.sh, см. docs/development.md). Проверка: python3 scripts/i18n_check.py
window.I18N_RU = {
'''

def write_ru(entries, ru, stale):
    lines = [RU_HEAD]
    last = None
    for sec, p in entries:
        if sec != last:
            lines.append(f'\n  // ----- {TITLES.get(sec, sec)} ({sec}{".html" if sec == "index" else ".js"}) -----\n')
            last = sec
        lines.append(f'  {json.dumps(p, ensure_ascii=False)}: {json.dumps(ru.get(p, p), ensure_ascii=False)},\n')
    if stale:
        lines.append('\n  // ----- Больше не встречаются в коде (можно удалить) -----\n')
        for p in stale:
            lines.append(f'  {json.dumps(p, ensure_ascii=False)}: {json.dumps(ru[p], ensure_ascii=False)},\n')
    lines.append('};\n')
    open(RU, 'w', encoding='utf-8').write(''.join(lines))

def main():
    entries = phrases()
    keys = [p for _, p in entries]
    en, ru = load_dict(EN), load_dict(RU)
    stale = [k for k in ru if k not in set(keys)]
    if '--sync' in sys.argv:
        added = [k for k in keys if k not in ru]
        if '--prune' in sys.argv:
            stale = []
        write_ru(entries, ru, stale)
        print(f'ru.js: добавлено фраз: {len(added)}, всего: {len(keys)}' + (f', больше не встречаются в коде: {len(stale)}' if stale else ''))
        return 0
    miss_en = [k for k in keys if k not in en]
    miss_ru = [k for k in keys if k not in ru]
    for k in miss_en: print('EN нет перевода:', k)
    for k in miss_ru: print('RU нет в ru.js:', k)
    for k in stale: print('RU больше нет в коде:', k)
    print(f'не переведено: {len(miss_en)} из {len(keys)}; в ru.js не хватает: {len(miss_ru)} (дописать: --sync); лишних в ru.js: {len(stale)}', file=sys.stderr)
    return 1 if miss_en or miss_ru else 0

if __name__ == '__main__':
    sys.exit(main())
