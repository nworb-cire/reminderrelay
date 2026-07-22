#!/usr/bin/env bash
# uninstall.sh — stop and remove ReminderRelay.
#
# Usage: bash deployment/uninstall.sh [--purge]
#
# --purge also removes the config file and state database.
set -euo pipefail

BINARY_NAME="reminderrelay"
APP_DIR="${HOME}/Applications/ReminderRelay.app"
PLIST_LABEL="com.github.njoerd114.reminderrelay"
PLIST_DEST="${HOME}/Library/LaunchAgents/${PLIST_LABEL}.plist"

PURGE=false
for arg in "$@"; do
    [[ "$arg" == "--purge" ]] && PURGE=true
done

# --------------------------------------------------------------------------- #
# 1. Unload launchd agent
# --------------------------------------------------------------------------- #
if [[ -f "${PLIST_DEST}" ]]; then
    echo "→ Unloading launchd agent…"
    launchctl unload "${PLIST_DEST}" 2>/dev/null || true
    rm -f "${PLIST_DEST}"
    echo "  Removed ${PLIST_DEST}"
else
    echo "  Plist not found, skipping unload."
fi

# --------------------------------------------------------------------------- #
# 2. Remove binary
# --------------------------------------------------------------------------- #
if [[ -d "${APP_DIR}" ]]; then
    echo "→ Removing application…"
    rm -rf "${APP_DIR}"
    echo "  Removed ${APP_DIR}"
else
    echo "  Application not found at ${APP_DIR}, skipping."
fi

# --------------------------------------------------------------------------- #
# 3. Optional purge
# --------------------------------------------------------------------------- #
if [[ "${PURGE}" == true ]]; then
    echo "→ Purging config and state database…"
    rm -rf "${HOME}/.config/reminderrelay"
    rm -rf "${HOME}/.local/share/reminderrelay"
    rm -rf "${HOME}/Library/Logs/reminderrelay"
    echo "  Config, state DB, and logs removed."
else
    echo ""
    echo "  Config and state DB preserved."
    echo "  Run with --purge to also remove them:"
    echo "    bash deployment/uninstall.sh --purge"
fi

echo ""
echo "✓ ReminderRelay uninstalled."
