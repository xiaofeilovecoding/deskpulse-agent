#!/usr/bin/env bash
set -euo pipefail

base="$(cd "$(dirname "$0")" && pwd)"
bundle="$base/../dist/DeskPulse Agent.app"
helper="$bundle/Contents/Helpers/deskpulse-model"
staging="$(mktemp -d)"
trap 'rm -f "$staging/deskpulse-model-amd64" "$staging/deskpulse-model-arm64"; rmdir "$staging"' EXIT
mkdir -p "$bundle/Contents/MacOS" "$bundle/Contents/Helpers"
cp "$base/Info.plist" "$bundle/Contents/Info.plist"
(cd "$base/.."
    for arch in amd64 arm64; do
        CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" \
            go build -trimpath -ldflags="-s -w" -o "$staging/deskpulse-model-$arch" ./cmd/deskpulse-model
    done
)
xcrun lipo -create "$staging/deskpulse-model-amd64" "$staging/deskpulse-model-arm64" -output "$helper"
codesign --force --sign - "$helper"
xcrun --sdk macosx clang -fobjc-arc -Wall -Wextra -Werror=return-type \
    -O2 -arch arm64 -arch x86_64 -mmacosx-version-min=13.0 \
    -framework Cocoa -framework IOKit -framework Network -framework Security \
    "$base/main.m" "$base/processes.m" "$base/latency.m" "$base/model.m" -o "$bundle/Contents/MacOS/deskpulse-agent"
codesign --force --sign - "$bundle"
printf '%s\n' "$bundle"
