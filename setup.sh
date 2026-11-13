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
trap 'rm -f "$tmp" "$tmp.sums" "$tmp.sig" "$tmp.sigbin" "$tmp.pem"' EXIT INT TERM

echo "downloading $file ($ref) to $dest"
if command -v curl >/dev/null 2>&1; then
  curl -fL --progress-bar -o "$tmp" "$url" || fail "download failed: $url"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$tmp" "$url" || fail "download failed: $url"
else
  fail "curl or wget is needed to download cactusdb"
fi
# Check it against the published checksums before running it, and the
# checksums against the release signature (dist/SHA256SUMS.sig, made with the
# cactusdb release key: its public key is below).
fetch() {
  curl -fsSL -o "$2" "https://github.com/$repo/raw/$ref/dist/$1" 2>/dev/null ||
    wget -qO "$2" "https://github.com/$repo/raw/$ref/dist/$1" 2>/dev/null
}
fetch SHA256SUMS "$tmp.sums" || fail "can't download dist/SHA256SUMS to check the file"
if fetch SHA256SUMS.sig "$tmp.sig"; then
  if command -v openssl >/dev/null 2>&1 && openssl version 2>/dev/null | grep -q '^OpenSSL [3-9]'; then
    printf '%s\n' '-----BEGIN PUBLIC KEY-----' 'MCowBQYDK2VwAyEAGDkn+i/onn7P5N3QN3Rb3EyJFH5FU5kLF40AR36lcn8=' '-----END PUBLIC KEY-----' > "$tmp.pem"
    (base64 -d < "$tmp.sig" || base64 -D < "$tmp.sig") > "$tmp.sigbin" 2>/dev/null
    openssl pkeyutl -verify -pubin -inkey "$tmp.pem" -rawin -in "$tmp.sums" -sigfile "$tmp.sigbin" >/dev/null 2>&1 ||
      fail "dist/SHA256SUMS isn't signed with the cactusdb release key: not installed"
    echo "release signature checked"
  else
    echo "note: the release signature isn't checked here (that needs OpenSSL 3); the checksum is"
  fi
elif [ "${CACTUSDB_REQUIRE_SIGNATURE:-}" = yes ]; then
  fail "dist/SHA256SUMS.sig is missing ($ref): not installed"
fi
want=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$tmp.sums")
[ -n "$want" ] || fail "dist/SHA256SUMS has no checksum for $file"
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp" | awk '{print $1}')
else got=$(shasum -a 256 "$tmp" | awk '{print $1}'); fi
[ "$got" = "$want" ] || fail "the download of $file is damaged: its SHA-256 isn't the one in SHA256SUMS"
chmod +x "$tmp"
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$tmp" 2>/dev/null || true # let macOS run it
fi
# Check it runs before replacing an existing cactusdb.
version=$("$tmp" --version 2>&1) || fail "the downloaded file does not run: $version"
mv -f "$tmp" "$dest" # a running cactusdb keeps running; the new one is used from its next start

echo "installed $version as $dest"
echo "start it with:  cd \"$dir\" && ./cactusdb --port 8080"
