#!/bin/sh
# Create Mockstro.app, a double-click launcher for `mockstro ui` that needs no terminal.
# It opens the UI in the browser, or just reopens it if it is already running.
set -eu
fail() { echo "mockstro launcher: $*" >&2; exit 1; }
usage() { echo 'Usage: sh install-ui-launcher.sh [/path/to/mockstro]'; }
if [ "${1:-}" = --help ]; then usage; exit 0; fi
[ "$#" -le 1 ] || { usage >&2; exit 1; }
[ "$(uname)" = Darwin ] || fail "the launcher is macOS only"
command -v osacompile >/dev/null 2>&1 || fail "osacompile is missing; it ships with macOS"

bin=${1:-$(command -v mockstro || true)}
[ -n "$bin" ] && [ -x "$bin" ] || fail "cannot find the mockstro binary; pass its full path"
bin=$(CDPATH='' cd -- "$(dirname -- "$bin")" && pwd)/$(basename -- "$bin")

dest=${MOCKSTRO_APP_DIR:-$HOME/Applications}
app=$dest/Mockstro.app
log=$HOME/Library/Logs/mockstro-ui.log
mkdir -p "$dest" "$(dirname "$log")"

script=$(mktemp "${TMPDIR:-/tmp}/mockstro-launcher.XXXXXX")
trap 'rm -f "$script"' EXIT
# Escape a value for an AppleScript string literal; `quoted form of` then quotes it for the shell.
as_string() { printf '"%s"' "$(printf '%s' "$1" | sed 's/[\\"]/\\&/g')"; }
{
  echo "set bin to $(as_string "$bin")"
  echo "set logFile to $(as_string "$log")"
  # Java discovery in the CLI also works with the desktop launcher PATH.
  cat <<'APPLESCRIPT'
set startUI to "export PATH=/opt/homebrew/bin:/usr/local/bin:$PATH; exec \"$1\" ui"
do shell script "/bin/zsh -lc " & quoted form of startUI & " mockstro-ui " & quoted form of bin & " >> " & quoted form of logFile & " 2>&1 &"
APPLESCRIPT
} > "$script"

rm -rf "$app"
osacompile -o "$app" "$script"
defaults write "$app/Contents/Info" LSUIElement -bool true # no Dock icon while it launches
codesign --force --sign - "$app" 2>/dev/null # editing Info.plist breaks osacompile's ad-hoc signature
touch "$app"

echo "Created $app"
echo "Double-click it to open Mockstro. Output goes to $log"
