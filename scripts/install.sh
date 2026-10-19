#!/bin/sh
# Add mockstro to an existing project without a global CLI or Go installation.
set -eu
fail() { echo "mockstro installer: $*" >&2; exit 1; }
usage() { echo 'Usage: sh install.sh vX.Y.Z [project-directory]'; }
if [ "${1:-}" = --help ]; then usage; exit 0; fi
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { usage >&2; exit 1; }
version=$1
case "$version" in *[!v0-9.]*|"") fail "version must be one release tag such as v1.2.3" ;; esac
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || fail "version must be a release tag such as v1.2.3"
project=$(CDPATH='' cd -- "${2:-.}" && pwd)
[ -d "$project/.maestro" ] || fail "expected an existing .maestro directory in $project"
for path in "$project/mockstrow" "$project/.maestro/.mockstro-version"; do
    [ ! -e "$path" ] && [ ! -L "$path" ] || fail "refusing to overwrite $path"
done
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
tmp=$(mktemp -d "${TMPDIR:-/tmp}/mockstro-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
url=https://github.com/FeernandoOFF/mockstro/releases/download/$version
fetch() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --connect-timeout 15 --max-time 300 --output "$tmp/$1" "$url/$1" ||
        fail "download failed; check connectivity and that release $version has $1"
}
fetch checksums.txt
expected=$(awk '$2 == "mockstrow" {print $1}' "$tmp/checksums.txt")
printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[a-fA-F0-9]{64}$' || fail "missing or invalid checksum for mockstrow"
fetch mockstrow
[ "$(sha256 "$tmp/mockstrow")" = "$expected" ] || fail "checksum mismatch for mockstrow; project unchanged"
chmod 755 "$tmp/mockstrow"
# The released wrapper is the single CLI downloader. Use its verified cache and
# the CLI's bootstrap command so both installation entry points behave alike.
mkdir "$tmp/.maestro"
printf '%s\n' "$version" > "$tmp/.maestro/.mockstro-version"
"$tmp/mockstrow" -C "$project" bootstrap --cli-version "$version"
