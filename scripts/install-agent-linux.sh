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
ENABLE_BACKUP="${SM_ENABLE_BACKUP:-0}"
BACKUP_REPOS="${SM_BACKUP_REPOS:-}"
BACKUP_REPO_NAMES="${SM_BACKUP_REPO_NAMES:-}"
BACKUP_PATHS="${SM_BACKUP_PATHS:-}"
BACKUP_TIME="${SM_BACKUP_TIME:-02:30}"
BACKUP_PRUNE_MODE="${SM_BACKUP_PRUNE_MODE:-host}"
BACKUP_S3_REGION="${SM_BACKUP_S3_REGION:-}"
BACKUP_S3_PATH_STYLE="${SM_BACKUP_S3_PATH_STYLE:-0}"
BACKUP_S3_ACCESS_KEY_ID="${SM_BACKUP_S3_ACCESS_KEY_ID:-}"
BACKUP_S3_SECRET_ACCESS_KEY="${SM_BACKUP_S3_SECRET_ACCESS_KEY:-}"
BACKUP_S3_SESSION_TOKEN="${SM_BACKUP_S3_SESSION_TOKEN:-}"
BACKUP_REST_USERNAME="${SM_BACKUP_REST_USERNAME:-}"
BACKUP_REST_PASSWORD="${SM_BACKUP_REST_PASSWORD:-}"
BACKUP_HAS_TUNNEL=0
BACKUP_PRESERVED_TUNNEL_SECTION=""
BACKUP_TUNNEL_SECTION_PRESERVED=0
REINSTALL="${SM_REINSTALL:-0}"
RESTIC_VERSION="0.19.0"

usage() {
  cat >&2 <<EOF
Usage: $0 --server URL --binary /path/to/sm-agent [--admin-token-file PATH] [--hostname NAME] [--interval SECONDS] [--enable-port-owners] [--enable-smart] [--enable-smart-nvme] [--enable-docker] [--enable-gpu] [--enable-network] [--enable-all] [--enable-backup] [--backup-repos URLS] [--backup-repo-names NAMES] [--backup-paths PATHS] [--backup-time HH:MM] [--reinstall]

Installs the ServerMonitor agent as a systemd service. Registers the host
with the server and writes /etc/servermonitor/agent.toml.

Re-running on a host that already has /etc/servermonitor/agent.toml reconfigures
the service in place: it re-derives capabilities and group memberships from the
--enable-* flags and restarts, without re-registering and without an admin token
(--server and --binary are not required in that mode). When --binary is given,
both agent copies are refreshed if its version differs. Pass --reinstall, or set
SM_REINSTALL=1, to force a full fresh install instead.

Admin token MUST come from SM_ADMIN_TOKEN env var or --admin-token-file PATH.
The --admin-token flag is deliberately not supported here: argv is visible in
/proc/<pid>/cmdline to any local user during the install window.

By default the agent gets no Linux capabilities and no supplementary group
memberships. Each --enable-* flag (or SM_ENABLE_<NAME>=1 env var) opts into one
extra collector's grant:
  --enable-port-owners  adds CAP_DAC_READ_SEARCH + CAP_SYS_PTRACE so the Ports tab can map a listening socket to its PID/process; this lets the unprivileged sm-agent read other processes' memory and environment (secrets), so enable it only where that owner mapping is worth the exposure
  --enable-smart        adds CAP_SYS_RAWIO + 'disk' group, auto-installs smartmontools via apt/dnf/yum/apk/pacman/zypper, and installs a udev rule opening the hardware RAID control nodes (MegaRAID/3ware/Adaptec) to the 'disk' group so drives behind those controllers are probed too. Covers SATA/SAS SMART (SG_IO) only; NVMe SMART needs CAP_SYS_ADMIN (see --enable-smart-nvme)
  --enable-smart-nvme   implies --enable-smart and additionally adds CAP_SYS_ADMIN so smartctl can issue NVME_IOCTL_ADMIN_CMD on NVMe drives. CAP_SYS_ADMIN is near-root and widens the agent well beyond the rest of this hardened unit, so enable it only where NVMe SMART telemetry is worth that exposure
  --enable-docker       adds 'docker' group membership (containers collector)
  --enable-gpu          adds 'video' group membership (some nvidia-smi setups)
  --enable-network      adds CAP_NET_ADMIN and CAP_NET_RAW (full connections / wifi)
  --enable-all          shortcut for all of the above EXCEPT --enable-smart-nvme and --enable-backup; CAP_SYS_ADMIN and whole-host backup read access must be opted into explicitly
  --enable-backup       provisions scheduled encrypted restic backups: installs a pinned restic to /usr/local/bin/sm-restic, writes /etc/servermonitor-backup/backup.toml, generates a repo key, runs 'sm-agent backup init', and adds an sm-backup systemd timer whose oneshot unit runs with CAP_DAC_READ_SEARCH so it can read every file on the host to back it up. That grant is confined to the backup unit, not the resident agent. NOT included in --enable-all. Requires --backup-repos (or SM_BACKUP_REPOS) on a fresh setup

Backup inputs (used with --enable-backup; each also settable via the matching SM_BACKUP_* env var):
  --backup-repos URLS         comma-separated restic repository URLs (e.g. rest:https://user:pass@host/repo). Required on a fresh backup setup
                               tunnel:NAME backs up over the built-in WireGuard tunnel to the ServerMonitor server (requires the server to run with BACKUP_WG_PORT)
  --backup-repo-names NAMES   comma-separated logical names matching --backup-repos (default repo1..repoN)
  --backup-paths PATHS        comma-separated paths to back up (default /etc,/home,/root,/var/lib)
  --backup-time HH:MM         local time for the nightly backup (default 02:30, with a randomized delay)
  --backup-prune-mode MODE    "host" (prune after each run; default) or "external" (host never prunes; a trusted box holding the key prunes). Use "external" against append-only endpoints
  --backup-s3-region REGION   region for s3: repositories (also SM_BACKUP_S3_REGION)
  --backup-s3-path-style      path-style S3 addressing, required by MinIO/Ceph/Garage and most self-hosted S3 (also SM_BACKUP_S3_PATH_STYLE=1)

S3/REST transport credentials are env-only (never flags: argv is world-readable) and are written to a
root-owned /etc/servermonitor-backup/repo-credentials.env (0440 root:sm-agent), applied to every s3:/b2:/rest: repo:
  SM_BACKUP_S3_ACCESS_KEY_ID / SM_BACKUP_S3_SECRET_ACCESS_KEY / SM_BACKUP_S3_SESSION_TOKEN   S3 or B2 object storage
  SM_BACKUP_REST_USERNAME / SM_BACKUP_REST_PASSWORD                                          a rest-server or ServerMonitor backup endpoint

Examples:
  SM_ADMIN_TOKEN=xxx $0 --server https://monitor.example.com --binary ./sm-agent
  $0 --server https://... --admin-token-file /root/admin.token --binary ./sm-agent --enable-smart --enable-docker
EOF
  exit 1
}

check_binary_source_dir() {
  [[ -f "$BIN_PATH" ]] || { echo "binary not found: $BIN_PATH" >&2; exit 1; }
  local bin_dir bin_dir_perms bin_dir_owner
  bin_dir="$(cd "$(dirname "$BIN_PATH")" && pwd)"
  bin_dir_perms="$(stat -c '%a' "$bin_dir")"
  bin_dir_owner="$(stat -c '%u' "$bin_dir")"
  if [[ "${bin_dir_perms: -1}" =~ [2367] ]]; then
    echo "refusing: binary source dir $bin_dir is world-writable (mode $bin_dir_perms)" >&2
    exit 1
  fi
  if [[ ${#bin_dir_perms} -ge 2 && "${bin_dir_perms: -2:1}" =~ [2367] && "$bin_dir_owner" != "0" ]]; then
    echo "refusing: binary source dir $bin_dir is group-writable and not root-owned (mode $bin_dir_perms owner uid $bin_dir_owner)" >&2
    exit 1
  fi
}

agent_binary_version() {
  "$1" --version 2>/dev/null | awk 'NR == 1 { print $2; exit }' || true
}

resident_agent_binary_version() {
  if command -v runuser >/dev/null 2>&1; then
    runuser -u sm-agent -- /opt/servermonitor/sm-agent --version 2>/dev/null | awk 'NR == 1 { print $2; exit }' || true
  else
    su -s /bin/sh -c '/opt/servermonitor/sm-agent --version 2>/dev/null' sm-agent | awk 'NR == 1 { print $2; exit }' || true
  fi
}

refresh_agent_binaries() {
  if [ "$RECONFIGURE" != "1" ]; then
    install -o root -g root -m 0755 "$BIN_PATH" /usr/local/bin/sm-agent
    install -o sm-agent -g sm-agent -m 0755 "$BIN_PATH" /opt/servermonitor/sm-agent
    return 0
  fi
  local old_version new_version resident_version
  if [ -n "$BIN_PATH" ]; then
    check_binary_source_dir
    new_version="$(agent_binary_version "$BIN_PATH")"
    old_version="$(agent_binary_version /usr/local/bin/sm-agent)"
    if [ "$new_version" != "$old_version" ]; then
      install -o root -g root -m 0755 "$BIN_PATH" /usr/local/bin/sm-agent
      install -o sm-agent -g sm-agent -m 0755 "$BIN_PATH" /opt/servermonitor/sm-agent
      echo "refreshed sm-agent binaries (v$old_version -> v$new_version)"
    fi
    return 0
  fi
  old_version="$(agent_binary_version /usr/local/bin/sm-agent)"
  resident_version="$(resident_agent_binary_version)"
  if [ "$old_version" != "$resident_version" ]; then
    echo "warning: /usr/local/bin/sm-agent is v$old_version but the resident agent is v$resident_version; the backup/restore units run stale code. Re-run with --binary /path/to/sm-agent to refresh." >&2
  fi
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
    --enable-backup)     ENABLE_BACKUP=1; shift ;;
    --backup-repos)      BACKUP_REPOS="$2"; shift 2 ;;
    --backup-repo-names) BACKUP_REPO_NAMES="$2"; shift 2 ;;
    --backup-paths)      BACKUP_PATHS="$2"; shift 2 ;;
    --backup-time)       BACKUP_TIME="$2"; shift 2 ;;
    --backup-prune-mode) BACKUP_PRUNE_MODE="$2"; shift 2 ;;
    --backup-s3-region)  BACKUP_S3_REGION="$2"; shift 2 ;;
    --backup-s3-path-style) BACKUP_S3_PATH_STYLE=1; shift ;;
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

install_bzip2() {
  if command -v bunzip2 >/dev/null 2>&1; then
    return 0
  fi
  echo "installing bzip2 (required to unpack restic) ..."
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null 2>&1 || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq bzip2 >/dev/null 2>&1 || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q bzip2 >/dev/null 2>&1 || true
  elif command -v yum >/dev/null 2>&1; then
    yum install -y -q bzip2 >/dev/null 2>&1 || true
  elif command -v zypper >/dev/null 2>&1; then
    zypper --non-interactive --quiet install bzip2 >/dev/null 2>&1 || true
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache --quiet bzip2 >/dev/null 2>&1 || true
  elif command -v pacman >/dev/null 2>&1; then
    pacman -S --noconfirm --needed --quiet bzip2 >/dev/null 2>&1 || true
  else
    echo "warning: no known package manager; install bzip2 manually to unpack restic" >&2
  fi
}

warn_backup_capability() {
  cat >&2 <<'WARN'

WARNING: --enable-backup grants the sm-backup unit CAP_DAC_READ_SEARCH, letting
         it read every file on the host to back it up. The grant is confined to
         the oneshot sm-backup.service and its timer, NOT the resident sm-agent
         daemon, and it is excluded from --enable-all. Enable it only where
         whole-host read access for backups is worth that exposure.

WARN
}

backup_trim() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "$s"
}

backup_toml_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

backup_validate_tunnel_repos() {
  local url tunnel_name
  local -a repo_arr
  BACKUP_HAS_TUNNEL=0
  IFS=',' read -ra repo_arr <<< "$BACKUP_REPOS"
  for url in "${repo_arr[@]}"; do
    url="$(backup_trim "$url")"
    case "$url" in
      tunnel:*)
        tunnel_name="${url#tunnel:}"
        if [[ ! "$tunnel_name" =~ ^([A-Za-z0-9._-]+/)?[A-Za-z0-9._-]+$ ]]; then
          echo "invalid tunnel repository '$url'; use tunnel:NAME (this server) or tunnel:NODE/NAME (a promoted backup node), each part matching ^[A-Za-z0-9._-]+$" >&2
          exit 1
        fi
        BACKUP_HAS_TUNNEL=1
        ;;
    esac
  done
}

backup_preserve_tunnel_section() {
  BACKUP_PRESERVED_TUNNEL_SECTION=""
  BACKUP_TUNNEL_SECTION_PRESERVED=0
  [ -f /etc/servermonitor-backup/backup.toml ] || return 0
  BACKUP_PRESERVED_TUNNEL_SECTION="$(awk '
    /^[[:space:]]*\[tunnel\][[:space:]]*$/ { found=1; print; next }
    found && /^[[:space:]]*\[/ { exit }
    found { print }
  ' /etc/servermonitor-backup/backup.toml)"
  [ -n "$BACKUP_PRESERVED_TUNNEL_SECTION" ] && BACKUP_TUNNEL_SECTION_PRESERVED=1
}

backup_has_tunnel_section() {
  [ -f /etc/servermonitor-backup/backup.toml ] && grep -Eq '^[[:space:]]*\[tunnel\][[:space:]]*$' /etc/servermonitor-backup/backup.toml
}

backup_tunnel_value() {
  local key="$1"
  awk -v key="$key" '
    /^[[:space:]]*\[tunnel\][[:space:]]*$/ { found=1; next }
    found && /^[[:space:]]*\[/ { exit }
    found && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      value=$0
      sub("^[[:space:]]*" key "[[:space:]]*=[[:space:]]*", "", value)
      sub(/[[:space:]]*$/, "", value)
      if (value ~ /^\".*\"$/) {
        sub(/^\"/, "", value)
        sub(/\"$/, "", value)
      }
      print value
      exit
    }
  ' /etc/servermonitor-backup/backup.toml
}

backup_require_tunnel_support() {
  local usage_output
  usage_output="$(/usr/local/bin/sm-agent backup 2>&1 || true)"
  if ! grep -q 'tunnel-enroll' <<< "$usage_output"; then
    echo "/usr/local/bin/sm-agent does not support backup tunnel-enroll; re-run with --reinstall and --binary pointing at a current sm-agent" >&2
    exit 1
  fi
}

backup_enroll_tunnel() {
  local enrolled=0
  [ -f /etc/servermonitor/agent.toml ] || { echo "/etc/servermonitor/agent.toml not found; cannot enroll the backup tunnel" >&2; exit 1; }
  if /usr/local/bin/sm-agent backup tunnel-enroll --config /etc/servermonitor-backup/backup.toml --agent-config /etc/servermonitor/agent.toml; then
    enrolled=1
  fi
  if [ -f /etc/servermonitor-backup/tunnel.key ]; then
    chown root:sm-agent /etc/servermonitor-backup/tunnel.key
    chmod 0440 /etc/servermonitor-backup/tunnel.key
  elif [ "$enrolled" = "1" ]; then
    echo "sm-agent backup tunnel-enroll succeeded but /etc/servermonitor-backup/tunnel.key was not created" >&2
    exit 1
  fi
  if [ "$enrolled" = "1" ]; then
    return 0
  fi
  if [ "$BACKUP_TUNNEL_SECTION_PRESERVED" = "1" ]; then
    echo "warning: sm-agent backup tunnel-enroll failed; keeping the preserved [tunnel] section and continuing" >&2
    return 0
  fi
  echo "sm-agent backup tunnel-enroll failed and no existing [tunnel] section is available; backups could never work" >&2
  exit 1
}

provision_restic() {
  local arch_raw arch sha url tmp bz2
  arch_raw="$(uname -m)"
  case "$arch_raw" in
    x86_64|amd64)              arch=amd64; sha="13176fe6d89d4357947a2cd107218ab2873a5f9d8e1ac2d4cd1c8e07e6839c21" ;;
    aarch64|arm64)             arch=arm64; sha="e522ce6bf748d753fee8093e8ec59359972cf5b6bc65fc7c7cf38ae952351d91" ;;
    armv7l|armv6l|armv7|armv6) arch=arm;   sha="2997e6ebd953a551abe33172876ce1a88aa1bb29a93425b167747ece7a38c850" ;;
    i686|i386)                 arch=386;   sha="0b58b04a7d2fffe290ed00ea841e97662af33296a2ba6abb52d4c62612b2e1e6" ;;
    *) echo "unsupported arch for restic: $arch_raw" >&2; exit 1 ;;
  esac
  if [ -x /usr/local/bin/sm-restic ] && /usr/local/bin/sm-restic version 2>/dev/null | grep -q "restic ${RESTIC_VERSION} "; then
    echo "restic ${RESTIC_VERSION} already present at /usr/local/bin/sm-restic"
    rm -f /opt/servermonitor/restic
    return 0
  fi
  command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum required to verify the restic download" >&2; exit 1; }
  install_bzip2
  command -v bunzip2 >/dev/null 2>&1 || { echo "bunzip2 (bzip2) required to unpack restic; install it and re-run" >&2; exit 1; }
  url="https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${arch}.bz2"
  tmp="$(mktemp -d)"
  bz2="$tmp/restic.bz2"
  echo "downloading restic ${RESTIC_VERSION} for linux_${arch} ..."
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$bz2" "$url" || { rm -rf "$tmp"; echo "restic download failed" >&2; exit 1; }
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$bz2" "$url" || { rm -rf "$tmp"; echo "restic download failed" >&2; exit 1; }
  else
    rm -rf "$tmp"; echo "curl or wget required to download restic" >&2; exit 1
  fi
  if ! printf '%s  %s\n' "$sha" "$bz2" | sha256sum -c - >/dev/null 2>&1; then
    rm -rf "$tmp"; echo "restic checksum mismatch for linux_${arch}; refusing to install" >&2; exit 1
  fi
  bunzip2 -c "$bz2" > "$tmp/restic"
  install -o root -g root -m 0755 "$tmp/restic" /usr/local/bin/sm-restic
  rm -rf "$tmp"
  rm -f /opt/servermonitor/restic
  echo "restic ${RESTIC_VERSION} installed at /usr/local/bin/sm-restic"
}

backup_migrate_legacy() {
  [ -f /etc/servermonitor/backup.toml ] || [ -f /etc/servermonitor/backup.key ] || return 0
  install -o root -g sm-agent -m 0750 -d /etc/servermonitor-backup
  if [ -f /etc/servermonitor/backup.toml ] && [ ! -f /etc/servermonitor-backup/backup.toml ]; then
    mv /etc/servermonitor/backup.toml /etc/servermonitor-backup/backup.toml
    chown root:sm-agent /etc/servermonitor-backup/backup.toml
    chmod 0640 /etc/servermonitor-backup/backup.toml
    sed -i 's|"/etc/servermonitor/backup.key"|"/etc/servermonitor-backup/backup.key"|; s|"/opt/servermonitor/restic"|"/usr/local/bin/sm-restic"|' /etc/servermonitor-backup/backup.toml
  fi
  if [ -f /etc/servermonitor/backup.key ] && [ ! -f /etc/servermonitor-backup/backup.key ]; then
    mv /etc/servermonitor/backup.key /etc/servermonitor-backup/backup.key
    chown root:sm-agent /etc/servermonitor-backup/backup.key
    chmod 0440 /etc/servermonitor-backup/backup.key
  fi
  if [ -f /etc/servermonitor/recovery-kit.txt ] && [ ! -f /etc/servermonitor-backup/recovery-kit.txt ]; then
    mv /etc/servermonitor/recovery-kit.txt /etc/servermonitor-backup/recovery-kit.txt
    chown root:root /etc/servermonitor-backup/recovery-kit.txt
    chmod 0400 /etc/servermonitor-backup/recovery-kit.txt
  fi
  echo "note: relocated backup config from /etc/servermonitor to /etc/servermonitor-backup"
}

backup_generate_key() {
  if [ -f /etc/servermonitor-backup/backup.key ]; then
    echo "note: existing /etc/servermonitor-backup/backup.key kept (guards existing snapshots; never regenerated)"
    return 1
  fi
  local tmp
  tmp="$(mktemp /etc/servermonitor-backup/backup.key.XXXXXX)"
  head -c 32 /dev/urandom | base64 > "$tmp"
  chmod 0440 "$tmp"
  chown root:sm-agent "$tmp"
  mv -f "$tmp" /etc/servermonitor-backup/backup.key
  return 0
}

backup_have_transport_creds() {
  [ -n "$BACKUP_S3_ACCESS_KEY_ID" ] || [ -n "$BACKUP_S3_SECRET_ACCESS_KEY" ] || \
    [ -n "$BACKUP_REST_USERNAME" ] || [ -n "$BACKUP_REST_PASSWORD" ]
}

backup_write_env_file() {
  local tmp
  tmp="$(mktemp /etc/servermonitor-backup/repo-credentials.env.XXXXXX)"
  {
    [ -n "$BACKUP_S3_ACCESS_KEY_ID" ]     && printf 'AWS_ACCESS_KEY_ID=%s\n' "$BACKUP_S3_ACCESS_KEY_ID"
    [ -n "$BACKUP_S3_SECRET_ACCESS_KEY" ] && printf 'AWS_SECRET_ACCESS_KEY=%s\n' "$BACKUP_S3_SECRET_ACCESS_KEY"
    [ -n "$BACKUP_S3_SESSION_TOKEN" ]     && printf 'AWS_SESSION_TOKEN=%s\n' "$BACKUP_S3_SESSION_TOKEN"
    [ -n "$BACKUP_REST_USERNAME" ]        && printf 'RESTIC_REST_USERNAME=%s\n' "$BACKUP_REST_USERNAME"
    [ -n "$BACKUP_REST_PASSWORD" ]        && printf 'RESTIC_REST_PASSWORD=%s\n' "$BACKUP_REST_PASSWORD"
  } > "$tmp"
  chmod 0440 "$tmp"
  chown root:sm-agent "$tmp"
  mv -f "$tmp" /etc/servermonitor-backup/repo-credentials.env
}

backup_write_toml() {
  local tmp i name url tunnel_name tunnel_node first item
  local -a repo_arr name_arr path_arr
  IFS=',' read -ra repo_arr <<< "$BACKUP_REPOS"
  IFS=',' read -ra name_arr <<< "$BACKUP_REPO_NAMES"
  IFS=',' read -ra path_arr <<< "${BACKUP_PATHS:-/etc,/home,/root,/var/lib}"
  tmp="$(mktemp /etc/servermonitor-backup/backup.toml.XXXXXX)"
  {
    echo "status_path = \"/var/lib/servermonitor/backup-status.json\""
    echo "restic_path = \"/usr/local/bin/sm-restic\""
    first=1
    printf 'paths = ['
    for item in "${path_arr[@]}"; do
      item="$(backup_trim "$item")"
      [ -z "$item" ] && continue
      if [ "$first" = 1 ]; then first=0; else printf ', '; fi
      printf '"%s"' "$(backup_toml_escape "$item")"
    done
    printf ']\n'
    echo "excludes = [\"**/.cache\", \"/var/lib/docker\", \"/var/lib/servermonitor\"]"
    echo "one_file_system = true"
    printf 'prune_mode = "%s"\n' "$BACKUP_PRUNE_MODE"
    echo ""
    echo "[retention]"
    echo "daily = 7"
    echo "weekly = 4"
    echo "monthly = 6"
    i=0
    for url in "${repo_arr[@]}"; do
      url="$(backup_trim "$url")"
      [ -z "$url" ] && continue
      name="$(backup_trim "${name_arr[$i]:-}")"
      tunnel_name=""
      tunnel_node=""
      case "$url" in
        tunnel:*)
          tunnel_name="${url#tunnel:}"
          case "$tunnel_name" in
            */*)
              tunnel_node="${tunnel_name%%/*}"
              tunnel_name="${tunnel_name#*/}"
              ;;
          esac
          [ -z "$name" ] && name="$tunnel_name"
          ;;
      esac
      [ -z "$name" ] && name="repo$((i+1))"
      echo ""
      echo "[[repo]]"
      printf 'name = "%s"\n' "$(backup_toml_escape "$name")"
      if [ -n "$tunnel_name" ]; then
        printf 'tunnel_name = "%s"\n' "$(backup_toml_escape "$tunnel_name")"
        [ -n "$tunnel_node" ] && printf 'tunnel_node = "%s"\n' "$(backup_toml_escape "$tunnel_node")"
      else
        printf 'url = "%s"\n' "$(backup_toml_escape "$url")"
      fi
      echo "password_file = \"/etc/servermonitor-backup/backup.key\""
      case "$url" in
        s3:*|b2:*|rest:*|tunnel:*)
          if [ -f /etc/servermonitor-backup/repo-credentials.env ]; then
            echo "env_file = \"/etc/servermonitor-backup/repo-credentials.env\""
          elif [ -n "$tunnel_name" ]; then
            printf 'note: repo "%s" targets tunnel: but no REST credentials were provided (set SM_BACKUP_REST_USERNAME/SM_BACKUP_REST_PASSWORD); authentication will fail\n' "$name" >&2
          else
            printf 'note: repo "%s" targets %s but no S3/REST credentials were provided (set SM_BACKUP_S3_* or SM_BACKUP_REST_*); authentication will fail\n' "$name" "${url%%:*}:" >&2
          fi
          ;;
      esac
      case "$url" in
        s3:*)
          [ -n "$BACKUP_S3_REGION" ] && printf 's3_region = "%s"\n' "$(backup_toml_escape "$BACKUP_S3_REGION")"
          [ "$BACKUP_S3_PATH_STYLE" = "1" ] && echo "s3_path_style = true"
          ;;
      esac
      i=$((i+1))
    done
    if [ -n "$BACKUP_PRESERVED_TUNNEL_SECTION" ]; then
      echo ""
      printf '%s\n' "$BACKUP_PRESERVED_TUNNEL_SECTION"
    fi
  } > "$tmp"
  chmod 0640 "$tmp"
  chown root:sm-agent "$tmp"
  mv -f "$tmp" /etc/servermonitor-backup/backup.toml
}

backup_write_units() {
  case "$BACKUP_TIME" in
    [01][0-9]:[0-5][0-9]|2[0-3]:[0-5][0-9]) ;;
    *) echo "backup time must be HH:MM (got: $BACKUP_TIME)" >&2; exit 1 ;;
  esac
  cat > /etc/systemd/system/sm-backup.service <<'UNIT'
[Unit]
Description=ServerMonitor Backup
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=sm-agent
Group=sm-agent
Environment=PATH=/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/usr/local/bin/sm-agent backup run --config /etc/servermonitor-backup/backup.toml
AmbientCapabilities=CAP_DAC_READ_SEARCH
CapabilityBoundingSet=CAP_DAC_READ_SEARCH
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/servermonitor
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
UNIT
  chmod 0644 /etc/systemd/system/sm-backup.service
  cat > /etc/systemd/system/sm-backup.timer <<UNIT
[Unit]
Description=ServerMonitor Backup schedule

[Timer]
OnCalendar=*-*-* ${BACKUP_TIME}:00
RandomizedDelaySec=900
Persistent=true

[Install]
WantedBy=timers.target
UNIT
  chmod 0644 /etc/systemd/system/sm-backup.timer
  cat > /etc/systemd/system/sm-backup-check.service <<'UNIT'
[Unit]
Description=ServerMonitor Backup Check
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=sm-agent
Group=sm-agent
Environment=PATH=/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/usr/local/bin/sm-agent backup check --config /etc/servermonitor-backup/backup.toml --read-data-subset 5%%
AmbientCapabilities=CAP_DAC_READ_SEARCH
CapabilityBoundingSet=CAP_DAC_READ_SEARCH
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/servermonitor
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
UNIT
  chmod 0644 /etc/systemd/system/sm-backup-check.service
  cat > /etc/systemd/system/sm-backup-check.timer <<'UNIT'
[Unit]
Description=ServerMonitor Backup Check schedule

[Timer]
OnCalendar=weekly
RandomizedDelaySec=21600
Persistent=true

[Install]
WantedBy=timers.target
UNIT
  chmod 0644 /etc/systemd/system/sm-backup-check.timer
  cat > /etc/systemd/system/sm-backup-restore@.service <<'UNIT'
[Unit]
Description=ServerMonitor Backup In-Place Restore %I
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=PATH=/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/usr/local/bin/sm-agent backup restore --config /etc/servermonitor-backup/backup.toml --in-place --instance %I
NoNewPrivileges=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
RestrictRealtime=true
LockPersonality=true
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
UNIT
  chmod 0644 /etc/systemd/system/sm-backup-restore@.service
  systemctl daemon-reload
  systemctl enable --now sm-backup.timer >/dev/null 2>&1 || systemctl enable sm-backup.timer
  systemctl enable --now sm-backup-check.timer >/dev/null 2>&1 || systemctl enable sm-backup-check.timer
}

backup_remove_units() {
  systemctl disable --now sm-backup.timer >/dev/null 2>&1 || true
  systemctl disable --now sm-backup-check.timer >/dev/null 2>&1 || true
  systemctl stop sm-backup.service >/dev/null 2>&1 || true
  systemctl stop sm-backup-check.service >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/sm-backup.timer /etc/systemd/system/sm-backup.service
  rm -f /etc/systemd/system/sm-backup-check.timer /etc/systemd/system/sm-backup-check.service /etc/systemd/system/sm-backup-restore@.service
  systemctl daemon-reload
  cat >&2 <<'NOTE'

note: removed the sm-backup timer and unit. backup.toml, backup.key and the
recovery kit were KEPT (they guard existing snapshots). tunnel.key is kept
alongside backup.key. A still-enrolled WireGuard peer can be revoked from the
server UI (Backups page). To remove the kept files:
  rm -rf /etc/servermonitor-backup

NOTE
}

backup_write_recovery_kit() {
  local tmp host now i name url tunnel_name tunnel_endpoint line key value
  local -a repo_arr name_arr
  IFS=',' read -ra repo_arr <<< "$BACKUP_REPOS"
  IFS=',' read -ra name_arr <<< "$BACKUP_REPO_NAMES"
  host="$(hostname -f 2>/dev/null || hostname)"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  tunnel_endpoint="$(backup_tunnel_value endpoint)"
  tmp="$(mktemp /etc/servermonitor-backup/recovery-kit.txt.XXXXXX)"
  {
    echo "ServerMonitor backup recovery kit"
    echo "host: $host"
    echo "date: $now"
    echo ""
    echo "repositories:"
    i=0
    for url in "${repo_arr[@]}"; do
      url="$(backup_trim "$url")"
      [ -z "$url" ] && continue
      name="$(backup_trim "${name_arr[$i]:-}")"
      case "$url" in
        tunnel:*)
          tunnel_name="${url#tunnel:}"
          [ -z "$name" ] && name="$tunnel_name"
          echo "  [$name] tunnel:$tunnel_name via $tunnel_endpoint"
          ;;
        *)
          [ -z "$name" ] && name="repo$((i+1))"
          echo "  [$name] $url"
          ;;
      esac
      i=$((i+1))
    done
    echo ""
    echo "repository password:"
    echo "  $(cat /etc/servermonitor-backup/backup.key)"
    if [ -f /etc/servermonitor-backup/repo-credentials.env ]; then
      echo ""
      echo "REST credentials:"
      while IFS= read -r line; do
        case "$line" in
          RESTIC_REST_USERNAME=*|RESTIC_REST_PASSWORD=*) echo "  $line" ;;
        esac
      done < /etc/servermonitor-backup/repo-credentials.env
    fi
    if backup_has_tunnel_section; then
      echo ""
      echo "wireguard tunnel:"
      for key in endpoint server_public_key local_ip server_ip rest_port; do
        value="$(backup_tunnel_value "$key")"
        echo "  $key: $value"
      done
      echo "  tunnel.key contents (WireGuard private key):"
      if [ -f /etc/servermonitor-backup/tunnel.key ]; then
        sed 's/^/    /' /etc/servermonitor-backup/tunnel.key
      else
        echo "    unavailable"
      fi
      echo ""
      echo "disaster recovery for tunnel repositories:"
      echo "  1. Register the recovery host or reuse a host token in agent.toml."
      echo "  2. If the old peer was revoked, run:"
      echo "     sm-agent backup tunnel-enroll --config backup.toml --agent-config agent.toml"
      echo "  3. Run: sm-agent backup proxy --config backup.toml"
      echo "  4. Use the printed RESTIC_REPOSITORY with restic and the repository password above."
    fi
    echo ""
    echo "restore any repo on a bare machine (needs restic + this password):"
    echo "  restic -r <url> restore latest --target /mnt/recover"
    echo ""
    echo "STORE THIS IN A PASSWORD MANAGER NOW. No other copy of this password exists"
    echo "anywhere -- the monitoring server never sees it. Lose this kit and the host,"
    echo "and the backups are unrecoverable."
  } > "$tmp"
  chmod 0400 "$tmp"
  chown root:root "$tmp"
  mv -f "$tmp" /etc/servermonitor-backup/recovery-kit.txt
}

backup_print_recovery_kit() {
  echo ""
  echo "================ BACKUP RECOVERY KIT (store offline NOW) ================"
  cat /etc/servermonitor-backup/recovery-kit.txt
  echo "========================================================================"
  echo "(also saved to /etc/servermonitor-backup/recovery-kit.txt, root-only 0400)"
  echo ""
}

backup_init_as_agent() {
  if command -v runuser >/dev/null 2>&1; then
    runuser -u sm-agent -- /usr/local/bin/sm-agent backup init --config /etc/servermonitor-backup/backup.toml
  else
    su -s /bin/sh -c '/usr/local/bin/sm-agent backup init --config /etc/servermonitor-backup/backup.toml' sm-agent
  fi
}

provision_backup() {
  backup_validate_tunnel_repos
  backup_migrate_legacy
  if [ ! -f /etc/servermonitor-backup/backup.toml ] && [ -z "$BACKUP_REPOS" ]; then
    echo "--enable-backup requires --backup-repos (or SM_BACKUP_REPOS) on a fresh setup" >&2
    exit 1
  fi
  case "$BACKUP_PRUNE_MODE" in
    host|external) ;;
    *) echo "SM_BACKUP_PRUNE_MODE / --backup-prune-mode must be \"host\" or \"external\"" >&2; exit 1 ;;
  esac
  if { [ -n "$BACKUP_S3_ACCESS_KEY_ID" ] && [ -z "$BACKUP_S3_SECRET_ACCESS_KEY" ]; } || \
     { [ -z "$BACKUP_S3_ACCESS_KEY_ID" ] && [ -n "$BACKUP_S3_SECRET_ACCESS_KEY" ]; }; then
    echo "SM_BACKUP_S3_ACCESS_KEY_ID and SM_BACKUP_S3_SECRET_ACCESS_KEY must be set together" >&2
    exit 1
  fi
  [ -x /usr/local/bin/sm-agent ] || { echo "/usr/local/bin/sm-agent not found; the backup unit runs the root-owned agent copy, not the sm-agent-writable one. Re-run with --reinstall to restore it" >&2; exit 1; }
  if [ -z "$BACKUP_REPOS" ] && [ -f /etc/servermonitor-backup/backup.toml ] && grep -Eq '^[[:space:]]*tunnel_name[[:space:]]*=' /etc/servermonitor-backup/backup.toml; then
    BACKUP_HAS_TUNNEL=1
  fi
  if [ "$BACKUP_HAS_TUNNEL" = "1" ]; then
    backup_preserve_tunnel_section
    [ "$RECONFIGURE" = "1" ] && backup_require_tunnel_support
  fi
  warn_backup_capability
  provision_restic
  install -o root -g sm-agent -m 0750 -d /etc/servermonitor-backup
  local key_fresh=0
  if backup_generate_key; then key_fresh=1; fi
  if backup_have_transport_creds; then
    backup_write_env_file
  fi
  local repos_written=0
  if [ -n "$BACKUP_REPOS" ]; then
    backup_write_toml
    repos_written=1
  elif [ -f /etc/servermonitor-backup/backup.toml ]; then
    echo "note: --backup-repos not provided; keeping existing /etc/servermonitor-backup/backup.toml"
    chmod 0640 /etc/servermonitor-backup/backup.toml
    chown root:sm-agent /etc/servermonitor-backup/backup.toml
  fi
  if [ "$BACKUP_HAS_TUNNEL" = "1" ]; then
    backup_enroll_tunnel
  fi
  if ! backup_init_as_agent; then
    echo "sm-agent backup init failed; backup not scheduled" >&2
    exit 1
  fi
  backup_write_units
  if [ "$key_fresh" = "1" ]; then
    backup_write_recovery_kit
    backup_print_recovery_kit
  elif [ "$repos_written" = "1" ]; then
    backup_write_recovery_kit
    echo "note: recovery kit updated at /etc/servermonitor-backup/recovery-kit.txt (password unchanged, not reprinted)"
  else
    echo "note: backup recovery kit at /etc/servermonitor-backup/recovery-kit.txt (password not reprinted)"
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
without re-registration or an admin token. When --binary is given, both agent
copies are refreshed if its version differs. To force a full fresh install
instead (re-register, rewrite config), re-run with --reinstall.

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

check_binary_source_dir
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

SMART_UDEV_RULES=/etc/udev/rules.d/90-servermonitor-smart.rules
if [ "$ENABLE_SMART" = "1" ]; then
  cat > "$SMART_UDEV_RULES" <<'RULES'
KERNEL=="megaraid_sas_ioctl_node", GROUP="disk", MODE="0660"
KERNEL=="megadev[0-9]*", GROUP="disk", MODE="0660"
KERNEL=="twa[0-9]*", GROUP="disk", MODE="0660"
KERNEL=="twl[0-9]*", GROUP="disk", MODE="0660"
KERNEL=="twe[0-9]*", GROUP="disk", MODE="0660"
KERNEL=="aac[0-9]*", GROUP="disk", MODE="0660"
RULES
  chmod 0644 "$SMART_UDEV_RULES"
  udevadm control --reload >/dev/null 2>&1 || true
  for node in /dev/megaraid_sas_ioctl_node /dev/megadev0 /dev/twa0 /dev/twl0 /dev/twe0 /dev/aac0; do
    if [ -e "$node" ]; then
      chgrp disk "$node" 2>/dev/null || true
      chmod 0660 "$node" 2>/dev/null || true
    fi
  done
else
  rm -f "$SMART_UDEV_RULES"
fi

install -o sm-agent -g sm-agent -m 0700 -d /etc/servermonitor
install -o sm-agent -g sm-agent -m 0700 -d /var/lib/servermonitor
install -o sm-agent -g sm-agent -m 0755 -d /opt/servermonitor
refresh_agent_binaries

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

DOCKER_ORDER=""
if [ "$ENABLE_DOCKER" = "1" ]; then
  DOCKER_ORDER="After=docker.service docker.socket
Wants=docker.socket"
fi

cat > /etc/systemd/system/sm-agent.service <<UNIT
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
systemctl status --no-pager sm-agent.service || true

if [ "$ENABLE_BACKUP" = "1" ]; then
  provision_backup
elif [ -f /etc/systemd/system/sm-backup.timer ] || [ -f /etc/systemd/system/sm-backup.service ]; then
  backup_migrate_legacy
  backup_remove_units
fi

echo
if [ "$RECONFIGURE" = "1" ]; then
  echo "reconfigured. agent restarted with the updated capabilities."
else
  echo "installed."
fi
echo "tail logs with: journalctl -u sm-agent -f"
