#!/usr/bin/env bash
# Вызывается server-up.sh --updates-only; аргументы — пакет и существующая папка обновлений.
set -euo pipefail
SOURCE="${1:?Нужна папка сборки}"
DEST="${2:?Нужна папка обновлений сервера}"
[[ -d "$DEST" && -f "$DEST/manifest.json" ]] || { echo "Нет существующего канала обновлений: $DEST"; exit 1; }
LOCK="$DEST/.publish-lock"
mkdir "$LOCK" 2>/dev/null || { echo "Публикация уже выполняется или остался .publish-lock после аварии"; exit 1; }
STAGE=""
cleanup() { [[ -z "$STAGE" ]] || rm -rf "$STAGE"; rmdir "$LOCK"; }
trap cleanup EXIT
VER="$(tr -d '\r\n' < "$SOURCE/VERSION")"
[[ "$VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "Некорректная версия"; exit 1; }

# Сначала проверяем весь комплект. Имена уже опубликованной версии не перезаписываем.
INPUTS=(FruityHub.exe FruityHub-mac-AppleSilicon.zip FruityHub-mac-Intel.zip)
PLATFORMS=(windows-amd64 darwin-arm64 darwin-amd64)
EXTS=(exe zip zip)
for i in 0 1 2; do
  f="$SOURCE/${INPUTS[$i]}"; name="${PLATFORMS[$i]}-$VER.${EXTS[$i]}"
  [[ -s "$f" ]] || { echo "Нет сборки ${INPUTS[$i]}"; exit 1; }
  if [[ -e "$DEST/$name" ]] && ! cmp -s "$f" "$DEST/$name"; then
    echo "Файл $name уже опубликован с другим содержимым; нужна новая версия"; exit 1
  fi
done

STAGE="$(mktemp -d "$DEST/.publish.XXXXXXXX")"
files=""
for i in 0 1 2; do
  pl="${PLATFORMS[$i]}"; name="$pl-$VER.${EXTS[$i]}"
  install -m 644 "$SOURCE/${INPUTS[$i]}" "$STAGE/$name"
  sum="$(sha256sum < "$STAGE/$name" | cut -d' ' -f1)"; size="$(stat -c %s "$STAGE/$name")"
  files="$files${files:+,}\"$pl\":{\"file\":\"$name\",\"size\":$size,\"sha256\":\"$sum\"}"
done
[[ "$(stat -c %s "$SOURCE/NOTES")" -le 16384 ]] || { echo "Описание длиннее 16 КиБ"; exit 1; }
notes="$(tr '\r\n\t' '   ' < "$SOURCE/NOTES")"
if LC_ALL=C printf '%s' "$notes" | LC_ALL=C grep -q '[[:cntrl:]]'; then
  echo "В описании есть недопустимые управляющие символы"; exit 1
fi
notes="${notes//\\/\\\\}"; notes="${notes//\"/\\\"}"
printf '{"version":"%s","notes":"%s","published":%s,"files":{%s}}\n' "$VER" "$notes" "$(date +%s)" "$files" > "$STAGE/manifest.json"
chmod 644 "$STAGE/manifest.json"
chown --reference="$DEST/manifest.json" "$STAGE/"*
for i in 0 1 2; do
  name="${PLATFORMS[$i]}-$VER.${EXTS[$i]}"
  mv -f "$STAGE/$name" "$DEST/$name"
done
# Старые файлы сохраняются для текущих скачиваний и отката. Манифест переключается последним.
cp -p "$DEST/manifest.json" "$STAGE/manifest.previous.json"
mv -f "$STAGE/manifest.previous.json" "$DEST/manifest.previous.json"
mv -f "$STAGE/manifest.json" "$DEST/manifest.json"
echo "✓ Версия $VER опубликована для всех клиентов; служба и настройки сервера сохранены"
