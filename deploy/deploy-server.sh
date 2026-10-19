#!/usr/bin/env bash
# Разворачивает (или обновляет) сервер «проги узбека» на твоём VPS — одной командой с Mac/Linux.
#
#   ./deploy-server.sh root@1.2.3.4                     # домен не нужен: адрес будет https://1-2-3-4.sslip.io
#   ./deploy-server.sh root@1.2.3.4 uzbek.example.com   # со своим доменом
#   ./deploy-server.sh ubuntu@1.2.3.4                    # не root — тоже можно, если у пользователя есть sudo
#
# Пароль от VPS спросят один раз. В конце скрипт покажет адрес и код сервера и сохранит их в server.txt
# рядом с собой — их вписывают в проге при регистрации.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
TARGET="${1:-}"
DOMAIN="${2:-}"
if [[ -z "$TARGET" ]]; then
  sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
fi
for f in uzbek-server-linux-amd64 uzbek-server-linux-arm64 install.sh; do
  [[ -f "$HERE/$f" ]] || { echo "Нет файла $f рядом со скриптом (нужна папка server из сборки)"; exit 1; }
done

SOCK="${TMPDIR:-/tmp}/uzbek-ssh-$$"
SSH=(ssh -o ControlMaster=auto -o ControlPath="$SOCK" -o ControlPersist=120 -o StrictHostKeyChecking=accept-new)
cleanup() { ssh -o ControlPath="$SOCK" -O exit "$TARGET" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "→ Подключаюсь к $TARGET…"
"${SSH[@]}" "$TARGET" true
ARCH="$("${SSH[@]}" "$TARGET" uname -m)"
case "$ARCH" in
  x86_64|amd64) BIN=uzbek-server-linux-amd64 ;;
  aarch64|arm64) BIN=uzbek-server-linux-arm64 ;;
  *) echo "Неизвестная архитектура сервера: $ARCH"; exit 1 ;;
esac
SUDO=""
[[ "$("${SSH[@]}" "$TARGET" id -u)" == "0" ]] || SUDO="sudo"

echo "→ Копирую сервер ($ARCH)…"
"${SSH[@]}" "$TARGET" "mkdir -p /tmp/uzbek-install"
scp -q -o ControlPath="$SOCK" "$HERE/$BIN" "$HERE/install.sh" "$TARGET:/tmp/uzbek-install/"

echo "→ Устанавливаю…"
OUT="$("${SSH[@]}" -t "$TARGET" "$SUDO bash /tmp/uzbek-install/install.sh $DOMAIN; rm -rf /tmp/uzbek-install" | tee /dev/stderr | tr -d '\r')"
ADDRESS="$(grep '^UZBEK_ADDRESS=' <<<"$OUT" | tail -1 | cut -d= -f2-)"
CODE="$(grep '^UZBEK_CODE=' <<<"$OUT" | tail -1 | cut -d= -f2-)"
[[ -n "$ADDRESS" ]] || { echo "Что-то пошло не так — см. сообщения выше"; exit 1; }

printf 'Адрес сервера: %s\nКод сервера:   %s\n' "$ADDRESS" "$CODE" > "$HERE/server.txt"
cat <<TXT

════════════════════════════════════════════
  Сервер готов
  Адрес: $ADDRESS
  Код:   $CODE
════════════════════════════════════════════
Впиши их в проге при регистрации (раздел «Сервер») и отправь другу.
Сохранено в $HERE/server.txt
Чтобы вшить адрес и код в прогу (друзьям ничего не вписывать):
  SERVER=$ADDRESS SERVER_CODE=$CODE ./build.sh
Обновить сервер — запусти эту же команду ещё раз.
TXT
