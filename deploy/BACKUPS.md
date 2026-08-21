# Managed backups

ServerMonitor agents can orchestrate scheduled, encrypted [restic](https://restic.net/) backups of their own host. Encryption is client-side: the repository password never leaves the host, so the storage backend only sees ciphertext. For locally configured repositories the monitoring server receives status and metrics only. For centrally managed direct S3 repositories it also holds destination metadata and encrypted transport credentials, but it still never receives the restic repository password.

Monitoring comes for free once a host backs up: the host detail page gains a **Backups** tab (last success age, duration, added bytes, snapshot inventory per repo), and the alert rule dialog offers suggested presets — *backup stale*, *backup failed*, *repo check failing* — built on the `backup_*` metrics.

> **Enable this deliberately.** Backing up a host means reading every file on it. On Linux the grant (`CAP_DAC_READ_SEARCH`) is confined to a oneshot systemd unit that runs only at backup time — the resident agent gains nothing — but it is still a whole-host read capability, which is why `--enable-backup` is excluded from `--enable-all`.

## Three separate layers

The UI and API keep these concepts independent:

- **Storage backend / destination** — where bytes ultimately live: ServerMonitor disk, the S3 bucket behind ServerMonitor's gateway, a promoted storage node, or an external S3/R2 destination.
- **Repository namespace** — one restic repository and its isolated path/prefix. A direct destination can be reused while each linked host gets a distinct expanded prefix such as `backups/hosts/17-web-01/nightly`.
- **Network/data path** — the systems that carry each object. This is not implied by the word “S3.”

| UI path | Storage backend | Effective data path |
|---|---|---|
| Server disk | Server filesystem | **Agent → Server disk** |
| Server gateway backed by S3 | `BACKUP_S3_*` bucket | **Agent → ServerMonitor → S3** |
| Promoted storage node | Node filesystem | **Agent → storage node** |
| Direct external S3 | Centrally defined S3/R2 destination | **Agent → S3 directly** |

The gateway-backed S3 path terminates the restic REST request on ServerMonitor, temporarily spools each object, and uploads it with the server's credentials. Direct external S3 uses ServerMonitor as control plane only; backup objects never traverse or spool on the monitoring server.

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

Pass `--enable-backup` plus at least one repository URL to the install script — on a host that already has an agent, re-running the script reconfigures in place (no token, no re-registration), exactly like the other `--enable-*` flags. The exception is a centrally assigned direct S3 repository: after its assignment has synchronized, `--enable-backup` can provision the runtime without a local repository URL.

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

### Containerized (Docker) agents

A host running only the [Dockerized agent](AGENT-DOCKER.md) can back **itself** up without a second host-installed agent — there is no `--enable-backup` installer step; the resident agent provisions and schedules everything from env. Set `SM_ENABLE_BACKUP=1` plus either a local `SM_BACKUP_REPOS`/credential set or a centrally synchronized direct assignment; full recipe in [AGENT-DOCKER.md → Managed backups](AGENT-DOCKER.md#managed-backups). The differences from a host install:

- **No systemd.** The schedule (daily backup + weekly check) runs inside the agent and is caught up on boot; there is no `sm-backup` timer/unit.
- **Chrooted backups.** The backup runs restic chrooted into `/host`, so snapshots record host-native paths and interoperate with host-installed snapshots — provided the recipe keeps `uts: host`, `cap_add: SYS_CHROOT`, and the state-volume aliases at `/tmp` and `/host/tmp`. The alias hides host `/tmp`, which therefore is not a supported container-managed backup path.
- **Supported repositories:** `rest:`, `s3:`, `b2:`, `gs:`, `azure:`, `swift:`, and `tunnel:`. `sftp:` is not supported because the agent image does not include SSH.
- **`prune_mode` defaults to `external`** (a container should not hold deletion authority over history).
- **Restore is staging-only**; in-place is refused (copy the staged restore out with `docker cp`).
- **Recovery kit on demand:** `docker compose … exec sm-agent /usr/local/bin/sm-agent backup recovery-kit`. The key lives only in the `sm-agent-state` volume — `docker compose down -v` destroys it, so keep the kit offline.

## The recovery kit

The kit contains each repository's logical name and destination, the repository password, endpoint transport credentials, and repository-specific restore commands. URL repositories use their real URLs. Tunnel repositories include a self-contained disaster-recovery walkthrough covering the physical server or storage node endpoint, `sm-agent backup proxy`, WireGuard re-enrollment on a replacement host, and the restic commands to run against the local proxy. **Store it in a password manager immediately.** No other copy of the repository password exists anywhere — the monitoring server deliberately never sees it, so there is nothing to recover it from. Lose the kit and the host together, and the backups are permanently unreadable.

The key file is never rotated or regenerated by the installer, not even with `--reinstall`: an existing key may guard existing snapshots, and silently replacing it would orphan them. Reconfigure runs print the kit's path instead of reprinting the password.

## backup.toml reference

`/etc/servermonitor-backup/backup.toml` (Windows: `%ProgramData%\ServerMonitor\Backup\backup.toml`; `SM_BACKUP_CONFIG` overrides the path for the `backup` subcommands):

```toml
status_path = "/var/lib/servermonitor/backup-status.json"
restic_path = "/usr/local/bin/sm-restic"
paths = ["/etc", "/home", "/root", "/var/lib"]
excludes = ["**/.cache", "/var/lib/servermonitor", "/var/lib/docker/overlay2", "/var/lib/docker/overlay", "/var/lib/docker/image", "/var/lib/docker/containers", "/var/lib/docker/buildkit", "/var/lib/docker/tmp", "/var/lib/docker/plugins", "/var/lib/docker/network", "/var/lib/docker/runtimes", "/var/lib/docker/aufs", "/var/lib/docker/btrfs", "/var/lib/docker/devicemapper", "/var/lib/docker/fuse-overlayfs", "/var/lib/docker/vfs", "/var/lib/docker/zfs", "/var/lib/containerd"]
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
| `status_path` | `/var/lib/servermonitor/backup-status.json` | Where run results land; the agent's backup collector reads this file for the Backups tab and `backup_*` metrics. Successful runs also record bytes and file counts for every configured backup path, measured from the new snapshot. Repo URLs never appear in it, and error messages are scrubbed of them. |
| `restic_path` | `/usr/local/bin/sm-restic` (Windows: `C:\Program Files\ServerMonitor\restic.exe`) | The restic binary the runner execs. Resolution order: this field, then `SM_RESTIC_PATH`, then the per-OS default. |
| `paths` | `/etc`, `/home`, `/root`, `/var/lib`, plus `/var/lib/docker/volumes` when that directory exists (Windows: `C:\Users`) | What gets backed up. The explicit Docker volumes target protects named volumes when Docker storage is a separate filesystem. |
| `excludes` | `**/.cache`, `/var/lib/servermonitor`, the reproducible `/var/lib/docker/*` subtrees (overlay2, image, containers, buildkit, and others), plus `/var/lib/containerd` (Windows: empty) | restic `--exclude` patterns. Named volumes and swarm state stay in scope. Use Linux installer flag `--backup-excludes` or `SM_BACKUP_EXCLUDES`; `none` disables excludes, a value beginning with `+` appends CSV patterns to the defaults, and any other CSV replaces them. Patterns cannot contain commas. |
| `one_file_system` | `true` (Linux only; `false` on Windows) | Don't cross mountpoints under the listed paths. Use Linux installer flag `--backup-one-file-system` or set `SM_BACKUP_ONE_FILE_SYSTEM` to a value accepted by boolean parsing when provisioning a container agent. |
| `prune_mode` | `"host"` | `"host"`: after each successful backup, run `restic forget --prune` with the `[retention]` keeps. `"external"`: never prune from the host — for append-only credentials; see endpoint setup below. |
| `[retention]` | `daily = 7`, `weekly = 4`, `monthly = 6` | Maps to `--keep-daily/--keep-weekly/--keep-monthly`. Only used when `prune_mode = "host"`; forget runs with `--group-by host` so path-list changes do not strand old snapshot groups. |
| `[[repo]]` | — | One block per endpoint: logical `name` (what the UI and metrics show), `url`, `password_file`. |
| `[repo.retention]` | inherits `[retention]` per field | Optional per-repo `daily`, `weekly`, `monthly` override. Unset fields inherit the effective global retention; setting a field to `0` omits that restic keep flag for that repo, and the merged result must keep at least one value above zero. |
| `env_file` (per-repo) | — | Absolute path to a root-owned `KEY=VALUE` file of transport credentials injected into restic's environment for this repo only. Keys are allowlisted (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_DEFAULT_REGION`, `B2_ACCOUNT_ID`, `B2_ACCOUNT_KEY`, `RESTIC_REST_USERNAME`, `RESTIC_REST_PASSWORD`); any other key is a hard error. The values never touch `backup.toml`, the status file, metrics, logs, or the UI, and are scrubbed from restic error text. |
| `s3_region` (per-repo) | — | Region for an `s3:` repository (sets `AWS_DEFAULT_REGION` unless the env file already set it). |
| `s3_path_style` (per-repo) | `false` | Path-style S3 addressing (`-o s3.bucket-lookup=path`). Required by MinIO, Ceph/RGW, Garage, and most self-hosted S3; leave `false` for AWS S3. |

Existing host installs keep their already-written `backup.toml` until an operator edits it or re-runs the installer with `--backup-repos`. `--backup-repos` is declarative, not additive: whenever it is supplied, the installer rewrites the repo list to exactly what it names, so adding a second destination means passing the full comma-separated list (`--backup-repos 'tunnel:nas-01/web-01,s3:https://s3.example.com/bucket/web-01'`), not just the new entry. Passing only the new entry drops the old repo from the config — never destructive (snapshots and `backup.key` are untouched, and the key is never regenerated once it exists), but the host silently stops backing up to the old destination until the full list is restored. Containerized agents regenerate the file on every boot, so the first run after upgrading uploads Docker named volumes; expect a one-time backup-size and quota jump. Set `SM_BACKUP_EXCLUDES=+/var/lib/docker` to pin the old behavior.

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

**Supplying credentials (local path).** The installer takes S3/B2 credentials as environment variables only (never flags — argv is world-readable) and writes them to a root-owned `/etc/servermonitor-backup/repo-credentials.env` (`0440 root:sm-agent`), then sets `env_file` on every `s3:`/`b2:`/`rest:` repo so restic reads them for that repo alone. They never enter `backup.toml`, the status file, metrics, logs, or the UI, and are scrubbed from restic error text. The explicitly requested recovery kit includes them because that offline artifact must be sufficient for a bare-machine restore.

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

## Centrally managed direct external S3/R2

This is the managed version of the object-storage path above. Configure an external destination once on **Backups → Direct external S3 destinations**, then create a repository namespace linked to a host. The agent receives that assignment and uploads to S3/R2 itself; ServerMonitor is not a proxy and never handles backup objects.

Before storing any destination credential, set a stable server master key and restart:

```bash
openssl rand -base64 32
# Put the output in deploy/.env as BACKUP_SECRETS_KEY, then restart the app.
```

Back up this key with the server's other disaster-recovery secrets. It is not stored in PostgreSQL, and changing or losing it makes the encrypted destination credentials already in the database unreadable. Once encrypted credentials exist, the server validates every ciphertext at startup and refuses to run with a missing or incorrect key. There is intentionally no plaintext fallback or automatic key reset.

**Destination setup.** Enter the endpoint (leave blank for AWS), bucket, region, addressing mode, and a prefix template. A custom endpoint must be an `https://` origin without embedded credentials, a path, a query, or a fragment. Templates must include both `{host_id}` and `{repository}`; `{hostname}` is optional. Hostnames and repository names are sanitized to safe path segments and the fully expanded prefix is saved on the repository record, so renaming a host cannot silently move a repository.

Credentials have two scopes:

- **Per-repository credentials (recommended)** — entered while assigning a namespace to a host. Use a provider key restricted to exactly that host prefix, ideally read/list/write with deletes denied.
- **Destination-shared credentials** — convenient for small installations, but every assigned agent receives the same key. A compromised agent can exercise every permission that key has, so the provider policy—not the UI prefix—is the security boundary.

Centrally managed direct repositories are always treated as externally pruned, even when the host already has `prune_mode = "host"` for older local repositories. Apply retention at S3/R2 or from a separately trusted prune runner; the managed agent credential should remain prefix-scoped and deny deletes.

ServerMonitor never creates IAM users, R2 tokens, bucket policies, lifecycle rules, or Object Lock settings. Create and revoke those at the provider. Revoking a repository in ServerMonitor removes its assignment and local credential file on the next agent sync, but cannot invalidate a copied provider key; revoke that key at S3/R2 as well.

**Secret handling.** Admin create/replace requests are write-only: list and mutation responses contain `credentials_configured`, never access keys, secret keys, session tokens, or ciphertext. PostgreSQL stores AES-256-GCM ciphertext bound to the destination or repository record, using `BACKUP_SECRETS_KEY`. The authenticated agent endpoint returns only an AES-GCM envelope derived from the calling agent's bearer token, with `Cache-Control: no-store`; plaintext credentials are never an API JSON response. HTTPS is still mandatory because the bearer token itself is sensitive. The agent decrypts only its own assignments and writes one mode-`0600` credential env file per repository beneath `/var/lib/servermonitor/managed-backups` (ACL-protected equivalent on Windows). Metadata, status, metrics, UI responses, and request logs exclude the secrets.

The restic repository password remains host-only and is shared by that host's local and centrally managed repositories. After the first managed assignment arrives, run `sm-agent backup recovery-kit` again and save the refreshed output offline; it is the break-glass copy of the repository URL, provider credential, and repository password.

Agents poll for assignments every minute. Existing backup-enabled hosts need no installer rerun or per-host S3 environment change: the next scheduled run loads the new repository and idempotently initializes it before backup. A host that has never enabled backups still needs the one-time opt-in below because that provisions restic, the repository password, schedule, and whole-host read capability. Wait up to one minute after assignment so the resident agent has written the managed configuration; no repository URL or provider secret is passed to either installer.

Linux:

```bash
SM_ENABLE_BACKUP=1 SM_BACKUP_PRUNE_MODE=external \
  sudo --preserve-env=SM_ENABLE_BACKUP,SM_BACKUP_PRUNE_MODE \
  bash -c "curl -fsSL https://monitor.example.com/install.sh | bash"
```

Windows (elevated PowerShell):

```powershell
$env:SM_ENABLE_BACKUP = "1"
$env:SM_BACKUP_PRUNE_MODE = "external"
iex (iwr -useb https://monitor.example.com/install.ps1).Content
```

After the last managed assignment is revoked, the already-enabled runtime remains healthy but idle, removes the revoked credential on its next configuration poll, and drops the retired repository from reported status on its next backup/check cycle. Provider credentials must still be revoked at S3/R2.

### Backward-compatible migration

No existing repository is converted automatically:

- Existing server and storage-node rows remain valid after migration `0019`; their IDs, namespaces, credentials, routes, and data stay unchanged.
- `/api/v1/backup-targets` remains an alias for existing clients. New code uses `/api/v1/backup-repositories` and exposes `storage_backend`, `repository_namespace`, and `data_path` explicitly.
- Existing agent-local `SM_BACKUP_S3_*` / `[[repo]]` configuration stays supported and is merged with centrally assigned direct repositories. Those environment variables now mean **local/unmanaged direct S3 only**.
- Existing cold archive `S3_*` variables remain lower-precedence aliases. New deployments should use `ARCHIVE_S3_*`.
- `BACKUP_S3_*` continues to mean only the S3 storage behind ServerMonitor's central REST gateway. The backup-specific credential pair overrides the shared AWS chain for that gateway only, with the old chain retained as fallback.

## Backing up to the ServerMonitor server (managed backup endpoint)

Instead of standing up a rest-server on a storage VPS by hand (the section above), you can **promote a ServerMonitor server itself into the restic endpoint** other hosts back up to. The server binary speaks the restic REST protocol on `/backup/…`; there is no second process to install or supervise.

**Enable it.** Set exactly one storage backend and restart the server:

- `BACKUP_DIR=/var/lib/servermonitor/backup-repos` — store blobs on the server's own disk. Simplest; watch capacity with an `fs_used_pct` alert (install the agent on the backup server itself).
- `BACKUP_S3_BUCKET=...` (plus `BACKUP_S3_REGION`, `BACKUP_S3_ENDPOINT` for self-hosted S3, `BACKUP_S3_USE_PATH_STYLE=1`, `BACKUP_S3_PREFIX`) — store blobs in a bring-your-own object-storage bucket. Set `BACKUP_S3_ACCESS_KEY_ID` and `BACKUP_S3_SECRET_ACCESS_KEY` (plus optional `BACKUP_S3_SESSION_TOKEN`) for backup-only credentials. If the backup-specific pair is unset, the server falls back to its standard AWS credential chain for compatibility. This lets the cold-metrics archive and backup storage use different accounts or providers.

Setting both is a startup error. `BACKUP_MAX_BLOB_BYTES` (default 1 GiB) caps a single upload.

**TLS is mandatory** — the endpoint authenticates with HTTP Basic, so the server refuses to expose `/backup` over plaintext unless `INSECURE_ALLOW_HTTP=1`. Three postures, chosen automatically:

- Already terminating TLS (`TLS_CERT_FILE`+`TLS_KEY_FILE`) or behind a trusted proxy (`TRUST_PROXY_TLS=1`) → reused as-is.
- A bare server with no proxy → set `BACKUP_ACME_DOMAIN=backup.example.com` (and optionally `ACME_EMAIL`, `ACME_CACHE_DIR`) and the server obtains and renews a Let's Encrypt certificate itself (HTTP-01 on :80, so the domain must resolve to the server and port 80/443 be reachable).

The Backups tab shows which posture is active.

**Provision a repository namespace in the UI.** Open **Backups → New repository**, choose the ServerMonitor destination, give the namespace a name, and optionally link a host and set a quota. The server mints a credential shown **once** and stored only as a SHA-256 hash — it authenticates uploads only and cannot decrypt anything (the repo password never leaves the client). The page renders the exact install/`backup.toml` snippet to paste on the target host, using the `SM_BACKUP_REST_USERNAME`/`SM_BACKUP_REST_PASSWORD` credential-file mechanism above. The repo URL is `rest:https://backup.example.com/backup/<name>`.

When `BACKUP_S3_BUCKET` is the gateway storage backend, the effective path is **Agent → ServerMonitor → S3**. ServerMonitor must receive the full upload, temporarily spools each restic object to disk, and then relays it to object storage. Use **Direct external S3** instead when the desired path is **Agent → S3 directly**.

Point a host at it exactly like any other repo, with `prune_mode = "external"` (the endpoint is append-only):

```bash
sudo SM_ENABLE_BACKUP=1 \
  SM_BACKUP_REPOS='rest:https://backup.example.com/backup/web-01' \
  SM_BACKUP_REST_USERNAME=web-01 \
  SM_BACKUP_REST_PASSWORD=<minted> \
  SM_BACKUP_PRUNE_MODE=external \
  ./scripts/install-agent-linux.sh --enable-backup --backup-repos '...'
```

**Guarantees.** The endpoint is **append-only**: it refuses every delete and every attempt to overwrite an existing object (the ransomware boundary), with the sole exception of restic's own lock files (so `restic unlock` works). Per-host isolation is absolute — a credential can only ever address its own `/backup/<name>` namespace. Revoking a repository from the UI stops new uploads immediately but never deletes stored blobs (append-only applies to admins too); reclaim space by deleting on the storage backend directly. **Prune/`forget` must run elsewhere** — from a trusted box holding a full-power credential and the repo password — never from the monitored host.

A repository the endpoint holds **no objects** for — one created by mistake, or emptied on the backend — can be deleted outright from the UI (**Delete** replaces **Revoke** on empty rows). This never touches stored data: the server refuses the delete with `409` if the backend namespace holds any object, down to a zero-byte one. The check is a preflight, not a lock, so in the narrow window where a first upload lands mid-delete the row can still be removed while that object is written; the result is an orphaned, still-encrypted repo you reclaim on the backend like any other — no snapshot is ever destroyed. Reusing a deleted name is refused while the old namespace still holds objects, so a new credential can never rebind to leftover blobs.

Use **Measure** on a repository to walk its namespace and refresh the stored-bytes figure and quota bar. Storage totals also appear in the summary tiles at the top of the Backups tab.

## Backing up over the built-in WireGuard tunnel (no exposed endpoint)

The managed endpoint above rides the server's public HTTPS listener. Tunnel mode removes even that: each backup host establishes a **per-host WireGuard tunnel** to the server, and the restic REST endpoint is served **only inside the tunnel** — it does not exist on any real network interface. The public transport is `BACKUP_WG_ENDPOINT:BACKUP_WG_PORT` over UDP. Inside the tunnel, restic reaches the fixed server address `10.83.0.1:8443` by default; port 8443 is not the public listener and should not be published. WireGuard never replies to a packet that isn't signed by an enrolled peer key, so scanners see nothing at all. TLS and `BACKUP_ACME_DOMAIN` become unnecessary for backups: the tunnel is the transport encryption, restic's client-side encryption still covers the data, and the per-repository Basic-auth credential still gates every request (a tunnel IP grants no authorization by itself).

Everything is userspace Go inside the existing binaries — no kernel WireGuard, no `wg-quick`, no TUN devices, no new capabilities on either side, no interference with an existing WireGuard or Tailscale setup on the host. The agent's tunnel exists **only while a backup, check, restore or init is running**; there is no resident tunnel process.

**Enable it on the server.** Keep one storage backend (`BACKUP_DIR` or `BACKUP_S3_BUCKET`) and add:

- `BACKUP_WG_PORT=51820` — turns tunnel mode on; this is the public UDP transport port to expose, not restic's internal port.
- `BACKUP_WG_ENDPOINT=` — optional `host[:port]` agents should dial. Defaults to the host agents already reach the server by, so most setups leave it empty.
- `BACKUP_WG_SUBNET=10.83.0.0/16` — tunnel address pool; the server takes the first address.
- `BACKUP_WG_MTU=1280` — conservative default that survives PPPoE/IPv6 encapsulation; raise toward 1420 on clean paths.
- `BACKUP_PUBLIC_HTTP=1` — optional. By default tunnel mode takes `/backup` **off** the public listener; set this to serve both (mixed fleet where some hosts cannot use UDP).

With the default subnet the resulting path is `agent → UDP BACKUP_WG_PORT → WireGuard → 10.83.0.1:8443 → ServerMonitor storage backend`. The Backups UI labels the external UDP endpoint and internal REST address separately so 8443 is never mistaken for a port to expose.

The server generates and persists its WireGuard key on first start; peers survive restarts. Publishing the UDP port is opt-in: add `-f deploy/docker-compose.wg.yml` to the `docker compose` command and it binds `${BACKUP_WG_PORT:-51820}` on the same host and container port. The base compose file reserves nothing, so a server with tunnel mode off never sits on UDP 51820. When tunnel mode is enabled, the server fails loudly at startup if that port is already in use. In node-only fleets this published server port is harmless and is not part of the backup data path; each promoted node's own UDP port must separately be reachable from backup clients. Storage-node endpoint start failures now surface on the Backups page.

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

**Trust model.** The node stores ciphertext only: repo passwords never leave the backing-up host, and the node verifies upload credentials against server-distributed hashes, so a compromised node can neither read nor forge backups — it can, at worst, delete its own disk (which is why the two-independent-repos rule still applies; pair a node repo with S3/B2 or a second node). The node's endpoint is append-only exactly like the server's. Upload credentials are still minted and revoked centrally in the UI; per-repository usage flows back to the server for quota display.

**Fate sharing note.** A node-hosted repo dies with the node's disk. For the borg-style posture: repo 1 on a storage node in another room/site, repo 2 on object storage with a deny-delete policy — both over their own transports, no shared fate, nothing internet-exposed except silent UDP.

## Retiring a dead storage node

When a node stops answering, the UI distinguishes *transient* from *permanent*. A node whose agent has merely stopped reporting shows **offline — last seen …** on the Storage nodes list, an **offline** badge on every repository stored there, a **node offline** state on the affected hosts in the fleet table, and — on each host's Backups tab — the failing run condensed to one line (`backup failed: destination unreachable (…)`, full restic output folded underneath) plus an amber note naming the node. A node whose host has been archived or deleted from the fleet shows **host archived** / **host removed** instead. Offline is treated as recoverable: the new-repository wizard refuses unavailable nodes, but nothing else changes — backups resume by themselves when the node returns.

**What is still recoverable.** The node holds ciphertext only, and a restic repository is a self-contained directory: if the node's *disk* survives, copy its `backup-store/<name>` directory to any rest-server (or a freshly promoted node) and every snapshot is intact — the repository password lives on the backing-up host (`backup.key`) and in the recovery kit, never on the node or the server. The snapshot catalog on each host's Backups tab keeps rendering the last-known inventory (count, sizes, paths) exactly so you can judge whether that disk is worth salvaging; an amber line above it warns that restore and browse will fail until the store is reachable again. Only when the disk is gone with the machine are the snapshots truly lost.

**The retirement sequence.** Order matters — the guidance in the UI stays on screen until the step that clears it is actually done:

1. **Repoint or disable backups on every affected host first.** The Backups tab shows what the *agent* reports, not what the server has on record, so the failing repo card and its snapshots persist — and the agent keeps dialing the dead endpoint on schedule — until the host's own config changes. Re-run the install snippet with a new `--backup-repos` destination, or edit `/etc/servermonitor-backup/backup.toml` and remove the repo's `[[repo]]` block. On the agent's next status report the card disappears.
2. **Revoke the repositories' upload credentials** on the Backups page. Revocation is bookkeeping at this point — the endpoint is gone — but demotion requires it.
3. **Demote the node** (Storage nodes → Demote; allowed once no active repositories remain), or archive/delete the host if the machine has left the fleet entirely. Deleting a host demotes its node automatically.
4. **Delete the repository records.** A node-hosted repository row gains a **Delete** action only once its node is demoted, archived, or removed — never while merely offline — and the confirmation spells out the contract: it removes the server-side record and credential only; data on the node's disk is never touched, and the server holds no copy. Delete after salvage, or once the data is written off.

Doing step 4 before step 1 is harmless but loses the repo-to-node linkage, so the amber note telling you (or a teammate) to reconfigure the host disappears while the agent is still failing — the sequence above keeps every hint visible until its work is done.

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
sm-agent backup ls --repo repo2 --snapshot latest /
sm-agent backup ls --repo repo2 --snapshot 1a2b3c4d --recursive --json /var/lib
```

`sm-agent backup ls` lists one directory level unless `--recursive` is supplied. `--snapshot` defaults to `latest`, `--repo` may be omitted when exactly one repository is configured, and `--json` emits the stable ServerMonitor entry array instead of restic's line stream. URL and `tunnel:` repositories are both supported.

The Backups tab shows the per-path bytes and file counts from each successful run, including zero-byte paths as an empty-backup warning. Its snapshot browser executes the same agent-side `restic ls` operation one directory at a time. Browse requests and results are transient in server memory, disappear on a server restart or after expiry, and every request is covered by the server's normal audit log. The resident agent uses its own `restic-browse-cache` directory next to the status file, separate from the privileged run cache.

Overlapping runs are safe: `backup run`, `backup check` and `backup restore` share one lock file (`backup.lock`, next to the status file) and any of them exits cleanly as a no-op if another is already in flight, so a scheduled backup, the weekly check and a manual restore never fight over the restic cache. The privileged restic cache lives in `restic-cache/` next to the status file, so the first run after enabling is slower than the rest.

The backup, check and restore units all deliberately execute the root/SYSTEM-owned agent copy (`/usr/local/bin/sm-agent`; Windows: `C:\Program Files\ServerMonitor\sm-agent.exe`) rather than directly executing the resident service's writable copy. A successful resident self-update now writes the server signature beside that binary. On Linux, a root-owned systemd path unit verifies the signature against an installer-pinned Ed25519 public key and then atomically promotes only a newer signed binary; a six-hour timer is the fallback reconciler. Windows performs the same independent verification from a SYSTEM scheduled task every five minutes. This keeps the privileged copy current without making the low-privilege service an authority over privileged code. Existing pre-0.3.7 installs need one installer rerun to bootstrap the pin and reconciliation job; after that, normal agent self-upgrades synchronize both copies automatically.

Privileged-agent drift metrics and the `stale_agent` and `agent_perms` states apply only to hosts with backups configured.

Backups tab banner states:

- **not configured** — the status file doesn't exist yet. Normal before the first run finishes; otherwise check that the timer is enabled (`systemctl list-timers sm-backup.timer`).
- **stale** — the status file hasn't been written in over 26 hours. The timer stopped firing (host was off past the `Persistent` catch-up, unit disabled) or every run is dying before it can write status — check `journalctl -u sm-backup`.
- **backups blocked** — the privileged agent copy exists but the `sm-agent` service account cannot execute it. The host page and Backups tab show the repair command.
- **error** — the status file exists but can't be parsed; the banner carries the message.

Common failures, all visible in the journal and as per-repo `error` in the status file (with repository URLs scrubbed out):

- **Wrong repository URL / bad credentials** — restic fails immediately; fix the `[[repo]]` `url` and re-run `sm-agent backup init` if the repo was never initialized.
- **Endpoint down** — that repo records a failure and fires the *backup failed* / *backup stale* alerts; other repos in the same run are unaffected.
- **Key file missing** — `backup init` and `backup run` refuse to start. The installer never regenerates a key on its own; restore `backup.key` from your recovery kit (it is the password, base64 line for line) rather than generating a fresh one, or existing snapshots become unreadable.
- **`status=203/EXEC` after an agent upgrade** — a pre-0.4.0 privileged-sync bug could leave `/usr/local/bin/sm-agent` as `root:root 0500`, so the scheduled services could not execute it. Repair it with `sudo chown root:root /usr/local/bin/sm-agent && sudo chmod 0755 /usr/local/bin/sm-agent`, or re-run the installer. Agent 0.4.0 and later sync runs repair the ownership and mode automatically, and the UI surfaces the condition as **backups blocked**.
