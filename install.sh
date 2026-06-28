#!/bin/bash

# Komari (PG Support Fork) — One-click Install Script
# Repository: https://github.com/vaspike/komari

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()  { echo -e "$1"; }
log_ok()   { echo -e "${GREEN}$1${NC}"; }
log_warn() { echo -e "${YELLOW}$1${NC}"; }
log_err()  { echo -e "${RED}$1${NC}"; }

GITHUB_REPO="vaspike/komari"
INSTALL_DIR="/opt/komari"
DATA_DIR="/opt/komari/data"
SERVICE_NAME="komari"
BINARY_PATH="$INSTALL_DIR/komari"

# --- defaults ---
DB_TYPE="${KOMARI_DB_TYPE:-sqlite}"
LISTEN_PORT="${KOMARI_LISTEN:-25774}"
DB_FILE="${KOMARI_DB_FILE:-$DATA_DIR/komari.db}"
DB_HOST="${KOMARI_DB_HOST:-127.0.0.1}"
DB_PORT="${KOMARI_DB_PORT:-5432}"
DB_USER="${KOMARI_DB_USER:-komari}"
DB_PASS="${KOMARI_DB_PASS:-}"
DB_NAME="${KOMARI_DB_NAME:-komari}"

detect_arch() {
    case "$(uname -m)" in
        x86_64)  echo "amd64" ;;
        aarch64) echo "arm64" ;;
        riscv64) echo "riscv64" ;;
        *) log_err "Unsupported arch: $(uname -m)"; exit 1 ;;
    esac
}

download_latest() {
    local arch="$1"
    local file="komari-linux-${arch}"
    local url="https://github.com/${GITHUB_REPO}/releases/latest/download/${file}"

    log_info "Downloading $url ..."
    curl -fsSL -o "$BINARY_PATH" "$url" || {
        log_err "Download failed. Check if a release exists at: https://github.com/${GITHUB_REPO}/releases"
        exit 1
    }
    chmod +x "$BINARY_PATH"
    log_ok "Binary installed: $BINARY_PATH"
}

write_systemd_service() {
    cat > "/etc/systemd/system/${SERVICE_NAME}.service" << SERVICE_EOF
[Unit]
Description=Komari Monitor Service
After=network.target
$([ "$DB_TYPE" = "postgres" ] && echo "After=postgresql.service\nWants=postgresql.service")

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
Environment=KOMARI_DB_TYPE=$DB_TYPE
$([ "$DB_TYPE" = "sqlite" ]     && echo "Environment=KOMARI_DB_FILE=$DB_FILE")
$([ "$DB_TYPE" = "postgres" ]   && echo "Environment=KOMARI_DB_HOST=$DB_HOST")
$([ "$DB_TYPE" = "postgres" ]   && echo "Environment=KOMARI_DB_PORT=$DB_PORT")
$([ "$DB_TYPE" = "postgres" ]   && echo "Environment=KOMARI_DB_USER=$DB_USER")
$([ "$DB_TYPE" = "postgres" ]   && echo "Environment=KOMARI_DB_PASS=$DB_PASS")
$([ "$DB_TYPE" = "postgres" ]   && echo "Environment=KOMARI_DB_NAME=$DB_NAME")
ExecStart=$BINARY_PATH server -l 0.0.0.0:${LISTEN_PORT}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
SERVICE_EOF

    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"
    systemctl restart "$SERVICE_NAME"
    log_ok "systemd service created"
}

show_install_guide() {
    echo
    echo "============================================"
    echo "  Komari installed successfully!"
    echo "============================================"
    echo
    echo "  Version:  $(curl -s "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" | grep tag_name | cut -d'"' -f4)"
    echo "  URL:     http://\$(hostname -I | awk '{print \$1}'):${LISTEN_PORT}"
    echo "  DB type: $DB_TYPE"
    echo
    echo "  Get admin password:"
    echo "    journalctl -u $SERVICE_NAME -n 100 | grep 'admin account created'"
    echo
    echo "  Commands:"
    echo "    systemctl status/restart/stop $SERVICE_NAME"
    echo "    journalctl -u $SERVICE_NAME -f"
    echo
}

# ===== main =====
if [ "$EUID" -ne 0 ]; then
    log_err "Please run as root: sudo bash install.sh"
    exit 1
fi

echo
log_info "Komari Installer (PG-Support Fork)"
log_info "Repo: https://github.com/${GITHUB_REPO}"
echo

# --- DB type ---
if [ -z "$KOMARI_DB_TYPE" ]; then
    echo "Select database type:"
    echo "  1) SQLite    (default, zero dependency)"
    echo "  2) PostgreSQL (requires PostgreSQL pre-installed)"
    read -p "Choice [1]: " db_choice
    case "$db_choice" in
        2) DB_TYPE="postgres" ;;
        *) DB_TYPE="sqlite" ;;
    esac
fi
log_info "Database type: $DB_TYPE"

# --- PG options ---
if [ "$DB_TYPE" = "postgres" ]; then
    [ -z "$KOMARI_DB_HOST" ] && read -p "PG host     [127.0.0.1]: " DB_HOST && DB_HOST="${DB_HOST:-127.0.0.1}"
    [ -z "$KOMARI_DB_PORT" ] && read -p "PG port     [5432]: "      DB_PORT && DB_PORT="${DB_PORT:-5432}"
    [ -z "$KOMARI_DB_USER" ] && read -p "PG user     [komari]: "     DB_USER && DB_USER="${DB_USER:-komari}"
    [ -z "$KOMARI_DB_PASS" ] && read -p "PG password: "             DB_PASS
    [ -z "$KOMARI_DB_NAME" ] && read -p "PG database [komari]: "     DB_NAME && DB_NAME="${DB_NAME:-komari}"
    echo
fi

mkdir -p "$INSTALL_DIR" "$DATA_DIR"

# --- install binary ---
download_latest "$(detect_arch)"

# --- systemd ---
write_systemd_service

# --- wait for startup ---
sleep 3
if systemctl is-active --quiet "$SERVICE_NAME"; then
    log_ok "Service is running"
    show_install_guide
else
    log_err "Service failed to start. Check: journalctl -u $SERVICE_NAME -n 50"
    exit 1
fi
