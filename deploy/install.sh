#!/usr/bin/env bash
# Установка сервера «проги узбека» на VPS (Ubuntu/Debian, нужен root).
#
#   bash install.sh                      # домен возьмётся сам: <IP>.sslip.io
#   bash install.sh uzbek.example.com    # свой домен (A-запись должна указывать на этот VPS)
#
# Повторный запуск обновляет сервер, данные (пользователи, проекты) сохраняются.
set -euo pipefail
cd "$(dirname "$0")"
[[ $EUID -eq 0 ]] || { echo "Запусти от root: sudo bash install.sh"; exit 1; }

case "$(uname -m)" in
  x86_64|amd64) BIN=uzbek-server-linux-amd64 ;;
  aarch64|arm64) BIN=uzbek-server-linux-arm64 ;;
  *) echo "Неизвестная архитектура $(uname -m)"; exit 1 ;;
esac

DOMAIN="${1:-}"
if [[ -z "$DOMAIN" ]]; then
  IP="$(curl -4 -fsS https://api.ipify.org || curl -4 -fsS https://ifconfig.me || true)"
  [[ -n "$IP" ]] || { echo "Не смог узнать внешний IP — укажи домен: bash install.sh мой.домен"; exit 1; }
  DOMAIN="${IP//./-}.sslip.io"
fi
echo "→ Домен: $DOMAIN"

id uzbek >/dev/null 2>&1 || useradd --system --home /var/lib/uzbek-server --shell /usr/sbin/nologin uzbek
install -d -o uzbek -g uzbek -m 700 /var/lib/uzbek-server
systemctl stop uzbek-server 2>/dev/null || true
install -m 755 "$BIN" /usr/local/bin/uzbek-server

cat > /etc/systemd/system/uzbek-server.service <<UNIT
[Unit]
Description=прога узбека — сервер
After=network-online.target
Wants=network-online.target

[Service]
User=uzbek
ExecStart=/usr/local/bin/uzbek-server -data /var/lib/uzbek-server -domain $DOMAIN
Restart=always
RestartSec=3
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/uzbek-server
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT

if command -v ufw >/dev/null && ufw status | grep -q active; then
  ufw allow 80/tcp >/dev/null; ufw allow 443/tcp >/dev/null
fi
systemctl daemon-reload
systemctl enable --now uzbek-server
sleep 2
systemctl --no-pager --lines=0 status uzbek-server || true

for i in $(seq 1 20); do [[ -f /var/lib/uzbek-server/admin-key ]] && break; sleep 0.5; done
KEY="$(cat /var/lib/uzbek-server/admin-key 2>/dev/null || echo '?')"
cat <<TXT

✓ Сервер запущен: https://$DOMAIN
  (сертификат HTTPS получится сам при первом заходе, это займёт несколько секунд)

Осталось один раз подключить его к GitHub — открой в браузере:
  https://$DOMAIN/setup?key=$KEY

Адрес сервера для проги: https://$DOMAIN
Логи: journalctl -u uzbek-server -f
TXT
