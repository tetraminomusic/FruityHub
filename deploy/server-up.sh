#!/usr/bin/env bash
# Сервер FruityHub на VPS — одной командой с Mac.
#
#   ./deploy/server-up.sh                                # поднять / обновить сервер на root@vpn_fi
#   ./deploy/server-up.sh root@1.2.3.4 [домен]           # на другом сервере (домен — необязательно)
#   ./deploy/server-up.sh backup [root@vpn_fi]           # скачать все данные сервера в server-backups/
#   ./deploy/server-up.sh restore ФАЙЛ root@новый [домен] # переезд: поднять сервер и восстановить данные
#
# При обычном запуске скрипт ещё и выкладывает на сервер новые версии приложения из dist/ (после ./build.sh):
# у всех появится плашка «Доступно обновление». Чтобы не выкладывать — добавь --no-update.
#
# vpn_fi — имя сервера из ~/.ssh/config (или из DNS). Пароль, если ключа нет, спросят один раз.
# В конце печатает адрес, код сервера и ключ администратора и сохраняет их в server.txt.
# Все данные сервера — в одной папке /var/lib/uzbek-server (db.json, server-code, admin-key…);
# перенос = backup на старом + restore на новом. Установщик для VPS зашит в конец этого файла.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(dirname "$HERE")"
ARCHIVE="${ARCHIVE:-$ROOT/dist/uzbek-server.tar.gz}"
DATA=/var/lib/uzbek-server

MODE=up; RESTORE=""; PUBLISH=1; ARGS=()
for a in "$@"; do
  case "$a" in
    --no-update) PUBLISH=0 ;;
    -h|--help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) ARGS+=("$a") ;;
  esac
done
set -- "${ARGS[@]+"${ARGS[@]}"}"
case "${1:-}" in
  backup) MODE=backup; shift ;;
  restore) MODE=restore; RESTORE="${2:-}"; shift 2 || true
    [[ -f "$RESTORE" ]] || { echo "Укажи файл бэкапа: ./deploy/server-up.sh restore server-backups/….tar.gz root@новый-сервер"; exit 1; } ;;
esac
TARGET="${1:-root@vpn_fi}"
DOMAIN="${2:-}"

WORK="$(mktemp -d)"
SOCK="${TMPDIR:-/tmp}/uzbek-ssh-$$"
SSHOPT=(-o ControlMaster=auto -o ControlPath="$SOCK" -o ControlPersist=120 -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)
cleanup() { ssh -o ControlPath="$SOCK" -O exit "$TARGET" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

connect() {
  echo "→ Подключаюсь к ${TARGET}…"
  if ! ssh "${SSHOPT[@]}" "$TARGET" true; then
    echo "Не получилось подключиться к $TARGET. Проверь, что 'ssh $TARGET' работает в Терминале."
    exit 1
  fi
  SUDO=""
  [[ "$(ssh "${SSHOPT[@]}" "$TARGET" id -u)" == "0" ]] || SUDO="sudo"
}

# ---------- backup: все данные сервера одним архивом ----------
if [[ "$MODE" == backup ]]; then
  connect
  mkdir -p "$ROOT/server-backups"
  OUTF="$ROOT/server-backups/fruityhub-server-$(date +%Y%m%d-%H%M%S).tar.gz"
  echo "→ Скачиваю данные сервера…"
  ssh "${SSHOPT[@]}" "$TARGET" "$SUDO tar -czf - -C $DATA --exclude=./updates --exclude=./certs ." > "$OUTF"
  tar -tzf "$OUTF" | grep -q 'db.json' || { echo "В архиве нет db.json — на сервере нет данных?"; rm -f "$OUTF"; exit 1; }
  echo "✓ Бэкап: $OUTF ($(du -h "$OUTF" | cut -f1))"
  echo "  Перенос на другой сервер: ./deploy/server-up.sh restore \"$OUTF\" root@НОВЫЙ"
  exit 0
fi

# ---------- бинарники сервера: из готового архива или собираем сами (нужен Go) ----------
mkdir -p "$WORK/server"
if [[ -f "$ARCHIVE" ]] && tar -tzf "$ARCHIVE" | grep -q 'uzbek-server-linux-amd64$'; then
  TMPX="$WORK/x"; mkdir -p "$TMPX"; tar -xzf "$ARCHIVE" -C "$TMPX"
  find "$TMPX" -name 'uzbek-server-linux-*' -exec cp {} "$WORK/server/" \;
elif command -v go >/dev/null && [[ -d "$ROOT/cmd/uzbek-server" ]]; then
  echo "→ Архива нет — собираю сервер…"
  for a in amd64 arm64; do
    (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" -o "$WORK/server/uzbek-server-linux-$a" ./cmd/uzbek-server)
  done
else
  echo "Нет архива $ARCHIVE и нет Go, чтобы собрать сервер. Возьми uzbek-server.tar.gz из папки dist сборки."
  exit 1
fi
ls "$WORK"/server/uzbek-server-linux-* >/dev/null 2>&1 || { echo "В архиве нет бинарников сервера"; exit 1; }

# ---------- новые версии приложения из dist (их раздаёт сервер) ----------
if [[ "$PUBLISH" == 1 && -f "$ROOT/dist/VERSION" ]]; then
  VER="$(tr -d '[:space:]' < "$ROOT/dist/VERSION")"
  mkdir -p "$WORK/server/updates"
  [[ -f "$ROOT/dist/FruityHub.exe" ]] && cp "$ROOT/dist/FruityHub.exe" "$WORK/server/updates/windows-amd64.exe"
  [[ -f "$ROOT/dist/FruityHub-mac-AppleSilicon.zip" ]] && cp "$ROOT/dist/FruityHub-mac-AppleSilicon.zip" "$WORK/server/updates/darwin-arm64.zip"
  [[ -f "$ROOT/dist/FruityHub-mac-Intel.zip" ]] && cp "$ROOT/dist/FruityHub-mac-Intel.zip" "$WORK/server/updates/darwin-amd64.zip"
  echo "$VER" > "$WORK/server/updates/VERSION"
  [[ -f "$ROOT/dist/NOTES" ]] && cp "$ROOT/dist/NOTES" "$WORK/server/updates/NOTES"
  echo "→ Выложу приложение версии $VER ($(ls "$WORK/server/updates" | grep -cv -e VERSION -e NOTES) сборки)"
fi

# ---------- бэкап для переезда ----------
RFLAG=""
if [[ "$MODE" == restore ]]; then
  cp "$RESTORE" "$WORK/server/restore.$( [[ "$RESTORE" == *.json ]] && echo json || echo tar.gz )"
  RFLAG="RESTORE=1"
  echo "→ Восстановлю данные из $(basename "$RESTORE")"
fi

# ---------- установщик для VPS: достаём из конца этого файла ----------
sed -n '/^#__INSTALL_SH__$/,$p' "$0" | tail -n +2 > "$WORK/server/install.sh"
(cd "$WORK" && tar -czf up.tar.gz server)

connect
echo "→ Заливаю ($(du -h "$WORK/up.tar.gz" | cut -f1))…"
scp -q "${SSHOPT[@]}" "$WORK/up.tar.gz" "$TARGET:/tmp/uzbek-up.tar.gz"

echo "→ Разворачиваю и запускаю…"
REMOTE="set -e; rm -rf /tmp/uzbek-up; mkdir -p /tmp/uzbek-up; tar -xzf /tmp/uzbek-up.tar.gz -C /tmp/uzbek-up; \
$SUDO env $RFLAG bash /tmp/uzbek-up/server/install.sh $DOMAIN; rm -rf /tmp/uzbek-up /tmp/uzbek-up.tar.gz"
OUT="$(ssh -t "${SSHOPT[@]}" "$TARGET" "$REMOTE" | tee /dev/stderr | tr -d '\r')"
ADDRESS="$(grep '^UZBEK_ADDRESS=' <<<"$OUT" | tail -1 | cut -d= -f2-)"
CODE="$(grep '^UZBEK_CODE=' <<<"$OUT" | tail -1 | cut -d= -f2-)"
ADMIN="$(grep '^UZBEK_ADMIN=' <<<"$OUT" | tail -1 | cut -d= -f2-)"
[[ -n "$ADDRESS" ]] || { echo "Что-то пошло не так — см. сообщения выше"; exit 1; }

echo "→ Проверяю с этого компьютера…"
if curl -fsS --max-time 10 "$ADDRESS/api/ping" 2>/dev/null | grep -q prog-uzbeka; then
  REACH="✓ отвечает"
else
  REACH="⚠ пока не отвечает отсюда — подожди минуту (сертификат) или открой порты в панели хостинга"
fi

printf 'Адрес сервера: %s\nКод сервера:   %s\nКлюч администратора: %s\n' "$ADDRESS" "$CODE" "$ADMIN" > "$ROOT/server.txt"
cat <<TXT

════════════════════════════════════════════
  Сервер на $TARGET запущен — $REACH
  Адрес: $ADDRESS
  Код:   $CODE
  Ключ администратора: $ADMIN
════════════════════════════════════════════
Адрес и код вписываются в FruityHub при регистрации (если не вшиты в сборку).
Ключ администратора — для режима разработчика: Настройки → «Режим разработчика» → Разработчик → Сервер.
Сохранено в $ROOT/server.txt (не отправляй этот файл никому).
Обновить сервер и выложить новую версию приложения — запусти этот скрипт ещё раз после ./build.sh.
TXT
[[ "$MODE" == restore ]] && echo "Данные восстановлены. На СТАРОМ сервере в админке укажи новый адрес в «Сервер переехал» — приложения переключатся сами."
exit 0

#__INSTALL_SH__
#!/usr/bin/env bash
# Установка (и обновление) сервера FruityHub на VPS с Linux. Нужен root (или sudo).
# Обычно его запускает server-up.sh с твоего компьютера, но можно и руками:
#
#   bash install.sh                      # адрес будет https://<IP>.sslip.io — домен не нужен
#   bash install.sh uzbek.example.com    # свой домен (A-запись должна указывать на этот VPS)
#
# Повторный запуск обновляет сервер; пользователи, проекты и код сервера сохраняются.
set -euo pipefail
cd "$(dirname "$0")"
[[ $EUID -eq 0 ]] || { echo "Нужны права root: sudo bash install.sh"; exit 1; }

case "$(uname -m)" in
  x86_64|amd64) BIN=uzbek-server-linux-amd64 ;;
  aarch64|arm64) BIN=uzbek-server-linux-arm64 ;;
  *) echo "Неизвестная архитектура $(uname -m)"; exit 1 ;;
esac
[[ -f "$BIN" ]] || { echo "Нет файла $BIN рядом со скриптом"; exit 1; }

IP="$(curl -4 -fsS --max-time 8 https://api.ipify.org 2>/dev/null || curl -4 -fsS --max-time 8 https://ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')"
DOMAIN="${1:-}"
[[ -z "$DOMAIN" && -f /etc/uzbek-server.domain ]] && DOMAIN="$(cat /etc/uzbek-server.domain)"
[[ -z "$DOMAIN" ]] && DOMAIN="${IP//./-}.sslip.io"
echo "$DOMAIN" > /etc/uzbek-server.domain

DATA=/var/lib/uzbek-server
id uzbek >/dev/null 2>&1 || useradd --system --home "$DATA" --shell /usr/sbin/nologin uzbek
install -d -o uzbek -g uzbek -m 700 "$DATA"
systemctl stop uzbek-server 2>/dev/null || true
install -m 755 "$BIN" /usr/local/bin/uzbek-server

# переезд: восстановить данные из бэкапа (архив папки данных или .json из админки)
if [[ -n "${RESTORE:-}" ]]; then
  if [[ -f restore.tar.gz ]]; then
    [[ -f "$DATA/db.json" ]] && cp "$DATA/db.json" "$DATA/db.json.before-restore"
    tar -xzf restore.tar.gz -C "$DATA"
  elif [[ -f restore.json ]]; then
    /usr/local/bin/uzbek-server -data "$DATA" -restore restore.json
  fi
  echo "→ Данные восстановлены из бэкапа"
fi

# новые версии приложения: файлы + manifest.json (сервер раздаёт их приложениям)
if [[ -f updates/VERSION ]]; then
  VER="$(tr -d '[:space:]' < updates/VERSION)"
  install -d -m 755 "$DATA/updates"
  find "$DATA/updates" -maxdepth 1 -type f ! -name manifest.json -delete
  files=""
  for f in updates/*-*.*; do
    [[ -f "$f" ]] || continue
    pl="$(basename "$f")"; pl="${pl%%.*}"; ext="${f##*.}"
    name="$pl-$VER.$ext"
    install -m 644 "$f" "$DATA/updates/$name"
    sum="$(sha256sum "$f" | cut -d' ' -f1)"; size="$(stat -c %s "$f")"
    files="$files${files:+,}\"$pl\":{\"file\":\"$name\",\"size\":$size,\"sha256\":\"$sum\"}"
  done
  notes=""; [[ -f updates/NOTES ]] && notes="$(head -c 2000 updates/NOTES | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')"
  printf '{"version":"%s","notes":"%s","published":%s,"files":{%s}}\n' "$VER" "$notes" "$(date +%s)" "$files" > "$DATA/updates/manifest.json"
  echo "→ Приложение версии $VER выложено для обновления"
fi
chown -R uzbek:uzbek "$DATA"

# Порт 443 нужен для HTTPS. На VPN-сервере он обычно занят самим VPN — тогда HTTPS на порту 47943
# (сертификат получаем через порт 80). Заняты оба — работаем по HTTP на 47900.
busy() { command -v ss >/dev/null && ss -ltn "( sport = :$1 )" | grep -q LISTEN; }
PORTS="80 443"
if ! busy 443; then
  ARGS="-data $DATA -domain $DOMAIN"
  ADDRESS="https://$DOMAIN"
elif ! busy 80; then
  ARGS="-data $DATA -domain $DOMAIN -https-port 47943"
  ADDRESS="https://$DOMAIN:47943"
  PORTS="80 47943"
  echo "→ Порт 443 занят (наверное, VPN) — сервер будет на HTTPS-порту 47943."
else
  ARGS="-data $DATA -addr :47900"
  ADDRESS="http://$IP:47900"
  PORTS="47900"
  echo "⚠ Порты 80 и 443 заняты другими программами — сервер будет работать без HTTPS на порту 47900."
fi

cat > /etc/systemd/system/uzbek-server.service <<UNIT
[Unit]
Description=FruityHub — сервер
After=network-online.target
Wants=network-online.target

[Service]
User=uzbek
ExecStart=/usr/local/bin/uzbek-server $ARGS
Restart=always
RestartSec=3
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=$DATA
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT

if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
  for p in $PORTS; do ufw allow "$p/tcp" >/dev/null; done
fi
if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
  for p in $PORTS; do firewall-cmd --permanent --add-port="$p/tcp" >/dev/null; done
  firewall-cmd --reload >/dev/null
fi
# голый iptables с политикой DROP (частая настройка на VPN-серверах)
if ! command -v ufw >/dev/null && ! command -v firewall-cmd >/dev/null && command -v iptables >/dev/null \
   && iptables -S INPUT 2>/dev/null | grep -q '^-P INPUT DROP'; then
  for p in $PORTS; do iptables -C INPUT -p tcp --dport "$p" -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport "$p" -j ACCEPT; done
  command -v netfilter-persistent >/dev/null && netfilter-persistent save >/dev/null 2>&1 || true
fi
systemctl daemon-reload
systemctl enable --now uzbek-server >/dev/null 2>&1

for i in $(seq 1 30); do [[ -f $DATA/server-code ]] && break; sleep 0.5; done
CODE="$(tr -d '[:space:]' < $DATA/server-code 2>/dev/null || echo '?')"
for i in $(seq 1 30); do [[ -f $DATA/admin-key ]] && break; sleep 0.5; done
ADMIN="$(tr -d '[:space:]' < $DATA/admin-key 2>/dev/null || echo '?')"

echo "→ Проверяю, что сервер отвечает (первый раз HTTPS-сертификат получается 5–30 секунд)…"
ok=""
for i in $(seq 1 30); do
  if curl -fsS --max-time 5 "$ADDRESS/api/ping" 2>/dev/null | grep -q prog-uzbeka; then ok=1; break; fi
  sleep 2
done
if [[ -z "$ok" ]]; then
  echo "⚠ Сервер запущен, но снаружи пока не отвечает. Проверь, что в панели хостинга открыты порты: $PORTS,"
  echo "  и посмотри логи: journalctl -u uzbek-server -n 50"
fi

echo
echo "✓ Готово"
echo "UZBEK_ADDRESS=$ADDRESS"
echo "UZBEK_CODE=$CODE"
echo "UZBEK_ADMIN=$ADMIN"
