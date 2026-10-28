# Разработка

## Сборка
Нужен Go 1.22+ и (для подписи Mac-версии) `rcodesign` или `codesign`:
```
SERVER=<адрес> SERVER_CODE=<код> NOTES="что нового" ./build.sh 0.9.1   # → dist/
go test ./...
```
`SERVER` и `SERVER_CODE` (из `server.txt` после `deploy/server-up.sh`) вшиваются в приложение — друзьям при регистрации ничего не вписывать. Без них адрес и код спросят при регистрации.

Тег `vX.Y.Z` на GitHub собирает всё сам и выкладывает в Releases (`.github/workflows/release.yml`; адрес сервера — переменная `UZBEK_SERVER`, код — секрет `UZBEK_SERVER_CODE`).

## Как устроена папка

| Папка | Что там |
|---|---|
| `cmd/fruityhub` | приложение (окно, локальный сервер для интерфейса); `rsrc_windows_amd64.syso` — иконка и манифест для Windows |
| `cmd/uzbek-server` | сервер FruityHub для VPS |
| `cmd/flpinfo` | разбор .flp из командной строки (для отладки) |
| `cmd/devstack` | поддельный GitHub + сервер на localhost — попробовать приложение без настоящего GitHub |
| `internal/app` | вся логика приложения: проекты, git/LFS, плагины, передачи, обновления |
| `internal/hub` | сервер: пользователи, проекты, приглашения, передачи, журнал, админка |
| `internal/flp` | чтение и правка файлов FL Studio (.flp) |
| `internal/ui` | нативное окно (WebView2 на Windows, WKWebView на Mac) |
| `internal/fakegh` | поддельный GitHub для тестов |
| `web` | интерфейс (встраивается в приложение): `index.html` — каркас страницы, `static/js` — код по частям экрана, `static/css/app.css` — оформление, `static/i18n` — тексты (`ru.js`, `en.js`); фон и иконки |
| `assets` | иконки приложения и манифест Windows |
| `deploy` | скрипты: сервер на VPS, раскладка по Windows-компьютерам, диагностика |
| `docs` | документация |
| `dist` | результат сборки (в git не попадает) |

## Локальные файлы (в git не попадают)
* `server.txt` — адрес, код сервера и ключ администратора (пишет `deploy/server-up.sh`).
* `deploy/server.local` — куда ставить сервер по умолчанию (`root@1.2.3.4` или имя из `~/.ssh/config`).
* `deploy/machines.local` — Windows-компьютеры для `deploy/deploy-windows.sh`: `ip|логин|пароль`.

## Интерфейс и тексты
* Каркас страницы — `web/index.html`; код — `web/static/js/*.js`, по файлу на часть экрана (список проектов, экран проекта, вкладки, отправка, настройки, окно «Плагины»…). Это обычные скрипты с общими функциями; `boot.js` подключается последним и запускает интерфейс. Новый файл — добавь `<script>` в `index.html` до `boot.js`.
* Оформление — `web/static/css/app.css`, цвета и размеры — переменные в `:root`.
* Тексты пишутся в коде по-русски, и эти фразы служат ключами. `web/static/i18n/ru.js` — как каждую фразу показывать по-русски (меняется правая часть), `web/static/i18n/en.js` — английский перевод. Подмена идёт прямо на экране, кусками, поэтому фразы с числами и именами тоже работают.
* После правки кода с новыми фразами: `python3 scripts/i18n_check.py --sync` дописывает их в `ru.js`, `python3 scripts/i18n_check.py` показывает, чего нет в `en.js` и что в `ru.js` больше не используется.
* Форматирование кода — [Prettier](https://prettier.io) с настройками из `.prettierrc` (`npx prettier --write "web/static/**/*.{js,css}"`).
* Сообщения от «движка» (ошибки, строки в карточках задач, журнал) — в `internal/app/*.go`, по-русски, в словари не входят.

## Картинки
Фон по умолчанию — `web/bg.jpg`, иконка в интерфейсе — `web/icon.png`, иконка приложения — `assets/icon.*` (исходник рисунка — `assets/icon-source.svg`, квадратная иконка без фона — `assets/icon.svg` (вариант на тёмной плитке — `assets/icon-tile.svg`, `assets/icon-tile.ico`; в `rsrc_windows_amd64.syso` три набора: 2 — тот, что видит Проводник, 10 — плитка, 18 — без фона: `rsrc -manifest assets/app.manifest -ico assets/icon.ico,assets/icon-tile.ico,assets/icon.ico -arch amd64 -o cmd/fruityhub/rsrc_windows_amd64.syso`); из неё сделаны `icon.png`, `icon.ico`, `icon.icns` и `cmd/fruityhub/rsrc_windows_amd64.syso`).
