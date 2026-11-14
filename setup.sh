#!/bin/sh
# Downloads the cactusdb binary for this computer (Linux or macOS, Intel/AMD
# or ARM) from GitHub and saves it as "cactusdb".
#
#   curl -fsSL https://raw.githubusercontent.com/albievan/cactusDB/main/setup.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/albievan/cactusDB/main/setup.sh | sh -s -- ~/cactusdb
#
# The folder to install into is the first argument, or $CACTUSDB_DIR, or the
# current folder. cactusdb keeps its saved connections and exports next to
# itself, so give it a folder of its own. $CACTUSDB_VERSION picks the
# version, e.g. 1.61.0 (default: the latest release). Running it again
# updates cactusdb.
set -eu

repo=albievan/cactusDB
ver=${CACTUSDB_VERSION:-}
ver=${ver#v}
case $ver in
  '') base=https://github.com/$repo/releases/latest/download ; ref=latest ;;
  *[!0-9.]*) echo "setup: CACTUSDB_VERSION=$ver isn't a version like 1.61.0" >&2; exit 1 ;;
  *) base=https://github.com/$repo/releases/download/v$ver ; ref=$ver ;;
esac
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
url=$base/$file
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)
dest=$dir/cactusdb
tmp=$dir/.cactusdb.download.$$
trap 'rm -f "$tmp" "$tmp.sums" "$tmp.sig" "$tmp.sigbin" "$tmp.pem" "$tmp.pl"' EXIT INT TERM

echo "downloading $file ($ref) to $dest"
if command -v curl >/dev/null 2>&1; then
  curl -fL --progress-bar -o "$tmp" "$url" || fail "download failed: $url"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$tmp" "$url" || fail "download failed: $url"
else
  fail "curl or wget is needed to download cactusdb"
fi
# Check it against the published checksums before running it, and the
# checksums against the release signature (SHA256SUMS.sig, made with the
# cactusdb release key: its public key is below).
fetch() {
  curl -fsSL -o "$2" "$base/$1" 2>/dev/null ||
    wget -qO "$2" "$base/$1" 2>/dev/null
}
fetch SHA256SUMS "$tmp.sums" || fail "can't download SHA256SUMS to check the file"
if fetch SHA256SUMS.sig "$tmp.sig"; then
  if command -v openssl >/dev/null 2>&1 && openssl version 2>/dev/null | grep -q '^OpenSSL [3-9]'; then
    printf '%s\n' '-----BEGIN PUBLIC KEY-----' 'MCowBQYDK2VwAyEAGDkn+i/onn7P5N3QN3Rb3EyJFH5FU5kLF40AR36lcn8=' '-----END PUBLIC KEY-----' > "$tmp.pem"
    (base64 -d < "$tmp.sig" || base64 -D < "$tmp.sig") > "$tmp.sigbin" 2>/dev/null
    openssl pkeyutl -verify -pubin -inkey "$tmp.pem" -rawin -in "$tmp.sums" -sigfile "$tmp.sigbin" >/dev/null 2>&1 ||
      fail "SHA256SUMS isn't signed with the cactusdb release key: not installed"
    echo "release signature checked"
  elif command -v perl >/dev/null 2>&1 && perl -MMath::BigInt -MDigest::SHA -MMIME::Base64 -e1 2>/dev/null; then
    # No OpenSSL 3 (macOS has LibreSSL): Perl, which macOS and most Linux
    # systems have, checks it instead.
    cat > "$tmp.pl" <<'PERL'
# Ed25519 signature check (RFC 8032): exits 0 when $ARGV[1] (base64) is the
# signature of file $ARGV[0] by the public key $ARGV[2] (base64), 1 otherwise.
use strict; use warnings;
use Math::BigInt try => 'GMP,FastCalc';
use Digest::SHA qw(sha512); use MIME::Base64 qw(decode_base64);
my ($mf, $sf, $pk) = @ARGV;
open(my $fh, '<:raw', $mf) or exit 1; my $m = do { local $/; <$fh> }; close $fh;
open($fh, '<', $sf) or exit 1; my $sig = decode_base64(do { local $/; <$fh> }); close $fh;
my $pub = decode_base64($pk);
exit 1 unless length($sig) == 64 && length($pub) == 32;
my $p = Math::BigInt->new(2)->bpow(255) - 19;
my $L = Math::BigInt->new(2)->bpow(252) + Math::BigInt->new('27742317777372353535851937790883648493');
sub inv { $_[0]->copy->bmodpow($p - 2, $p) }
sub num { Math::BigInt->from_hex('0x' . (unpack('H*', scalar reverse $_[0]) || '0')) }
my $d = (Math::BigInt->new(-121665) * inv(Math::BigInt->new(121666))) % $p;
my $d2 = (2 * $d) % $p;
my $I = Math::BigInt->new(2)->bmodpow(($p - 1) / 4, $p);
sub add {
    my ($P, $Q) = @_;
    my $A = (($P->[1] - $P->[0]) * ($Q->[1] - $Q->[0])) % $p; my $B = (($P->[1] + $P->[0]) * ($Q->[1] + $Q->[0])) % $p;
    my $C = ($P->[3] * $d2 * $Q->[3]) % $p; my $D = (2 * $P->[2] * $Q->[2]) % $p;
    my ($E, $F, $G, $H) = ($B - $A, $D - $C, $D + $C, $B + $A);
    return [($E * $F) % $p, ($G * $H) % $p, ($F * $G) % $p, ($E * $H) % $p];
}
sub mul {
    my ($s, $P) = @_;
    my $Q = [Math::BigInt->new(0), Math::BigInt->new(1), Math::BigInt->new(1), Math::BigInt->new(0)];
    for my $bit (split //, substr($s->as_bin, 2)) { $Q = add($Q, $Q); $Q = add($Q, $P) if $bit }
    return $Q;
}
sub point { # from y and the sign of x; undef when not on the curve
    my ($y, $sign) = @_;
    return undef if $y >= $p;
    my $u = ($y * $y - 1) % $p; my $v = ($d * $y * $y + 1) % $p;
    my $x = (($u * inv($v)) % $p)->bmodpow(($p + 3) / 8, $p);
    $x = ($x * $I) % $p if ($v * $x * $x - $u) % $p != 0;
    return undef if ($v * $x * $x - $u) % $p != 0;
    return undef if $x->is_zero && $sign;
    $x = $p - $x if ($x % 2) != $sign;
    return [$x, $y, Math::BigInt->new(1), ($x * $y) % $p];
}
sub decode { my $b = $_[0]; my $sign = (ord(substr($b, 31, 1)) >> 7) & 1; substr($b, 31, 1) = chr(ord(substr($b, 31, 1)) & 0x7f); point(num($b), $sign) }
my $A = decode($pub); my $R = decode(substr($sig, 0, 32));
exit 1 unless $A && $R;
my $S = num(substr($sig, 32, 32));
exit 1 unless $S < $L;
my $h = num(sha512(substr($sig, 0, 32) . $pub . $m)) % $L;
my $Bp = point((4 * inv(Math::BigInt->new(5))) % $p, 0);
my $l = mul($S, $Bp); my $r = add($R, mul($h, $A));
exit((($l->[0] * $r->[2] - $r->[0] * $l->[2]) % $p == 0 && ($l->[1] * $r->[2] - $r->[1] * $l->[2]) % $p == 0) ? 0 : 1);
PERL
    perl "$tmp.pl" "$tmp.sums" "$tmp.sig" 'GDkn+i/onn7P5N3QN3Rb3EyJFH5FU5kLF40AR36lcn8=' ||
      fail "SHA256SUMS isn't signed with the cactusdb release key: not installed"
    echo "release signature checked"
  else
    echo "note: the release signature isn't checked here (that needs OpenSSL 3 or Perl); the checksum is"
  fi
elif [ "${CACTUSDB_REQUIRE_SIGNATURE:-}" = yes ]; then
  fail "SHA256SUMS.sig is missing ($ref): not installed"
fi
want=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$tmp.sums")
[ -n "$want" ] || fail "SHA256SUMS has no checksum for $file"
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
