#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_PATH="${APP_PATH:-$ROOT_DIR/mac/dist/AppleMusicRPC.app}"
LABEL="${LABEL:-com.feralthedogg.AppleMusicRPC}"
DOMAIN="gui/$(id -u)"
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/amrpc-update.XXXXXX")"
trap 'rm -rf "$BUILD_DIR"' EXIT

if [[ -z "${CONFIG_SRC:-}" && -f "$APP_PATH/Contents/Resources/config.json" ]]; then
  export CONFIG_SRC="$APP_PATH/Contents/Resources/config.json"
fi
# Keep the current app running until the new signed build is ready.
DIST_DIR="$BUILD_DIR" "$ROOT_DIR/scripts/build-macos.sh"
NEW_APP="$BUILD_DIR/AppleMusicRPC.app"
"$NEW_APP/Contents/MacOS/AppleMusicRPC" --version

WAS_SERVICE=false
if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then WAS_SERVICE=true; fi
launchctl bootout "$DOMAIN/$LABEL" >/dev/null 2>&1 || true
pkill -TERM -x AppleMusicRPC >/dev/null 2>&1 || true
for attempt in {1..100}; do
  if ! pgrep -x AppleMusicRPC >/dev/null; then break; fi
  sleep 0.1
done
if pgrep -x AppleMusicRPC >/dev/null; then
  echo "The old app is still running; update was not installed." >&2
  exit 1
fi

BACKUP_DIR="$HOME/Library/Application Support/Feralthedogg/AppleMusicRPC/backups"
mkdir -p "$BACKUP_DIR" "$(dirname "$APP_PATH")"
BACKUP_PATH="$(mktemp -d "$BACKUP_DIR/update.XXXXXX")/AppleMusicRPC.app"
if [[ -d "$APP_PATH" ]]; then mv "$APP_PATH" "$BACKUP_PATH"; fi
if ! mv "$NEW_APP" "$APP_PATH"; then
  if [[ -d "$BACKUP_PATH" ]]; then mv "$BACKUP_PATH" "$APP_PATH"; fi
  if $WAS_SERVICE; then APP_PATH="$APP_PATH" "$ROOT_DIR/scripts/install-macos-launchagent.sh"; fi
  exit 1
fi

# iCloud Desktop may attach FinderInfo when the bundle is moved into place.
xattr -d com.apple.FinderInfo "$APP_PATH" >/dev/null 2>&1 || true
codesign --verify --strict=symlinks "$APP_PATH"

if $WAS_SERVICE; then
  APP_PATH="$APP_PATH" LABEL="$LABEL" "$ROOT_DIR/scripts/install-macos-launchagent.sh"
else
  open "$APP_PATH"
fi

# Wait for the new process to finish initializing its native menu and Music
# provider. LaunchServices can set the bundle's FinderInfo during that startup.
STATUS_PATH="$HOME/Library/Application Support/Feralthedogg/AppleMusicRPC/status.json"
for attempt in {1..100}; do
  CURRENT_PID="$(pgrep -x AppleMusicRPC | head -n 1 || true)"
  STATUS_PID="$(plutil -extract pid raw -o - "$STATUS_PATH" 2>/dev/null || true)"
  STATUS_RUNNING="$(plutil -extract running raw -o - "$STATUS_PATH" 2>/dev/null || true)"
  if [[ -n "$CURRENT_PID" && "$CURRENT_PID" == "$STATUS_PID" && "$STATUS_RUNNING" == "true" ]]; then break; fi
  sleep 0.1
done
if [[ -z "$CURRENT_PID" || "$CURRENT_PID" != "$STATUS_PID" || "$STATUS_RUNNING" != "true" ]]; then
  echo "The updated app did not finish starting; check ~/Library/Logs/AppleMusicRPC/launchd.err.log." >&2
  exit 1
fi
xattr -d com.apple.FinderInfo "$APP_PATH" >/dev/null 2>&1 || true
codesign --verify --strict=symlinks "$APP_PATH"
echo "Updated: $APP_PATH"
echo "Previous app: $BACKUP_PATH"
