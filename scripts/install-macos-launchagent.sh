#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_PATH="${APP_PATH:-$ROOT_DIR/mac/dist/AppleMusicRPC.app}"
APP_EXE="$APP_PATH/Contents/MacOS/AppleMusicRPC"
LABEL="${LABEL:-com.feralthedogg.AppleMusicRPC}"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG_DIR="$HOME/Library/Logs/AppleMusicRPC"
DOMAIN="gui/$(id -u)"

if [[ ! -x "$APP_EXE" ]]; then
  echo "app executable not found: $APP_EXE" >&2
  echo "run ./scripts/build-macos.sh first" >&2
  exit 1
fi

mkdir -p "$(dirname "$PLIST")" "$LOG_DIR"
TEMP_PLIST="$(mktemp "$(dirname "$PLIST")/.amrpc-launchagent.XXXXXX")"
trap 'rm -f "$TEMP_PLIST"' EXIT
plutil -create xml1 "$TEMP_PLIST"
plutil -insert Label -string "$LABEL" "$TEMP_PLIST"
plutil -insert ProgramArguments -xml '<array/>' "$TEMP_PLIST"
plutil -insert ProgramArguments.0 -string "$APP_EXE" "$TEMP_PLIST"
plutil -insert RunAtLoad -bool true "$TEMP_PLIST"
# Quit should stay quit; only crashes should restart the app.
plutil -insert KeepAlive -xml '<dict/>' "$TEMP_PLIST"
plutil -insert KeepAlive.SuccessfulExit -bool false "$TEMP_PLIST"
plutil -insert ProcessType -string Interactive "$TEMP_PLIST"
plutil -insert LimitLoadToSessionType -string Aqua "$TEMP_PLIST"
plutil -insert ThrottleInterval -integer 10 "$TEMP_PLIST"
plutil -insert ExitTimeOut -integer 20 "$TEMP_PLIST"
plutil -insert StandardOutPath -string "$LOG_DIR/launchd.out.log" "$TEMP_PLIST"
plutil -insert StandardErrorPath -string "$LOG_DIR/launchd.err.log" "$TEMP_PLIST"
plutil -lint "$TEMP_PLIST" >/dev/null

launchctl bootout "$DOMAIN/$LABEL" >/dev/null 2>&1 || true
mv "$TEMP_PLIST" "$PLIST"
launchctl enable "$DOMAIN/$LABEL"
launchctl bootstrap "$DOMAIN" "$PLIST"
echo "$PLIST"
