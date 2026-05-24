#!/usr/bin/env bash
set -euo pipefail

SERVER_URL="__SERVER_URL__"
TOKEN="${SM_TOKEN:-}"
INTERVAL="${SM_INTERVAL:-10}"
INSECURE="${SM_INSECURE:-0}"
PERSIST_INSECURE="${SM_PERSIST_INSECURE:-0}"
ENABLE_SMART="${SM_ENABLE_SMART:-0}"
ENABLE_DOCKER="${SM_ENABLE_DOCKER:-0}"
ENABLE_GPU="${SM_ENABLE_GPU:-0}"
ENABLE_NETWORK="${SM_ENABLE_NETWORK:-0}"
ENABLE_ALL="${SM_ENABLE_ALL:-0}"
if [ "$ENABLE_ALL" = "1" ]; then
    ENABLE_SMART=1
    ENABLE_DOCKER=1
    ENABLE_GPU=1
    ENABLE_NETWORK=1
fi

err() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "$1 is required"; }

[ "$(id -u)" = "0" ] || err "run as root (use sudo)"
need curl
need install
need uname
need useradd

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

id -u sm-agent >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin --user-group --comment 'ServerMonitor agent' sm-agent

EXTRA_GROUPS=()
[ "$ENABLE_SMART" = "1" ]  && EXTRA_GROUPS+=(disk)
[ "$ENABLE_DOCKER" = "1" ] && EXTRA_GROUPS+=(docker)
[ "$ENABLE_GPU" = "1" ]    && EXTRA_GROUPS+=(video)
for grp in "${EXTRA_GROUPS[@]}"; do
    if getent group "$grp" >/dev/null 2>&1; then
        usermod -aG "$grp" sm-agent
    else
        printf 'note: group %s not present; skipping (re-run after creating it)\n' "$grp" >&2
    fi
done

install -o sm-agent -g sm-agent -m 0700 -d /etc/servermonitor
install -o sm-agent -g sm-agent -m 0700 -d /var/lib/servermonitor
install -o sm-agent -g sm-agent -m 0755 -d /opt/servermonitor
install -o root -g root -m 0755 "$TMP/sm-agent" /usr/local/bin/sm-agent
install -o sm-agent -g sm-agent -m 0755 "$TMP/sm-agent" /opt/servermonitor/sm-agent

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

CAPS="CAP_DAC_READ_SEARCH"
[ "$ENABLE_SMART" = "1" ]   && CAPS="$CAPS CAP_SYS_RAWIO"
[ "$ENABLE_NETWORK" = "1" ] && CAPS="$CAPS CAP_NET_ADMIN CAP_NET_RAW"

cat >/etc/systemd/system/sm-agent.service <<UNIT
[Unit]
Description=ServerMonitor Agent
After=network-online.target
Wants=network-online.target

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
systemctl enable --now sm-agent.service

printf '\ninstalled. agent is running.\n'
printf 'logs: journalctl -u sm-agent -f\n'
