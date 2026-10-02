#!/bin/sh
# tote installer for macOS and Linux.
#
#   curl -fsSL <base>/install.sh | sh -s -- <ticket>
#
# Downloads tote into ~/.tote-guest/bin (only for you, nothing system-wide,
# no admin), checks it against SHA256SUMS, then opens your box in guest mode.
# `tote leave` removes everything, including tote itself.
set -eu

BASE="${TOTE_BASE:-https://github.com/adityasinghin01-hash/tote/releases/latest/download}"
TICKET="${1:-}"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "tote: this system ($(uname -s)) isn't supported yet" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "tote: this processor ($(uname -m)) isn't supported yet" >&2; exit 1 ;;
esac

root="${TOTE_GUEST_ROOT:-$HOME/.tote-guest}"
dir="$root/bin"
mkdir -p "$dir"
chmod 700 "$root"
file="tote-$os-$arch"

fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
  else echo "tote: needs curl or wget" >&2; exit 1; fi
}
sha() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  else sha256sum "$1" | cut -d' ' -f1; fi
}

echo "Downloading tote ($os/$arch)…"
fetch "$BASE/$file" "$dir/.tote.part"
fetch "$BASE/SHA256SUMS" "$dir/.sums"
want=$(grep " $file\$" "$dir/.sums" | cut -d' ' -f1 || true)
got=$(sha "$dir/.tote.part")
rm -f "$dir/.sums"
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  rm -f "$dir/.tote.part"
  echo "tote: the download didn't match its checksum — not running it" >&2
  exit 1
fi
chmod 700 "$dir/.tote.part"
mv -f "$dir/.tote.part" "$dir/tote"
echo "tote is in $dir/tote (only for you; 'tote leave' removes it)"
echo

if [ -z "$TICKET" ]; then
  echo "Now run:  $dir/tote guest <ticket>"
  exit 0
fi
# Questions and the PIN need the keyboard, not the pipe this script came from.
# Use the terminal's real device: macOS can't watch the /dev/tty alias, and
# programs like Claude Code crash on it.
if [ -z "${TOTE_YES:-}" ]; then
  term=$(tty <&2 2>/dev/null || true)
  case "$term" in
    /dev/tty) ;;
    /dev/*) [ -r "$term" ] && exec "$dir/tote" guest "$TICKET" --then-run <"$term" ;;
  esac
  [ -r /dev/tty ] && exec "$dir/tote" guest "$TICKET" --then-run </dev/tty
fi
exec "$dir/tote" guest "$TICKET" --then-run --yes
