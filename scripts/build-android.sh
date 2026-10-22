#!/usr/bin/env bash
# Build the Android library: an .aar holding the Go code for each ABI and its
# Java bindings (package io.github.anhydrous99.satlink.mobile).
#
# Prerequisites:
#   the Android SDK, with ANDROID_HOME set (gomobile compiles the Java
#   bindings against its android.jar), and the Android NDK, with
#   ANDROID_NDK_HOME set (cgo builds the bindings' glue with its clang);
#   a JDK for javac.
# Desktop serial-port access is left out of Android builds by build
# constraint (mobile/serial.go).
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=${1:-android/build/generated}

# gomobile looks gobind up on PATH, `go tool` puts no tool there, and
# `gomobile init` would fetch an unpinned one: build the go.mod-pinned
# gobind instead.
tools=$(mktemp -d)
trap 'rm -rf "$tools"' EXIT
go build -o "$tools/gobind" golang.org/x/mobile/cmd/gobind

# The ABIs — armeabi-v7a, arm64-v8a and x86_64 — rather than gomobile's
# default four. API level 21 is the lowest that current NDKs still ship.
mkdir -p "$OUT"
PATH="$tools:$PATH" go tool gomobile bind \
    -target=android/arm,android/arm64,android/amd64 \
    -androidapi 21 \
    -javapkg io.github.anhydrous99.satlink \
    -o "$OUT/satlink.aar" \
    ./mobile

cat <<EOF
Done.
  Android library: $OUT/satlink.aar (Java sources: $OUT/satlink-sources.jar)
Add the .aar to your app module, plus android/src/main (transport, example and
device filter).
EOF
