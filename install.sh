#!/bin/sh
# Installs Quarry for the current user on Linux (x86_64) and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/gxd3v/quarry/main/install.sh | sh
#
# QUARRY_VERSION picks a release tag (default: the latest release) and
# QUARRY_INSTALL_DIR the folder of the app (default: ~/.local/share/quarry on
# Linux, ~/Applications on macOS). The install is user-writable, so Quarry
# updates itself in place.
set -eu

repo=gxd3v/quarry
version=${QUARRY_VERSION:-latest}

say() { printf '%s\n' "$*"; }
fail() { say "quarry install: $*" >&2; exit 1; }

if [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
else
	base="https://github.com/$repo/releases/download/$version"
fi

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }
need curl
need uname

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=macos ;;
*) fail "unsupported system $(uname -s); see https://github.com/$repo#install" ;;
esac
if [ "$os" = linux ] && [ "$(uname -m)" != x86_64 ]; then
	fail "unsupported architecture $(uname -m); Linux builds are x86_64 only"
fi

if [ "$os" = linux ]; then
	asset=quarry-linux-amd64.tar.gz
else
	asset=quarry-macos-universal.zip
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset ($version)..."
curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "download of $base/$asset failed"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || fail "download of $base/SHA256SUMS failed"

want=$(awk -v f="$asset" '{ n = $2; sub(/^\*/, "", n); if (n == f) print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || fail "SHA256SUMS lists no $asset"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
	got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $asset"

if [ "$os" = linux ]; then
	dir=${QUARRY_INSTALL_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/quarry}
	bin=$HOME/.local/bin
	apps=${XDG_DATA_HOME:-$HOME/.local/share}/applications
	icons=${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor/512x512/apps
	mkdir -p "$dir" "$bin" "$apps" "$icons"
	tar -xzf "$tmp/$asset" -C "$tmp"
	# Replace by rename, which works while an older copy is running.
	cp "$tmp/quarry" "$dir/.quarry.new"
	chmod 755 "$dir/.quarry.new"
	mv -f "$dir/.quarry.new" "$dir/quarry"
	cp "$tmp/quarry.png" "$icons/quarry.png"
	ln -sf "$dir/quarry" "$bin/quarry"
	sed "s|^Exec=quarry|Exec=$dir/quarry|" "$tmp/quarry.desktop" > "$apps/quarry.desktop"
	command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$apps" >/dev/null 2>&1 || true

	say "Installed $("$dir/quarry" -version 2>/dev/null || echo quarry) in $dir"
	case ":$PATH:" in
	*":$bin:"*) ;;
	*) say "$bin is not on PATH; the app menu entry works without it." ;;
	esac

	# Quarry needs GTK 3 and WebKitGTK 4.1 from the system.
	if command -v ldconfig >/dev/null 2>&1 && ! ldconfig -p 2>/dev/null | grep -q 'libwebkit2gtk-4.1.so'; then
		say ""
		say "WebKitGTK 4.1 is missing. It installs with:"
		if command -v apt-get >/dev/null 2>&1; then
			say "  sudo apt-get install libgtk-3-0 libwebkit2gtk-4.1-0"
		elif command -v dnf >/dev/null 2>&1; then
			say "  sudo dnf install gtk3 webkit2gtk4.1"
		elif command -v pacman >/dev/null 2>&1; then
			say "  sudo pacman -S gtk3 webkit2gtk-4.1"
		elif command -v zypper >/dev/null 2>&1; then
			say "  sudo zypper install libgtk-3-0 libwebkit2gtk-4_1-0"
		else
			say "  the distribution's GTK 3 and WebKitGTK 4.1 packages"
		fi
	fi
else
	need unzip
	dir=${QUARRY_INSTALL_DIR:-$HOME/Applications}
	mkdir -p "$dir"
	unzip -q "$tmp/$asset" -d "$tmp/app"
	rm -rf "$dir/Quarry.app.old"
	[ -d "$dir/Quarry.app" ] && mv "$dir/Quarry.app" "$dir/Quarry.app.old"
	mv "$tmp/app/Quarry.app" "$dir/Quarry.app"
	rm -rf "$dir/Quarry.app.old"
	# The app is not notarized; without this, Gatekeeper blocks the first start.
	xattr -dr com.apple.quarantine "$dir/Quarry.app" 2>/dev/null || true
	say "Installed Quarry.app in $dir"
	say "It starts from Launchpad, Spotlight or: open \"$dir/Quarry.app\""
fi
