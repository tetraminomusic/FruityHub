#!/usr/bin/env bash
# Собирает FruityHub под Windows и macOS, а сервер — под Linux (из любой ОС, нужен только Go 1.22+).
#   SERVER=https://1-2-3-4.sslip.io SERVER_CODE=a1b2c3d4 ./build.sh 0.6.0
# SERVER и SERVER_CODE (из server.txt после server-up.sh) вшиваются в приложение — друзьям ничего не вписывать.
# После сборки ./deploy/server-up.sh выложит эту версию на сервер — у всех появится «Доступно обновление».
# Можно не указывать: тогда адрес и код спросят при регистрации.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${1:-$(date +%Y.%m.%d)}"
SERVER="${SERVER:-}"
SERVER_CODE="${SERVER_CODE:-}"
LD="-s -w -X main.version=$VERSION -X flsync/internal/app.DefaultServer=$SERVER -X flsync/internal/app.DefaultServerCode=$SERVER_CODE"
NAME="FruityHub"
rm -rf dist && mkdir -p dist
echo "$VERSION" > dist/VERSION
[[ -n "${NOTES:-}" ]] && printf '%s\n' "$NOTES" > dist/NOTES
[[ -z "$SERVER" ]] && echo "⚠ SERVER не задан: адрес и код сервера нужно будет вписать при регистрации"
export CGO_ENABLED=0

echo "→ Windows"
# rsrc_windows_amd64.syso (иконка + манифест) подхватывается автоматически
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LD -H windowsgui" -o "dist/$NAME.exe" .

make_app() { # $1 arch, $2 zip name
  local app="dist/mac-$1/$NAME.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  GOOS=darwin GOARCH=$1 go build -trimpath -ldflags "$LD" -o "$app/Contents/MacOS/FruityHub" .
  cp assets/icon.icns "$app/Contents/Resources/icon.icns"
  cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>$NAME</string>
  <key>CFBundleDisplayName</key><string>$NAME</string>
  <key>CFBundleIdentifier</key><string>local.programma-uzbeka</string>
  <key>CFBundleExecutable</key><string>FruityHub</string>
  <key>CFBundleIconFile</key><string>icon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict></plist>
PLIST
  # Полная ad-hoc подпись бандла (привязывает Info.plist и ресурсы). Без неё новые
  # версии macOS могут убить приложение с «Code Signature Invalid».
  if command -v rcodesign >/dev/null; then
    rcodesign sign "$app" >/dev/null
  elif command -v codesign >/dev/null; then
    codesign --force --deep -s - "$app"
  else
    echo "⚠ нет rcodesign/codesign — $app подписан только линкером"
  fi
  (cd "dist/mac-$1" && zip -qry "../$2" "$NAME.app")
}
echo "→ macOS (Apple Silicon)"; make_app arm64 "FruityHub-mac-AppleSilicon.zip"
echo "→ macOS (Intel)";         make_app amd64 "FruityHub-mac-Intel.zip"
(cd dist && zip -qj "FruityHub-Windows.zip" "$NAME.exe")

echo "→ сервер (Linux)"
mkdir -p dist/server
for a in amd64 arm64; do
  GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" -o "dist/server/uzbek-server-linux-$a" ./cmd/uzbek-server
done
(cd dist && tar -czf uzbek-server.tar.gz server)
ls -lh dist dist/server
