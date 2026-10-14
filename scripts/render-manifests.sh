#!/usr/bin/env sh
# Fills the package manager manifests in packaging/ for a release.
# Usage: scripts/render-manifests.sh <version without v> <dist dir> <out dir>
set -eu
version=$1
dist=$2
out=$3
mkdir -p "$out"
sum() { sha256sum "$dist/$1" | cut -d' ' -f1; }
for f in packaging/aur/PKGBUILD packaging/homebrew/quarry.rb packaging/scoop/quarry.json packaging/winget/gxd3v.Quarry.yaml; do
  # Arch versions cannot contain "-" (1.0.0-rc.1 becomes 1.0.0_rc.1).
  sed -e "s/@AUR_VERSION@/$(echo "$version" | tr - _)/g"     -e "s/@VERSION@/$version/g" \
    -e "s/@SHA256_LINUX_TAR@/$(sum quarry-linux-amd64.tar.gz)/g" \
    -e "s/@SHA256_MACOS_ZIP@/$(sum quarry-macos-universal.zip)/g" \
    -e "s/@SHA256_WINDOWS_ZIP@/$(sum quarry-windows-amd64.zip)/g" \
    -e "s/@SHA256_WINDOWS_INSTALLER@/$(sum quarry-windows-amd64-installer.exe)/g" \
    "$f" > "$out/$(basename "$f")"
done
