#!/usr/bin/env bash
set -euo pipefail

SERVER_URL='__SERVER_URL__'
TOKEN="${SM_TOKEN:-}"
INTERVAL="${SM_INTERVAL:-10}"
INSECURE="${SM_INSECURE:-0}"
PERSIST_INSECURE="${SM_PERSIST_INSECURE:-0}"
ENABLE_SMART="${SM_ENABLE_SMART:-0}"
ENABLE_SMART_NVME="${SM_ENABLE_SMART_NVME:-0}"
ENABLE_DOCKER="${SM_ENABLE_DOCKER:-0}"
ENABLE_GPU="${SM_ENABLE_GPU:-0}"
ENABLE_NETWORK="${SM_ENABLE_NETWORK:-0}"
ENABLE_PORT_OWNERS="${SM_ENABLE_PORT_OWNERS:-0}"
ENABLE_ALL="${SM_ENABLE_ALL:-0}"
if [ "$ENABLE_ALL" = "1" ]; then
    ENABLE_SMART=1
    ENABLE_DOCKER=1
    ENABLE_GPU=1
    ENABLE_NETWORK=1
    ENABLE_PORT_OWNERS=1
fi
[ "$ENABLE_SMART_NVME" = "1" ] && ENABLE_SMART=1

err() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "$1 is required"; }

has_nvme_device() {
    local dev
    for dev in /dev/nvme*; do
        [ -e "$dev" ] && return 0
    done
    return 1
}

warn_nvme_smart() {
    has_nvme_device || return 0
    if [ "$ENABLE_SMART_NVME" = "1" ]; then
        cat >&2 <<'WARN'

WARNING: SM_ENABLE_SMART_NVME grants CAP_SYS_ADMIN to the sm-agent service.
         NVMe SMART reads issue NVME_IOCTL_ADMIN_CMD, which the kernel gates
         behind CAP_SYS_ADMIN regardless of file or group permissions.
         CAP_SYS_ADMIN is near-root and widens the agent well beyond the rest
         of this hardened unit. Keep it only where NVMe SMART is worth that.

WARN
    else
        cat >&2 <<'WARN'

WARNING: NVMe device(s) detected, but SM_ENABLE_SMART grants only CAP_SYS_RAWIO.
         CAP_SYS_RAWIO covers SATA/SAS SMART (SG_IO); it does NOT cover NVMe,
         whose SMART reads need CAP_SYS_ADMIN. smartctl will scan these devices
         but report no SMART data for them. To collect NVMe SMART, re-run with
         SM_ENABLE_SMART_NVME=1 (adds CAP_SYS_ADMIN, near-root). SATA/SAS is fine.

WARN
    fi
}

install_smartmontools() {
    if command -v smartctl >/dev/null 2>&1; then
        printf 'smartmontools already present: %s\n' "$(command -v smartctl)"
        return 0
    fi
    printf 'installing smartmontools (required by SMART collector) ...\n'
    if command -v apt-get >/dev/null 2>&1; then
        DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null 2>&1 || true
        DEBIAN_FRONTEND=noninteractive apt-get install -y -qq smartmontools >/dev/null 2>&1 || true
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y -q smartmontools >/dev/null 2>&1 || true
    elif command -v yum >/dev/null 2>&1; then
        yum install -y -q smartmontools >/dev/null 2>&1 || true
    elif command -v zypper >/dev/null 2>&1; then
        zypper --non-interactive --quiet install smartmontools >/dev/null 2>&1 || true
    elif command -v apk >/dev/null 2>&1; then
        apk add --no-cache --quiet smartmontools >/dev/null 2>&1 || true
    elif command -v pacman >/dev/null 2>&1; then
        pacman -S --noconfirm --needed --quiet smartmontools >/dev/null 2>&1 || true
    else
        printf 'warning: no known package manager; install smartmontools manually for SMART support\n' >&2
        return 0
    fi
    if command -v smartctl >/dev/null 2>&1; then
        printf 'smartmontools installed: %s\n' "$(command -v smartctl)"
    else
        printf 'warning: smartmontools install attempt finished but smartctl is not on PATH; install manually for SMART support\n' >&2
    fi
}

[ "$(id -u)" = "0" ] || err "run as root (use sudo)"
need curl
need install
need uname
need useradd

REINSTALL="${SM_REINSTALL:-0}"
RECONFIGURE=0
if [ "$REINSTALL" != "1" ] && [ -f /etc/servermonitor/agent.toml ]; then
    RECONFIGURE=1
    [ -x /opt/servermonitor/sm-agent ] || err "found /etc/servermonitor/agent.toml but no agent binary at /opt/servermonitor/sm-agent; re-run with SM_REINSTALL=1 for a full install"
    cat >&2 <<'NOTE'

reconfiguring the existing sm-agent install in place: re-applying capabilities
and group memberships from the SM_ENABLE_* flags, keeping the current identity
and binary. No re-registration and no token needed. To force a full fresh
install instead (re-download the binary, rewrite the config), set SM_REINSTALL=1.

NOTE
fi

if [ "$RECONFIGURE" != "1" ]; then
if [ -z "$TOKEN" ]; then
    if [ -r /dev/tty ]; then
        printf 'Agent token: ' >/dev/tty
        IFS= read -rs TOKEN < /dev/tty
        printf '\n' >/dev/tty
    else
        err "agent token required: set SM_TOKEN env var or run in an interactive terminal"
    fi
fi
[ -n "$TOKEN" ] || err "no agent token supplied"

case "$SERVER_URL" in
    https://*) ;;
    http://*)
        [ "$INSECURE" = "1" ] || err "refusing http:// server URL (agent token would leak); set SM_INSECURE=1 to override (debug only)"
        ;;
    *) err "server URL must start with https:// (got: $SERVER_URL)" ;;
esac

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) err "unsupported arch: $ARCH_RAW" ;;
esac

PLATFORM="${OS}-${ARCH}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

CURL_OPTS=(-fsSL)
[ "$INSECURE" = "1" ] && CURL_OPTS+=(-k)

printf 'downloading %s agent for %s ...\n' "$SERVER_URL" "$PLATFORM"
curl "${CURL_OPTS[@]}" --config - -o "$TMP/sm-agent" "$SERVER_URL/api/v1/agent/binary?platform=$PLATFORM" <<CURLCFG
header = "X-Agent-Token: $TOKEN"
CURLCFG
fi

id -u sm-agent >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin --user-group --comment 'ServerMonitor agent' sm-agent

[ "$ENABLE_SMART" = "1" ] && install_smartmontools
[ "$ENABLE_SMART" = "1" ] && warn_nvme_smart

WANTED_GROUPS=()
[ "$ENABLE_SMART" = "1" ]  && WANTED_GROUPS+=(disk)
[ "$ENABLE_DOCKER" = "1" ] && WANTED_GROUPS+=(docker)
[ "$ENABLE_GPU" = "1" ]    && WANTED_GROUPS+=(video)
PRESENT_GROUPS=()
for grp in "${WANTED_GROUPS[@]}"; do
    if getent group "$grp" >/dev/null 2>&1; then
        PRESENT_GROUPS+=("$grp")
    else
        printf 'note: group %s not present; skipping (re-run after creating it)\n' "$grp" >&2
    fi
done
usermod -G "$(IFS=,; printf '%s' "${PRESENT_GROUPS[*]:-}")" sm-agent

install -o sm-agent -g sm-agent -m 0700 -d /etc/servermonitor
install -o sm-agent -g sm-agent -m 0700 -d /var/lib/servermonitor
install -o sm-agent -g sm-agent -m 0755 -d /opt/servermonitor
if [ "$RECONFIGURE" != "1" ]; then
    install -o root -g root -m 0755 "$TMP/sm-agent" /usr/local/bin/sm-agent
    install -o sm-agent -g sm-agent -m 0755 "$TMP/sm-agent" /opt/servermonitor/sm-agent
fi

if [ "$RECONFIGURE" != "1" ]; then
INSECURE_LINE=false
[ "$INSECURE" = "1" ] && [ "$PERSIST_INSECURE" = "1" ] && INSECURE_LINE=true

umask 0077
TMP_CFG="$(mktemp /etc/servermonitor/agent.toml.XXXXXX)"
cat >"$TMP_CFG" <<EOF
server_url = "$SERVER_URL"
token      = "$TOKEN"
interval_s = $INTERVAL
spool_path = "/var/lib/servermonitor/spool.db"
insecure_skip_verify = $INSECURE_LINE
EOF
chmod 0600 "$TMP_CFG"
chown sm-agent:sm-agent "$TMP_CFG"
mv -f "$TMP_CFG" /etc/servermonitor/agent.toml
fi

CAPS=""
[ "$ENABLE_PORT_OWNERS" = "1" ] && CAPS="CAP_DAC_READ_SEARCH CAP_SYS_PTRACE"
[ "$ENABLE_SMART" = "1" ]       && CAPS="${CAPS:+$CAPS }CAP_SYS_RAWIO"
[ "$ENABLE_SMART_NVME" = "1" ]  && CAPS="${CAPS:+$CAPS }CAP_SYS_ADMIN"
[ "$ENABLE_NETWORK" = "1" ]     && CAPS="${CAPS:+$CAPS }CAP_NET_ADMIN CAP_NET_RAW"

DOCKER_ORDER=""
if [ "$ENABLE_DOCKER" = "1" ]; then
  DOCKER_ORDER="After=docker.service docker.socket
Wants=docker.socket"
fi

cat >/etc/systemd/system/sm-agent.service <<UNIT
[Unit]
Description=ServerMonitor Agent
After=network-online.target
Wants=network-online.target
$DOCKER_ORDER

[Service]
Type=simple
User=sm-agent
Group=sm-agent
Environment=PATH=/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/opt/servermonitor/sm-agent --config /etc/servermonitor/agent.toml
Restart=always
RestartSec=5
SuccessExitStatus=78 75
RestartPreventExitStatus=78
LimitNOFILE=4096

AmbientCapabilities=$CAPS
CapabilityBoundingSet=$CAPS

NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/etc/servermonitor /var/lib/servermonitor /opt/servermonitor
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
RemoveIPC=true
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK

[Install]
WantedBy=multi-user.target
UNIT

chmod 0644 /etc/systemd/system/sm-agent.service

systemctl daemon-reload
systemctl enable sm-agent.service >/dev/null 2>&1 || true
systemctl restart sm-agent.service

if [ "$RECONFIGURE" = "1" ]; then
    printf '\nreconfigured. agent restarted with the updated capabilities.\n'
else
    printf '\ninstalled. agent is running.\n'
fi
printf 'logs: journalctl -u sm-agent -f\n'
