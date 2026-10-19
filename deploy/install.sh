#!/usr/bin/env bash
# Установка (и обновление) сервера «проги узбека» на VPS с Linux. Нужен root (или sudo).
# Обычно его запускает deploy-server.sh с твоего компьютера, но можно и руками:
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

# порт 443 нужен для HTTPS; если его уже занимает другой веб-сервер — работаем по HTTP на 47900
busy443=""
if command -v ss >/dev/null && ss -ltn '( sport = :443 )' | grep -q LISTEN; then busy443=1; fi
if [[ -z "$busy443" ]]; then
  ARGS="-data $DATA -domain $DOMAIN"
  ADDRESS="https://$DOMAIN"
else
  ARGS="-data $DATA -addr :47900"
  ADDRESS="http://$IP:47900"
  echo "⚠ Порт 443 занят другой программой — сервер будет работать без HTTPS на порту 47900."
fi

cat > /etc/systemd/system/uzbek-server.service <<UNIT
[Unit]
Description=прога узбека — сервер
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
  ufw allow 80/tcp >/dev/null; ufw allow 443/tcp >/dev/null; ufw allow 47900/tcp >/dev/null
fi
systemctl daemon-reload
systemctl enable --now uzbek-server >/dev/null 2>&1

for i in $(seq 1 30); do [[ -f $DATA/server-code ]] && break; sleep 0.5; done
CODE="$(tr -d '[:space:]' < $DATA/server-code 2>/dev/null || echo '?')"

echo "→ Проверяю, что сервер отвечает (первый раз HTTPS-сертификат получается 5–30 секунд)…"
ok=""
for i in $(seq 1 30); do
  if curl -fsS --max-time 5 "$ADDRESS/api/ping" 2>/dev/null | grep -q prog-uzbeka; then ok=1; break; fi
  sleep 2
done
if [[ -z "$ok" ]]; then
  echo "⚠ Сервер запущен, но снаружи пока не отвечает. Проверь, что в панели хостинга открыты порты 80 и 443,"
  echo "  и посмотри логи: journalctl -u uzbek-server -n 50"
fi

echo
echo "✓ Готово"
echo "UZBEK_ADDRESS=$ADDRESS"
echo "UZBEK_CODE=$CODE"
