#!/usr/bin/env bash
set -euo pipefail

KEEP_DATA=0
KEEP_USER=0

usage() {
  cat >&2 <<EOF
Usage: $0 [--keep-data] [--keep-user]

Removes the ServerMonitor agent installed by install-agent-linux.sh: stops and
deletes the sm-agent systemd service, removes the binaries (/usr/local/bin/sm-agent
and /opt/servermonitor), the config and spool directories, and the sm-agent system
user and group.

  --keep-data  preserve /etc/servermonitor and /var/lib/servermonitor (agent.toml
               identity + disk spool) so a later reinstall keeps the same host;
               the deregistration sentinel is still cleared
  --keep-user  do not delete the sm-agent system user and group

This does not deregister the host on the server. To also drop its stored history,
use Settings -> Remove in the web UI (or DELETE /api/v1/admin/hosts/{id}).
EOF
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep-data) KEEP_DATA=1; shift ;;
    --keep-user) KEEP_USER=1; shift ;;
    -h|--help)   usage ;;
    *) echo "unknown arg $1" >&2; usage ;;
  esac
done

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }

echo "stopping and removing sm-agent.service ..."
systemctl stop sm-agent-privileged-sync.path sm-agent-privileged-sync.timer 2>/dev/null || true
systemctl disable sm-agent-privileged-sync.path sm-agent-privileged-sync.timer 2>/dev/null || true
systemctl stop sm-agent.service 2>/dev/null || true
systemctl disable sm-agent.service 2>/dev/null || true
rm -f /etc/systemd/system/sm-agent.service \
  /etc/systemd/system/sm-agent-privileged-sync.service \
  /etc/systemd/system/sm-agent-privileged-sync.path \
  /etc/systemd/system/sm-agent-privileged-sync.timer
systemctl daemon-reload 2>/dev/null || true
systemctl reset-failed sm-agent.service 2>/dev/null || true

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

echo
echo "uninstalled."
if [ "$KEEP_DATA" = "1" ]; then
  echo "config + spool retained; reinstall with install-agent-linux.sh to reuse this host identity"
fi
