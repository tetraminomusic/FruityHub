#!/usr/bin/env bash
# Раскладывает прогу (или любые другие exe) на рабочие столы Windows-компьютеров
# в локальной сети — через общий доступ к файлам Windows (SMB). Запускать с Mac или Linux.
#
#   ./deploy/deploy-windows.sh                         # компьютеры из deploy/hosts-windows.txt
#   ./deploy/deploy-windows.sh 192.168.1.126           # только этот компьютер
#   ./deploy/deploy-windows.sh -f setup.exe -f other.exe 192.168.1.111   # другие файлы
#   ./deploy/deploy-windows.sh -u Vasya                # на рабочий стол пользователя Vasya
#
# По умолчанию кладёт dist/прога узбека.exe на ОБЩИЙ рабочий стол (C:\Users\Public\Desktop):
# его видят все пользователи компьютера. Логин и пароль Windows спросят один раз
# (или задай WIN_USER и WIN_PASS); нужна учётная запись администратора на тех компьютерах.
# Если exe на том компьютере сейчас запущен — старая версия отодвигается, новая всё равно встаёт.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(dirname "$HERE")"

FILES=()
DESKTOP_USER=""
HOSTS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -f) FILES+=("$2"); shift 2 ;;
    -u) DESKTOP_USER="$2"; shift 2 ;;
    -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) HOSTS+=("$1"); shift ;;
  esac
done
[[ ${#FILES[@]} -gt 0 ]] || FILES=("$ROOT/dist/прога узбека.exe")
for f in "${FILES[@]}"; do [[ -f "$f" ]] || { echo "Нет файла: $f"; exit 1; }; done
if [[ ${#HOSTS[@]} -eq 0 ]]; then
  [[ -f "$HERE/hosts-windows.txt" ]] || { echo "Укажи IP или заполни deploy/hosts-windows.txt"; exit 1; }
  while read -r line; do
    line="${line%%#*}"; line="$(echo "$line" | tr -d '[:space:]')"
    [[ -n "$line" ]] && HOSTS+=("$line")
  done < "$HERE/hosts-windows.txt"
fi

OS="$(uname)"
if [[ "$OS" != "Darwin" ]] && ! command -v smbclient >/dev/null; then
  echo "Нужен smbclient: sudo apt install smbclient"; exit 1
fi

if [[ -z "${WIN_USER:-}" ]]; then
  read -r -p "Логин Windows с правами администратора (для учётки Microsoft — почта): " WIN_USER
fi
if [[ -z "${WIN_PASS:-}" ]]; then
  read -r -s -p "Пароль (не ПИН-код): " WIN_PASS; echo
fi

urlenc() { # для адреса smb:// (по байтам UTF-8 — логин может быть по-русски)
  local LC_ALL=C s="$1" out="" c i
  for ((i = 0; i < ${#s}; i++)); do
    c="${s:i:1}"
    case "$c" in [a-zA-Z0-9._~-]) out+="$c" ;; *) out+="$(printf '%%%02X' "'$c")" ;; esac
  done
  printf '%s' "$out"
}

port_open() { nc -z -w 3 "$1" 445 >/dev/null 2>&1; }

hint_access() {
  cat <<'TXT'
     Что проверить на том компьютере:
       • Параметры → Сеть → профиль «Частная сеть»; включён «Общий доступ к файлам и принтерам».
       • Учётка — администратор. Если это локальная учётка (не Microsoft), один раз выполни там
         в командной строке от имени администратора:
         reg add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System /v LocalAccountTokenFilterPolicy /t REG_DWORD /d 1 /f
TXT
}

declare -a REPORT
TS="$(date +%Y%m%d-%H%M%S)"

deploy_mac() { # $1 host
  local host="$1" mnt dest pub
  mnt="$(mktemp -d /tmp/uzbek-smb.XXXX)"
  local who; who="$(urlenc "$WIN_USER")"
  if ! mount_smbfs -N "//$who:$(urlenc "$WIN_PASS")@$host/C\$" "$mnt" 2>/tmp/uzbek-smb-err; then
    rmdir "$mnt"
    echo "   ✗ не пустил: $(tr -d '\n' </tmp/uzbek-smb-err)"
    hint_access
    return 1
  fi
  pub="$mnt/Users/Public"
  if [[ -n "$DESKTOP_USER" ]]; then
    dest="$mnt/Users/$DESKTOP_USER/Desktop"
    [[ -d "$mnt/Users/$DESKTOP_USER/OneDrive/Desktop" ]] && dest="$mnt/Users/$DESKTOP_USER/OneDrive/Desktop"
    [[ -d "$mnt/Users/$DESKTOP_USER/OneDrive/Рабочий стол" ]] && dest="$mnt/Users/$DESKTOP_USER/OneDrive/Рабочий стол"
  else
    dest="$pub/Desktop"
  fi
  local rc=0
  if [[ ! -d "$dest" ]]; then
    echo "   ✗ нет папки $(sed "s|$mnt|C:|" <<<"$dest")"; rc=1
  else
    for f in "${FILES[@]}"; do
      local name; name="$(basename "$f")"
      if ! cp "$f" "$dest/.$name.part"; then echo "   ✗ не смог скопировать $name"; rc=1; continue; fi
      if [[ -e "$dest/$name" ]]; then
        # запущенный exe нельзя перезаписать, но можно переименовать — убираем его с рабочего стола
        mv "$dest/$name" "$pub/$name.old-$TS" 2>/dev/null || mv "$dest/$name" "$dest/$name.old-$TS"
        rm -f "$pub/$name.old-"* 2>/dev/null || true
      fi
      mv "$dest/.$name.part" "$dest/$name"
      echo "   ✓ $name → $(sed "s|$mnt|C:|; s|/|\\\\|g" <<<"$dest")"
    done
  fi
  umount "$mnt" 2>/dev/null || diskutil unmount force "$mnt" >/dev/null 2>&1 || true
  rmdir "$mnt" 2>/dev/null || true
  return $rc
}

deploy_linux() { # $1 host
  local host="$1" dir
  if [[ -n "$DESKTOP_USER" ]]; then dir="Users/$DESKTOP_USER/Desktop"; else dir="Users/Public/Desktop"; fi
  local cmds="cd \"$dir\"; "
  for f in "${FILES[@]}"; do
    local name; name="$(basename "$f")"
    cmds+="put \"$f\" \".$name.part\"; rename \"$name\" \"../$name.old-$TS\"; rename \".$name.part\" \"$name\"; "
  done
  local out
  if out="$(printf '%s\n%s\n' "username=$WIN_USER" "password=$WIN_PASS" >/tmp/uzbek-smb-auth; \
            smbclient "//$host/C\$" -A /tmp/uzbek-smb-auth -c "$cmds" 2>&1)"; then
    rm -f /tmp/uzbek-smb-auth
    for f in "${FILES[@]}"; do echo "   ✓ $(basename "$f") → C:\\${dir//\//\\}"; done
  else
    rm -f /tmp/uzbek-smb-auth
    echo "   ✗ $(tail -1 <<<"$out")"
    hint_access
    return 1
  fi
}

for host in "${HOSTS[@]}"; do
  echo "→ $host"
  if ! port_open "$host"; then
    echo "   ✗ компьютер не отвечает (выключен, не в этой сети или закрыт общий доступ к файлам)"
    REPORT+=("✗ $host — не в сети")
    continue
  fi
  if [[ "$OS" == "Darwin" ]]; then deploy_mac "$host" && ok=1 || ok=""; else deploy_linux "$host" && ok=1 || ok=""; fi
  if [[ -n "$ok" ]]; then REPORT+=("✓ $host"); else REPORT+=("✗ $host — не получилось"); fi
done

echo
echo "Итого:"
for r in "${REPORT[@]}"; do echo "  $r"; done
[[ "${REPORT[*]}" != *"✗"* ]]
