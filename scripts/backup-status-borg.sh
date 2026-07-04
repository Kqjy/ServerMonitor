#!/usr/bin/env bash
set -euo pipefail

name=""
status_file="/var/lib/servermonitor/backup-status.json"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --name)
      shift
      if [ "$#" -eq 0 ]; then
        echo "backup-status-borg.sh: --name requires a value" >&2
        exit 2
      fi
      name="$1"
      shift
      ;;
    --status-file)
      shift
      if [ "$#" -eq 0 ]; then
        echo "backup-status-borg.sh: --status-file requires a value" >&2
        exit 2
      fi
      status_file="$1"
      shift
      ;;
    --)
      shift
      break
      ;;
    *)
      echo "backup-status-borg.sh: unknown wrapper option $1" >&2
      exit 2
      ;;
  esac
done

if [ -z "$name" ]; then
  echo "backup-status-borg.sh: --name is required" >&2
  exit 2
fi

if [ "$#" -eq 0 ]; then
  echo "backup-status-borg.sh: borg command is required after --" >&2
  exit 2
fi

started=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
started_epoch=$(date -u +"%s")
set +e
"$@"
rc=$?
set -e
finished=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
finished_epoch=$(date -u +"%s")
duration_s=$((finished_epoch - started_epoch))

status_dir=$(dirname "$status_file")
mkdir -p "$status_dir"
tmp=$(mktemp "$status_dir/.backup-status.XXXXXX")
chmod 0644 "$tmp"
info_tmp=$(mktemp "$status_dir/.borg-info.XXXXXX")
list_tmp=$(mktemp "$status_dir/.borg-list.XXXXXX")
info_err=$(mktemp "$status_dir/.borg-info-err.XXXXXX")
list_err=$(mktemp "$status_dir/.borg-list-err.XXXXXX")
existing_tmp=""

cleanup() {
  rm -f "$tmp" "$info_tmp" "$list_tmp" "$info_err" "$list_err"
  if [ -n "$existing_tmp" ]; then
    rm -f "$existing_tmp"
  fi
}
trap cleanup EXIT

printf '{}\n' > "$info_tmp"
printf '{"archives":[]}\n' > "$list_tmp"

if [ -z "${BORG_REPO:-}" ]; then
  echo "backup-status-borg.sh: BORG_REPO is required for borg info/list" >&2
else
  if ! borg info --json --last 1 "$BORG_REPO" > "$info_tmp" 2> "$info_err"; then
    echo "backup-status-borg.sh: borg info failed: $(tr '\n' ' ' < "$info_err")" >&2
    printf '{}\n' > "$info_tmp"
  fi
  if ! borg list --json "$BORG_REPO" > "$list_tmp" 2> "$list_err"; then
    echo "backup-status-borg.sh: borg list failed: $(tr '\n' ' ' < "$list_err")" >&2
    printf '{"archives":[]}\n' > "$list_tmp"
  fi
fi

if command -v python3 >/dev/null 2>&1; then
  python3 - "$status_file" "$tmp" "$name" "$started" "$finished" "$rc" "$duration_s" "$info_tmp" "$list_tmp" <<'PY'
import datetime
import json
import os
import sys

status_path, tmp_path, name, started, finished, rc, duration_s, info_path, list_path = sys.argv[1:]
success = rc == "0"

def load_json(path, default):
    if not os.path.exists(path):
        return default
    with open(path, "r", encoding="utf-8") as f:
        text = f.read().strip()
    if not text:
        return default
    return json.loads(text)

def first_number(obj, keys):
    if not isinstance(obj, dict):
        return None
    for key in keys:
        value = obj.get(key)
        if isinstance(value, (int, float)):
            return int(value)
    return None

def normalize_time(value):
    if not value:
        return None
    text = str(value)
    try:
        parsed = datetime.datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        return text
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=datetime.timezone.utc)
    return parsed.astimezone(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")

status = load_json(status_path, {"version": 1, "repos": []})
if not isinstance(status, dict):
    raise SystemExit("backup-status-borg.sh: status file must be a JSON object")
repos = status.get("repos")
if not isinstance(repos, list):
    repos = []

previous = {}
for repo in repos:
    if isinstance(repo, dict) and repo.get("name") == name:
        previous = repo
        break

info = load_json(info_path, {})
listing = load_json(list_path, {"archives": []})
info_archives = info.get("archives") if isinstance(info, dict) else []
if not isinstance(info_archives, list):
    info_archives = []
list_archives = listing.get("archives") if isinstance(listing, dict) else []
if not isinstance(list_archives, list):
    list_archives = []
archive = info_archives[-1] if info_archives and isinstance(info_archives[-1], dict) else {}
stats = archive.get("stats") if isinstance(archive, dict) else {}
if not isinstance(stats, dict):
    stats = {}

entry = {
    "name": name,
    "engine": "borg",
    "last_started": started,
    "last_finished": finished,
    "success": success,
    "error": "" if success else f"borg command exited {rc}",
    "duration_s": int(duration_s),
}

last_success = finished if success else previous.get("last_success")
if last_success:
    entry["last_success"] = last_success

added_bytes = first_number(stats, ["deduplicated_size", "compressed_size", "original_size"])
if added_bytes is not None:
    entry["added_bytes"] = added_bytes

cache_stats = info.get("cache", {}).get("stats", {}) if isinstance(info, dict) else {}
total_bytes = first_number(cache_stats, ["total_size", "total_csize", "unique_size"])
if total_bytes is None:
    total_bytes = first_number(stats, ["original_size"])
if total_bytes is not None:
    entry["total_bytes"] = total_bytes

if list_archives:
    entry["snapshot_count"] = len(list_archives)
elif info_archives:
    entry["snapshot_count"] = len(info_archives)

snapshots = []
for item in list_archives[-50:]:
    if not isinstance(item, dict):
        continue
    snap = {}
    snap_id = item.get("id") or item.get("archive") or item.get("name")
    snap_time = normalize_time(item.get("time") or item.get("start"))
    if snap_id:
        snap["id"] = str(snap_id)
    if snap_time:
        snap["time"] = snap_time
    snap["paths"] = []
    snapshots.append(snap)
if snapshots:
    entry["snapshots"] = snapshots

replaced = False
merged = []
for repo in repos:
    if isinstance(repo, dict) and repo.get("name") == name:
        merged.append(entry)
        replaced = True
    else:
        merged.append(repo)
if not replaced:
    merged.append(entry)

status["version"] = 1
status["repos"] = merged
with open(tmp_path, "w", encoding="utf-8") as f:
    json.dump(status, f, sort_keys=True, separators=(",", ":"))
    f.write("\n")
os.replace(tmp_path, status_path)
PY
elif command -v jq >/dev/null 2>&1; then
  existing_tmp=$(mktemp "$status_dir/.backup-status-existing.XXXXXX")
  if [ -f "$status_file" ]; then
    cp "$status_file" "$existing_tmp"
  else
    printf '{"version":1,"repos":[]}\n' > "$existing_tmp"
  fi
  success_json=false
  if [ "$rc" -eq 0 ]; then
    success_json=true
  fi
  jq -n --arg name "$name" --arg started "$started" --arg finished "$finished" --arg rc "$rc" --argjson success "$success_json" --argjson duration "$duration_s" --slurpfile status "$existing_tmp" --slurpfile info "$info_tmp" --slurpfile list "$list_tmp" '
    ($status[0] // {"version":1,"repos":[]}) as $s |
    ($info[0] // {}) as $i |
    ($list[0] // {"archives":[]}) as $l |
    ($s.repos // []) as $repos |
    ([$repos[]? | select(.name == $name)][0] // {}) as $prev |
    (($i.archives // []) | .[-1] // {}) as $archive |
    ($archive.stats // {}) as $stats |
    ($l.archives // []) as $archives |
    {
      name: $name,
      engine: "borg",
      last_started: $started,
      last_finished: $finished,
      last_success: (if $success then $finished else ($prev.last_success // null) end),
      success: $success,
      error: (if $success then "" else ("borg command exited " + $rc) end),
      duration_s: $duration,
      added_bytes: ($stats.deduplicated_size // $stats.compressed_size // $stats.original_size // null),
      total_bytes: ($i.cache.stats.total_size // $i.cache.stats.total_csize // $i.cache.stats.unique_size // $stats.original_size // null),
      snapshot_count: (if ($archives | length) > 0 then ($archives | length) elif (($i.archives // []) | length) > 0 then (($i.archives // []) | length) else null end),
      snapshots: ($archives[-50:] | map({id: ((.id // .archive // .name // "") | tostring), time: (.time // .start // empty), paths: []} | with_entries(select(.value != "" and .value != null))))
    } | with_entries(select(.value != null)) as $entry |
    ($repos | map(if .name == $name then $entry else . end)) as $replaced |
    (if ([$repos[]? | select(.name == $name)] | length) > 0 then $replaced else ($repos + [$entry]) end) as $outRepos |
    $s + {version: 1, repos: $outRepos}
  ' > "$tmp"
  mv "$tmp" "$status_file"
else
  echo "backup-status-borg.sh: python3 or jq is required to update $status_file" >&2
  exit 1
fi

exit "$rc"
