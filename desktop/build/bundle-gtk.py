"""Stage the Ubuntu 24.04 amd64 desktop runtime from installed distro packages."""
from pathlib import Path
import re
import shutil
import subprocess
import sys


SYSTEM_LIBS = {
    # Keep the loader and glibc together; the host's GPU drivers also need its C++ runtime.
    'libc.so.6', 'libm.so.6', 'libmvec.so.1', 'libdl.so.2', 'libpthread.so.0',
    'librt.so.1', 'libresolv.so.2', 'libutil.so.1', 'libgcc_s.so.1', 'libstdc++.so.6',
    'libEGL.so.1', 'libGL.so.1', 'libGLX.so.0', 'libGLdispatch.so.0',
    'libgbm.so.1', 'libdrm.so.2', 'libwayland-client.so.0', 'libwayland-server.so.0',
    'libwayland-egl.so.1', 'libwayland-cursor.so.0', 'libfontconfig.so.1', 'libfreetype.so.6',
}
MULTIARCH = Path('/usr/lib/x86_64-linux-gnu')


def bundle(appdir):
    lib = appdir / 'usr/lib'
    share = appdir / 'usr/share'
    webkit = MULTIARCH / 'webkitgtk-6.0'
    gui = lib / 'filecloud-desktop/filecloud-desktop'
    inputs = [gui]
    sources = set()

    def copy_elf(source, target):
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target)
        sources.add(source.resolve())

    helpers = [webkit / 'WebKitWebProcess', webkit / 'WebKitNetworkProcess',
               webkit / 'injected-bundle/libwebkitgtkinjectedbundle.so']
    if (webkit / 'WebKitGPUProcess').exists():
        helpers.append(webkit / 'WebKitGPUProcess')
    for source in helpers:
        copy_elf(source, lib / source.relative_to(MULTIARCH))
    inputs.extend(helpers)

    # TLS, settings and image loaders are opened by GLib at runtime, not declared in DT_NEEDED.
    gio = MULTIARCH / 'gio/modules'
    for name in ['libgiognutls.so', 'libdconfsettings.so']:
        copy_elf(gio / name, lib / 'gio/modules' / name)
        inputs.append(gio / name)
    subprocess.run([MULTIARCH / 'glib-2.0/gio-querymodules', lib / 'gio/modules'], check=True)
    pixbuf = MULTIARCH / 'gdk-pixbuf-2.0'
    # PNG is built in; the desktop theme also uses SVG. Do not collect unrelated
    # codecs just because the build runner happens to have them installed.
    svg = pixbuf / '2.10.0/loaders/libpixbufloader-svg.so'
    copy_elf(svg, lib / svg.name)
    inputs.append(svg)
    cache = subprocess.check_output([pixbuf / 'gdk-pixbuf-query-loaders', svg], text=True)
    (lib / 'loaders.cache').write_text(re.sub(r'^"/[^"\n]+/([^/"\n]+\.so)"$', r'"\1"', cache, flags=re.M))

    gst = MULTIARCH / 'gstreamer-1.0'
    for name in ['libgstapp.so', 'libgstcoreelements.so']:
        copy_elf(gst / name, lib / 'gstreamer-1.0' / name)
        inputs.append(gst / name)
    scanner = MULTIARCH / 'gstreamer1.0/gstreamer-1.0/gst-plugin-scanner'
    copy_elf(scanner, lib / 'gstreamer-1.0/gst-plugin-scanner')
    inputs.append(scanner)

    for name, source in libraries(inputs).items():
        copy_elf(source, lib / name)
    relocate_helpers(lib / 'libwebkitgtk-6.0.so.4', webkit)
    # RPATH is inherited by indirect dependencies, but not by a separately executed
    # host program (e.g. xdg-open running the host's newer gio or KDE file manager).
    executables = [gui, lib / 'gstreamer-1.0/gst-plugin-scanner',
                   *(lib / webkit.name).glob('WebKit*Process')]
    for executable in executables:
        subprocess.run(['patchelf', '--force-rpath', '--set-rpath', '$ORIGIN/..', executable], check=True)

    schemas = share / 'glib-2.0/schemas'
    schemas.mkdir(parents=True)
    schema_packages = ['gsettings-desktop-schemas', 'libgtk-4-common']
    for package in schema_packages:
        for path in subprocess.check_output(['dpkg-query', '-L', package], text=True).splitlines():
            source = Path(path)
            if source.parent == Path('/usr/share/glib-2.0/schemas') and source.is_file():
                shutil.copy2(source, schemas)
    subprocess.run([MULTIARCH / 'glib-2.0/glib-compile-schemas', '--strict', schemas], check=True)
    shutil.copytree('/usr/share/icons/Adwaita', share / 'icons/Adwaita', symlinks=True)
    notices(lib / 'filecloud-desktop/licenses/linux-runtime', sources, [*schema_packages, 'adwaita-icon-theme'])


def libraries(inputs):
    result = {}
    for path in inputs:
        # Only run ldd on our own build and trusted Ubuntu package objects.
        listing = subprocess.check_output(['ldd', path], text=True)
        for line in listing.splitlines():
            if '=> not found' in line:
                raise SystemExit(f'{path.name}: {line.strip()}')
            match = re.match(r'\s*(\S+) => (/\S+) \(', line)
            if match and match[1] not in SYSTEM_LIBS:
                result[match[1]] = Path(match[2])
    return result


def relocate_helpers(library, helper_dir):
    # Release WebKit hardcodes PKGLIBEXECDIR (WEBKIT_EXEC_PATH is developer-only).
    # Use the Wails v3.1.0 relocation convention, preserving ELF layout and the sandbox.
    old = bytes(helper_dir) + b'\0'
    relative = b'././webkitgtk-6.0/\0'
    data = library.read_bytes()
    if old not in data:
        raise SystemExit('WebKit helper directory changed; review AppImage relocation')
    library.write_bytes(data.replace(old, relative.ljust(len(old), b'\0')))


def notices(dest, files, data_packages):
    packages = set(data_packages)
    for path in sorted(files):
        candidates = [str(path)]
        if str(path).startswith('/usr/lib/'):
            candidates.append(str(path)[4:])  # Some packages still record the pre-usrmerge path.
        for name in candidates:
            found = subprocess.run(['dpkg-query', '-S', name], capture_output=True, text=True)
            if found.returncode == 0:
                packages.add(found.stdout.split(': ', 1)[0])
                break
        else:
            raise SystemExit(f'No Ubuntu package identified for {path}')
    dest.mkdir(parents=True)
    manifest = []
    for package in sorted(packages):
        record = subprocess.check_output(['dpkg-query', '-W', '-f=${Package}\t${Version}\t${source:Package}\t${source:Version}', package], text=True)
        name = record.split('\t')[0]
        shutil.copyfile(Path('/usr/share/doc') / name / 'copyright', dest / (name + '.copyright'))
        manifest.append(record)
    (dest / 'packages.tsv').write_text('Package\tVersion\tSource\tSourceVersion\n' + '\n'.join(manifest) + '\n')
    shutil.copytree('/usr/share/common-licenses', dest / 'common-licenses')
    (dest / 'README.txt').write_text(
        'Libraries are from the Ubuntu source packages and versions in packages.tsv.\n'
        'Filecloud relocates the WebKit helper directory and ELF search paths for AppImage use.\n'
        'See desktop/build/bundle-gtk.py in the Filecloud sources for these packaging changes.\n')


if __name__ == '__main__':
    bundle(Path(sys.argv[1]).resolve())
