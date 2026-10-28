#!/usr/bin/env bash
# Раскладывает FruityHub по Windows-ноутбукам в локальной сети — ПАРАЛЛЕЛЬНО на все сразу.
# Запускать с Mac. Работает через общий доступ к файлам Windows (SMB, админ-шара C$).
#
#   ./deploy/deploy-windows.sh                      # на все ноутбуки из списка ниже
#   ./deploy/deploy-windows.sh 192.168.1.126        # только на один
#   ./deploy/deploy-windows.sh -f другой.exe        # другой файл (можно несколько -f)
#
# FruityHub.exe (по умолчанию dist/FruityHub.exe) кладётся в установленный FruityHub каждого пользователя
# (C:\Users\<имя>\AppData\Local\Programs\FruityHub), а где его ещё не ставили — на ОБЩИЙ рабочий стол
# C:\Users\Public\Desktop: при первом запуске он покажет окно установки. Другие файлы (-f) — на общий рабочий стол.
# Каждый шаг с таймаутом и замером времени; подробный журнал каждого ноутбука — deploy/logs/<ip>.log.
# Копирование идёт под временным именем и ставится на место только после сверки размера;
# если старый exe занят запущенной прогой, он отодвигается.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(dirname "$HERE")"

# ---- компьютеры: ip|логин|пароль — в файле deploy/machines.local (он не попадает в git, пароли не утекут) ----
MACHINES_FILE="$HERE/machines.local"
if [[ ! -f "$MACHINES_FILE" ]]; then
  cat > "$MACHINES_FILE" <<'EOF'
# Windows-компьютеры для deploy-windows.sh: по одному в строке — ip|логин|пароль
# 192.168.1.126|user|пароль
EOF
  echo "Создал $MACHINES_FILE — впиши туда компьютеры (ip|логин|пароль) и запусти ещё раз."; exit 1
fi
MACHINES=()
while IFS= read -r line || [[ -n "$line" ]]; do
  [[ "$line" =~ ^[[:space:]]*# ]] && continue
  line="$(printf '%s' "$line" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e 's/\r$//')"
  [[ -n "$line" ]] && MACHINES+=("$line")
done < "$MACHINES_FILE"

FILES=(); ONLY=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -f) FILES+=("$2"); shift 2 ;;
    -h|--help) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) ONLY+=("$1"); shift ;;
  esac
done
[[ ${#FILES[@]} -gt 0 ]] || FILES=("$ROOT/dist/FruityHub.exe")
for f in "${FILES[@]}"; do [[ -f "$f" ]] || { echo "Нет файла: $f (сначала собери: ./build.sh)"; exit 1; }; done
[[ "$(uname)" == "Darwin" ]] || { echo "Скрипт для Mac (mount_smbfs)."; exit 1; }
mkdir -p "$HERE/logs"

urlenc() { # по байтам UTF-8
  local LC_ALL=C s="$1" out="" c i
  for ((i = 0; i < ${#s}; i++)); do
    c="${s:i:1}"
    case "$c" in [a-zA-Z0-9._~-]) out+="$c" ;; *) out+="$(printf '%%%02X' "'$c")" ;; esac
  done
  printf '%s' "$out"
}

# run_to N команда… — выполнить с ограничением N секунд (код 124 = не уложилась)
run_to() { local n="$1"; shift; "$@" >>"$LOG" 2>&1 & local p=$! i
  for ((i = 0; i < n * 2; i++)); do kill -0 $p 2>/dev/null || { wait $p; return $?; }; sleep 0.5; done
  kill -9 $p 2>/dev/null; wait $p 2>/dev/null; return 124; }

# ---- один ноутбук (выполняется в своём подпроцессе) ----
deploy_one() { # ip user pass
  local HOST="$1" USER="$2" PASS="$3"
  LOG="$HERE/logs/$HOST.log"; : > "$LOG"
  local T00=$SECONDS TAG="[$HOST]"; MNT=""
  say()  { local m="$TAG [$((SECONDS - T00))с] $*"; echo "$m"; echo "$m" >> "$LOG"; }
  cleanup() {
    [[ -n "$MNT" && -d "$MNT" ]] || return 0
    cd /tmp; sync
    run_to 8 umount "$MNT" || run_to 8 diskutil unmount force "$MNT" || say "⚠ диск не отключился сам (не страшно)"
    rmdir "$MNT" 2>/dev/null; MNT=""
  }
  fail() { say "✗ $*"; say "  подробности: $LOG"; cleanup; exit 1; }
  trap cleanup EXIT

  say "ping"
  local P; P="$(ping -c 4 -W 1000 "$HOST" 2>&1 | tail -2)"; echo "$P" >> "$LOG"
  grep -q '0.0% packet loss' <<<"$P" || say "⚠ потери пакетов: $(grep -o '[0-9.]*% packet loss' <<<"$P") — сеть нестабильна"
  nc -z -G 3 "$HOST" 445 >/dev/null 2>&1 || fail "ноутбук не отвечает (выключен, спит или закрыт общий доступ к файлам)"

  say "подключаю C\$"
  MNT="$(mktemp -d /tmp/uzbek-smb.XXXX)"
  run_to 25 mount_smbfs -o nobrowse -N "//$(urlenc "$USER"):$(urlenc "$PASS")@$HOST/C\$" "$MNT" \
    || { rmdir "$MNT"; MNT=""; fail "не подключился ($(tail -1 "$LOG" | tr -d '\n')). Проверь: сеть «Частная», общий доступ к файлам, права администратора; для локальной учётки — LocalAccountTokenFilterPolicy"; }
  local D="$MNT/Users/Public/Desktop"
  [[ -d "$D" ]] || fail "нет папки C:\\Users\\Public\\Desktop"

  put_file() { # файл папка-назначение
    local f="$1" D="$2" name size tmp where
    name="$(basename "$f")"; size=$(stat -f%z "$f"); tmp="$D/.$name.part"
    where="C:\\${D#"$MNT"/}"; where="${where//\//\\}"
    say "копирую $name ($((size / 1048576)) МБ) → $where"
    ( cp "$f" "$tmp" 2>>"$LOG" ) & local cp=$!
    local last=-1 stall=0 S=$SECONDS shown=-1 sz pct
    while kill -0 $cp 2>/dev/null; do
      sleep 2; sz=$(stat -f%z "$tmp" 2>/dev/null || echo 0); pct=$((sz * 100 / size))
      (( pct / 25 != shown )) && { shown=$((pct / 25)); say "  $pct%"; }
      [[ "$sz" == "$last" ]] && stall=$((stall + 2)) || stall=0; last=$sz
      if (( stall >= 30 )); then kill -9 $cp 2>/dev/null; rm -f "$tmp" 2>/dev/null
        fail "копирование встало на $pct% — сеть/Wi-Fi или антивирус на ноутбуке"; fi
    done
    wait $cp || fail "ошибка копирования (см. журнал)"
    [[ "$(stat -f%z "$tmp")" == "$size" ]] || fail "размер не совпал — файл повреждён при передаче"
    say "  скопировано за $((SECONDS - S)) с, ставлю на место"
    if [[ -e "$D/$name" ]] && ! run_to 10 mv -f "$tmp" "$D/$name"; then
      say "  старый $name занят (приложение запущено) — отодвигаю"
      run_to 10 mv "$D/$name" "$D/$name.old-$$" || fail "не смог отодвинуть старый файл"
      run_to 10 mv "$tmp" "$D/$name" || fail "не смог поставить новый файл"
      rm -f "$D/$name.old-"* 2>/dev/null
    elif [[ ! -e "$D/$name" ]]; then
      run_to 10 mv "$tmp" "$D/$name" || fail "не смог переименовать (если Защитник Windows держит файл — добавь FruityHub в исключения)"
    fi
    [[ "$(stat -f%z "$D/$name")" == "$size" ]] || fail "после замены размер не тот"
    say "✓ $name → $where"
  }

  # куда класть: FruityHub.exe — в установленный FruityHub каждого пользователя (%LOCALAPPDATA%\Programs\FruityHub),
  # а если его ещё не ставили — на общий рабочий стол (при первом запуске он покажет окно установки)
  local f name dest targets
  for f in "${FILES[@]}"; do
    name="$(basename "$f")"; targets=()
    if [[ "$name" == "FruityHub.exe" ]]; then
      for dest in "$MNT"/Users/*/AppData/Local/Programs/FruityHub; do
        [[ -f "$dest/FruityHub.exe" ]] && targets+=("$dest")
      done
    fi
    [[ ${#targets[@]} -gt 0 ]] || targets=("$D")
    for dest in "${targets[@]}"; do put_file "$f" "$dest"; done
  done
  # старое название приложения — убираем с рабочего стола, чтобы не было двух ярлыков
  for oldname in "прога узбека.exe"; do
    if [[ -e "$D/$oldname" ]]; then
      run_to 10 rm -f "$D/$oldname" || run_to 10 mv "$D/$oldname" "$D/.$oldname.old-$$" || true
      say "  убрал старый «$oldname»"
    fi
  done
  cleanup
  say "готово за $((SECONDS - T00)) с"
}

# ---- запуск на всех ноутбуках одновременно ----
PIDS=(); HOSTS=()
for m in "${MACHINES[@]}"; do
  IFS='|' read -r host user pass <<<"$m"
  if [[ ${#ONLY[@]} -gt 0 ]] && [[ " ${ONLY[*]} " != *" $host "* ]]; then continue; fi
  ( deploy_one "$host" "$user" "$pass" ) &
  PIDS+=($!); HOSTS+=("$host")
done
[[ ${#PIDS[@]} -gt 0 ]] || { echo "Нет такого ноутбука в списке"; exit 1; }

BAD=0
echo
for i in "${!PIDS[@]}"; do
  if wait "${PIDS[$i]}"; then R="✓ ${HOSTS[$i]}"; else R="✗ ${HOSTS[$i]} — не получилось (журнал: deploy/logs/${HOSTS[$i]}.log)"; BAD=1; fi
  REP+=("$R")
done
echo "Итого:"; for r in "${REP[@]}"; do echo "  $r"; done
exit $BAD
