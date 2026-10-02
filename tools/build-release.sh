#!/bin/sh
# Builds tote for every supported system into dist/ with a SHA256SUMS file.
# Usage: tools/build-release.sh [version] [hosted-mailbox-url] [install-base-url]
set -eu
cd "$(dirname "$0")/.."
VERSION="${1:-0.1.0-dev}"
HOSTED="${2:-}"
BASE="${3:-}"
LDFLAGS="-s -w -X main.version=$VERSION -X github.com/adityasinghin01-hash/tote/internal/mailbox.DefaultHostedURL=$HOSTED -X main.installBase=$BASE"
rm -rf dist && mkdir dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}; arch=${target#*/}; ext=""
  [ "$os" = windows ] && ext=".exe"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "dist/tote-$os-$arch$ext" ./cmd/tote
done
cp install/install.sh install/install.ps1 dist/
(cd dist && shasum -a 256 tote-* > SHA256SUMS)
ls -lh dist
