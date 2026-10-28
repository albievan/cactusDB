#!/bin/sh
# Cross-compiles cactusdb for Linux, macOS and Windows into ./dist.
# All drivers are pure Go, so CGO is disabled and no toolchains are needed.
set -e
VERSION=${VERSION:-1.13.1}
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}; arch=${target#*/}
  out=dist/cactusdb-$os-$arch
  [ "$os" = windows ] && out=$out.exe
  echo "building $out"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" .
done
