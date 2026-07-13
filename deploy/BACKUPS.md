# Managed backups

ServerMonitor agents can orchestrate scheduled, encrypted [restic](https://restic.net/) backups of their own host. Encryption is client-side: the repository password never leaves the host, so the backup endpoint — a rest-server VPS, an S3 bucket, anything restic speaks to — only ever sees ciphertext. The monitoring server sees even less: it receives status and metrics (`backup_*`, labeled per repo), never the repository password or the repository URLs, which may embed credentials.

Monitoring comes for free once a host backs up: the host detail page gains a **Backups** tab (last success age, duration, added bytes, snapshot inventory per repo), and the alert rule dialog offers suggested presets — *backup stale*, *backup failed*, *repo check failing* — built on the `backup_*` metrics.

> **Enable this deliberately.** Backing up a host means reading every file on it. On Linux the grant (`CAP_DAC_READ_SEARCH`) is confined to a oneshot systemd unit that runs only at backup time — the resident agent gains nothing — but it is still a whole-host read capability, which is why `--enable-backup` is excluded from `--enable-all`.

## Disaster recovery — the runbook

This section is first on purpose: restoring must work on a bare machine with **zero ServerMonitor infrastructure** — no server, no agent, no host. All you need is the restic binary and the recovery kit that was printed (and saved to `/etc/servermonitor-backup/recovery-kit.txt`) when backup was enabled. The kit contains each repository URL and the repository password.

List what's in a repository:

```bash
export RESTIC_PASSWORD='<password from the recovery kit>'
restic -r 'rest:https://host1:...@backup-a.example.com/host1' snapshots
```

Restore the latest snapshot to a staging directory:

```bash
restic -r 'rest:https://host1:...@backup-a.example.com/host1' restore latest --target /mnt/recover
```

Browse interactively instead (FUSE, Linux/macOS):

```bash
restic -r 'rest:https://host1:...@backup-a.example.com/host1' mount /mnt/browse
```

Restore a specific snapshot or path with `restore <snapshot-id> --include /etc/nginx`. That's the whole dependency chain: restic + URL + password. Test it once per fleet, before you need it.

## Everyday restore — the managed path

The runbook above is the break-glass path with nothing but restic. On a host that still has an agent, `sm-agent backup restore` is the day-to-day tool: it reads the repository URL and password from the root-owned `backup.toml`, so you name a *logical repo*, never a URL, and credentials stay out of your shell history and the process table.

```bash
sm-agent backup restore --repo repo1 --snapshot 3a1f9c2b --include /etc/nginx
```

By default this restores into a staging directory under the agent's own state dir — `/var/lib/servermonitor/restore/<snapshot-id>/` — and nowhere else. That default is a guardrail, not a limitation: the command refuses any `--target` that resolves outside `/var/lib/servermonitor/restore` (it cleans the path, rejects `..` escapes and absolute paths, and resolves symlinked ancestors before checking), so a fat-fingered target can never scribble over the live system. `/var/lib/servermonitor` is the one tree the agent may write; monitoring data and restore staging share it. Copy what you need out of staging by hand. A non-empty target is refused unless you pass `--in-place`.

| Flag | Meaning |
|---|---|
| `--repo NAME` | Logical repo to restore from (required). Never a URL. |
| `--snapshot ID` | Snapshot to restore (required); `restic snapshots` / `sm-agent backup snapshots` list them. Short ids work. |
| `--include PATH` | Restore only this path; repeatable. Omit to restore the whole snapshot. |
| `--target DIR` | Restore into `DIR` instead of the default staging dir. Must resolve inside `/var/lib/servermonitor/restore`. Mutually exclusive with `--in-place`. |
| `--in-place` | Restore over the live files at their original locations (`restic restore … --target /`). No shorthand — you spell it out. Linux only; refused on Windows (see below). |
| `--config PATH` | Path to `backup.toml` (defaults to the per-OS location, overridable with `SM_BACKUP_CONFIG`). |
| `--instance repo:snap[:inc,inc]` | Identity source for the sanctioned in-place restore unit; see below. Not for hand use. |

Overlapping operations are safe: restore takes the same `backup.lock` as `backup run` and `backup check` and exits cleanly as a no-op (exit 0) if one of them is already in flight, so a restore never fights a scheduled backup over the restic cache. Errors are scrubbed of repository URLs, exit is 0 on success and 1 on failure.

### Restoring in place

In-place restore overwrites live files across the whole host, so it is inherently a root operation — reading every file needs `CAP_DAC_READ_SEARCH`, but *writing* `/etc`, `/boot`, restoring ownership and modes needs root, which `CAP_DAC_READ_SEARCH` alone does not grant. Two sanctioned ways to run it, both sourcing every argument from root:

Direct, as root:

```bash
sudo /usr/local/bin/sm-agent backup restore \
  --config /etc/servermonitor-backup/backup.toml \
  --in-place --repo repo1 --snapshot 3a1f9c2b --include /etc/nginx
```

Or through the installed template unit `sm-backup-restore@.service`. It shares the backup unit's baseline hardening (`NoNewPrivileges`, private tmp, kernel protections) but necessarily steps up from it: it runs as root and drops `ProtectSystem=strict`, because writing live paths across the whole filesystem is exactly its job. What it keeps absolute is the invariant that matters: it executes only the root-owned `/usr/local/bin/sm-agent`, reads config only from root-owned `/etc/servermonitor-backup/`, and its argument is the systemd instance name, which the unit passes as `--instance %I` — encoded `repo:snapshot[:comma-separated-includes]`. Because the instance is part of the unit name, the arguments come only from whoever can start the unit (root), never from the agent. Encode it with `systemd-escape` so slashes in include paths survive systemd's word-splitting, and `%I` decodes it back verbatim:

```bash
systemctl start "sm-backup-restore@$(systemd-escape 'repo1:3a1f9c2b:/etc/nginx,/etc/hosts').service"
journalctl -u "sm-backup-restore@*" -f
```

Omit the third field to restore the whole snapshot in place: `systemd-escape 'repo1:3a1f9c2b'`. There is deliberately **no** timer for this unit — an in-place restore only ever runs when a human starts it.

Staging (non-in-place) restores get no unit: run `sm-agent backup restore` directly as shown above. For the same sandbox as the scheduled jobs without a persistent unit, `systemd-run` works as a one-liner:

```bash
systemd-run --unit sm-restore-once --property Type=oneshot \
  /usr/local/bin/sm-agent backup restore \
  --config /etc/servermonitor-backup/backup.toml \
  --repo repo1 --snapshot 3a1f9c2b --target /var/lib/servermonitor/restore/nginx
```

**Windows:** there is no restore task, and `--in-place` is refused outright — restic on Windows cannot restore to original locations: given a whole-drive target it recreates the drive letter as a *subdirectory* of the target (`C:\C\Users\...`), so an "in-place" restore would silently land in the wrong place. The Windows ritual is always staging plus a manual copy, from an elevated PowerShell, using the Program Files agent copy:

```powershell
& 'C:\Program Files\ServerMonitor\sm-agent.exe' backup restore `
  --repo repo1 --snapshot 3a1f9c2b --include C:\Users\alice\Documents
```

Staging lands under `%ProgramData%\ServerMonitor\restore\<snapshot-id>\`, with the snapshot's drive letters as top-level directories (`...\restore\3a1f9c2b\C\Users\...`). Inspect what arrived, then copy it into place yourself (`Copy-Item -Recurse -Force`, or `robocopy` when ACLs and timestamps must survive the trip).

## Verification — proving the backups are restorable

A backup that has never been read back is a hope, not a backup. Two mechanisms verify the repositories on a schedule, both driven by `sm-agent backup check`.

The installer provisions a **weekly** check: `sm-backup-check.timer` (`OnCalendar=weekly`, up to 6h of randomized delay, `Persistent=true`) triggers `sm-backup-check.service`, which runs `sm-agent backup check --read-data-subset 5%` under the same hardened, read-only sandbox as the backup unit. On Windows the equivalent is a weekly **ServerMonitor Backup Check** scheduled task running as SYSTEM with a 6-hour random delay. Each check runs against **every** configured repository sequentially and continues past a failing one; it exits non-zero if any repo failed, and writes only `check_last` + `check_success` per repo into the status file — the backup fields (last success, snapshots, added bytes, prior backup error) are left untouched, so a check never masks a backup problem. The Backups tab and the `backup_check_ok` metric key off those fields; the *repo check failing* alert preset fires on them.

`restic check` always verifies repository structure and metadata. `--read-data-subset` additionally re-reads and re-hashes some fraction of the actual pack data, which is what catches silent bit-rot at the endpoint. It's a cost knob:

- `5%` (the default the units install) re-reads a twentieth of the data each week, so the whole repository is covered roughly every five months for a fraction of the bandwidth and CPU of a full read.
- A percentage (`5%`), a size (`250M`, `10G`), or a fraction (`1/5`) are all accepted; anything else is rejected before restic runs.
- Pass `100%` (or a large size) periodically, or from a machine with spare I/O, for a full data verification.

Run it by hand against one repo or all:

```bash
sm-agent backup check --read-data-subset 25%
sm-agent backup check --repo repo2 --read-data-subset 100%
```

**Check both repositories.** Two independent endpoints only protect you if both are known-good — an unverified second copy is not a second copy. The default check covers every repo for exactly this reason.

`restic check` needs only the repository and its password, not the host's data, so it can run from **any** machine holding the recovery kit — a storage box, an admin workstation — to offload the read I/O from the production host entirely. That is the same append-only-friendly property used by the storage-VPS external prune pattern below.

### The restore drill

Structure and data integrity still don't prove the *restore path* works end to end. `--drill` does: after a repo's check passes, it lists the latest snapshot, deterministically samples up to five regular files of 4 MiB or less (sorted by path for repeatability), restores just those to a scratch dir under the agent state dir, and byte-compares each against the live file. A file that is missing on the live host, or whose mtime is newer than the snapshot (legitimately changed since the backup), is *skipped*, not failed — only a file that should be identical but isn't counts as a mismatch, and any mismatch or restore error flips that repo's `check_success` to false. The scratch dir is removed afterward.

```bash
sm-agent backup check --read-data-subset 5% --drill
```

The drill is opt-in and not part of the installed weekly check. To run it automatically, add `--drill` to the check service's `ExecStart` via a systemd drop-in (`systemctl edit sm-backup-check.service`) so the installer doesn't overwrite it on the next reconfigure run.

## Enabling backup on a host

Pass `--enable-backup` plus at least one repository URL to the install script — on a host that already has an agent, re-running the script reconfigures in place (no token, no re-registration), exactly like the other `--enable-*` flags:

```bash
sudo ./scripts/install-agent-linux.sh \
  --enable-backup \
  --backup-repos 'rest:https://host1:PASS@backup-a.example.com/host1'
```

Or via environment variables (`SM_ENABLE_BACKUP=1`, `SM_BACKUP_REPOS=...`), which is also how the server-hosted one-line installer takes them. Optional inputs: `--backup-repo-names` (logical names for the UI and metrics, default `repo1..repoN`), `--backup-paths` (default `/etc,/home,/root,/var/lib`), `--backup-time` (local `HH:MM`, default `02:30`, plus up to 15 minutes of randomized delay so a fleet doesn't stampede one endpoint).

What the installer provisions:

- **restic 0.19.0**, downloaded from the official GitHub release, verified against a SHA256 checksum pinned in the script, installed to `/usr/local/bin/sm-restic` (root:root). A distro restic already on `PATH` is never trusted or used.
- **`/etc/servermonitor-backup/backup.toml`** — the backup policy (paths, excludes, repos, retention), in a dedicated root-owned directory. See the reference below for why it does not live next to `agent.toml`.
- **`/etc/servermonitor-backup/backup.key`** — a generated 32-byte repository password, `root:sm-agent 0440`. Never regenerated once it exists.
- **`sm-agent backup init`** — initializes each repository (idempotent; an already-initialized repo is detected and skipped).
- **`sm-backup.service` + `sm-backup.timer`** — a `Type=oneshot` unit running `sm-agent backup run` as the `sm-agent` user with `CAP_DAC_READ_SEARCH`, hardened like the agent unit (`NoNewPrivileges`, `ProtectSystem=strict`, private tmp) but without `ProtectHome`, since `/home` is usually part of what gets backed up. The unit executes the root-owned agent copy at `/usr/local/bin/sm-agent`, never the copy the resident agent can replace through self-upgrade. `Persistent=true` on the timer means a host that was off at backup time backs up at boot instead of skipping the day.
- **`sm-backup-check.service` + `sm-backup-check.timer`** — the weekly verification job (`sm-agent backup check --read-data-subset 5%`), same read-only sandbox and root-owned `ExecStart` as the backup unit. `OnCalendar=weekly`, up to 6h of randomized delay, `Persistent=true`. See the Verification section above for what it does.
- **`sm-backup-restore@.service`** — a template unit (no timer) for the sanctioned in-place restore. It drops `ProtectSystem=strict` because an in-place restore must write live paths, and it runs as root because writing and re-owning files across the whole filesystem is beyond `CAP_DAC_READ_SEARCH`. Its restore arguments come only from the systemd instance name (`--instance %I`, encoded `repo:snapshot[:includes]`), so nothing the agent controls can steer it. See the in-place restore ritual above.
- **The recovery kit**, printed once to the terminal and saved to `/etc/servermonitor-backup/recovery-kit.txt` (root-only, `0400`).

The installer prints a capability warning when enabling, and rewrites all three units on every run — so a plain re-run converges them and a reconfigure that flips a flag reapplies them. Re-running the installer *without* `--enable-backup` removes the timers and units (`sm-backup`, `sm-backup-check`, `sm-backup-restore@`) but keeps `backup.toml`, `backup.key`, and the recovery kit — they guard existing snapshots — and tells you how to delete them manually if you really mean it.

**Windows:** same flow with `-EnableBackup`, `-BackupRepos`, `-BackupRepoNames`, `-BackupPaths`, `-BackupTime` (or the same `SM_*` env vars). restic goes to `C:\Program Files\ServerMonitor\restic.exe`; config, key, and recovery kit live in `%ProgramData%\ServerMonitor\Backup\`, a subdirectory locked to SYSTEM and Administrators only (the agent's service account cannot touch it); the schedule is a **ServerMonitor Backup** scheduled task plus a weekly **ServerMonitor Backup Check** task (SYSTEM, 6-hour random delay, `backup check --read-data-subset 5%`), both executing the admin-only agent copy in `C:\Program Files\ServerMonitor`. There is no restore task — restore is the documented elevated invocation above. Backups run with `--use-fs-snapshot`, so open files are captured consistently via VSS. Default path is `C:\Users`.

## The recovery kit

The kit contains the hostname, each repository's logical name and full URL (credentials included), the repository password, and the restore one-liner. **Store it in a password manager immediately.** No other copy exists anywhere — the monitoring server deliberately never sees the password, so there is nothing to recover it from. Lose the kit and the host together, and the backups are permanently unreadable.

The key file is never rotated or regenerated by the installer, not even with `--reinstall`: an existing key may guard existing snapshots, and silently replacing it would orphan them. Reconfigure runs print the kit's path instead of reprinting the password.

## backup.toml reference

`/etc/servermonitor-backup/backup.toml` (Windows: `%ProgramData%\ServerMonitor\Backup\backup.toml`; `SM_BACKUP_CONFIG` overrides the path for the `backup` subcommands):

```toml
status_path = "/var/lib/servermonitor/backup-status.json"
restic_path = "/usr/local/bin/sm-restic"
paths = ["/etc", "/home", "/root", "/var/lib"]
excludes = ["**/.cache", "/var/lib/docker", "/var/lib/servermonitor"]
one_file_system = true
prune_mode = "host"

[retention]
daily = 7
weekly = 4
monthly = 6

[[repo]]
name = "repo1"
url = "rest:https://host1:PASS@backup-a.example.com/host1"
password_file = "/etc/servermonitor-backup/backup.key"
  [repo.retention]
  daily = 30

[[repo]]
name = "offsite-s3"
url = "s3:https://s3.us-west-2.amazonaws.com/mybucket/host1"
password_file = "/etc/servermonitor-backup/backup.key"
env_file = "/etc/servermonitor-backup/repo-credentials.env"
s3_region = "us-west-2"
s3_path_style = false
```

| Field | Default | Meaning |
|---|---|---|
| `status_path` | `/var/lib/servermonitor/backup-status.json` | Where run results land; the agent's backup collector reads this file for the Backups tab and `backup_*` metrics. Repo URLs never appear in it, and error messages are scrubbed of them. |
| `restic_path` | `/usr/local/bin/sm-restic` (Windows: `C:\Program Files\ServerMonitor\restic.exe`) | The restic binary the runner execs. Resolution order: this field, then `SM_RESTIC_PATH`, then the per-OS default. |
| `paths` | `/etc`, `/home`, `/root`, `/var/lib` (Windows: `C:\Users`) | What gets backed up. |
| `excludes` | `**/.cache`, `/var/lib/docker`, `/var/lib/servermonitor` (Windows: empty) | restic `--exclude` patterns. The agent's own state dir is excluded so spool and cache churn don't inflate snapshots. |
| `one_file_system` | `true` (Linux only; `false` on Windows) | Don't cross mountpoints under the listed paths. |
| `prune_mode` | `"host"` | `"host"`: after each successful backup, run `restic forget --prune` with the `[retention]` keeps. `"external"`: never prune from the host — for append-only credentials; see endpoint setup below. |
| `[retention]` | `daily = 7`, `weekly = 4`, `monthly = 6` | Maps to `--keep-daily/--keep-weekly/--keep-monthly`. Only used when `prune_mode = "host"`. |
| `[[repo]]` | — | One block per endpoint: logical `name` (what the UI and metrics show), `url`, `password_file`. |
| `[repo.retention]` | inherits `[retention]` per field | Optional per-repo `daily`, `weekly`, `monthly` override. Unset fields inherit the effective global retention; setting a field to `0` omits that restic keep flag for that repo, and the merged result must keep at least one value above zero. |
| `env_file` (per-repo) | — | Absolute path to a root-owned `KEY=VALUE` file of transport credentials injected into restic's environment for this repo only. Keys are allowlisted (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_DEFAULT_REGION`, `B2_ACCOUNT_ID`, `B2_ACCOUNT_KEY`, `RESTIC_REST_USERNAME`, `RESTIC_REST_PASSWORD`); any other key is a hard error. The values never touch `backup.toml`, the status file, metrics, logs, or the UI, and are scrubbed from restic error text. |
| `s3_region` (per-repo) | — | Region for an `s3:` repository (sets `AWS_DEFAULT_REGION` unless the env file already set it). |
| `s3_path_style` (per-repo) | `false` | Path-style S3 addressing (`-o s3.bucket-lookup=path`). Required by MinIO, Ceph/RGW, Garage, and most self-hosted S3; leave `false` for AWS S3. |

The file is root-owned (`root:sm-agent 0640`) and lives in its own root-owned directory (`/etc/servermonitor-backup`, `root:sm-agent 0750`) **by design**: the backup unit runs with a read-everything capability, and if the unprivileged `sm-agent` user could edit — or, via write access to the containing directory, replace — the policy, a compromised agent could re-aim that job at arbitrary paths or a hostile repository. `/etc/servermonitor` itself is writable by the `sm-agent` user (the agent rewrites `agent.toml` there at runtime), which is exactly why the backup policy does not live in it — and the same rule puts the restic binary at root-owned `/usr/local/bin/sm-restic` rather than under agent-owned `/opt/servermonitor`, which must never hold anything the caps-bearing unit executes. So edits need root — but only root. Changing paths, excludes, or retention does **not** require re-running the installer: edit the file and the next timer run picks it up. (Adding a repository is also just an edit plus one `sm-agent backup init` run.)

## Multiple endpoints

List two or more `[[repo]]` blocks (or pass a comma-separated `--backup-repos`) and each run backs up to every repository **sequentially and independently** — separate `restic backup` invocations, separate chunking, separate history. This is deliberate corruption isolation: a damaged repository cannot infect the other, and an endpoint being down never costs the other endpoint its backup. One repo failing is recorded (`success: false`, per-repo error in the status file and metrics) and the run continues; the run only exits non-zero when *every* repository failed. Two independent endpoints are the recommended posture; the rest-server and object-storage guides below show the two usual endpoints without coupling their failure domains.

## Endpoint setup

The endpoint should be narrower and less trusted than the host it protects. The monitored host gets credentials that can add encrypted history and read it back for restore; it should not get the power to rewrite, prune, or delete that history. That is the managed equivalent of the old storage-VPS ritual: low-privilege endpoint account, one namespace per source, append-only writes, and pruning from a trusted place.

### rest-server on a storage VPS

Use `rest-server` when you want the storage-VPS story: simple files on disk, per-host credentials, append-only enforcement at the endpoint, and local pruning from the storage box. Install a pinned release, not a distro package that drifts independently of the runbook. As of the upstream release page, `v0.14.0` is the latest stable rest-server release; keep the version and checksum explicit in the install transcript the same way the agent installer pins restic.

```bash
REST_SERVER_VERSION=0.14.0
REST_SERVER_ARCHIVE="rest-server_${REST_SERVER_VERSION}_linux_amd64.tar.gz"
REST_SERVER_SHA256='replace-with-linux-amd64-sha256-from-v0.14.0-release'
tmpdir="$(mktemp -d)"
curl -fL -o "${tmpdir}/${REST_SERVER_ARCHIVE}" "https://github.com/restic/rest-server/releases/download/v${REST_SERVER_VERSION}/${REST_SERVER_ARCHIVE}"
printf '%s  %s\n' "$REST_SERVER_SHA256" "${tmpdir}/${REST_SERVER_ARCHIVE}" | sha256sum -c -
tar -C "$tmpdir" -xzf "${tmpdir}/${REST_SERVER_ARCHIVE}"
install -o root -g root -m 0755 "$(find "$tmpdir" -type f -name rest-server | head -n 1)" /usr/local/bin/rest-server
rm -rf "$tmpdir"
/usr/local/bin/rest-server --version
```

Create one service account and one htpasswd credential per monitored host. The username is the namespace. With `--private-repos`, user `host1` can use `rest:https://host1@backup-a.example.com:8000/host1` and cannot use `/`, `/host2`, or any sibling repository; the noninteractive configured URL usually embeds the htpasswd secret as `rest:https://host1:PASS@backup-a.example.com:8000/host1`. Store that URL in `backup.toml` and the recovery kit, not in shell history.

```bash
groupadd --system rest-server
useradd --system --gid rest-server --home-dir /srv/restic --shell /usr/sbin/nologin rest-server
install -d -o rest-server -g rest-server -m 0700 /srv/restic
install -d -o root -g rest-server -m 0750 /etc/rest-server
htpasswd -B -c /etc/rest-server/htpasswd host1
htpasswd -B /etc/rest-server/htpasswd host2
chown root:rest-server /etc/rest-server/htpasswd
chmod 0640 /etc/rest-server/htpasswd
```

Prefer a local-only rest-server listener plus a real TLS reverse proxy. The rest-server process stays private; nginx owns certificates, renewal, HTTP hardening, and public port 443. With this layout the public repo URL shape is `rest:https://host1@backup-a.example.com/host1`, usually configured as `rest:https://host1:PASS@backup-a.example.com/host1`; if you expose rest-server's built-in TLS directly on port 8000 instead, keep the same namespace rule and use the `:8000` form.

```nginx
server {
    listen 443 ssl http2;
    server_name backup-a.example.com;

    ssl_certificate /etc/letsencrypt/live/backup-a.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/backup-a.example.com/privkey.pem;

    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:8000;
        proxy_http_version 1.1;
        proxy_request_buffering off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
        proxy_set_header Host $host;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

Run the endpoint under its own low-privilege user and make the data directory the only writable path:

```ini
[Unit]
Description=restic REST backup endpoint
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=rest-server
Group=rest-server
ExecStart=/usr/local/bin/rest-server --path /srv/restic --append-only --private-repos --listen 127.0.0.1:8000 --htpasswd-file /etc/rest-server/htpasswd
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/srv/restic
ReadOnlyPaths=/etc/rest-server
CapabilityBoundingSet=
AmbientCapabilities=
LockPersonality=true
MemoryDenyWriteExecute=true
ProtectClock=true
ProtectControlGroups=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
RestrictRealtime=true
SystemCallArchitectures=native
SystemCallFilter=@system-service
UMask=0077

[Install]
WantedBy=multi-user.target
```

`--append-only` is the ransomware boundary. Root on a monitored host can read its repository and add new encrypted packs, but every destructive REST operation is refused at the endpoint, so the host cannot delete snapshots, rewrite packs, or prune history after compromise. Pair that with `prune_mode = "external"` in that host's `backup.toml`: the host runs backup and check, but retention runs from the storage VPS against the local repository path, or from another trusted machine with direct filesystem access and the repository password.

```cron
17 3 * * * flock -n /run/restic-prune-host1.lock env RESTIC_PASSWORD_FILE=/etc/rest-server/keys/host1 /usr/local/bin/restic -r /srv/restic/host1 forget --prune --keep-daily 7 --keep-weekly 4 --keep-monthly 6
```

`prune_mode = "host"` is simpler: every backup run can immediately enforce retention. The trade is that the monitored host must hold credentials powerful enough to delete repository data, which collapses the ransomware boundary. Use host pruning for low-risk local endpoints and labs; use external pruning plus append-only rest-server for internet-facing hosts.

The storage VPS itself is now production infrastructure. Install `sm-agent` on it too and alert on `fs_used_pct` for the filesystem holding `/srv/restic`; this dogfoods the exact disk-pressure signal that tells you whether the place all other hosts depend on is about to fill.

### Migration from borg/SSH

The managed restic setup preserves the old security posture and removes the handwritten glue.

| borg/SSH ritual | Managed restic equivalent |
|---|---|
| Low-privilege `borg` user on the storage VPS | One rest-server htpasswd credential per monitored host |
| SSH forced into one repository directory | `--private-repos`, where the URL path must match the username |
| `borg serve --append-only` | `rest-server --append-only` |
| Two separate borg repositories | Two independent `[[repo]]` blocks |
| `borg key export` saved out of band | ServerMonitor recovery kit |

Keep two repositories if that was the old corruption boundary; the managed setup does not merge them or copy between them. The observability delta is the real upgrade: the existing borg cron wrapper, `scripts/backup-status-borg.sh`, already writes the same status file shape, so a gradual migration with borg on one endpoint and managed restic on the other remains fully monitored in the Backups tab and alert rules.

### Object storage (S3/B2)

For object storage, use a bucket per fleet and a prefix plus access key per host. The repository URL is the namespace and, for self-hosted S3, carries the endpoint: `s3:s3.amazonaws.com/servermonitor-backups/host1` for AWS, `s3:https://minio.internal:9000/servermonitor-backups/host1` for MinIO/Ceph/Garage, `b2:servermonitor-backups:host1` for B2. Keep the same two-repo posture by adding two `[[repo]]` blocks, not by copying objects between endpoints.

**Supplying credentials (managed path).** The installer takes S3/B2 credentials as environment variables only (never flags — argv is world-readable) and writes them to a root-owned `/etc/servermonitor-backup/repo-credentials.env` (`0440 root:sm-agent`), then sets `env_file` on every `s3:`/`b2:`/`rest:` repo so restic reads them for that repo alone. They never enter `backup.toml`, the recovery kit, the status file, metrics, logs, or the UI, and are scrubbed from restic error text.

```bash
sudo SM_ENABLE_BACKUP=1 \
  SM_BACKUP_REPOS='s3:https://minio.internal:9000/servermonitor-backups/host1' \
  SM_BACKUP_S3_ACCESS_KEY_ID=AKIA... \
  SM_BACKUP_S3_SECRET_ACCESS_KEY=... \
  SM_BACKUP_S3_REGION=us-west-2 \
  SM_BACKUP_S3_PATH_STYLE=1 \
  SM_BACKUP_PRUNE_MODE=external \
  ./scripts/install-agent-linux.sh --enable-backup --backup-repos '...'
```

`--backup-s3-region` / `--backup-s3-path-style` / `--backup-prune-mode` are the non-secret flag equivalents. Use `s3_path_style` (`SM_BACKUP_S3_PATH_STYLE=1`) for MinIO, Ceph/RGW, Garage, and most self-hosted S3 — virtual-host addressing fails against a bare host:port endpoint. On Windows, pass the same `SM_BACKUP_*` variables; the credentials file lands in the ACL-locked `%ProgramData%\ServerMonitor\Backup\repo-credentials.env`.

**Hardening the bucket against ransomware.** Set `prune_mode = "external"` (`SM_BACKUP_PRUNE_MODE=external`) so the monitored host never issues deletes, and give its access key an append-only policy (below). The S3 append-only shape is an allow-list for reads, writes, and listing under one prefix, plus an explicit deny for deletes. Prune then cannot run with the monitored host's key; it must run from a trusted box with a separate full-power key and the same repository password.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ListHostPrefix",
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::servermonitor-backups",
      "Condition": {
        "StringLike": {
          "s3:prefix": [
            "host1",
            "host1/*"
          ]
        }
      }
    },
    {
      "Sid": "ReadWriteHostPrefix",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": "arn:aws:s3:::servermonitor-backups/host1/*"
    },
    {
      "Sid": "DenyHostDeletes",
      "Effect": "Deny",
      "Action": "s3:DeleteObject",
      "Resource": "arn:aws:s3:::servermonitor-backups/host1/*"
    }
  ]
}
```

If your S3-compatible endpoint models multipart uploads as separate write-side permissions, add only the required multipart permissions on the same host prefix and keep the monitored host's delete deny absolute.

Lifecycle rules and Object Lock are endpoint controls, not ServerMonitor controls — **the installer and server never create, modify, or read bucket policies, IAM, lifecycle rules, or Object Lock configuration.** You apply them out of band. For compliance-grade immutability on S3, enable Object Lock before objects are written and set governance or compliance retention at the bucket or prefix policy layer. restic 0.17 and newer fit this style when writers are limited to read/list/write and destructive retention is separated onto the trusted prune credential; do not try to make the monitored host both append-only and the pruner. Note the interaction: leaving `prune_mode = "host"` against a deny-delete bucket makes each run's `restic forget --prune` fail — the agent records that as a repo error, which is correct. Set `prune_mode = "external"` for those endpoints.

For B2, create one application key per host scoped to the bucket and host prefix with list/read/write capability and no `deleteFiles`; keep a separate key that can delete only on the trusted prune machine.

## Backing up to the ServerMonitor server (managed backup endpoint)

Instead of standing up a rest-server on a storage VPS by hand (the section above), you can **promote a ServerMonitor server itself into the restic endpoint** other hosts back up to. The server binary speaks the restic REST protocol on `/backup/…`; there is no second process to install or supervise.

**Enable it.** Set exactly one storage backend and restart the server:

- `BACKUP_DIR=/var/lib/servermonitor/backup-repos` — store blobs on the server's own disk. Simplest; watch capacity with an `fs_used_pct` alert (install the agent on the backup server itself).
- `BACKUP_S3_BUCKET=...` (plus `BACKUP_S3_REGION`, `BACKUP_S3_ENDPOINT` for self-hosted S3, `BACKUP_S3_USE_PATH_STYLE=1`, `BACKUP_S3_PREFIX`) — store blobs in a bring-your-own object-storage bucket the server holds credentials for (standard AWS credential chain).

Setting both is a startup error. `BACKUP_MAX_BLOB_BYTES` (default 1 GiB) caps a single upload.

**TLS is mandatory** — the endpoint authenticates with HTTP Basic, so the server refuses to expose `/backup` over plaintext unless `INSECURE_ALLOW_HTTP=1`. Three postures, chosen automatically:

- Already terminating TLS (`TLS_CERT_FILE`+`TLS_KEY_FILE`) or behind a trusted proxy (`TRUST_PROXY_TLS=1`) → reused as-is.
- A bare server with no proxy → set `BACKUP_ACME_DOMAIN=backup.example.com` (and optionally `ACME_EMAIL`, `ACME_CACHE_DIR`) and the server obtains and renews a Let's Encrypt certificate itself (HTTP-01 on :80, so the domain must resolve to the server and port 80/443 be reachable).

The Backups tab shows which posture is active.

**Provision a target in the UI.** Open **Backups → New backup target**, give it a name (becomes the repo path and login), an optional linked host, and an optional quota. The server mints a credential shown **once** and stored only as a SHA-256 hash — it authenticates uploads only and cannot decrypt anything (the repo password never leaves the client). The page renders the exact install/`backup.toml` snippet to paste on the target host, using the `SM_BACKUP_REST_USERNAME`/`SM_BACKUP_REST_PASSWORD` credential-file mechanism above. The repo URL is `rest:https://backup.example.com/backup/<name>`.

Point a host at it exactly like any other repo, with `prune_mode = "external"` (the endpoint is append-only):

```bash
sudo SM_ENABLE_BACKUP=1 \
  SM_BACKUP_REPOS='rest:https://backup.example.com/backup/web-01' \
  SM_BACKUP_REST_USERNAME=web-01 \
  SM_BACKUP_REST_PASSWORD=<minted> \
  SM_BACKUP_PRUNE_MODE=external \
  ./scripts/install-agent-linux.sh --enable-backup --backup-repos '...'
```

**Guarantees.** The endpoint is **append-only**: it refuses every delete and every attempt to overwrite an existing object (the ransomware boundary), with the sole exception of restic's own lock files (so `restic unlock` works). Per-host isolation is absolute — a credential can only ever address its own `/backup/<name>` namespace. Revoking a target from the UI stops new uploads immediately but never deletes stored blobs (append-only applies to admins too); reclaim space by deleting on the storage backend directly. **Prune/`forget` must run elsewhere** — from a trusted box holding a full-power credential and the repo password — never from the monitored host.

A target the endpoint holds **no objects** for — one created by mistake, or emptied on the backend — can be deleted outright from the UI (**Delete** replaces **Revoke** on empty rows). This never touches stored data: the server refuses the delete with `409` if the backend namespace holds any object, down to a zero-byte one. The check is a preflight, not a lock, so in the narrow window where a first upload lands mid-delete the row can still be removed while that object is written; the result is an orphaned, still-encrypted repo you reclaim on the backend like any other — no snapshot is ever destroyed. Reusing a deleted name is refused while the old namespace still holds objects, so a new credential can never rebind to leftover blobs.

Use **Measure** on a target to walk its repo and refresh the stored-bytes figure and quota bar. Storage totals also appear in the summary tiles at the top of the Backups tab.

## Backing up over the built-in WireGuard tunnel (no exposed endpoint)

The managed endpoint above rides the server's public HTTPS listener. Tunnel mode removes even that: each backup host establishes a **per-host WireGuard tunnel** to the server, and the restic REST endpoint is served **only inside the tunnel** — it does not exist on any real network interface. The only thing the internet can see is one UDP port, and WireGuard never replies to a packet that isn't signed by an enrolled peer key, so scanners see nothing at all. TLS and `BACKUP_ACME_DOMAIN` become unnecessary for backups: the tunnel is the transport encryption, restic's client-side encryption still covers the data, and the per-target Basic-auth credential still gates every request (a tunnel IP grants no authorization by itself).

Everything is userspace Go inside the existing binaries — no kernel WireGuard, no `wg-quick`, no TUN devices, no new capabilities on either side, no interference with an existing WireGuard or Tailscale setup on the host. The agent's tunnel exists **only while a backup, check, restore or init is running**; there is no resident tunnel process.

**Enable it on the server.** Keep one storage backend (`BACKUP_DIR` or `BACKUP_S3_BUCKET`) and add:

- `BACKUP_WG_PORT=51820` — turns tunnel mode on; the UDP port to expose.
- `BACKUP_WG_ENDPOINT=` — optional `host[:port]` agents should dial. Defaults to the host agents already reach the server by, so most setups leave it empty.
- `BACKUP_WG_SUBNET=10.83.0.0/16` — tunnel address pool; the server takes the first address.
- `BACKUP_WG_MTU=1280` — conservative default that survives PPPoE/IPv6 encapsulation; raise toward 1420 on clean paths.
- `BACKUP_PUBLIC_HTTP=1` — optional. By default tunnel mode takes `/backup` **off** the public listener; set this to serve both (mixed fleet where some hosts cannot use UDP).

The server generates and persists its WireGuard key on first start; peers survive restarts. The supplied Docker Compose file publishes `${BACKUP_WG_PORT:-51820}` over UDP on the same host and container port. Leaving `BACKUP_WG_PORT` empty still reserves UDP 51820 on the host, but the server does not listen on it or enable tunnel mode. A custom `BACKUP_WG_PORT` changes both sides of the mapping. In node-only fleets this published server port is harmless and is not part of the backup data path; each promoted node's own UDP port must separately be reachable from backup clients.

**Point a host at it.** Use the `tunnel:` scheme instead of a `rest:` URL — everything else (credential env vars, prune mode) is identical:

```bash
sudo SM_ENABLE_BACKUP=1 \
  SM_BACKUP_REPOS='tunnel:web-01' \
  SM_BACKUP_REST_USERNAME=web-01 \
  SM_BACKUP_REST_PASSWORD=<minted> \
  SM_BACKUP_PRUNE_MODE=external \
  ./scripts/install-agent-linux.sh --enable-backup --backup-repos 'tunnel:web-01'
```

The installer runs `sm-agent backup tunnel-enroll`, which generates a WireGuard key (root-owned `tunnel.key` next to `backup.key`, never regenerated), registers the public key with the server over the existing agent-token channel, and writes the returned `[tunnel]` section (server public key, endpoint, tunnel addresses) into `backup.toml`. In `backup.toml` a tunnel repo carries `tunnel_name = "web-01"` instead of `url`. At run time the agent brings the tunnel up in-process, proxies restic through a loopback listener, and tears it down when the run ends. A `tunnel:` repo and `rest:`/`s3:`/`b2:` repos mix freely in one config; if the tunnel cannot come up, only the tunnel repos fail that run — the rest proceed and the failure lands in the status file and alerts like any other repo error.

**Peer management.** The Backups page shows the tunnel card: endpoint, server public key, and every enrolled peer with tunnel IP, last handshake and traffic. Revoking a peer removes tunnel access immediately (independent of the repo credential). Deleting a host revokes its peer automatically. Re-running the installer (or `sm-agent backup tunnel-enroll`) re-enrolls, keeping the existing key and tunnel IP; a host restored from scratch generates a new key and simply enrolls again with its agent token.

**Serialization.** Because both ends of the tunnel key a single WireGuard identity, every tunnel-using command takes the shared `backup.lock` — including `snapshots` and `init`, which run lock-free in URL mode. A second command reports the lock holder and exits instead of silently stealing the tunnel.

**Disaster recovery.** The recovery kit gains the tunnel parameters, the REST credential and the tunnel key. On a rebuilt machine: reinstall the agent (or `sm-agent register` for a token), run `sm-agent backup tunnel-enroll`, then `sm-agent backup proxy --config /etc/servermonitor-backup/backup.toml` — it opens the tunnel, prints a ready-made `RESTIC_REPOSITORY=rest:http://127.0.0.1:<port>/<name>`, and stays up until Ctrl+C so you can drive plain restic against it with the repository password from the kit. Remember the structural caveat: with the ServerMonitor server as the endpoint, the monitoring server and the backup destination share fate — keep the second repo on independent storage (S3/B2 with deny-delete, or another rest-server), exactly like the two-repo posture this feature is built around.

**Constraints.** Agents need outbound UDP to the server's tunnel port (corporate egress filters may block it — those hosts keep using `rest:` over HTTPS with `BACKUP_PUBLIC_HTTP=1`). Throughput through the userspace stack is roughly a few hundred Mbit/s — far above what nightly incremental backups need, but plan restores of multi-TB repos accordingly.

## Storage nodes: promote a monitored host into a backup destination

Any connected host can be **promoted into a storage node**: its resident agent starts serving the same append-only restic REST endpoint — inside its own WireGuard tunnel — and other hosts back up straight to it. The monitoring server stays out of the data path entirely; it is only the control plane (identity, tunnel IPs, credentials, observability). This reproduces the classic storage-VPS posture with zero manual setup on the node.

**Promotion is a UI action, no SSH required.** On the Backups page, *Storage nodes → Promote a host*: pick the host, the endpoint other hosts dial it at (LAN name or public address — one UDP port, silent to non-peers), and the port (default 51821). The node's agent picks the role up within a minute: it generates a tunnel key, enrolls with the server, opens the UDP listener, and serves storage from `/var/lib/servermonitor/backup-store` (Windows: `%ProgramData%\ServerMonitor\backup-store`). Everything is userspace — the agent gains no privileges, and demotion stops it just as fast (refused while the node still has active repositories).

Requirements: the server must run with `BACKUP_WG_PORT` (the tunnel control plane); the node's UDP port must be reachable from the hosts that back up to it. `BACKUP_DIR`/`BACKUP_S3_BUCKET` on the server are **not** required for node-only fleets.

**Create repositories on it.** New repository → destination *Node · hostname*. Node-hosted repositories require the linked host (that's how the node knows which tunnel peer to admit). The install snippet uses the node form of the scheme:

```bash
--backup-repos 'tunnel:nas-01/web-01'
```

`tunnel:NODE/NAME` = repository `NAME` on node `NODE`; plain `tunnel:NAME` still targets the server. In `backup.toml` this becomes `tunnel_name = "web-01"` plus `tunnel_node = "nas-01"`, and `sm-agent backup tunnel-enroll` resolves the node's public key, endpoint and tunnel address into `[[tunnel.node]]` blocks. A host can mix destinations freely — one repo on a node, one on S3 — and each still fails independently.

**Trust model.** The node stores ciphertext only: repo passwords never leave the backing-up host, and the node verifies upload credentials against server-distributed hashes, so a compromised node can neither read nor forge backups — it can, at worst, delete its own disk (which is why the two-independent-repos rule still applies; pair a node repo with S3/B2 or a second node). The node's endpoint is append-only exactly like the server's. Upload credentials are still minted and revoked centrally in the UI; per-target usage flows back to the server for quota display.

**Fate sharing note.** A node-hosted repo dies with the node's disk. For the borg-style posture: repo 1 on a storage node in another room/site, repo 2 on object storage with a deny-delete policy — both over their own transports, no shared fate, nothing internet-exposed except silent UDP.

## Manual runs and troubleshooting

Kick off a backup outside the schedule (this runs it under the unit, with the right user, capability, and sandbox):

```bash
systemctl start sm-backup.service
journalctl -u sm-backup -f
```

Inspect what exists:

```bash
sm-agent backup snapshots
sm-agent backup snapshots --repo repo2 --json
```

Overlapping runs are safe: `backup run`, `backup check` and `backup restore` share one lock file (`backup.lock`, next to the status file) and any of them exits cleanly as a no-op if another is already in flight, so a scheduled backup, the weekly check and a manual restore never fight over the restic cache. The restic cache lives in `restic-cache/` next to the status file, so the first run after enabling is slower than the rest.

One trade-off to know about: the backup, check and restore units all deliberately execute the root-owned agent copy (`/usr/local/bin/sm-agent`; Windows: `C:\Program Files\ServerMonitor\sm-agent.exe`) rather than the copy the resident agent maintains — otherwise a compromised agent could swap the binary these read-everything (and, for in-place restore, write-everything) jobs run. The flip side is that this copy is **not** touched by agent self-upgrades, so the `backup` subcommands can lag behind the resident agent. That is harmless day to day; re-running the installer refreshes it and rewrites all of the units (`sm-backup`, `sm-backup-check`, `sm-backup-restore@`).

Backups tab banner states:

- **not configured** — the status file doesn't exist yet. Normal before the first run finishes; otherwise check that the timer is enabled (`systemctl list-timers sm-backup.timer`).
- **stale** — the status file hasn't been written in over 26 hours. The timer stopped firing (host was off past the `Persistent` catch-up, unit disabled) or every run is dying before it can write status — check `journalctl -u sm-backup`.
- **error** — the status file exists but can't be parsed; the banner carries the message.

Common failures, all visible in the journal and as per-repo `error` in the status file (with repository URLs scrubbed out):

- **Wrong repository URL / bad credentials** — restic fails immediately; fix the `[[repo]]` `url` and re-run `sm-agent backup init` if the repo was never initialized.
- **Endpoint down** — that repo records a failure and fires the *backup failed* / *backup stale* alerts; other repos in the same run are unaffected.
- **Key file missing** — `backup init` and `backup run` refuse to start. The installer never regenerates a key on its own; restore `backup.key` from your recovery kit (it is the password, base64 line for line) rather than generating a fresh one, or existing snapshots become unreadable.
