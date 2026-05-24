#!/usr/bin/env bash
set -euo pipefail

SERVER_URL="${SM_SERVER_URL:-}"
ADMIN_TOKEN="${SM_ADMIN_TOKEN:-}"
ADMIN_TOKEN_FILE=""
INTERVAL=10
BIN_PATH=""
HOSTNAME_OVERRIDE=""
ENABLE_SMART="${SM_ENABLE_SMART:-0}"
ENABLE_DOCKER="${SM_ENABLE_DOCKER:-0}"
ENABLE_GPU="${SM_ENABLE_GPU:-0}"
ENABLE_NETWORK="${SM_ENABLE_NETWORK:-0}"
ENABLE_ALL="${SM_ENABLE_ALL:-0}"

usage() {
  cat >&2 <<EOF
Usage: $0 --server URL --binary /path/to/sm-agent [--admin-token-file PATH] [--hostname NAME] [--interval SECONDS] [--enable-smart] [--enable-docker] [--enable-gpu] [--enable-network] [--enable-all]

Installs the ServerMonitor agent as a systemd service. Registers the host
with the server and writes /etc/servermonitor/agent.toml.

Admin token MUST come from SM_ADMIN_TOKEN env var or --admin-token-file PATH.
The --admin-token flag is deliberately not supported here: argv is visible in
/proc/<pid>/cmdline to any local user during the install window.

By default the agent gets only CAP_DAC_READ_SEARCH and CAP_SYS_PTRACE and no
supplementary group memberships. Each --enable-* flag (or SM_ENABLE_<NAME>=1
env var) opts into one extra collector's grant:
  --enable-smart    adds CAP_SYS_RAWIO and 'disk' group membership (smartctl)
  --enable-docker   adds 'docker' group membership (containers collector)
  --enable-gpu      adds 'video' group membership (some nvidia-smi setups)
  --enable-network  adds CAP_NET_ADMIN and CAP_NET_RAW (full connections / wifi)
  --enable-all      shortcut for all of the above

Examples:
  SM_ADMIN_TOKEN=xxx $0 --server https://monitor.example.com --binary ./sm-agent
  $0 --server https://... --admin-token-file /root/admin.token --binary ./sm-agent --enable-smart --enable-docker
EOF
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)            SERVER_URL="$2"; shift 2 ;;
    --admin-token-file)  ADMIN_TOKEN_FILE="$2"; shift 2 ;;
    --hostname)          HOSTNAME_OVERRIDE="$2"; shift 2 ;;
    --interval)          INTERVAL="$2"; shift 2 ;;
    --binary)            BIN_PATH="$2"; shift 2 ;;
    --enable-smart)      ENABLE_SMART=1; shift ;;
    --enable-docker)     ENABLE_DOCKER=1; shift ;;
    --enable-gpu)        ENABLE_GPU=1; shift ;;
    --enable-network)    ENABLE_NETWORK=1; shift ;;
    --enable-all)        ENABLE_ALL=1; shift ;;
    -h|--help)           usage ;;
    *) echo "unknown arg $1" >&2; usage ;;
  esac
done

if [ "$ENABLE_ALL" = "1" ]; then
  ENABLE_SMART=1
  ENABLE_DOCKER=1
  ENABLE_GPU=1
  ENABLE_NETWORK=1
fi

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
[[ -z "$SERVER_URL" || -z "$BIN_PATH" ]] && usage

if [[ -z "$ADMIN_TOKEN" && -n "$ADMIN_TOKEN_FILE" ]]; then
  [[ -f "$ADMIN_TOKEN_FILE" ]] || { echo "admin-token-file not found: $ADMIN_TOKEN_FILE" >&2; exit 1; }
  ADMIN_TOKEN="$(tr -d '[:space:]' < "$ADMIN_TOKEN_FILE")"
fi
[[ -z "$ADMIN_TOKEN" ]] && { echo "admin token required: set SM_ADMIN_TOKEN or pass --admin-token-file" >&2; usage; }

case "$SERVER_URL" in
  https://*) ;;
  http://*)  echo "refusing http:// server URL (admin token would leak); use https://" >&2; exit 1 ;;
  *)         echo "server URL must start with https://" >&2; exit 1 ;;
esac

[[ -f "$BIN_PATH" ]] || { echo "binary not found: $BIN_PATH" >&2; exit 1; }

BIN_DIR="$(cd "$(dirname "$BIN_PATH")" && pwd)"
BIN_DIR_PERMS="$(stat -c '%a' "$BIN_DIR")"
if [[ "${BIN_DIR_PERMS: -1}" =~ [2367] ]]; then
  echo "refusing: binary source dir $BIN_DIR is world-writable (mode $BIN_DIR_PERMS)" >&2
  exit 1
fi

id -u sm-agent >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin --user-group --comment 'ServerMonitor agent' sm-agent

EXTRA_GROUPS=()
[ "$ENABLE_SMART" = "1" ]  && EXTRA_GROUPS+=(disk)
[ "$ENABLE_DOCKER" = "1" ] && EXTRA_GROUPS+=(docker)
[ "$ENABLE_GPU" = "1" ]    && EXTRA_GROUPS+=(video)
for grp in "${EXTRA_GROUPS[@]}"; do
  if getent group "$grp" >/dev/null 2>&1; then
    usermod -aG "$grp" sm-agent
  else
    echo "note: group '$grp' not present; skipping (re-run after creating it)" >&2
  fi
done

install -o root -g root -m 0755 "$BIN_PATH" /usr/local/bin/sm-agent

install -o sm-agent -g sm-agent -m 0700 -d /etc/servermonitor
install -o sm-agent -g sm-agent -m 0700 -d /var/lib/servermonitor
install -o sm-agent -g sm-agent -m 0755 -d /opt/servermonitor
install -o sm-agent -g sm-agent -m 0755 "$BIN_PATH" /opt/servermonitor/sm-agent

ARGS=(--server "$SERVER_URL" --interval "$INTERVAL")
[[ -n "$HOSTNAME_OVERRIDE" ]] && ARGS+=(--hostname "$HOSTNAME_OVERRIDE")
SM_ADMIN_TOKEN="$ADMIN_TOKEN" /usr/local/bin/sm-agent register "${ARGS[@]}"
unset ADMIN_TOKEN
unset SM_ADMIN_TOKEN

chown sm-agent:sm-agent /etc/servermonitor/agent.toml
chmod 0600 /etc/servermonitor/agent.toml

CAPS="CAP_DAC_READ_SEARCH CAP_SYS_PTRACE"
[ "$ENABLE_SMART" = "1" ]   && CAPS="$CAPS CAP_SYS_RAWIO"
[ "$ENABLE_NETWORK" = "1" ] && CAPS="$CAPS CAP_NET_ADMIN CAP_NET_RAW"

cat > /etc/systemd/system/sm-agent.service <<UNIT
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
systemctl status --no-pager sm-agent.service || true

echo
echo "installed. tail logs with: journalctl -u sm-agent -f"
