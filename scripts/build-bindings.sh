#!/usr/bin/env bash
# Build the macOS framework for Swift and Objective-C apps,
# Satlink.xcframework, whose names start with SatlinkMobile, serial-port
# access included.
#
# Prerequisite: a full Xcode, as gomobile refuses the Command Line Tools
# alone. gomobile has no Kotlin/JVM desktop target, so this script builds
# no JVM bindings.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=${1:-build/bindings}

# gomobile looks gobind up on PATH, `go tool` puts no tool there, and
# `gomobile init` would fetch an unpinned one: build the go.mod-pinned
# gobind instead.
tools=$(mktemp -d)
trap 'rm -rf "$tools"' EXIT
go build -o "$tools/gobind" golang.org/x/mobile/cmd/gobind

mkdir -p "$OUT"
PATH="$tools:$PATH" go tool gomobile bind \
    -target=macos \
    -prefix Satlink \
    -o "$OUT/Satlink.xcframework" \
    ./mobile

# gomobile copies gobind's header into each slice of the framework as it
# is, and there NSObject's init would let an app make objects with no Go
# side, whose first use ends the app: mark it unavailable where gobind
# declares no argument-less init.
headers=()
while IFS= read -r -d '' header; do
    headers+=("$header")
done < <(find "$OUT/Satlink.xcframework" -type f -name SatlinkMobile.objc.h -print0)
if [ ${#headers[@]} -eq 0 ]; then
    echo "no SatlinkMobile.objc.h in $OUT/Satlink.xcframework" >&2
    exit 1
fi
scripts/mark-unusable-inits.sh "${headers[@]}"

echo "Satlink.xcframework written to $OUT"
