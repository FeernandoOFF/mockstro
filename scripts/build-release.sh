#!/bin/sh
# Build the same assets locally and in CI; this script never publishes them.
set -eu
version=${1:?usage: scripts/build-release.sh vX.Y.Z [output-directory]}
case "$version" in *[!0-9A-Za-z.-]*|"") echo 'expected vX.Y.Z' >&2; exit 1 ;; esac
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || exit 1
out=${2:-dist}
mkdir -p "$out"
# Refuse stale assets in a reused directory.
[ -z "$(ls -A "$out")" ] || { echo "$out must be empty" >&2; exit 1; }
for os in darwin linux; do
    for arch in amd64 arm64; do
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
            -ldflags "-s -w -X main.version=$version" \
            -o "$out/mockstro_${version}_${os}_${arch}" ./cmd/mockstro
    done
done
cp internal/bootstrap/mockstrow "$out/mockstrow"
cp scripts/install.sh "$out/install.sh"
chmod 755 "$out/mockstrow" "$out/install.sh"
(cd "$out"; if command -v sha256sum >/dev/null 2>&1; then
    sha256sum mockstro* install.sh > checksums.txt
else
    shasum -a 256 mockstro* install.sh > checksums.txt
fi)
