#!/usr/bin/env bash
set -euo pipefail

SERVER_URL="${SM_SERVER_URL:-}"
ADMIN_TOKEN="${SM_ADMIN_TOKEN:-}"
ADMIN_TOKEN_FILE=""
INTERVAL=10
BIN_PATH=""
HOSTNAME_OVERRIDE=""
ENABLE_SMART="${SM_ENABLE_SMART:-0}"
ENABLE_SMART_NVME="${SM_ENABLE_SMART_NVME:-0}"
ENABLE_DOCKER="${SM_ENABLE_DOCKER:-0}"
ENABLE_GPU="${SM_ENABLE_GPU:-0}"
ENABLE_NETWORK="${SM_ENABLE_NETWORK:-0}"
ENABLE_PORT_OWNERS="${SM_ENABLE_PORT_OWNERS:-0}"
ENABLE_ALL="${SM_ENABLE_ALL:-0}"
REINSTALL="${SM_REINSTALL:-0}"

usage() {
  cat >&2 <<EOF
Usage: $0 --server URL --binary /path/to/sm-agent [--admin-token-file PATH] [--hostname NAME] [--interval SECONDS] [--enable-port-owners] [--enable-smart] [--enable-smart-nvme] [--enable-docker] [--enable-gpu] [--enable-network] [--enable-all] [--reinstall]

Installs the ServerMonitor agent as a systemd service. Registers the host
with the server and writes /etc/servermonitor/agent.toml.

Re-running on a host that already has /etc/servermonitor/agent.toml reconfigures
the service in place: it re-derives capabilities and group memberships from the
--enable-* flags and restarts, without re-registering and without an admin token
(--server and --binary are not required in that mode). Pass --reinstall, or set
SM_REINSTALL=1, to force a full fresh install instead.

Admin token MUST come from SM_ADMIN_TOKEN env var or --admin-token-file PATH.
The --admin-token flag is deliberately not supported here: argv is visible in
/proc/<pid>/cmdline to any local user during the install window.

By default the agent gets no Linux capabilities and no supplementary group
memberships. Each --enable-* flag (or SM_ENABLE_<NAME>=1 env var) opts into one
extra collector's grant:
  --enable-port-owners  adds CAP_DAC_READ_SEARCH + CAP_SYS_PTRACE so the Ports tab can map a listening socket to its PID/process; this lets the unprivileged sm-agent read other processes' memory and environment (secrets), so enable it only where that owner mapping is worth the exposure
  --enable-smart        adds CAP_SYS_RAWIO + 'disk' group, auto-installs smartmontools via apt/dnf/yum/apk/pacman/zypper. Covers SATA/SAS SMART (SG_IO) only; NVMe SMART needs CAP_SYS_ADMIN (see --enable-smart-nvme)
  --enable-smart-nvme   implies --enable-smart and additionally adds CAP_SYS_ADMIN so smartctl can issue NVME_IOCTL_ADMIN_CMD on NVMe drives. CAP_SYS_ADMIN is near-root and widens the agent well beyond the rest of this hardened unit, so enable it only where NVMe SMART telemetry is worth that exposure
  --enable-docker       adds 'docker' group membership (containers collector)
  --enable-gpu          adds 'video' group membership (some nvidia-smi setups)
  --enable-network      adds CAP_NET_ADMIN and CAP_NET_RAW (full connections / wifi)
  --enable-all          shortcut for all of the above EXCEPT --enable-smart-nvme; CAP_SYS_ADMIN must be opted into explicitly

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
    --enable-port-owners) ENABLE_PORT_OWNERS=1; shift ;;
    --enable-smart)      ENABLE_SMART=1; shift ;;
    --enable-smart-nvme) ENABLE_SMART_NVME=1; shift ;;
    --enable-docker)     ENABLE_DOCKER=1; shift ;;
    --enable-gpu)        ENABLE_GPU=1; shift ;;
    --enable-network)    ENABLE_NETWORK=1; shift ;;
    --enable-all)        ENABLE_ALL=1; shift ;;
    --reinstall)         REINSTALL=1; shift ;;
    -h|--help)           usage ;;
    *) echo "unknown arg $1" >&2; usage ;;
  esac
done

if [ "$ENABLE_ALL" = "1" ]; then
  ENABLE_SMART=1
  ENABLE_DOCKER=1
  ENABLE_GPU=1
  ENABLE_NETWORK=1
  ENABLE_PORT_OWNERS=1
fi

[ "$ENABLE_SMART_NVME" = "1" ] && ENABLE_SMART=1

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

WARNING: --enable-smart-nvme grants CAP_SYS_ADMIN to the sm-agent service.
         NVMe SMART reads issue NVME_IOCTL_ADMIN_CMD, which the kernel gates
         behind CAP_SYS_ADMIN regardless of file or group permissions.
         CAP_SYS_ADMIN is near-root and widens the agent well beyond the rest
         of this hardened unit. Keep it only where NVMe SMART is worth that.

WARN
  else
    cat >&2 <<'WARN'

WARNING: NVMe device(s) detected, but --enable-smart grants only CAP_SYS_RAWIO.
         CAP_SYS_RAWIO covers SATA/SAS SMART (SG_IO); it does NOT cover NVMe,
         whose SMART reads need CAP_SYS_ADMIN. smartctl will scan these devices
         but report no SMART data for them. To collect NVMe SMART, re-run with
         --enable-smart-nvme (adds CAP_SYS_ADMIN, near-root). SATA/SAS is fine.

WARN
  fi
}

install_smartmontools() {
  if command -v smartctl >/dev/null 2>&1; then
    echo "smartmontools already present: $(command -v smartctl)"
    return 0
  fi
  echo "installing smartmontools (required by SMART collector) ..."
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
    echo "warning: no known package manager; install smartmontools manually for SMART support" >&2
    return 0
  fi
  if command -v smartctl >/dev/null 2>&1; then
    echo "smartmontools installed: $(command -v smartctl)"
  else
    echo "warning: smartmontools install attempt finished but smartctl is not on PATH; install manually for SMART support" >&2
  fi
}

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }

RECONFIGURE=0
if [ "$REINSTALL" != "1" ] && [[ -f /etc/servermonitor/agent.toml ]]; then
  RECONFIGURE=1
  [[ -x /opt/servermonitor/sm-agent ]] || { echo "found /etc/servermonitor/agent.toml but no agent binary at /opt/servermonitor/sm-agent; re-run with --reinstall for a full install" >&2; exit 1; }
  cat >&2 <<'NOTE'

reconfiguring the existing sm-agent install in place: re-applying capabilities
and group memberships from the --enable-* flags, keeping the current identity
and binary. No re-registration and no admin token needed. To force a full fresh
install instead (re-register, rewrite config), re-run with --reinstall.

NOTE
fi

if [ "$RECONFIGURE" != "1" ]; then
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
BIN_DIR_OWNER="$(stat -c '%u' "$BIN_DIR")"
if [[ "${BIN_DIR_PERMS: -1}" =~ [2367] ]]; then
  echo "refusing: binary source dir $BIN_DIR is world-writable (mode $BIN_DIR_PERMS)" >&2
  exit 1
fi
if [[ ${#BIN_DIR_PERMS} -ge 2 && "${BIN_DIR_PERMS: -2:1}" =~ [2367] && "$BIN_DIR_OWNER" != "0" ]]; then
  echo "refusing: binary source dir $BIN_DIR is group-writable and not root-owned (mode $BIN_DIR_PERMS owner uid $BIN_DIR_OWNER)" >&2
  exit 1
fi
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
    echo "note: group '$grp' not present; skipping (re-run after creating it)" >&2
  fi
done
usermod -G "$(IFS=,; printf '%s' "${PRESENT_GROUPS[*]:-}")" sm-agent

[ "$RECONFIGURE" = "1" ] || install -o root -g root -m 0755 "$BIN_PATH" /usr/local/bin/sm-agent

install -o sm-agent -g sm-agent -m 0700 -d /etc/servermonitor
install -o sm-agent -g sm-agent -m 0700 -d /var/lib/servermonitor
install -o sm-agent -g sm-agent -m 0755 -d /opt/servermonitor
[ "$RECONFIGURE" = "1" ] || install -o sm-agent -g sm-agent -m 0755 "$BIN_PATH" /opt/servermonitor/sm-agent

if [ "$RECONFIGURE" != "1" ]; then
ARGS=(--server "$SERVER_URL" --interval "$INTERVAL")
[[ -n "$HOSTNAME_OVERRIDE" ]] && ARGS+=(--hostname "$HOSTNAME_OVERRIDE")
SM_ADMIN_TOKEN="$ADMIN_TOKEN" /usr/local/bin/sm-agent register "${ARGS[@]}"
unset ADMIN_TOKEN
unset SM_ADMIN_TOKEN

chown sm-agent:sm-agent /etc/servermonitor/agent.toml
chmod 0600 /etc/servermonitor/agent.toml
fi

CAPS=""
[ "$ENABLE_PORT_OWNERS" = "1" ] && CAPS="CAP_DAC_READ_SEARCH CAP_SYS_PTRACE"
[ "$ENABLE_SMART" = "1" ]       && CAPS="${CAPS:+$CAPS }CAP_SYS_RAWIO"
[ "$ENABLE_SMART_NVME" = "1" ]  && CAPS="${CAPS:+$CAPS }CAP_SYS_ADMIN"
[ "$ENABLE_NETWORK" = "1" ]     && CAPS="${CAPS:+$CAPS }CAP_NET_ADMIN CAP_NET_RAW"

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
systemctl enable sm-agent.service >/dev/null 2>&1 || true
systemctl restart sm-agent.service
systemctl status --no-pager sm-agent.service || true

echo
if [ "$RECONFIGURE" = "1" ]; then
  echo "reconfigured. agent restarted with the updated capabilities."
else
  echo "installed."
fi
echo "tail logs with: journalctl -u sm-agent -f"
