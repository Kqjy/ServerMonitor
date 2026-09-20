#!/usr/bin/env bash
set -euo pipefail

SERVER_URL='__SERVER_URL__'
CANONICAL_INSTALLER_SHA256='a149facf0c3295abc9ed089c6c596b0003ba7b6695d8e449aa091a0fafb67eb7'
TOKEN="${SM_TOKEN:-}"
DOWNLOADED_SERVER_PUBKEY=""
INTERVAL="${SM_INTERVAL:-10}"
INSECURE="${SM_INSECURE:-0}"
PERSIST_INSECURE="${SM_PERSIST_INSECURE:-0}"
ENABLE_SMART="${SM_ENABLE_SMART:-0}"
ENABLE_SMART_NVME="${SM_ENABLE_SMART_NVME:-0}"
ENABLE_DOCKER="${SM_ENABLE_DOCKER:-0}"
ENABLE_GPU="${SM_ENABLE_GPU:-0}"
ENABLE_NETWORK="${SM_ENABLE_NETWORK:-0}"
ENABLE_PORT_OWNERS="${SM_ENABLE_PORT_OWNERS:-0}"
ENABLE_IPBAN="${SM_ENABLE_IPBAN:-0}"
ENABLE_ALL="${SM_ENABLE_ALL:-0}"
ENABLE_BACKUP=0
DISABLE_BACKUP=0
case "$(printf '%s' "${SM_ENABLE_BACKUP:-}" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes|on) ENABLE_BACKUP=1 ;;
    0|false|no|off) DISABLE_BACKUP=1 ;;
esac
BACKUP_REPOS="${SM_BACKUP_REPOS:-}"
BACKUP_REPO_NAMES="${SM_BACKUP_REPO_NAMES:-}"
BACKUP_PATHS="${SM_BACKUP_PATHS:-}"
BACKUP_EXCLUDES="${SM_BACKUP_EXCLUDES:-}"
BACKUP_ONE_FILE_SYSTEM="${SM_BACKUP_ONE_FILE_SYSTEM:-}"
BACKUP_TIME="${SM_BACKUP_TIME:-02:30}"
BACKUP_PRUNE_MODE="${SM_BACKUP_PRUNE_MODE:-host}"
BACKUP_S3_REGION="${SM_BACKUP_S3_REGION:-}"
BACKUP_S3_PATH_STYLE="${SM_BACKUP_S3_PATH_STYLE:-0}"
BACKUP_S3_ACCESS_KEY_ID="${SM_BACKUP_S3_ACCESS_KEY_ID:-}"
BACKUP_S3_SECRET_ACCESS_KEY="${SM_BACKUP_S3_SECRET_ACCESS_KEY:-}"
BACKUP_S3_SESSION_TOKEN="${SM_BACKUP_S3_SESSION_TOKEN:-}"
BACKUP_REST_USERNAME="${SM_BACKUP_REST_USERNAME:-}"
BACKUP_REST_PASSWORD="${SM_BACKUP_REST_PASSWORD:-}"
RESTIC_VERSION="0.19.0"
DEFAULT_BACKUP_EXCLUDES_CSV='**/.cache,/var/lib/servermonitor,/var/lib/docker/overlay2,/var/lib/docker/overlay,/var/lib/docker/image,/var/lib/docker/containers,/var/lib/docker/buildkit,/var/lib/docker/tmp,/var/lib/docker/plugins,/var/lib/docker/network,/var/lib/docker/runtimes,/var/lib/docker/aufs,/var/lib/docker/btrfs,/var/lib/docker/devicemapper,/var/lib/docker/fuse-overlayfs,/var/lib/docker/vfs,/var/lib/docker/zfs,/var/lib/containerd'
DEFAULT_BACKUP_EXCLUDES_TOML='excludes = ["**/.cache", "/var/lib/servermonitor", "/var/lib/docker/overlay2", "/var/lib/docker/overlay", "/var/lib/docker/image", "/var/lib/docker/containers", "/var/lib/docker/buildkit", "/var/lib/docker/tmp", "/var/lib/docker/plugins", "/var/lib/docker/network", "/var/lib/docker/runtimes", "/var/lib/docker/aufs", "/var/lib/docker/btrfs", "/var/lib/docker/devicemapper", "/var/lib/docker/fuse-overlayfs", "/var/lib/docker/vfs", "/var/lib/docker/zfs", "/var/lib/containerd"]'
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

detect_platform() {
    local os arch_raw
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch_raw="$(uname -m)"
    case "$arch_raw" in
        x86_64|amd64) printf '%s-amd64' "$os" ;;
        aarch64|arm64) printf '%s-arm64' "$os" ;;
        *) return 1 ;;
    esac
}

agent_binary_version() {
    "$1" --version 2>/dev/null | awk 'NR == 1 { print $2; exit }' || true
}

agent_token_from_config() {
    awk -F'"' '/^token[[:space:]]*=/ { print $2; exit }' /etc/servermonitor/agent.toml
}

capture_downloaded_server_pubkey() {
    local headers="$1" value
    value="$(awk -F':[[:space:]]*' 'tolower($1) == "x-agent-pubkey" { gsub("\r", "", $2); print $2; exit }' "$headers")"
    if [[ "$value" =~ ^[0-9a-fA-F]{64}$ ]]; then
        DOWNLOADED_SERVER_PUBKEY="$value"
    fi
}

refresh_agent_binaries() {
    if [ "$RECONFIGURE" != "1" ]; then
        install -o root -g root -m 0755 "$TMP/sm-agent" /usr/local/bin/sm-agent
        install -o sm-agent -g sm-agent -m 0755 "$TMP/sm-agent" /opt/servermonitor/sm-agent
        return 0
    fi
    if [ -f /usr/local/bin/sm-agent ]; then
        chown root:root /usr/local/bin/sm-agent
        chmod 0755 /usr/local/bin/sm-agent
    fi
    local platform token old_version new_version
    local -a curl_opts
    if ! platform="$(detect_platform)"; then
        printf 'warning: unsupported arch %s for a server binary download; keeping the current sm-agent binaries\n' "$(uname -m)" >&2
        return 0
    fi
    token="$(agent_token_from_config)"
    if [ -z "$token" ]; then
        printf 'warning: no token found in /etc/servermonitor/agent.toml; keeping the current sm-agent binaries\n' >&2
        return 0
    fi
    curl_opts=(-fsSL)
    [ "$INSECURE" = "1" ] && curl_opts+=(-k)
    grep -Eq '^[[:space:]]*insecure_skip_verify[[:space:]]*=[[:space:]]*true' /etc/servermonitor/agent.toml && curl_opts+=(-k)
    if ! curl "${curl_opts[@]}" --config - -D "$TMP/agent.headers" -o "$TMP/sm-agent" "$SERVER_URL/api/v1/agent/binary?platform=$platform" <<CURLCFG
header = "X-Agent-Token: $token"
CURLCFG
    then
        printf 'warning: could not download the current sm-agent from %s; keeping the current binaries\n' "$SERVER_URL" >&2
        return 0
    fi
    capture_downloaded_server_pubkey "$TMP/agent.headers"
    chmod 0755 "$TMP/sm-agent"
    new_version="$(agent_binary_version "$TMP/sm-agent")"
    old_version="$(agent_binary_version /usr/local/bin/sm-agent)"
    if [ -z "$new_version" ]; then
        printf 'warning: downloaded sm-agent did not report a version; keeping the current binaries\n' >&2
        return 0
    fi
    if [ "$new_version" != "$old_version" ]; then
        install -o root -g root -m 0755 "$TMP/sm-agent" /usr/local/bin/sm-agent
        install -o sm-agent -g sm-agent -m 0755 "$TMP/sm-agent" /opt/servermonitor/sm-agent
        printf 'refreshed sm-agent binaries (v%s -> v%s)\n' "$old_version" "$new_version"
    fi
}

pin_agent_signing_pubkey() {
    local pin=/etc/servermonitor-privileged/agent-signing.pub
    local configured existing
    install -o root -g root -m 0700 -d /etc/servermonitor-privileged
    configured="$DOWNLOADED_SERVER_PUBKEY"
    if [ -z "$configured" ]; then
        configured="$(awk -F'"' '/^[[:space:]]*server_pubkey[[:space:]]*=/ { print $2; exit }' /etc/servermonitor/agent.toml)"
    fi
    if [ -f "$pin" ]; then
        existing="$(tr -d '[:space:]' < "$pin")"
        if [ -n "$configured" ] && [ "$configured" != "$existing" ]; then
            printf 'warning: the server reports a different agent signing key; keeping the existing root-owned update pin at %s. Re-register explicitly if the signing key was intentionally rotated.\n' "$pin" >&2
        fi
        chown root:root "$pin"
        chmod 0400 "$pin"
        return 0
    fi
    if ! [[ "$configured" =~ ^[0-9a-fA-F]{64}$ ]]; then
        printf 'warning: no valid server signing key was returned; privileged backup-agent auto-sync is disabled until the agent is re-registered and this installer is rerun\n' >&2
        return 0
    fi
    printf '%s\n' "$configured" > "$pin"
    chown root:root "$pin"
    chmod 0400 "$pin"
}

set_agent_config_bool() {
    local key="$1" value="$2" rendered=false tmp
    [ "$value" = "1" ] && rendered=true
    tmp="$(mktemp /etc/servermonitor/agent.toml.XXXXXX)"
    awk -v key="$key" -v value="$rendered" '
      BEGIN { found = 0 }
      $0 ~ "^[[:space:]]*" key "[[:space:]]*=" { print key " = " value; found = 1; next }
      { print }
      END { if (!found) print key " = " value }
    ' /etc/servermonitor/agent.toml > "$tmp"
    chown sm-agent:sm-agent "$tmp"
    chmod 0600 "$tmp"
    mv -f "$tmp" /etc/servermonitor/agent.toml
}

write_privileged_sync_units() {
    local pin=/etc/servermonitor-privileged/agent-signing.pub
    [ -s "$pin" ] || return 0
    cat > /etc/systemd/system/sm-agent-privileged-sync.service <<'UNIT'
[Unit]
Description=Synchronize ServerMonitor's signed privileged backup agent copy
After=sm-agent.service
ConditionPathExists=/opt/servermonitor/sm-agent.sig

[Service]
Type=oneshot
ExecStart=/usr/local/bin/sm-agent sync-privileged --source /opt/servermonitor/sm-agent --signature /opt/servermonitor/sm-agent.sig --pubkey-file /etc/servermonitor-privileged/agent-signing.pub
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/usr/local/bin
ProtectHome=true
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
RestrictAddressFamilies=AF_UNIX
UNIT
    chmod 0644 /etc/systemd/system/sm-agent-privileged-sync.service

    cat > /etc/systemd/system/sm-agent-privileged-sync.path <<'UNIT'
[Unit]
Description=Watch for a signed ServerMonitor resident-agent update

[Path]
PathChanged=/opt/servermonitor/sm-agent.sig
Unit=sm-agent-privileged-sync.service

[Install]
WantedBy=multi-user.target
UNIT
    chmod 0644 /etc/systemd/system/sm-agent-privileged-sync.path

    cat > /etc/systemd/system/sm-agent-privileged-sync.timer <<'UNIT'
[Unit]
Description=Fallback reconciliation for ServerMonitor's privileged agent copy

[Timer]
OnBootSec=10min
OnUnitActiveSec=6h
RandomizedDelaySec=15min
Persistent=true
Unit=sm-agent-privileged-sync.service

[Install]
WantedBy=timers.target
UNIT
    chmod 0644 /etc/systemd/system/sm-agent-privileged-sync.timer
}

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

install_bzip2() {
    if command -v bunzip2 >/dev/null 2>&1; then
        return 0
    fi
    printf 'installing bzip2 (required to unpack restic) ...\n'
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
        printf 'warning: no known package manager; install bzip2 manually to unpack restic\n' >&2
    fi
}

warn_backup_capability() {
    cat >&2 <<'WARN'

WARNING: SM_ENABLE_BACKUP grants the sm-backup unit CAP_DAC_READ_SEARCH, letting
         it read every file on the host to back it up. The grant is confined to
         the oneshot sm-backup.service and its timer, NOT the resident sm-agent
         daemon, and it is excluded from SM_ENABLE_ALL. Enable it only where
         whole-host read access for backups is worth that exposure.

WARN
}

warn_ipban_capability() {
    cat >&2 <<'WARN'

WARNING: SM_ENABLE_IPBAN grants the resident sm-agent daemon CAP_NET_ADMIN so it
         can write bans into nftables, and joins it to the systemd-journal and
         adm groups so it can read every service's logs (login failures live
         next to whatever else your services log). A compromised agent could
         then rewrite the host firewall. It is excluded from SM_ENABLE_ALL;
         enable it only where reactive IP banning is worth that exposure.
         Bans persist in the kernel table 'inet sm_agent' until they expire;
         'sm-agent ipban status' lists them and 'sm-agent ipban teardown'
         removes the table.

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
            if (value ~ /^".*"$/) {
                sub(/^"/, "", value)
                sub(/"$/, "", value)
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
        echo "/usr/local/bin/sm-agent does not support backup tunnel-enroll; re-run with SM_REINSTALL=1 to download a current sm-agent" >&2
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
        *) err "unsupported arch for restic: $arch_raw" ;;
    esac
    if [ -x /usr/local/bin/sm-restic ] && /usr/local/bin/sm-restic version 2>/dev/null | grep -q "restic ${RESTIC_VERSION} "; then
        printf 'restic %s already present at /usr/local/bin/sm-restic\n' "$RESTIC_VERSION"
        rm -f /opt/servermonitor/restic
        return 0
    fi
    command -v sha256sum >/dev/null 2>&1 || err "sha256sum required to verify the restic download"
    install_bzip2
    command -v bunzip2 >/dev/null 2>&1 || err "bunzip2 (bzip2) required to unpack restic; install it and re-run"
    url="https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${arch}.bz2"
    tmp="$(mktemp -d)"
    bz2="$tmp/restic.bz2"
    printf 'downloading restic %s for linux_%s ...\n' "$RESTIC_VERSION" "$arch"
    if ! curl -fsSL -o "$bz2" "$url"; then
        rm -rf "$tmp"; err "restic download failed"
    fi
    if ! printf '%s  %s\n' "$sha" "$bz2" | sha256sum -c - >/dev/null 2>&1; then
        rm -rf "$tmp"; err "restic checksum mismatch for linux_${arch}; refusing to install"
    fi
    bunzip2 -c "$bz2" > "$tmp/restic"
    install -o root -g root -m 0755 "$tmp/restic" /usr/local/bin/sm-restic
    rm -rf "$tmp"
    rm -f /opt/servermonitor/restic
    printf 'restic %s installed at /usr/local/bin/sm-restic\n' "$RESTIC_VERSION"
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
    printf 'note: relocated backup config from /etc/servermonitor to /etc/servermonitor-backup\n'
}

backup_generate_key() {
    if [ -f /etc/servermonitor-backup/backup.key ]; then
        printf 'note: existing /etc/servermonitor-backup/backup.key kept (guards existing snapshots; never regenerated)\n'
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
    local tmp i name url tunnel_name tunnel_node first item path_csv excludes_value one_file_system
    local -a repo_arr name_arr path_arr exclude_arr default_exclude_arr appended_exclude_arr
    IFS=',' read -ra repo_arr <<< "$BACKUP_REPOS"
    IFS=',' read -ra name_arr <<< "$BACKUP_REPO_NAMES"
    path_csv="$BACKUP_PATHS"
    if [ -z "$path_csv" ]; then
        path_csv="/etc,/home,/root,/var/lib"
        [ -d /var/lib/docker/volumes ] && path_csv="$path_csv,/var/lib/docker/volumes"
    fi
    IFS=',' read -ra path_arr <<< "$path_csv"
    excludes_value="$(backup_trim "$BACKUP_EXCLUDES")"
    IFS=',' read -ra default_exclude_arr <<< "$DEFAULT_BACKUP_EXCLUDES_CSV"
    case "$(printf '%s' "$excludes_value" | tr '[:upper:]' '[:lower:]')" in
        '') exclude_arr=("${default_exclude_arr[@]}") ;;
        none) exclude_arr=() ;;
        +*)
            IFS=',' read -ra appended_exclude_arr <<< "${excludes_value#+}"
            exclude_arr=("${default_exclude_arr[@]}" ${appended_exclude_arr[@]+"${appended_exclude_arr[@]}"})
            ;;
        *) IFS=',' read -ra exclude_arr <<< "$excludes_value" ;;
    esac
    case "$(printf '%s' "$(backup_trim "$BACKUP_ONE_FILE_SYSTEM")" | tr '[:upper:]' '[:lower:]')" in
        0|false|no) one_file_system=false ;;
        *) one_file_system=true ;;
    esac
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
        if [ -z "$excludes_value" ]; then
            echo "$DEFAULT_BACKUP_EXCLUDES_TOML"
        else
            first=1
            printf 'excludes = ['
            for item in ${exclude_arr[@]+"${exclude_arr[@]}"}; do
                item="$(backup_trim "$item")"
                [ -z "$item" ] && continue
                if [ "$first" = 1 ]; then first=0; else printf ', '; fi
                printf '"%s"' "$(backup_toml_escape "$item")"
            done
            printf ']\n'
        fi
        printf 'one_file_system = %s\n' "$one_file_system"
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
        *) err "SM_BACKUP_TIME must be HH:MM (got: $BACKUP_TIME)" ;;
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
recovery kit were KEPT (they guard existing snapshots). To remove them:
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

backup_have_managed_repos() {
    [ -r /var/lib/servermonitor/managed-backups/repositories.json ] &&
        grep -Eq '"repositories"[[:space:]]*:[[:space:]]*\[[[:space:]]*\{' /var/lib/servermonitor/managed-backups/repositories.json
}

backup_write_effective_recovery_kit() {
    local tmp
    tmp="$(mktemp /etc/servermonitor-backup/recovery-kit.txt.XXXXXX)"
    if ! /usr/local/bin/sm-agent backup recovery-kit --config /etc/servermonitor-backup/backup.toml > "$tmp"; then
        rm -f "$tmp"
        return 1
    fi
    chmod 0400 "$tmp"
    chown root:root "$tmp"
    mv -f "$tmp" /etc/servermonitor-backup/recovery-kit.txt
}

provision_backup() {
    backup_validate_tunnel_repos
    backup_migrate_legacy
    local managed_repos=0
    if backup_have_managed_repos; then managed_repos=1; fi
    if [ ! -f /etc/servermonitor-backup/backup.toml ] && [ -z "$BACKUP_REPOS" ] && [ "$managed_repos" != "1" ]; then
        err "SM_ENABLE_BACKUP requires SM_BACKUP_REPOS, or a centrally assigned direct repository that the resident agent has already synchronized"
    fi
    case "$BACKUP_PRUNE_MODE" in
        host|external) ;;
        *) err "SM_BACKUP_PRUNE_MODE must be \"host\" or \"external\"" ;;
    esac
    if { [ -n "$BACKUP_S3_ACCESS_KEY_ID" ] && [ -z "$BACKUP_S3_SECRET_ACCESS_KEY" ]; } || \
       { [ -z "$BACKUP_S3_ACCESS_KEY_ID" ] && [ -n "$BACKUP_S3_SECRET_ACCESS_KEY" ]; }; then
        err "SM_BACKUP_S3_ACCESS_KEY_ID and SM_BACKUP_S3_SECRET_ACCESS_KEY must be set together"
    fi
    [ -x /usr/local/bin/sm-agent ] || err "/usr/local/bin/sm-agent not found; the backup unit runs the root-owned agent copy, not the sm-agent-writable one. Re-run with SM_REINSTALL=1 to restore it"
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
        printf 'note: SM_BACKUP_REPOS not provided; keeping existing /etc/servermonitor-backup/backup.toml\n'
        chmod 0640 /etc/servermonitor-backup/backup.toml
        chown root:sm-agent /etc/servermonitor-backup/backup.toml
    elif [ "$managed_repos" = "1" ]; then
        backup_write_toml
        repos_written=1
        printf 'note: wrote backup.toml for the centrally assigned direct repository\n'
    fi
    if [ "$BACKUP_HAS_TUNNEL" = "1" ]; then
        backup_enroll_tunnel
    fi
    if ! backup_init_as_agent; then
        err "sm-agent backup init failed; backup not scheduled"
    fi
    if [ "$managed_repos" = "1" ]; then
        backup_write_effective_recovery_kit || err "could not write a recovery kit containing the effective managed repository; backup not scheduled"
    fi
    backup_write_units
    if [ "$managed_repos" = "1" ] && [ "$key_fresh" = "1" ]; then
        backup_print_recovery_kit
    elif [ "$managed_repos" = "1" ]; then
        printf 'note: recovery kit updated from the effective managed configuration at /etc/servermonitor-backup/recovery-kit.txt (password unchanged, not reprinted)\n'
    elif [ "$key_fresh" = "1" ]; then
        backup_write_recovery_kit
        backup_print_recovery_kit
    elif [ "$repos_written" = "1" ]; then
        backup_write_recovery_kit
        printf 'note: recovery kit updated at /etc/servermonitor-backup/recovery-kit.txt (password unchanged, not reprinted)\n'
    else
        printf 'note: backup recovery kit at /etc/servermonitor-backup/recovery-kit.txt (password not reprinted)\n'
    fi
}

[ "$(id -u)" = "0" ] || err "run as root (use sudo)"
need curl
need install
need uname
need useradd

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

REINSTALL="${SM_REINSTALL:-0}"
RECONFIGURE=0
if [ "$REINSTALL" != "1" ] && [ -f /etc/servermonitor/agent.toml ]; then
    RECONFIGURE=1
    [ -x /opt/servermonitor/sm-agent ] || err "found /etc/servermonitor/agent.toml but no agent binary at /opt/servermonitor/sm-agent; re-run with SM_REINSTALL=1 for a full install"
    cat >&2 <<'NOTE'

reconfiguring the existing sm-agent install in place: re-applying capabilities
and group memberships from the SM_ENABLE_* flags, keeping the current identity.
The binary is refreshed from the server when its version differs. No
re-registration or token input is needed. To force a full fresh install instead
(re-download the binary, rewrite the config), set SM_REINSTALL=1.

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

PLATFORM="$(detect_platform)" || err "unsupported arch: $(uname -m)"

CURL_OPTS=(-fsSL)
[ "$INSECURE" = "1" ] && CURL_OPTS+=(-k)

printf 'downloading %s agent for %s ...\n' "$SERVER_URL" "$PLATFORM"
curl "${CURL_OPTS[@]}" --config - -D "$TMP/agent.headers" -o "$TMP/sm-agent" "$SERVER_URL/api/v1/agent/binary?platform=$PLATFORM" <<CURLCFG
header = "X-Agent-Token: $TOKEN"
CURLCFG
capture_downloaded_server_pubkey "$TMP/agent.headers"
fi

id -u sm-agent >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin --user-group --comment 'ServerMonitor agent' sm-agent

[ "$ENABLE_SMART" = "1" ] && install_smartmontools
[ "$ENABLE_SMART" = "1" ] && warn_nvme_smart
[ "$ENABLE_IPBAN" = "1" ] && warn_ipban_capability

WANTED_GROUPS=()
[ "$ENABLE_SMART" = "1" ]  && WANTED_GROUPS+=(disk)
[ "$ENABLE_DOCKER" = "1" ] && WANTED_GROUPS+=(docker)
[ "$ENABLE_GPU" = "1" ]    && WANTED_GROUPS+=(video)
[ "$ENABLE_IPBAN" = "1" ]  && WANTED_GROUPS+=(systemd-journal adm)
PRESENT_GROUPS=()
for grp in "${WANTED_GROUPS[@]}"; do
    if getent group "$grp" >/dev/null 2>&1; then
        PRESENT_GROUPS+=("$grp")
    else
        printf 'note: group %s not present; skipping (re-run after creating it)\n' "$grp" >&2
    fi
done
usermod -G "$(IFS=,; printf '%s' "${PRESENT_GROUPS[*]:-}")" sm-agent

SMART_UDEV_RULES=/etc/udev/rules.d/90-servermonitor-smart.rules
SMART_IOCTL_NODES="/dev/megaraid_sas_ioctl_node /dev/megadev0 /dev/twa0 /dev/twl0 /dev/twe0 /dev/aac0"
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
    for node in $SMART_IOCTL_NODES; do
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

if [ "$ENABLE_IPBAN" != "1" ] && [ -x /usr/local/bin/sm-agent ]; then
    /usr/local/bin/sm-agent ipban teardown >/dev/null 2>&1 || true
fi

if [ "$RECONFIGURE" != "1" ]; then
INSECURE_LINE=false
[ "$INSECURE" = "1" ] && [ "$PERSIST_INSECURE" = "1" ] && INSECURE_LINE=true

umask 0077
TMP_CFG="$(mktemp /etc/servermonitor/agent.toml.XXXXXX)"
cat >"$TMP_CFG" <<EOF
server_url = "$SERVER_URL"
token      = "$TOKEN"
server_pubkey = "$DOWNLOADED_SERVER_PUBKEY"
interval_s = $INTERVAL
spool_path = "/var/lib/servermonitor/spool.db"
insecure_skip_verify = $INSECURE_LINE
enable_ipban = false
EOF
chmod 0600 "$TMP_CFG"
chown sm-agent:sm-agent "$TMP_CFG"
mv -f "$TMP_CFG" /etc/servermonitor/agent.toml
fi
set_agent_config_bool enable_ipban "$ENABLE_IPBAN"

pin_agent_signing_pubkey
write_privileged_sync_units

CAPS=""
[ "$ENABLE_PORT_OWNERS" = "1" ] && CAPS="CAP_DAC_READ_SEARCH CAP_SYS_PTRACE"
[ "$ENABLE_SMART" = "1" ]       && CAPS="${CAPS:+$CAPS }CAP_SYS_RAWIO"
[ "$ENABLE_SMART_NVME" = "1" ]  && CAPS="${CAPS:+$CAPS }CAP_SYS_ADMIN"
[ "$ENABLE_NETWORK" = "1" ]     && CAPS="${CAPS:+$CAPS }CAP_NET_ADMIN CAP_NET_RAW"
if [ "$ENABLE_IPBAN" = "1" ]; then
    case " $CAPS " in
        *" CAP_NET_ADMIN "*) ;;
        *) CAPS="${CAPS:+$CAPS }CAP_NET_ADMIN" ;;
    esac
fi

DOCKER_ORDER=""
if [ "$ENABLE_DOCKER" = "1" ]; then
  DOCKER_ORDER="After=docker.service docker.socket
Wants=docker.socket"
fi

SMART_NODE_FIXUP=""
if [ "$ENABLE_SMART" = "1" ]; then
  SMART_NODE_FIXUP="ExecStartPre=+/bin/sh -c 'for n in $SMART_IOCTL_NODES; do [ -e \$n ] || continue; chgrp disk \$n || echo \"sm-agent: chgrp disk \$n failed\" >&2; chmod 0660 \$n || echo \"sm-agent: chmod 0660 \$n failed\" >&2; done; exit 0'"
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
$SMART_NODE_FIXUP
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
if [ -s /etc/servermonitor-privileged/agent-signing.pub ]; then
    systemctl enable --now sm-agent-privileged-sync.path >/dev/null 2>&1 || true
    systemctl enable --now sm-agent-privileged-sync.timer >/dev/null 2>&1 || true
fi
systemctl restart sm-agent.service

if [ "$ENABLE_BACKUP" = "1" ]; then
    provision_backup
elif [ -f /etc/systemd/system/sm-backup.timer ] || [ -f /etc/systemd/system/sm-backup.service ]; then
    if [ "$DISABLE_BACKUP" = "1" ]; then
        backup_migrate_legacy
        backup_remove_units
    else
        printf 'note: keeping the existing sm-backup schedule; the refreshed agent keeps backing up. Pass SM_ENABLE_BACKUP=0 to remove it.\n'
    fi
fi

if [ "$RECONFIGURE" = "1" ]; then
    printf '\nreconfigured. agent restarted with the updated capabilities.\n'
else
    printf '\ninstalled. agent is running.\n'
fi
printf 'logs: journalctl -u sm-agent -f\n'
