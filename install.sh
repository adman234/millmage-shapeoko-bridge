#!/bin/sh
# Installs MillMage Shapeoko Bridge on Linux as a systemd service.
#   curl -fsSL https://raw.githubusercontent.com/adman234/millmage-shapeoko-bridge/main/install.sh | sudo sh
# Running it again updates the program and keeps your settings.
# To remove: sudo sh install.sh --uninstall
set -eu

REPO="adman234/millmage-shapeoko-bridge"
BIN="/usr/local/bin/millmage-bridge"
CONF_DIR="/etc/millmage-bridge"
CONF="$CONF_DIR/bridge.conf"
UNIT="/etc/systemd/system/millmage-bridge.service"

if [ "$(id -u)" -ne 0 ]; then
    echo "Please run this with sudo." >&2
    exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
    echo "This installer needs systemd. Download the program from" >&2
    echo "https://github.com/$REPO/releases and run it your own way." >&2
    exit 1
fi

if [ "${1:-}" = "--uninstall" ]; then
    systemctl disable --now millmage-bridge.service 2>/dev/null || true
    rm -f "$UNIT" "$BIN"
    systemctl daemon-reload
    echo "Removed. Settings stay in $CONF_DIR and jobs in /var/lib/millmage-bridge."
    exit 0
fi

case "$(uname -m)" in
    x86_64 | amd64) ARCH="amd64" ;;
    aarch64 | arm64) ARCH="arm64" ;;
    armv7l | armv8l) ARCH="arm" ;;
    *)
        echo "Unsupported processor: $(uname -m)" >&2
        exit 1
        ;;
esac
URL="https://github.com/$REPO/releases/latest/download/millmage-bridge-linux-$ARCH"

echo "Downloading $URL"
TMP="$(mktemp)"
if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$TMP" "$URL"
else
    wget -qO "$TMP" "$URL"
fi
install -m 0755 "$TMP" "$BIN"
rm -f "$TMP"

mkdir -p "$CONF_DIR"
if [ ! -f "$CONF" ]; then
    cat >"$CONF" <<'EOF'
# MillMage Shapeoko Bridge settings. After changing them:
#   sudo systemctl restart millmage-bridge
# All options: https://github.com/adman234/millmage-shapeoko-bridge#settings
listen = :23
http = :8080
backend = carbide
# Address of the computer running Carbide Motion.
carbide-addr = 127.0.0.1:6280
jobs-dir = /var/lib/millmage-bridge/jobs
require-end = true
travel = 838,444,100
EOF
fi

cat >"$UNIT" <<EOF
[Unit]
Description=MillMage Shapeoko Bridge
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$BIN -config $CONF
Restart=always
RestartSec=5
DynamicUser=yes
StateDirectory=millmage-bridge
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable millmage-bridge.service >/dev/null 2>&1
systemctl restart millmage-bridge.service
sleep 1

IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "$IP" ] || IP="<this computer>"

echo
echo "MillMage Shapeoko Bridge $("$BIN" version) is installed and running."
echo "  Settings: $CONF"
echo "  Jobs:     /var/lib/millmage-bridge/jobs"
echo "  Log:      journalctl -u millmage-bridge -f"
echo
echo "Next steps:"
echo "  1. In Carbide Motion, open Settings and switch on Allow Remote Access."
echo "     If Carbide Motion runs on another computer, put its address in"
echo "     carbide-addr in $CONF and restart the service."
echo "  2. In MillMage, add a GRBL device with a network (TCP) connection to $IP, port 23."
echo "  3. Status page: http://$IP:8080"
