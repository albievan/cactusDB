#!/bin/sh
# Downloads the cactusdb binary for this computer (Linux or macOS, Intel/AMD
# or ARM) from GitHub and saves it as "cactusdb".
#
#   curl -fsSL https://raw.githubusercontent.com/albievan/cactusDB/main/setup.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/albievan/cactusDB/main/setup.sh | sh -s -- ~/cactusdb
#
# The folder to install into is the first argument, or $CACTUSDB_DIR, or the
# current folder. cactusdb keeps its saved connections and exports next to
# itself, so give it a folder of its own. $CACTUSDB_REF picks the branch or
# tag to download from (default main). Running it again updates cactusdb.
set -eu

repo=albievan/cactusDB
ref=${CACTUSDB_REF:-main}
dir=${1:-${CACTUSDB_DIR:-.}}

fail() { echo "setup: $*" >&2; exit 1; }

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) fail "this is Windows: run setup.ps1 in PowerShell instead" ;;
  *) fail "no cactusdb build for $(uname -s) (there are Linux, macOS and Windows builds)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no cactusdb build for the $(uname -m) processor (there are amd64 and arm64 builds)" ;;
esac
# A Mac with Apple Silicon running this shell under Rosetta reports x86_64.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" = 1 ]; then
  arch=arm64
fi

file=cactusdb-$os-$arch
url=https://github.com/$repo/raw/$ref/dist/$file
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)
dest=$dir/cactusdb
tmp=$dir/.cactusdb.download.$$
trap 'rm -f "$tmp"' EXIT INT TERM

echo "downloading $file ($ref) to $dest"
if command -v curl >/dev/null 2>&1; then
  curl -fL --progress-bar -o "$tmp" "$url" || fail "download failed: $url"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$tmp" "$url" || fail "download failed: $url"
else
  fail "curl or wget is needed to download cactusdb"
fi
chmod +x "$tmp"
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$tmp" 2>/dev/null || true # let macOS run it
fi
# Check it runs before replacing an existing cactusdb.
version=$("$tmp" --version 2>&1) || fail "the downloaded file does not run: $version"
mv -f "$tmp" "$dest" # a running cactusdb keeps running; the new one is used from its next start

echo "installed $version as $dest"
echo "start it with:  cd \"$dir\" && ./cactusdb --port 8080"
