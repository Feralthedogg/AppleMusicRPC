#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE_DIR="$ROOT_DIR/windows"

APP_NAME="${APP_NAME:-AppleMusicRPC}"
BUNDLE_ID="${BUNDLE_ID:-com.feralthedogg.AppleMusicRPC}"
VERSION="${VERSION:-1.0.1}"
DIST_DIR="${DIST_DIR:-$ROOT_DIR/mac/dist}"
CONFIG_SRC="${CONFIG_SRC:-$MODULE_DIR/dist/config.json}"

APP_DIR="$DIST_DIR/$APP_NAME.app"
CONTENTS_DIR="$APP_DIR/Contents"
MACOS_DIR="$CONTENTS_DIR/MacOS"
RESOURCES_DIR="$CONTENTS_DIR/Resources"
EXECUTABLE="$MACOS_DIR/$APP_NAME"

if [[ ! -f "$CONFIG_SRC" ]]; then
  echo "config not found: $CONFIG_SRC" >&2
  exit 1
fi

rm -rf "$APP_DIR"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR"

(
  cd "$MODULE_DIR"
  CGO_ENABLED=1 GOOS=darwin go build \
    -trimpath \
    -ldflags="-s -w" \
    -o "$EXECUTABLE" \
    ./cmd/amrpc
)

cp "$CONFIG_SRC" "$RESOURCES_DIR/config.json"

cat > "$CONTENTS_DIR/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>en</string>
  <key>CFBundleDisplayName</key>
  <string>$APP_NAME</string>
  <key>CFBundleExecutable</key>
  <string>$APP_NAME</string>
  <key>CFBundleIdentifier</key>
  <string>$BUNDLE_ID</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>$APP_NAME</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>$VERSION</string>
  <key>CFBundleVersion</key>
  <string>$VERSION</string>
  <key>LSMinimumSystemVersion</key>
  <string>12.0</string>
  <key>LSUIElement</key>
  <true/>
  <key>NSAppleEventsUsageDescription</key>
  <string>AppleMusicRPC reads the currently playing track from Music to update Discord Rich Presence.</string>
</dict>
</plist>
PLIST

chmod +x "$EXECUTABLE"

if command -v xattr >/dev/null 2>&1; then
  xattr -cr "$APP_DIR"
fi

if command -v codesign >/dev/null 2>&1; then
  codesign --force --deep --sign - "$APP_DIR" >/dev/null
fi

echo "$APP_DIR"
