#!/usr/bin/env bash
# Wrap the Ubuntu preview payload without rebuilding the GUI or the companion CLI.
set -euo pipefail
export LC_ALL=C
if [[ $# != 2 ]]; then echo "usage: $0 package.deb output.AppImage" >&2; exit 2; fi
build_dir=$(cd "$(dirname "$0")" && pwd)
stage=$(mktemp -d "$(dirname "$2")/appimage.XXXXXX"); trap 'rm -rf "$stage"' EXIT
appdir="$stage/AppDir"
dpkg-deb -x "$1" "$appdir"
app=org.filecloud.desktop
bin=usr/lib/filecloud-desktop
python3 "$build_dir/bundle-gtk.py" "$appdir"
install -m 0755 "$build_dir/AppRun" "$appdir/AppRun"
mkdir -p "$appdir/usr/bin"
ln -s ../lib/filecloud-desktop/filecloud-desktop "$appdir/usr/bin/filecloud-desktop"
# The .deb's absolute Exec path is not valid on a machine with only the AppImage.
entry="usr/share/applications/$app.desktop"
sed -i 's|^Exec=.*|Exec=filecloud-desktop|' "$appdir/$entry"
ln -s "$entry" "$appdir/$app.desktop"
ln -s "usr/share/icons/hicolor/1024x1024/apps/$app.png" "$appdir/$app.png"
ln -s "$app.png" "$appdir/.DirIcon"
cp "$build_dir/appimage-runtime.LICENSE" "$appdir/$bin/licenses/AppImage-runtime.txt"

# Immutable upstream asset and digest used by the pinned Wails v3.1.0 packager.
runtime=${APPIMAGE_RUNTIME:-$stage/runtime-x86_64}
if [[ -z ${APPIMAGE_RUNTIME:-} ]]; then
  curl --fail --location --silent --show-error --retry 3 --proto '=https' \
    -H 'Accept: application/octet-stream' \
    https://api.github.com/repos/AppImage/type2-runtime/releases/assets/657374641 -o "$runtime"
fi
printf '%s  %s\n' 617aa77314d57c8b8e4fae9c3f0b35af7b30d5529cb69e98f64f92510ed98a46 "$runtime" | sha256sum -c -
mksquashfs "$appdir" "$stage/payload" -noappend -all-root -comp gzip -no-progress >/dev/null
cat "$runtime" "$stage/payload" > "$stage/image"
chmod 755 "$stage/image"
mv -f "$stage/image" "$2"
