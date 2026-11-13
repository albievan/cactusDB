#!/bin/sh
# Cross-compiles cactusdb for Linux, macOS and Windows into ./dist.
# All drivers are pure Go, so CGO is disabled and no toolchains are needed.
set -e
# The version is in the VERSION file, which running cactusdbs check for updates.
VERSION=${VERSION:-$(cat VERSION)}
# The release key signs dist/SHA256SUMS; its public half is built into the
# binaries, which install an update only when the signature checks out. It
# lives outside the repository: keep a backup of it, as installed cactusdbs
# refuse updates signed with any other key. setup.sh and setup.ps1 carry the
# same public key (as PEM): a new key must be put there too.
KEY=${CACTUSDB_RELEASE_KEY:-$HOME/.config/cactusdb/release-ed25519.key}
helper=$(mktemp -d)
trap 'rm -rf "$helper"' EXIT
go build -o "$helper/cactusdb" .
if [ ! -f "$KEY" ]; then
  echo "creating the release key $KEY (back it up)"
  "$helper/cactusdb" release-keygen "$KEY"
fi
PUB=$("$helper/cactusdb" release-pubkey "$KEY")
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}; arch=${target#*/}
  out=dist/cactusdb-$os-$arch
  [ "$os" = windows ] && out=$out.exe
  echo "building $out"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.releaseKey=$PUB" -o "$out" .
done
# Checksums: an upgrade from the web UI checks the downloaded binary with them.
cd dist
if command -v sha256sum >/dev/null 2>&1; then sha256sum cactusdb-*-* > SHA256SUMS
else shasum -a 256 cactusdb-*-* > SHA256SUMS; fi
"$helper/cactusdb" release-sign "$KEY" SHA256SUMS
echo "wrote dist/SHA256SUMS and its signature dist/SHA256SUMS.sig"
