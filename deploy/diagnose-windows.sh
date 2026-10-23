#!/usr/bin/env bash
# Диагностика: почему не деплоится на Windows-ноутбук. Запускать с Mac.
#   ./deploy/diagnose-windows.sh                 # проверяет 192.168.1.111
#   ./deploy/diagnose-windows.sh 192.168.1.126
# Логин и пароль берутся из deploy-windows.sh. Ничего на ноутбуке не ломает: пишет и удаляет только свой тестовый файл.
HERE="$(cd "$(dirname "$0")" && pwd)"
HOST="${1:-192.168.1.111}"
LINE="$(grep "\"$HOST|" "$HERE/deploy-windows.sh" | head -1 | tr -d ' "')"
[[ -n "$LINE" ]] || { echo "Нет $HOST в списке MACHINES в deploy-windows.sh"; exit 1; }
IFS='|' read -r _ USER PASS <<<"$LINE"
ok() { echo "  ✓ $*"; }; bad() { echo "  ✗ $*"; }; info() { echo "  · $*"; }
urlenc() { local LC_ALL=C s="$1" out="" c i; for ((i=0;i<${#s};i++)); do c="${s:i:1}"; case "$c" in [a-zA-Z0-9._~-]) out+="$c";; *) out+="$(printf '%%%02X' "'$c")";; esac; done; printf '%s' "$out"; }
echo "Диагностика $HOST (логин $USER)"

echo "1. Сеть"
P="$(ping -c 5 -W 1000 "$HOST" 2>&1 | tail -2)"
LOSS="$(grep -o '[0-9.]*% packet loss' <<<"$P")"; AVG="$(grep -o '= [0-9.]*/[0-9.]*/' <<<"$P" | cut -d/ -f2)"
if [[ -z "$LOSS" ]]; then bad "ping не проходит (выключен, спит или другая сеть)"; else
  info "потери: $LOSS, средний пинг: ${AVG:-?} мс"
  [[ "${LOSS%%.*}" == "0" ]] && ok "пакеты не теряются" || bad "теряются пакеты — слабый Wi-Fi/сигнал: это и есть вероятная причина зависаний"
  [[ -n "$AVG" && "${AVG%%.*}" -gt 50 ]] && bad "пинг высокий — сеть нестабильна"
fi
nc -z -G 3 "$HOST" 445 >/dev/null 2>&1 && ok "порт 445 (файлы Windows) открыт" || bad "порт 445 закрыт: включи «Общий доступ к файлам и принтерам», сеть «Частная», проверь брандмауэр"

echo "2. Вход"
echo "  Имя и общие папки ноутбука:"
smbutil lookup "$HOST" 2>&1 | sed 's/^/     /' | head -5
timeout_v="$(smbutil view -N "//$(urlenc "$USER"):$(urlenc "$PASS")@$HOST" 2>&1)"; echo "$timeout_v" | sed 's/^/     /' | head -15
NB="$(smbutil lookup "$HOST" 2>/dev/null | grep -o 'name.*' | head -1)"
MNT="$(mktemp -d /tmp/uzbek-diag.XXXX)"
T0=$SECONDS; OKV=""
# пробуем разные способы указать пользователя (локальная учётка часто требует "ИМЯ_КОМПЬЮТЕРА;логин" или "WORKGROUP;логин")
for DOM in "" "WORKGROUP;" "$HOST;" ; do
  for SH in 'C$' 'Users'; do
    U="$(urlenc "$DOM$USER")"; U="${U//%3B/;}"
    if mount_smbfs -N "//$U:$(urlenc "$PASS")@$HOST/$SH" "$MNT" 2>/tmp/uzbek-diag-err; then
      OKV="$DOM|$SH"; ok "вошёл как '${DOM}${USER}' в шару '$SH' за $((SECONDS-T0)) с"; break 2
    else info "'${DOM}${USER}' → $SH: $(tr -d '\n' </tmp/uzbek-diag-err | sed 's/.*: //')"; fi
  done
done
if [[ -z "$OKV" ]]; then
  bad "ни один способ входа не сработал"
  echo "     «Input/output error» чаще всего значит: на ноутбуке не включена общая папка C\$ или SMB не согласовался."
  echo "     Проверь на ноутбуке (от администратора, в PowerShell): Get-SmbShare   — должен быть C\$"
  echo "     и: Get-SmbServerConfiguration | select EnableSMB2Protocol,RequireSecuritySignature"
  echo "     Если C\$ нет — выполни:  reg add HKLM\\SYSTEM\\CurrentControlSet\\Services\\LanmanServer\\Parameters /v AutoShareWks /t REG_DWORD /d 1 /f  и перезагрузи."
  echo "     Для локальной учётки ещё: reg add HKLM\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Policies\\System /v LocalAccountTokenFilterPolicy /t REG_DWORD /d 1 /f"
  rmdir "$MNT"; exit 1
fi
[[ "${OKV#*|}" == 'C$' ]] || { info "C\$ недоступна, но обычная общая папка Users открылась — значит, дело именно в админ-шаре C\$"; }
trap 'umount "$MNT" 2>/dev/null || diskutil unmount force "$MNT" >/dev/null 2>&1; rmdir "$MNT" 2>/dev/null' EXIT

echo "3. Папка и права"
if [[ "${OKV#*|}" == "Users" ]]; then D="$MNT/Public/Desktop"; else D="$MNT/Users/Public/Desktop"; fi
[[ -d "$D" ]] && ok "C:\\Users\\Public\\Desktop есть" || { bad "нет C:\\Users\\Public\\Desktop"; ls "$MNT/Users" 2>/dev/null | sed 's/^/     /'; exit 1; }
FREE="$(df -m "$MNT" | awk 'NR==2{print $4}')"; info "свободно на диске C: ${FREE:-?} МБ"
[[ -n "$FREE" && "$FREE" -lt 300 ]] && bad "мало места на диске"
for e in "$D"/*.exe*; do [[ -e "$e" ]] && info "на столе: $(basename "$e") ($(stat -f%z "$e") байт)"; done

echo "4. Запись и скорость"
T="$D/.uzbek-diag.tmp"
for MB in 1 8 32; do
  S=$SECONDS
  if timeout_out="$(dd if=/dev/zero of="$T" bs=1m count=$MB 2>&1)"; then
    el=$((SECONDS-S)); ok "записал ${MB} МБ за ${el} с$([[ $el -gt 0 ]] && echo " (~$((MB/el)) МБ/с)")"
  else bad "запись ${MB} МБ не удалась: $(tail -1 <<<"$timeout_out")"; echo "     Нет прав на запись в общий рабочий стол — зайди под другим администратором"; break; fi
done
rm -f "$T" 2>/dev/null
echo "5. Реальный файл"
EXE="$HERE/../dist/FruityHub.exe"
if [[ -f "$EXE" ]]; then
  SZ=$(( $(stat -f%z "$EXE") / 1048576 )); info "размер exe: ${SZ} МБ"
  ( cp "$EXE" "$T" ) & pid=$!; last=-1; stall=0; S=$SECONDS
  while kill -0 $pid 2>/dev/null; do
    sleep 2; sz="$(stat -f%z "$T" 2>/dev/null || echo 0)"; echo "     $((sz/1048576)) МБ"
    [[ "$sz" == "$last" ]] && stall=$((stall+2)) || stall=0; last="$sz"
    if (( stall >= 20 )); then kill -9 $pid 2>/dev/null; bad "застряло на $((sz/1048576)) МБ — зависает именно здесь (сеть/антивирус на ноутбуке)"; break; fi
  done
  wait $pid 2>/dev/null && ok "exe скопирован за $((SECONDS-S)) с"
  rm -f "$T" 2>/dev/null
else info "нет $EXE — пропускаю"; fi
echo "Готово. Пришли этот вывод целиком."
