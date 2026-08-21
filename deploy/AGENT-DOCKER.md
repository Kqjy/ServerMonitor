# Dockerized agent

Run the ServerMonitor agent as a container on hosts where everything runs in containers and you don't want to install host packages.

> **Prefer the host install for ordinary VPS/VMs** — including hosts that currently run Glances in a container. Run `scripts/install-agent-linux.sh` instead: the host itself isn't containerized, so the installed agent sees the host *and* its Docker containers with none of the setup below.

The agent is a single static binary. This image runs it with host-namespace visibility and the two capabilities needed for listening-port owner attribution — `CAP_DAC_READ_SEARCH` and `CAP_SYS_PTRACE` — with all others dropped. On a host install those two are opt-in (`--enable-port-owners`); this recipe bakes them in, because port-owner mapping is the main reason to run host-namespaced in the first place.

## 1. Build and push the image

Once, on a build host that has the source:

```bash
docker build -f deploy/agent.Dockerfile -t registry.example.com/servermonitor-agent:0.4.9 .
docker push registry.example.com/servermonitor-agent:0.4.9
```

The image reports its version from the compiled-in `pkg/version` constant.

## 2. Register the host

Each agent needs a unique token. Register against the admin API:

```bash
curl -sS -X POST \
  -H "X-Admin-Token: $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"hostname":"web-07"}' \
  https://monitor.example.com/api/v1/admin/hosts
```

```json
{"host_id":7,"token":"<agent-token>","sample_interval_s":10,"server_pubkey":"<hex>"}
```

Keep `token` — it's what the agent authenticates with. The response also returns `server_pubkey`, but this recipe doesn't set it: the agent pins it automatically from the server's first ingest ack (over the same TLS channel), and an externally-managed agent never self-upgrades, which is the only thing the pubkey gates.

## 3. Configure the endpoint

Copy `deploy/.env.agent.example` to `.env.agent` beside the compose file and fill it in:

```ini
SM_SERVER_URL=https://monitor.example.com
SM_TOKEN=<agent-token from step 2>
SM_AGENT_IMAGE=registry.example.com/servermonitor-agent:0.4.9
```

Treat `.env.agent` as a secret (`chmod 600`) and don't commit it — the token authenticates the agent.

## 4. Run

The service declares both an `image:` and a `build:` section, and sets no `pull_policy`. So a bare `docker compose up -d` runs the local image if one is already present, and otherwise **builds** from source at `..` — once a build section exists, Compose never pulls from the registry on its own. Pick the command that matches the host.

**Registry install** — image pushed in step 1, no source on the host. Pull and run, never build:

```bash
docker compose -f deploy/docker-compose.agent.yml --env-file .env.agent up -d --pull always --no-build
docker compose -f deploy/docker-compose.agent.yml logs -f sm-agent
```

`--pull always` fetches `SM_AGENT_IMAGE` from the registry; `--no-build` turns a failed pull into an error instead of letting Compose fall back to a build the host can't perform.

**Build from source** — run from a checkout of the repo, so `..` is a valid build context:

```bash
docker compose -f deploy/docker-compose.agent.yml --env-file .env.agent up -d --build
```

### What the recipe gives the container

| Setting | Effect |
|---|---|
| `pid: host` + `network_mode: host` | The agent sees host processes and interfaces, not the container's namespace. |
| `cap_drop: ALL`, then `cap_add: DAC_READ_SEARCH, SYS_PTRACE` | Exactly the caps the host unit grants under `--enable-port-owners`, nothing more — plus `no-new-privileges`. |
| `read_only` rootfs | All state (spool, health file, deregistration sentinel) lives in the `sm-agent-state` volume. |
| `/var/run/docker.sock` → `:ro` | The `containers` collector can list the host's containers. **See the security note below.** |
| `/` → `/host` (`:ro`) + `SM_HOST_FS_ROOT=/host` | The `fs` collector reports the host's filesystem usage instead of the container overlay. |

**The Docker socket is full host trust.** `:ro` marks the bind-mount read-only, not the Docker API — it is **not a security boundary**. Any process that can reach the socket can still issue write calls (start a privileged container, bind-mount `/`, add a host user), which is equivalent to root on the host. If that's unacceptable, put a read-only socket proxy in front (e.g. `tecnativa/docker-socket-proxy` with only `CONTAINERS=1`) and point `DOCKER_HOST` at the proxy instead of bind-mounting the raw socket — the collector connects via `client.FromEnv` and honours it.

**Why the host root is mounted at `/host`.** `pid: host` exposes the host's mount table but not its mount namespace, so without `/host` the agent would `statfs` the container overlay and report wrong or missing filesystems. The mount is read-only and the agent only `statfs`-es mountpoints — it never reads file contents (the same approach `node-exporter` uses).

## 5. Verify

The new host appears in the UI within ~2 intervals and reports CPU / memory / network / disk plus container counts. Check liveness from the host with:

```bash
docker compose -f deploy/docker-compose.agent.yml exec sm-agent \
  /usr/local/bin/sm-agent healthz --config /var/lib/servermonitor/agent.toml
```

## Notes

### Upgrades

A containerized agent cannot replace its own binary — the rootfs is read-only, and the binary would be reverted on the next image pull anyway. So it reports itself as **externally managed** (via `SM_EXTERNALLY_MANAGED=true`, and it also auto-detects `/.dockerenv`). The server then never offers it a remote or automatic upgrade:

- the host detail page shows "Managed externally — redeploy a new agent image to update" instead of an *Update now* button,
- the auto-update toggle is disabled, and
- the upgrade API returns `412`.

To upgrade: build and push a new image tag, bump `SM_AGENT_IMAGE`, then re-run the registry command —

```bash
docker compose -f deploy/docker-compose.agent.yml --env-file .env.agent up -d --pull always --no-build
```

(A bare `up -d` would try to *build* the new tag from source rather than pull it.)

Upgrade the server before deploying a 0.4.0 agent image: server ingest rejects unknown fields, so a pre-0.4.0 server rejects the backup-scope fields sent by the newer agent.

**Older images (pre-0.1.9).** The behavior above only holds for agents running **0.1.9+**, the release that added `externally_managed` reporting. The server can't tell that an *older* containerized agent — new enough to accept remote upgrade (0.1.1+) but too old to report the field — is read-only, so it may still show *Update now* and honour an auto-upgrade toggle. The agent then attempts a self-replace that fails on the read-only rootfs and retries with backoff (harmless but noisy). For such images, set `SM_AUTO_UPGRADE=false` as defense in depth and don't click *Update now* until you redeploy onto a 0.1.9+ image, which closes the gap. (Host-installed agents self-upgrade normally.)

### Sampling interval

An interval pushed from the server applies immediately but is **not** persisted across container restarts — config is env-only. Set `SM_INTERVAL_S` in `.env.agent` to pin it across restarts; the compose recipe forwards it into the container.

`SM_SMART_SAMPLE_S` controls the SMART background-sampling cadence in seconds. It defaults to 300 and is clamped between 60 and 3600; the compose recipe forwards a blank value to use the default.

### SMART / RAID / Wi-Fi

Not covered by this recipe — they need extra device access and host capabilities. Use the host install where you need them.

On the host install, note that `--enable-smart` (`CAP_SYS_RAWIO` + `disk` group) covers **SATA/SAS** SMART only. **NVMe** SMART reads use `NVME_IOCTL_ADMIN_CMD`, which the kernel gates behind `CAP_SYS_ADMIN` — so NVMe-only hosts (e.g. most modern bare-metal/Proxmox) need `--enable-smart-nvme` (`SM_ENABLE_SMART_NVME=1`). That cap is near-root, kept as a separate opt-in, and is **not** part of `--enable-all`.

### Managed backups

The containerized agent can back up its host itself — no second host-installed agent. It provisions its own restic setup into the `sm-agent-state` volume and runs a scheduled backup + weekly integrity check, reporting to the same host under its **Backups** tab.

**Enable it.** Create a repository on the server first (**Backups → New repository**) to get a `rest:` URL + upload credential, then add to `.env.agent`:

```ini
SM_ENABLE_BACKUP=1
SM_BACKUP_REPOS=rest:https://monitor.example.com/backup/web-07
SM_BACKUP_REST_USERNAME=web-07
SM_BACKUP_REST_PASSWORD=<the minted credential>
SM_BACKUP_TIME=02:30
```

Then `docker compose … up -d`. Optional: `SM_BACKUP_PATHS` (default `/etc,/home,/root,/var/lib`), `SM_BACKUP_EXCLUDES`, `SM_BACKUP_ONE_FILE_SYSTEM`, `SM_BACKUP_PRUNE_MODE` (default `external`), `SM_BACKUP_S3_*` for an S3/B2 endpoint, `SM_BACKUP_SCHEDULE`, `SM_BACKUP_CHECK_TIME`, `SM_BACKUP_CHECK_WEEKDAY`, `SM_BACKUP_CHECK_READ_DATA_SUBSET`, and `TZ` (so schedule times are interpreted in your zone). Repos must use `rest:`, `s3:`, `b2:`, `gs:`, `azure:`, `swift:`, or `tunnel:`; `sftp:` is not supported because the agent image does not include SSH.

**What gets backed up.** The default paths are `/etc`, `/home`, `/root`, and `/var/lib`, with `/var/lib/docker/volumes` added explicitly when it exists. Reproducible Docker image layers, writable container layers, build caches, container logs, networking state, and containerd content are excluded; named volumes and Docker swarm state remain in scope. `SM_BACKUP_EXCLUDES=none` disables the defaults, a value beginning with `+` appends comma-separated patterns, and any other comma-separated value replaces the defaults. The first backup after upgrading can be substantially larger because it includes named volumes; check endpoint quota first, or set `SM_BACKUP_EXCLUDES=+/var/lib/docker` to retain the old scope.

**Tunnel repositories.** Set `SM_BACKUP_REPOS=tunnel:NAME` for storage on the monitoring server or `SM_BACKUP_REPOS=tunnel:NODE/NAME` for a promoted storage node, and set `SM_BACKUP_REPO_NAMES` to the repository's logical name. A `tunnel:` repository authenticates with the **same minted upload credential** as a `rest:` repository: `SM_BACKUP_REST_USERNAME` and `SM_BACKUP_REST_PASSWORD` must both be set. The tunnel replaces the public network path; it does not replace endpoint authentication. Enrollment is automatic at boot over the agent-token channel and needs only outbound UDP to the server or node WireGuard endpoint; WireGuard runs in userspace, so it needs no new capabilities or kernel module. The create-once `tunnel.key` lives in the state volume. `docker compose down -v` destroys it, which does not affect backup data because a fresh key enrolls again, but the `backup.key` recovery warning below still applies. During a run, restic uses a transient loopback proxy; with `network_mode: host` this is the host loopback, the same exposure class as a host install, and it exists only for the duration of the run.

**What the recipe already provides for this** (don't remove): `uts: host` (so restic records the host's hostname — needed for restic's `host,paths` retention grouping), `cap_add: SYS_CHROOT`, and the `sm-agent-state` volume mounted at `/var/lib/servermonitor`, `/tmp`, and `/host/tmp`.

**How it works.** The agent generates `backup.key` **once** (never regenerated), writes `backup.toml` from the env on every boot, and stages the image's pinned restic into the volume. The `/tmp` + `/host/tmp` aliases give restic the same state paths before and after it chroots into `/host`; using the already-existing host `/tmp` mountpoint also avoids runc having to create a directory beneath the read-only `/host` bind. Each backup therefore records host-native paths (`/etc`, not `/host/etc`) and is interchangeable with a host-installed agent's snapshots. Host `/tmp` is hidden from this container and cannot be selected as a container-managed backup path. The schedule lives in the agent (daily backup + weekly `restic check`) — no systemd needed — and survives restarts (missed runs are caught up on boot). To run the first backup immediately instead of waiting for `SM_BACKUP_TIME`:

```bash
docker compose -f deploy/docker-compose.agent.yml exec sm-agent \
  /usr/local/bin/sm-agent backup run
```

List the root of the latest snapshot from the managed repository:

```bash
docker compose -f deploy/docker-compose.agent.yml exec sm-agent \
  /usr/local/bin/sm-agent backup ls --snapshot latest /
```

**Coolify/runc migration from 0.3.5–0.3.6.** Those recipes nested the state volume at `/host/var/lib/servermonitor`. On a fresh host where that directory did not already exist, runc tried to create the mountpoint after `/host` had become read-only and the container failed during initialization with `create mountpoint ... mkdirat`. Use the 0.3.7 Compose recipe as a unit: it keeps the existing named volume (and therefore the identity/key) but replaces that failing nested target with the already-existing `/host/tmp` target and its matching `/tmp` alias. Simply deleting the old nested mount lets the process start but breaks chrooted backups.

**Recovery kit — do this once, keep it offline:**

```bash
docker compose -f deploy/docker-compose.agent.yml exec sm-agent \
  /usr/local/bin/sm-agent backup recovery-kit
```

It prints the repository password. **`docker compose down -v` destroys the volume and the key** — without the kit, existing backups become unrecoverable ciphertext.

**Restore is staging-only.** `sm-agent backup restore …` restores into `/var/lib/servermonitor/restore/<snapshot>/` inside the volume; copy it onto the host with `docker cp`. In-place restore is refused from a container (restoring to `/` would hit the container, and `/host` is read-only); restore in place from the host runbook instead. See [BACKUPS.md](BACKUPS.md).

**Security notes.** Backups add **no new host-read power** — the resident agent already holds `CAP_DAC_READ_SEARCH` over `/host`. The real root-equivalence is the **Docker socket**; put a read-only socket proxy in front (see the socket note above) when backups are on. Keep `prune_mode = external` (the default) so a compromised agent can add snapshots but not delete history against an append-only endpoint. On rootless Docker or SELinux-enforcing hosts, verify `chroot` + the nested rw-over-ro `/host/tmp` mount work before relying on it.

### Port / process owner attribution

The Ports tab lists every listening socket, but the PID and process columns are **blank** in this recipe. Mapping a socket to its process means `readlink`-ing `/proc/<pid>/fd`, which is ptrace-gated, and Docker's `docker-default` AppArmor profile blocks the agent from reading unconfined host processes **even with** `CAP_SYS_PTRACE`.

- **Preferred:** use the host install and pass `--enable-port-owners` — a targeted grant of the two caps, with full systemd hardening kept and no AppArmor profile to strip.
- **If it must run from a container:** ship a **scoped AppArmor profile** — clone `docker-default`, add a narrow `ptrace (read)` for unconfined peers, and select it with `security_opt: apparmor=<profile>`.

Do **not** use `apparmor=unconfined`: it strips the whole profile on a container that already has `pid: host`, `CAP_SYS_PTRACE`, host-FS access, and the Docker socket. (Earlier revisions of this doc recommended it — if you applied that, remove it.)

### Container metrics

These come from the Docker socket. On hosts using Podman or containerd without a Docker-compatible socket they're skipped, and every other collector still works.

The `containers` collector reconnects on its own: if the daemon is unreachable when the agent first ticks (a boot race after `apt upgrade` restarts Docker) or the connection drops mid-run, it retries on the next tick and then every 30s while the daemon stays down, resuming as soon as the socket answers again — no agent restart needed.

One edge case the agent can't paper over: if the daemon recreates the socket *inode* while this container keeps running — `systemctl restart docker` (or an `apt upgrade` of `docker-ce`) on a host with `"live-restore": true`, which leaves containers up across the restart — the `:ro` bind-mount of the socket **file** is frozen to the now-dead inode, so re-dialing the same path can't recover. A full host reboot recreates the container with a fresh mount and is unaffected. If your hosts run `live-restore`, resolve the socket through the already-mounted host root instead of bind-mounting the file: drop the `/var/run/docker.sock` volume and add `DOCKER_HOST: unix:///host/run/docker.sock` to `environment` — each reconnect then resolves the current socket through the `/host` directory mount. The collector reads `DOCKER_HOST` via `client.FromEnv`.
