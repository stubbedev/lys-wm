#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
	echo "Usage: $0 <package.deb|package.rpm>" >&2
	exit 1
fi

PACKAGE="$(readlink -f "$1")"

case "${PACKAGE}" in
*.deb) apt-get install -y --no-install-recommends "${PACKAGE}" ;;
*.rpm) dnf -y --setopt=install_weak_deps=False install "${PACKAGE}" ;;
*)
	echo "Not a .deb or .rpm: ${PACKAGE}" >&2
	exit 1
	;;
esac

fail() {
	echo "smoke test: $*" >&2
	exit 1
}

for file in /usr/lib/systemd/user/lys-wm-session.target /usr/share/xdg-desktop-portal/lys-wm-portals.conf \
	/usr/share/wayland-sessions/lys-wm.desktop /usr/share/lys-wm/DejaVuSans.ttf /usr/share/hwdata/pnp.ids; do
	[[ -f ${file} ]] || fail "${file} is missing"
done

for soname in libxkbcommon.so.0 libEGL.so.1 libGLESv2.so.2 libgbm.so.1 libinput.so.10; do
	ldconfig -p | grep -qF "${soname} " || fail "${soname}, which lys-wm loads, is not on the library path"
done

expect_error() {
	local want="$1" out status=0
	shift
	out="$("$@" 2>&1)" || status=$?
	[[ ${status} -eq 1 ]] || fail "$* exited ${status}, want 1"
	[[ ${out} == *"${want}"* ]] || fail "$* printed ${out@Q}, want ${want@Q}"
}

expect_error 'unknown backend "none"' lys-wm -backend none
expect_error 'no command' lys-wmctl

echo "smoke test: ${PACKAGE} installs and runs"
