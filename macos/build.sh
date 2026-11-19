#!/usr/bin/env bash
set -euo pipefail

base="$(cd "$(dirname "$0")" && pwd)"
bundle="$base/../dist/DeskPulse Agent.app"
mkdir -p "$bundle/Contents/MacOS"
cp "$base/Info.plist" "$bundle/Contents/Info.plist"
xcrun --sdk macosx clang -fobjc-arc -Wall -Wextra -Werror=return-type \
    -O2 -arch arm64 -arch x86_64 -mmacosx-version-min=13.0 \
    -framework Cocoa -framework IOKit "$base/main.m" -o "$bundle/Contents/MacOS/deskpulse-agent"
codesign --force --sign - "$bundle"
printf '%s\n' "$bundle"
