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
| `web` | интерфейс — одна страница `index.html`, фон и иконка; встраивается в приложение |
| `assets` | иконки приложения и манифест Windows |
| `deploy` | скрипты: сервер на VPS, раскладка по Windows-компьютерам, диагностика |
| `docs` | документация |
| `dist` | результат сборки (в git не попадает) |

## Локальные файлы (в git не попадают)
* `server.txt` — адрес, код сервера и ключ администратора (пишет `deploy/server-up.sh`).
* `deploy/server.local` — куда ставить сервер по умолчанию (`root@1.2.3.4` или имя из `~/.ssh/config`).
* `deploy/machines.local` — Windows-компьютеры для `deploy/deploy-windows.sh`: `ip|логин|пароль`.

## Картинки
Фон по умолчанию — `web/bg.jpg`, иконка в интерфейсе — `web/icon.png`, иконка приложения — `assets/icon.*` (исходник рисунка — `assets/icon-source.svg`, квадратная иконка без фона — `assets/icon.svg` (вариант на тёмной плитке — `assets/icon-tile.svg`); из неё сделаны `icon.png`, `icon.ico`, `icon.icns` и `cmd/fruityhub/rsrc_windows_amd64.syso`).
