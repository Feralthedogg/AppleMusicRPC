#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE_DIR="$ROOT_DIR/mac"
APP_NAME="${APP_NAME:-AppleMusicRPC}"
BUNDLE_ID="${BUNDLE_ID:-com.feralthedogg.AppleMusicRPC}"
VERSION="${VERSION:-2.0.0}"
DIST_DIR="${DIST_DIR:-$MODULE_DIR/dist}"
CONFIG_SRC="${CONFIG_SRC:-$MODULE_DIR/config.json}"
APP_DIR="$DIST_DIR/$APP_NAME.app"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "The macOS app must be built on macOS." >&2
  exit 1
fi
if [[ ! -f "$CONFIG_SRC" ]]; then
  echo "config not found: $CONFIG_SRC" >&2
  exit 1
fi

# Compile before touching the installed bundle. Cargo.lock pins the dependencies.
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-12.0}"
cargo build --manifest-path "$MODULE_DIR/Cargo.toml" --release --locked
mkdir -p "$DIST_DIR"
STAGING_DIR="$(mktemp -d "$DIST_DIR/.amrpc-build.XXXXXX")"
trap 'rm -rf "$STAGING_DIR"' EXIT
STAGED_APP="$STAGING_DIR/$APP_NAME.app"
CONTENTS_DIR="$STAGED_APP/Contents"
mkdir -p "$CONTENTS_DIR/MacOS" "$CONTENTS_DIR/Resources"
cp "$MODULE_DIR/target/release/AppleMusicRPC" "$CONTENTS_DIR/MacOS/$APP_NAME"
cp "$CONFIG_SRC" "$CONTENTS_DIR/Resources/config.json"

PLIST="$CONTENTS_DIR/Info.plist"
plutil -create xml1 "$PLIST"
plutil -insert CFBundleDevelopmentRegion -string en "$PLIST"
plutil -insert CFBundleDisplayName -string "$APP_NAME" "$PLIST"
plutil -insert CFBundleExecutable -string "$APP_NAME" "$PLIST"
plutil -insert CFBundleIdentifier -string "$BUNDLE_ID" "$PLIST"
plutil -insert CFBundleInfoDictionaryVersion -string 6.0 "$PLIST"
plutil -insert CFBundleName -string "$APP_NAME" "$PLIST"
plutil -insert CFBundlePackageType -string APPL "$PLIST"
plutil -insert CFBundleShortVersionString -string "$VERSION" "$PLIST"
plutil -insert CFBundleVersion -string "$VERSION" "$PLIST"
plutil -insert LSMinimumSystemVersion -string "$MACOSX_DEPLOYMENT_TARGET" "$PLIST"
plutil -insert LSUIElement -bool true "$PLIST"
plutil -insert NSAppleEventsUsageDescription -string "AppleMusicRPC reads the current track and original album cover from Music to update Discord Rich Presence." "$PLIST"
chmod +x "$CONTENTS_DIR/MacOS/$APP_NAME"
xattr -cr "$STAGED_APP"
codesign --force --sign - --identifier "$BUNDLE_ID" "$STAGED_APP"
codesign --verify --strict "$STAGED_APP"
plutil -lint "$PLIST" >/dev/null

# A failed compilation/signing leaves the existing app intact.
if [[ -d "$APP_DIR" ]]; then
  mv "$APP_DIR" "$STAGING_DIR/previous.app"
fi
if ! mv "$STAGED_APP" "$APP_DIR"; then
  if [[ -d "$STAGING_DIR/previous.app" ]]; then mv "$STAGING_DIR/previous.app" "$APP_DIR"; fi
  exit 1
fi
xattr -d com.apple.FinderInfo "$APP_DIR" >/dev/null 2>&1 || true
# FileProvider may restore FinderInfo in an iCloud destination. Resource seals
# and symlinks still need verification; sideband metadata is checked in staging.
codesign --verify --strict=symlinks "$APP_DIR"
echo "$APP_DIR"
