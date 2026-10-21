#!/bin/sh
# Add mockstro to a project without a global CLI or Go installation. Extra flags
# go to init, which creates .maestro/mockstro.yaml when it's missing.
set -eu
fail() { echo "mockstro installer: $*" >&2; exit 1; }
usage() { echo 'Usage: sh install.sh vX.Y.Z [-C project-directory] [init flags, e.g. --android-app-id com.example.app]'; }
if [ "${1:-}" = --help ]; then usage; exit 0; fi
[ "$#" -ge 1 ] || { usage >&2; exit 1; }
version=$1
shift
dir=.
if [ "${1:-}" = -C ]; then
    [ "$#" -ge 2 ] || { usage >&2; exit 1; }
    dir=$2
    shift 2
fi
case "$version" in *[!0-9A-Za-z.-]*|"") fail "version must be one release tag such as v1.2.3" ;; esac
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || fail "version must be a release tag such as v1.2.3"
project=$(CDPATH='' cd -- "$dir" && pwd)
[ ! -e "$project/mockstrow" ] && [ ! -L "$project/mockstrow" ] || fail "refusing to overwrite $project/mockstrow"
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
# The released wrapper is the single CLI downloader; init does the rest.
mkdir "$tmp/.maestro"
printf 'cliVersion: %s\n' "$version" > "$tmp/.maestro/mockstro.yaml"
"$tmp/mockstrow" -C "$project" init --cli-version "$version" "$@"
