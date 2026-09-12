#!/usr/bin/env bash
#
# install.sh — Build pingm and install it to /usr/local/bin
#
set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

if ! command -v go >/dev/null 2>&1; then
    echo "Error: Go is required to build pingm (https://go.dev/dl/)" >&2
    exit 1
fi

echo "Building pingm ..."
cd "$SCRIPT_DIR"
go build -trimpath -ldflags "-s -w" -o pingm .

echo "Installing to ${INSTALL_DIR}/pingm ..."
if [[ -w "$INSTALL_DIR" ]]; then
    install -m 0755 pingm "${INSTALL_DIR}/pingm"
else
    echo "Need sudo to write to ${INSTALL_DIR}"
    sudo install -m 0755 pingm "${INSTALL_DIR}/pingm"
fi

echo "✔ Installed: $(${INSTALL_DIR}/pingm -v)"
echo "  Run 'pingm -h' to get started."

# pingm uses an unprivileged ICMP datagram socket, which needs no special
# rights on macOS. Most current Linux distributions allow it too; the sysctl
# below is the fix on the ones that do not.
if [[ "$(uname -s)" == "Linux" ]]; then
    range=$(sysctl -n net.ipv4.ping_group_range 2>/dev/null || echo "1 0")
    read -r low high <<< "$range"
    if (( low > high )); then
        echo
        echo "Note: this kernel restricts unprivileged ICMP, so pingm would need root."
        echo "      To allow it for everyone:"
        echo "        sudo sysctl -w net.ipv4.ping_group_range=\"0 2147483647\""
    fi
fi
