#!/usr/bin/env bash
set -euo pipefail

LABEL="${LABEL:-com.feralthedogg.AppleMusicRPC}"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

launchctl bootout "gui/$(id -u)" "$PLIST" >/dev/null 2>&1 || true
rm -f "$PLIST"
