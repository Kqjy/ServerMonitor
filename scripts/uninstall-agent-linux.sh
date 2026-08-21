#!/usr/bin/env bash
set -euo pipefail

KEEP_DATA=0
KEEP_USER=0
PURGE_BACKUP_KEYS=0

usage() {
  cat >&2 <<EOF
Usage: $0 [--keep-data] [--keep-user] [--purge-backup-keys]

Removes the ServerMonitor agent installed by install-agent-linux.sh: stops and
deletes the sm-agent and sm-backup systemd units, removes the agent's nftables
ban table (inet sm_agent) if --enable-ipban was used, removes the binaries
(/usr/local/bin/sm-agent and /opt/servermonitor), the config and spool
directories, and the sm-agent system user and group.

/etc/servermonitor-backup is kept by default: it holds backup.key, tunnel.key and
recovery-kit.txt, without which existing encrypted snapshots cannot be read.

  --keep-data  preserve /etc/servermonitor and /var/lib/servermonitor (agent.toml
               identity + disk spool) so a later reinstall keeps the same host;
               the deregistration sentinel is still cleared
  --keep-user  do not delete the sm-agent system user and group
  --purge-backup-keys
               also delete /etc/servermonitor-backup; existing encrypted
               snapshots become permanently unreadable

This does not deregister the host on the server. To also drop its stored history,
use Settings -> Remove in the web UI (or DELETE /api/v1/admin/hosts/{id}).
EOF
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep-data) KEEP_DATA=1; shift ;;
    --keep-user) KEEP_USER=1; shift ;;
    --purge-backup-keys) PURGE_BACKUP_KEYS=1; shift ;;
    -h|--help)   usage ;;
    *) echo "unknown arg $1" >&2; usage ;;
  esac
done

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }

echo "stopping and removing sm-agent.service ..."
systemctl stop sm-agent-privileged-sync.path sm-agent-privileged-sync.timer 2>/dev/null || true
systemctl disable sm-agent-privileged-sync.path sm-agent-privileged-sync.timer 2>/dev/null || true
systemctl stop sm-backup.timer sm-backup.service sm-backup-check.timer sm-backup-check.service 2>/dev/null || true
systemctl disable sm-backup.timer sm-backup-check.timer 2>/dev/null || true
systemctl stop 'sm-backup-restore@*.service' 2>/dev/null || true
systemctl stop sm-agent.service 2>/dev/null || true
systemctl disable sm-agent.service 2>/dev/null || true
rm -f /etc/systemd/system/sm-agent.service \
  /etc/systemd/system/sm-agent-privileged-sync.service \
  /etc/systemd/system/sm-agent-privileged-sync.path \
  /etc/systemd/system/sm-agent-privileged-sync.timer \
  /etc/systemd/system/sm-backup.service \
  /etc/systemd/system/sm-backup.timer \
  /etc/systemd/system/sm-backup-check.service \
  /etc/systemd/system/sm-backup-check.timer \
  /etc/systemd/system/sm-backup-restore@.service
systemctl daemon-reload 2>/dev/null || true
systemctl reset-failed sm-agent.service sm-backup.service sm-backup-check.service 2>/dev/null || true

if [ -x /usr/local/bin/sm-agent ]; then
  /usr/local/bin/sm-agent ipban teardown 2>/dev/null || true
elif [ -x /opt/servermonitor/sm-agent ]; then
  /opt/servermonitor/sm-agent ipban teardown 2>/dev/null || true
fi

rm -f /usr/local/bin/sm-agent
rm -rf /opt/servermonitor
rm -rf /etc/servermonitor-privileged
rm -f /etc/servermonitor/deregistered

if [ "$KEEP_DATA" = "1" ]; then
  echo "keeping /etc/servermonitor and /var/lib/servermonitor"
else
  rm -rf /etc/servermonitor /var/lib/servermonitor
fi

if [ "$KEEP_USER" = "1" ]; then
  echo "keeping sm-agent user"
elif id -u sm-agent >/dev/null 2>&1; then
  userdel sm-agent 2>/dev/null || echo "warning: could not delete sm-agent user (still referenced?)" >&2
fi

if [ "$PURGE_BACKUP_KEYS" = "1" ]; then
  rm -rf /etc/servermonitor-backup
fi

echo
echo "uninstalled."
if [ "$KEEP_DATA" = "1" ]; then
  echo "config + spool retained; reinstall with install-agent-linux.sh to reuse this host identity"
fi
if [ "$PURGE_BACKUP_KEYS" != "1" ] && [ -d /etc/servermonitor-backup ]; then
  echo "kept /etc/servermonitor-backup (backup.key, tunnel.key, recovery-kit.txt): deleting it makes existing encrypted snapshots unreadable forever."
  echo "re-run with --purge-backup-keys once those snapshots are gone, or remove the directory by hand."
fi
