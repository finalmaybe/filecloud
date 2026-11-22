#!/usr/bin/env bash
# Build on the target OS. Windows uses Git Bash and NSIS.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
os=$(go env GOHOSTOS); arch=$(go env GOHOSTARCH)
case "$os/$arch" in linux/amd64|darwin/arm64|windows/amd64);; *) echo 'Unsupported native target' >&2; exit 1;; esac
version=${VERSION:-dev}; commit=${COMMIT:-$(git rev-parse HEAD)}; date=${BUILD_DATE:-$(git show -s --format=%cI HEAD)}
prefix=filecloud-desktop
if [[ $version != dev ]]; then
  [[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Expected VERSION=vMAJOR.MINOR.PATCH or dev' >&2; exit 1; }
  prefix+="-$version"
fi
out="$root/desktop/out"; mkdir -p "$out"; stage=$(mktemp -d "$out/stage.XXXXXX"); trap 'rm -rf "$stage"' EXIT
if [[ ${FRONTEND_PREBUILT:-0} == 1 ]]; then test -s desktop/frontend/dist/app.js; else (cd desktop/frontend && npm ci --ignore-scripts && npm run build); fi
case "$os" in
 linux) bin="$stage/pkg/usr/lib/filecloud-desktop";;
 darwin) app="$stage/Filecloud.app"; bin="$app/Contents/MacOS";;
 windows) bin="$stage/bin";;
esac
mkdir -p "$bin"; ext=; gui=''; if [[ $os == windows ]]; then ext=.exe; gui='-H windowsgui'; fi
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main._version=$version -X main._commit=$commit -X main._buildDate=$date" -o "$bin/filecloud-client$ext" ./cmd/filecloud-client
(cd desktop && CGO_ENABLED=$([[ $os == windows ]] && echo 0 || echo 1) go build -trimpath -tags production -ldflags "-s -w $gui -X main.version=$version -X main.commit=$commit" -o "$bin/filecloud-desktop$ext" .)
legal="$bin/licenses"; [[ $os != darwin ]] || legal="$app/Contents/Resources/licenses"
go run ./tools/notices --desktop --target "$os/$arch" --output "$legal"
cp LICENSE "$legal/Filecloud.txt"
case "$os" in
 linux)
  pkg="$stage/pkg"; data="$pkg/usr/share"; mkdir -p "$pkg/DEBIAN" "$data/applications" "$data/icons/hicolor/1024x1024/apps"; cp desktop/build/org.filecloud.desktop.desktop "$data/applications/"; cp assets/brand/app-icon-1024.png "$data/icons/hicolor/1024x1024/apps/org.filecloud.desktop.png"
  debversion=${version#v}; [[ $debversion != dev ]] || debversion='0~dev'; printf 'Package: filecloud-desktop\nVersion: %s\nArchitecture: amd64\nMaintainer: Filecloud\nDepends: libgtk-4-1, libwebkitgtk-6.0-4\nDescription: Filecloud desktop sync client\n' "$debversion" > "$pkg/DEBIAN/control"
  deb="$out/$prefix-linux-amd64.deb"
  dpkg-deb --build --root-owner-group "$pkg" "$deb"
  bash desktop/build/appimage.sh "$deb" "$out/$prefix-linux-amd64.AppImage";;
 darwin)
  cp desktop/build/Info.plist "$app/Contents/Info.plist"; if [[ $version != dev ]]; then /usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString ${version#v}" "$app/Contents/Info.plist"; fi; mkdir -p "$app/Contents/Resources" "$stage/Filecloud.iconset"; for size in 16 32 128 256 512; do for factor in 1 2; do suffix=''; [[ $factor == 1 ]] || suffix='@2x'; s=$((size*factor)); sips -z "$s" "$s" assets/brand/app-icon-1024.png --out "$stage/Filecloud.iconset/icon_${size}x${size}${suffix}.png" >/dev/null; done; done; iconutil -c icns "$stage/Filecloud.iconset" -o "$app/Contents/Resources/Filecloud.icns"; signing=${MAC_SIGN_IDENTITY:--}; codesign --force --options runtime --sign "$signing" "$bin/filecloud-client"; codesign --force --options runtime --sign "$signing" "$app"; hdiutil create -ov -volname Filecloud -srcfolder "$app" "$out/$prefix-darwin-arm64.dmg";;
 windows)
  export FILECLOUD_STAGE=$(cygpath -aw "$bin") FILECLOUD_SETUP=$(cygpath -aw "$out/$prefix-windows-amd64.exe"); cp assets/brand/favicon.ico "$bin/filecloud.ico"; makensis desktop/build/installer.nsi;;
esac
