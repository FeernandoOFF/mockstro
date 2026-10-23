#!/bin/sh
# Install a global mockstro CLI and, on macOS, the Mockstro.app launcher. Rerun to update.
set -eu
fail() { echo "mockstro installer: $*" >&2; exit 1; }
usage() { echo 'Usage: sh install-global.sh vX.Y.Z [--no-launcher]'; }
if [ "${1:-}" = --help ]; then usage; exit 0; fi
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { usage >&2; exit 1; }
version=$1
launcher=yes
case "${2:-}" in
    "") ;;
    --no-launcher) launcher=no ;;
    *) usage >&2; exit 1 ;;
esac
case "$version" in *[!0-9A-Za-z.-]*|"") fail "version must be one release tag such as v1.2.3" ;; esac
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || fail "version must be a release tag such as v1.2.3"
case $(uname -s) in
    Darwin) os=darwin ;;
    Linux) os=linux; launcher=no ;;
    *) fail "supported systems: macOS and Linux" ;;
esac
case $(uname -m) in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64) arch=amd64 ;;
    *) fail "supported architectures: arm64 and amd64" ;;
esac
command -v curl >/dev/null 2>&1 || fail "install curl to download mockstro"
sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        fail "install sha256sum or shasum for checksum verification"
    fi
}
bin_dir=${BIN_DIR:-$HOME/.local/bin}
mkdir -p "$bin_dir"
bin_dir=$(CDPATH='' cd -- "$bin_dir" && pwd)
tmp=$(mktemp -d "$bin_dir/.mockstro-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
url=https://github.com/FeernandoOFF/mockstro/releases/download/$version
fetch() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --connect-timeout 15 --max-time 300 --output "$tmp/$1" "$url/$1" ||
        fail "download failed; check connectivity and that release $version has $1"
    expected=$(awk -v asset="$1" '$2 == asset {print $1}' "$tmp/checksums.txt")
    printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[a-fA-F0-9]{64}$' || fail "missing or invalid checksum for $1"
    [ "$(sha256 "$tmp/$1")" = "$expected" ] || fail "checksum mismatch for $1; nothing installed"
}
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 300 --output "$tmp/checksums.txt" "$url/checksums.txt" ||
    fail "download failed; check connectivity and that release $version exists"
asset=mockstro_${version}_${os}_${arch}
fetch "$asset"
[ "$launcher" = no ] || fetch install-ui-launcher.sh
chmod 755 "$tmp/$asset"
# Same-filesystem rename: a running mockstro keeps its old file.
mv -f "$tmp/$asset" "$bin_dir/mockstro"
echo "Installed mockstro $version to $bin_dir/mockstro"
case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *) echo "Add $bin_dir to your PATH to run mockstro from a terminal, e.g. in ~/.zshrc:"
       echo "  export PATH=\"$bin_dir:\$PATH\"" ;;
esac
[ "$launcher" = no ] || sh "$tmp/install-ui-launcher.sh" "$bin_dir/mockstro"
