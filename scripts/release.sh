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

# Go stamps every binary with vcs.modified from the state of the checkout, and
# the version line shows that as "+dirty". A release built from a changed tree
# would say so on every user's machine, so refuse before building anything and
# name the files. This runs before the output folder is created, so a refusal
# leaves nothing behind.
if ! changed=$(git -C "$root" status --porcelain 2>/dev/null); then
  echo "A release must be built from a git checkout, and '$root' is not one." >&2
  exit 1
fi
if [ -n "$changed" ]; then
  echo "The tree has uncommitted changes, so the binaries would say +dirty:" >&2
  echo "$changed" >&2
  echo "Commit or remove them, then run this again." >&2
  exit 1
fi

mkdir -p "$out"
out=$(cd "$out" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

cd "$root"
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)

# Build every binary before writing any archive. When the output folder sits
# inside the checkout, the first archive is an untracked file, and Go would
# stamp every later build vcs.modified=true (the 2026-10-19 dry run showed
# +dirty on darwin/arm64 for this reason).
for target in "${targets[@]}"; do
  os=${target%/*}
  arch=${target#*/}
  dir="$stage/${os}_${arch}"
  mkdir -p "$dir"
  # CGO off: the result is a static binary and one Linux runner can build every
  # target without a C cross-compiler. -trimpath keeps the builder's directory
  # out of the binary so the same source gives the same bytes anywhere.
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$dir/afk" .
  # Whatever changed the tree mid-run, a dirty binary is never archived.
  if go version -m "$dir/afk" | grep -q 'vcs.modified=true'; then
    echo "The $target binary was stamped as modified (+dirty), so nothing was archived." >&2
    echo "Something changed the tree during the build; check git status." >&2
    exit 1
  fi
done

for target in "${targets[@]}"; do
  os=${target%/*}
  arch=${target#*/}
  dir="$stage/${os}_${arch}"
  files=(afk)
  if [ -f LICENSE ]; then cp LICENSE "$dir/"; files+=(LICENSE); fi
  cp README.md "$dir/"; files+=(README.md)
  # Files sit at the archive root so extracting gives ./afk directly.
  tar -czf "$out/afk_${version}_${os}_${arch}.tar.gz" -C "$dir" "${files[@]}"
done

# Run from inside out-dir so the names are bare and `sha256sum -c` works there.
(cd "$out" && sha256sum afk_"${version}"_*.tar.gz > checksums.txt)
echo "Built $(find "$out" -maxdepth 1 -type f | wc -l) files in $out"
