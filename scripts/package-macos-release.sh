#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-$(awk -F '"' '/^version =/ { print $2; exit }' "$ROOT_DIR/mac/Cargo.toml")}"
RELEASE_DIR="${RELEASE_DIR:-$ROOT_DIR/mac/release}"
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/amrpc-release.XXXXXX")"
trap 'rm -rf "$BUILD_DIR"' EXIT

# Build with the distribution defaults, without touching the running app.
DIST_DIR="$BUILD_DIR/build" "$ROOT_DIR/scripts/build-macos.sh"
APP="$BUILD_DIR/build/AppleMusicRPC.app"
BINARY="$APP/Contents/MacOS/AppleMusicRPC"
cp "$ROOT_DIR/LICENSE" "$APP/Contents/Resources/LICENSE"
BUNDLE_ID="$(plutil -extract CFBundleIdentifier raw -o - "$APP/Contents/Info.plist")"
codesign --force --sign - --identifier "$BUNDLE_ID" "$APP"
ARCH="$(lipo -archs "$BINARY")"
case "$ARCH" in
  arm64|x86_64) ;;
  "arm64 x86_64"|"x86_64 arm64") ARCH=universal ;;
  *) echo "Unsupported release architecture: $ARCH" >&2; exit 1 ;;
esac

mkdir -p "$RELEASE_DIR"
ARCHIVE="$RELEASE_DIR/AppleMusicRPC-$VERSION-macos-$ARCH.zip"
# Exclude Finder/iCloud metadata and AppleDouble files from the signed bundle.
ditto -c -k --keepParent --norsrc --noextattr --noqtn "$APP" "$ARCHIVE"
ditto -x -k "$ARCHIVE" "$BUILD_DIR/verify"
VERIFIED_APP="$BUILD_DIR/verify/AppleMusicRPC.app"
codesign --verify --strict "$VERIFIED_APP"
test -x "$VERIFIED_APP/Contents/MacOS/AppleMusicRPC"
"$VERIFIED_APP/Contents/MacOS/AppleMusicRPC" --version
(
  cd "$RELEASE_DIR"
  shasum -a 256 "$(basename "$ARCHIVE")" > "$(basename "$ARCHIVE").sha256"
)
echo "$ARCHIVE"
echo "$ARCHIVE.sha256"
