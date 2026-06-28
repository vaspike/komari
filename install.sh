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

detect_os() {
    if command -v apt >/dev/null 2>&1; then
        echo "apt"
    elif command -v yum >/dev/null 2>&1; then
        echo "yum"
    elif command -v dnf >/dev/null 2>&1; then
        echo "dnf"
    elif command -v apk >/dev/null 2>&1; then
        echo "apk"
    else
        echo "unknown"
    fi
}

detect_arch() {
    case "$(uname -m)" in
        x86_64)  echo "amd64" ;;
        aarch64) echo "arm64" ;;
        riscv64) echo "riscv64" ;;
        *) log_err "Unsupported arch: $(uname -m)"; exit 1 ;;
    esac
}

install_postgresql() {
    if command -v psql >/dev/null 2>&1; then
        log_ok "PostgreSQL already installed"
        return 0
    fi

    log_info "Installing PostgreSQL..."
    local os="$(detect_os)"
    case "$os" in
        apt)
            apt-get update -qq
            apt-get install -y -qq postgresql
            ;;
        yum)
            yum install -y postgresql-server
            sudo postgresql-setup --initdb
            ;;
        dnf)
            dnf install -y postgresql-server
            sudo postgresql-setup --initdb
            ;;
        apk)
            apk add postgresql
            ;;
        *)
            log_err "Cannot install PostgreSQL automatically. Please install it manually."
            exit 1
            ;;
    esac

    systemctl enable postgresql
    systemctl start postgresql
    sleep 2
    log_ok "PostgreSQL installed and running"
}

setup_postgresql_db() {
    # Create user if not exists
    if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='$DB_USER'" 2>/dev/null | grep -q 1; then
        sudo -u postgres createuser --superuser "$DB_USER" 2>/dev/null || true
        log_info "PG user '$DB_USER' created"
    else
        log_info "PG user '$DB_USER' already exists"
    fi

    # Set password
    local pass="${DB_PASS:-$(openssl rand -base64 16 | tr -d '/+=' )}"
    sudo -u postgres psql -c "ALTER USER \"$DB_USER\" PASSWORD '$pass';" >/dev/null
    DB_PASS="$pass"
    log_info "PG password set"

    # Create database if not exists
    if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" 2>/dev/null | grep -q 1; then
        sudo -u postgres createdb -O "$DB_USER" "$DB_NAME" 2>/dev/null || true
        log_info "PG database '$DB_NAME' created"
    else
        log_info "PG database '$DB_NAME' already exists"
    fi
}

install_nginx() {
    if command -v nginx >/dev/null 2>&1; then
        log_ok "Nginx already installed"
        return 0
    fi

    log_info "Installing Nginx..."
    local os="$(detect_os)"
    case "$os" in
        apt) apt-get install -y -qq nginx ;;
        yum|dnf) "$os" install -y nginx ;;
        apk) apk add nginx ;;
        *) log_warn "Cannot install Nginx. Skipping reverse proxy setup."; return 1 ;;
    esac
    systemctl enable nginx
    systemctl start nginx
    log_ok "Nginx installed"
}

write_nginx_config() {
    local port="${LISTEN_PORT:-25774}"
    local proxy_port="${NGINX_PORT:-8443}"

    mkdir -p /etc/nginx/ssl
    if [ ! -f /etc/nginx/ssl/komari.crt ]; then
        openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
            -keyout /etc/nginx/ssl/komari.key \
            -out /etc/nginx/ssl/komari.crt \
            -subj "/CN=komari" 2>/dev/null
    fi

    cat > /etc/nginx/conf.d/komari.conf << NGINX_EOF
map \$http_upgrade \$connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen ${proxy_port} ssl;
    server_name _;

    ssl_certificate /etc/nginx/ssl/komari.crt;
    ssl_certificate_key /etc/nginx/ssl/komari.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;

    client_max_body_size 5g;
    proxy_read_timeout 300s;
    proxy_send_timeout 300s;

    location / {
        proxy_pass http://127.0.0.1:${port};
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Origin '';
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$connection_upgrade;
    }
}
NGINX_EOF

    nginx -t 2>/dev/null && nginx -s reload 2>/dev/null || systemctl restart nginx
    log_ok "Nginx reverse proxy ready on port $proxy_port"
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
    local access_port="${LISTEN_PORT}"
    local access_scheme="http"

    if command -v nginx >/dev/null 2>&1 && nginx -t >/dev/null 2>&1 && grep -q 'listen 8443' /etc/nginx/conf.d/komari.conf 2>/dev/null; then
        access_port="8443"
        access_scheme="https"
    fi

    echo
    echo "============================================"
    echo "  Komari installed successfully!"
    echo "============================================"
    echo
    echo "  Version:  $(curl -s "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" | grep tag_name | cut -d'"' -f4 2>/dev/null || echo 'v1.2.5-pg1')"
    echo "  URL:     ${access_scheme}://\$(hostname -I | awk '{print \$1}'):${access_port}"
    echo "  DB type: $DB_TYPE"
    if [ "$DB_TYPE" = "postgres" ]; then
        echo "  PG user: $DB_USER  /  password: $DB_PASS  /  database: $DB_NAME"
    fi
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
    echo "  2) PostgreSQL (auto-install if needed)"
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
    [ -z "$KOMARI_DB_PASS" ] && read -p "PG password [auto-generate]: " DB_PASS
    [ -z "$KOMARI_DB_NAME" ] && read -p "PG database [komari]: "     DB_NAME && DB_NAME="${DB_NAME:-komari}"
    echo

    install_postgresql
    setup_postgresql_db
fi

mkdir -p "$INSTALL_DIR" "$DATA_DIR"

# --- install binary ---
download_latest "$(detect_arch)"

# --- systemd ---
write_systemd_service

# --- nginx (optional) ---
if [ "$DB_TYPE" = "postgres" ] || command -v nginx >/dev/null 2>&1; then
    echo
    read -p "Install Nginx reverse proxy on port 8443? [Y/n]: " nginx_choice
    if [[ ! "$nginx_choice" =~ ^[Nn]$ ]]; then
        install_nginx && write_nginx_config
    fi
fi

# --- wait for startup ---
sleep 3
if systemctl is-active --quiet "$SERVICE_NAME"; then
    log_ok "Service is running"
    show_install_guide
else
    log_err "Service failed to start. Check: journalctl -u $SERVICE_NAME -n 50"
    exit 1
fi
