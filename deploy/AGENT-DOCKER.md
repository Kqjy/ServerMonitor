# Dockerized agent

Run the ServerMonitor agent as a container on hosts where everything runs in containers and you don't want to install host packages.

> **Prefer the host install for ordinary VPS/VMs** — including hosts that currently run Glances in a container. Run `scripts/install-agent-linux.sh` instead: the host itself isn't containerized, so the installed agent sees the host *and* its Docker containers with none of the setup below.

The agent is a single static binary. This image runs it with host-namespace visibility and the two capabilities needed for listening-port owner attribution — `CAP_DAC_READ_SEARCH` and `CAP_SYS_PTRACE` — with all others dropped. On a host install those two are opt-in (`--enable-port-owners`); this recipe bakes them in, because port-owner mapping is the main reason to run host-namespaced in the first place.

## 1. Build and push the image

Once, on a build host that has the source:

```bash
docker build -f deploy/agent.Dockerfile -t registry.example.com/servermonitor-agent:0.2.0 .
docker push registry.example.com/servermonitor-agent:0.2.0
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
SM_AGENT_IMAGE=registry.example.com/servermonitor-agent:0.2.0
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

**Older images (pre-0.1.9).** The behavior above only holds for agents running **0.1.9+**, the release that added `externally_managed` reporting. The server can't tell that an *older* containerized agent — new enough to accept remote upgrade (0.1.1+) but too old to report the field — is read-only, so it may still show *Update now* and honour an auto-upgrade toggle. The agent then attempts a self-replace that fails on the read-only rootfs and retries with backoff (harmless but noisy). For such images, set `SM_AUTO_UPGRADE=false` as defense in depth and don't click *Update now* until you redeploy onto a 0.1.9+ image, which closes the gap. (Host-installed agents self-upgrade normally.)

### Sampling interval

An interval pushed from the server applies immediately but is **not** persisted across container restarts — config is env-only. Set `SM_INTERVAL_S` in `.env.agent` to pin it across restarts; the compose recipe forwards it into the container.

### SMART / RAID / Wi-Fi

Not covered by this recipe — they need extra device access and host capabilities. Use the host install where you need them.

### Port / process owner attribution

The Ports tab lists every listening socket, but the PID and process columns are **blank** in this recipe. Mapping a socket to its process means `readlink`-ing `/proc/<pid>/fd`, which is ptrace-gated, and Docker's `docker-default` AppArmor profile blocks the agent from reading unconfined host processes **even with** `CAP_SYS_PTRACE`.

- **Preferred:** use the host install and pass `--enable-port-owners` — a targeted grant of the two caps, with full systemd hardening kept and no AppArmor profile to strip.
- **If it must run from a container:** ship a **scoped AppArmor profile** — clone `docker-default`, add a narrow `ptrace (read)` for unconfined peers, and select it with `security_opt: apparmor=<profile>`.

Do **not** use `apparmor=unconfined`: it strips the whole profile on a container that already has `pid: host`, `CAP_SYS_PTRACE`, host-FS access, and the Docker socket. (Earlier revisions of this doc recommended it — if you applied that, remove it.)

### Container metrics

These come from the Docker socket. On hosts using Podman or containerd without a Docker-compatible socket they're skipped, and every other collector still works.
