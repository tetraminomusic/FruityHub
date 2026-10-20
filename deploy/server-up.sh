#!/usr/bin/env bash
# Поднимает (или обновляет) сервер «проги узбека» на VPS одной командой:
# берёт архив сервера, заливает его на VPS, разворачивает и запускает.
#
#   ./deploy/server-up.sh                              # на root@vpn_fi, архив dist/uzbek-server.tar.gz
#   ./deploy/server-up.sh root@vpn_fi uzbek.example.com  # со своим доменом
#   ./deploy/server-up.sh root@1.2.3.4                 # на другой сервер
#   ARCHIVE=~/Downloads/uzbek-server.tar.gz ./deploy/server-up.sh
#
# vpn_fi — имя сервера из ~/.ssh/config (или из DNS). Пароль, если ключа нет, спросят один раз.
# В конце печатает адрес и код сервера и сохраняет их в server.txt в корне проекта.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(dirname "$HERE")"
TARGET="${1:-root@vpn_fi}"
DOMAIN="${2:-}"
ARCHIVE="${ARCHIVE:-$ROOT/dist/uzbek-server.tar.gz}"

# --- архив сервера: готовый из сборки или собираем сами (нужен Go) ---
if [[ ! -f "$ARCHIVE" ]]; then
  if command -v go >/dev/null; then
    echo "→ Архива нет — собираю сервер…"
    TMP="$(mktemp -d)"
    mkdir -p "$TMP/server"
    for a in amd64 arm64; do
      (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" -o "$TMP/server/uzbek-server-linux-$a" ./cmd/uzbek-server)
    done
    cp "$HERE/install.sh" "$HERE/deploy-server.sh" "$TMP/server/"
    ARCHIVE="$TMP/uzbek-server.tar.gz"
    (cd "$TMP" && tar -czf uzbek-server.tar.gz server)
  else
    echo "Нет архива $ARCHIVE и нет Go, чтобы собрать его. Возьми uzbek-server.tar.gz из папки dist сборки."
    exit 1
  fi
fi
tar -tzf "$ARCHIVE" | grep -q '^server/install.sh$' || { echo "$ARCHIVE — не архив сервера проги (нет server/install.sh)"; exit 1; }

# --- одно SSH-соединение на весь скрипт: пароль спросят один раз ---
SOCK="${TMPDIR:-/tmp}/uzbek-ssh-$$"
SSHOPT=(-o ControlMaster=auto -o ControlPath="$SOCK" -o ControlPersist=120 -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)
cleanup() { ssh -o ControlPath="$SOCK" -O exit "$TARGET" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "→ Подключаюсь к $TARGET…"
if ! ssh "${SSHOPT[@]}" "$TARGET" true; then
  echo "Не получилось подключиться к $TARGET. Проверь, что 'ssh $TARGET' работает в Терминале."
  exit 1
fi
SUDO=""
[[ "$(ssh "${SSHOPT[@]}" "$TARGET" id -u)" == "0" ]] || SUDO="sudo"

echo "→ Заливаю $(basename "$ARCHIVE") ($(du -h "$ARCHIVE" | cut -f1))…"
scp -q "${SSHOPT[@]}" "$ARCHIVE" "$TARGET:/tmp/uzbek-server.tar.gz"

echo "→ Разворачиваю и запускаю…"
REMOTE="set -e; rm -rf /tmp/uzbek-up; mkdir -p /tmp/uzbek-up; tar -xzf /tmp/uzbek-server.tar.gz -C /tmp/uzbek-up; \
$SUDO bash /tmp/uzbek-up/server/install.sh $DOMAIN; rm -rf /tmp/uzbek-up /tmp/uzbek-server.tar.gz"
OUT="$(ssh -t "${SSHOPT[@]}" "$TARGET" "$REMOTE" | tee /dev/stderr | tr -d '\r')"
ADDRESS="$(grep '^UZBEK_ADDRESS=' <<<"$OUT" | tail -1 | cut -d= -f2-)"
CODE="$(grep '^UZBEK_CODE=' <<<"$OUT" | tail -1 | cut -d= -f2-)"
[[ -n "$ADDRESS" ]] || { echo "Что-то пошло не так — см. сообщения выше"; exit 1; }

echo "→ Проверяю с этого компьютера…"
if curl -fsS --max-time 10 "$ADDRESS/api/ping" 2>/dev/null | grep -q prog-uzbeka; then
  REACH="✓ отвечает"
else
  REACH="⚠ пока не отвечает отсюда — подожди минуту (сертификат) или открой порты в панели хостинга"
fi

printf 'Адрес сервера: %s\nКод сервера:   %s\n' "$ADDRESS" "$CODE" > "$ROOT/server.txt"
cat <<TXT

════════════════════════════════════════════
  Сервер на $TARGET запущен — $REACH
  Адрес: $ADDRESS
  Код:   $CODE
════════════════════════════════════════════
Впиши адрес и код в проге при регистрации (раздел «Сервер»).
Сохранено в $ROOT/server.txt
Обновить сервер — запусти этот скрипт ещё раз.
TXT
