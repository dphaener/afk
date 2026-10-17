#!/usr/bin/env bash
# Build the release archives and checksums.txt for one afk version.
#
# Usage: scripts/release.sh <version> <out-dir>
#   <version> is semver without a leading "v" (1.2.3 or 1.2.3-rc.1).
#   <out-dir> must not exist yet, or be empty.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "Usage: scripts/release.sh <version> <out-dir>" >&2
  exit 2
fi

version=$1
out=$2

# The tag carries the "v"; the version in the binary and the archive names does
# not, so a "v1.2.3" here is a mistake worth refusing rather than guessing.
if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]]; then
  echo "Version '$version' is not a version like 1.2.3 (no leading v)." >&2
  exit 2
fi

# Never mix new files into an old release directory: checksums.txt would
# describe files this run did not build.
if [ -e "$out" ] && [ -n "$(ls -A "$out" 2>/dev/null)" ]; then
  echo "Output directory '$out' is not empty. Pick a new one." >&2
  exit 1
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

cd "$root"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}
  arch=${target#*/}
  dir="$stage/${os}_${arch}"
  mkdir -p "$dir"
  # CGO off: the result is a static binary and one Linux runner can build every
  # target without a C cross-compiler. -trimpath keeps the builder's directory
  # out of the binary so the same source gives the same bytes anywhere.
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$dir/afk" .
  files=(afk)
  if [ -f LICENSE ]; then cp LICENSE "$dir/"; files+=(LICENSE); fi
  cp README.md "$dir/"; files+=(README.md)
  # Files sit at the archive root so extracting gives ./afk directly.
  tar -czf "$out/afk_${version}_${os}_${arch}.tar.gz" -C "$dir" "${files[@]}"
done

# Run from inside out-dir so the names are bare and `sha256sum -c` works there.
(cd "$out" && sha256sum afk_"${version}"_*.tar.gz > checksums.txt)
echo "Built $(ls "$out" | wc -l) files in $out"
