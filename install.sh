#!/usr/bin/env bash
#
# install.sh — Install pingm to /usr/local/bin
#
set -e

INSTALL_DIR="/usr/local/bin"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SOURCE="${SCRIPT_DIR}/pingm"

if [[ ! -f "$SOURCE" ]]; then
    echo "Error: pingm not found in ${SCRIPT_DIR}" >&2
    exit 1
fi

echo "Installing pingm to ${INSTALL_DIR}/pingm ..."

if [[ -w "$INSTALL_DIR" ]]; then
    cp "$SOURCE" "${INSTALL_DIR}/pingm"
    chmod +x "${INSTALL_DIR}/pingm"
else
    echo "Need sudo to write to ${INSTALL_DIR}"
    sudo cp "$SOURCE" "${INSTALL_DIR}/pingm"
    sudo chmod +x "${INSTALL_DIR}/pingm"
fi

echo "✔ Installed: $(${INSTALL_DIR}/pingm -v)"
echo "  Run 'pingm -h' to get started."
