#!/usr/bin/env bash
# install.sh — build and install ReminderRelay as a launchd user agent.
#
# Usage: bash deployment/install.sh [--config <path>]
#
# Requirements: devbox (or Go 1.24+ in PATH), macOS, iCloud sign-in.
set -euo pipefail

BINARY_NAME="reminderrelay"
APP_DIR="${HOME}/Applications/ReminderRelay.app"
BINARY_PATH="${APP_DIR}/Contents/MacOS/${BINARY_NAME}"
INFO_PLIST="internal/setup/app_info.plist"
ENTITLEMENTS="internal/setup/entitlements.plist"
SIGN_IDENTITY="${REMINDERRELAY_CODESIGN_IDENTITY:--}"
PLIST_LABEL="com.github.nworb-cire.reminderrelay"
PLIST_TEMPLATE="$(dirname "$0")/${PLIST_LABEL}.plist"
PLIST_DEST="${HOME}/Library/LaunchAgents/${PLIST_LABEL}.plist"
LOG_DIR="${HOME}/Library/Logs/reminderrelay"

# --------------------------------------------------------------------------- #
# 1. Build
# --------------------------------------------------------------------------- #
echo "→ Building ${BINARY_NAME}…"
if command -v devbox &>/dev/null; then
    devbox run -- just build
elif command -v just &>/dev/null; then
    just build
else
    echo "just is required so the binary receives its macOS privacy metadata" >&2
    exit 1
fi

# --------------------------------------------------------------------------- #
# 2. Install binary
# --------------------------------------------------------------------------- #
echo "→ Installing application to ${APP_DIR}"
mkdir -p "${APP_DIR}/Contents/MacOS"
install -m 755 "${BINARY_NAME}" "${BINARY_PATH}"
install -m 644 "${INFO_PLIST}" "${APP_DIR}/Contents/Info.plist"
codesign --force --deep --options runtime --timestamp=none --sign "${SIGN_IDENTITY}" --entitlements "${ENTITLEMENTS}" "${APP_DIR}"
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "${APP_DIR}"
rm -f "${BINARY_NAME}"

# --------------------------------------------------------------------------- #
# 3. Create log directory
# --------------------------------------------------------------------------- #
mkdir -p "${LOG_DIR}"

# --------------------------------------------------------------------------- #
# 4. Install plist (substitute __HOME__ placeholder)
# --------------------------------------------------------------------------- #
echo "→ Installing launchd plist to ${PLIST_DEST}"
mkdir -p "$(dirname "${PLIST_DEST}")"
sed "s|__HOME__|${HOME}|g" "${PLIST_TEMPLATE}" > "${PLIST_DEST}"

# --------------------------------------------------------------------------- #
# 5. Load the agent
# --------------------------------------------------------------------------- #
echo "→ Loading launchd agent…"
if launchctl list | grep -q "${PLIST_LABEL}" 2>/dev/null; then
    launchctl unload "${PLIST_DEST}" 2>/dev/null || true
fi
launchctl load "${PLIST_DEST}"

echo ""
echo "✓ ReminderRelay installed and running."
echo ""
echo "  Logs:    ${LOG_DIR}/"
echo "  Config:  ${HOME}/.config/reminderrelay/config.yaml"
echo "  DB:      ${HOME}/.local/share/reminderrelay/state.db"
echo ""
echo "If you haven't created a config file yet:"
echo "  mkdir -p ${HOME}/.config/reminderrelay"
echo "  cp config.example.yaml ${HOME}/.config/reminderrelay/config.yaml"
echo "  # then edit it with your HA URL and token"
