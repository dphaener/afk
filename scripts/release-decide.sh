#!/usr/bin/env bash
# Answers the release workflow's decide job: for a push of <from-sha>..<to-sha>,
# print "skip" or the version to publish.
#
# Usage: release-decide.sh <from-sha> <to-sha>
#
# A v* tag on <to-sha> means someone tagged this commit by hand, so a release
# already exists (or the tag's own run is making it) and a second one would
# publish the same commit twice. Otherwise scripts/nextversion decides from the
# commit messages and the files changed.
#
# Nothing is written to stdout unless the answer is complete, so an error can
# never be read as a version. The script writes no file in the work tree: the
# release script refuses a changed tree.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: release-decide.sh <from-sha> <to-sha>" >&2
  exit 2
fi
from="$1"
to="$2"

# The scripts folder's parent is the repository root, wherever we are called from.
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if ! tags=$(git tag --points-at "$to" --list 'v*'); then
  echo "release-decide: cannot look up the tags on $to" >&2
  exit 1
fi
if [ -n "$tags" ]; then
  echo skip
  exit 0
fi

status=0
answer=$(go run ./scripts/nextversion "$from" "$to") || status=$?
if [ "$status" -ne 0 ]; then
  exit "$status"
fi
echo "$answer"
