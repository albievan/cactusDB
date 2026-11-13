#!/bin/sh
# Publishes release/ (made by build.sh) as the GitHub release v<VERSION>: the
# six binaries, SHA256SUMS and SHA256SUMS.sig, with the version's CHANGELOG
# entry as its notes. Run it after committing the release and before pushing
# main: running cactusdbs see the new VERSION on main and download from this
# release, so it must exist first.
#
#   ./build.sh && git commit … && ./release.sh && git push origin main
set -e
VERSION=$(cat VERSION)
tag=v$VERSION
command -v gh >/dev/null 2>&1 || { echo "release: the GitHub CLI (gh) is needed" >&2; exit 1; }
# The files must be the ones whose checksums are committed.
(cd release && if command -v sha256sum >/dev/null 2>&1; then sha256sum -c --quiet SHA256SUMS; else shasum -a 256 -c --quiet SHA256SUMS; fi) ||
  { echo "release: release/ doesn't match release/SHA256SUMS: run ./build.sh" >&2; exit 1; }
git ls-files --error-unmatch release/SHA256SUMS release/SHA256SUMS.sig >/dev/null 2>&1 &&
  git diff --quiet HEAD -- release/SHA256SUMS release/SHA256SUMS.sig VERSION ||
  { echo "release: commit release/SHA256SUMS, its signature and VERSION first" >&2; exit 1; }
"./release/cactusdb-$(go env GOOS)-$(go env GOARCH)" --version | grep -qx "cactusdb $VERSION" ||
  { echo "release: the binaries in release/ aren't version $VERSION: run ./build.sh" >&2; exit 1; }
# The tag is pushed on its own, so the release exists before main has the
# new VERSION.
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  [ "$(git rev-parse "$tag^{commit}")" = "$(git rev-parse HEAD)" ] ||
    { echo "release: tag $tag is on another commit than HEAD" >&2; exit 1; }
else
  git tag -a "$tag" -m "cactusdb $VERSION"
fi
git push origin "refs/tags/$tag"
notes=$(mktemp)
trap 'rm -f "$notes"' EXIT
awk -v v="$VERSION" '/^## / { on = index($0, "## " v " ") == 1; next } on' CHANGELOG.md > "$notes"
gh release create "$tag" --verify-tag --title "cactusdb $VERSION" --notes-file "$notes" \
  release/cactusdb-* release/SHA256SUMS release/SHA256SUMS.sig
echo "published $tag: now push main (git push origin main)"
