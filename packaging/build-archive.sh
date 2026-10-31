#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
	echo "Usage: $0 <dist-dir> <version> <arch>" >&2
	exit 1
fi

DIST_DIR="$(readlink -f "$1")"
VERSION="$2"
ARCH="$3"

cd "$(dirname "$0")/.."

STAGING="lys-wm-${VERSION}-${ARCH}-linux"
rm -rf "${STAGING}"
mkdir -p "${STAGING}"
cp "${DIST_DIR}/lys-wm" "${DIST_DIR}/lys-wmctl" LICENSE THIRD_PARTY.md \
	packaging/systemd/lys-wm-session.target \
	packaging/xdg-desktop-portal/lys-wm-portals.conf \
	packaging/wayland-sessions/lys-wm.desktop \
	"${STAGING}/"
cp third_party/xgb/LICENSE "${STAGING}/xgb.LICENSE"
tar czf "${STAGING}.tar.gz" "${STAGING}"
rm -rf "${STAGING}"
