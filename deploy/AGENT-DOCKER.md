# Dockerized agent

For hosts where everything runs in containers (no host packages). For ordinary VPS/VMs — **including hosts that currently run Glances in a container** — prefer the host install via `scripts/install-agent-linux.sh`: the host itself isn't containerized, so the installed agent sees the host *and* its Docker containers without any of the setup below.

The agent is a single static binary. This image runs it with host-namespace visibility and the two capabilities the host unit grants for listening-port owner attribution (`CAP_DAC_READ_SEARCH` + `CAP_SYS_PTRACE`), all others dropped. On the host those two are opt-in (`--enable-port-owners`); this recipe bakes them in because port-owner mapping is the main reason to run host-namespaced.

## 1. Build and push the image (once, on a build host with the source)

```
docker build -f deploy/agent.Dockerfile -t registry.example.com/servermonitor-agent:0.1.9 .
docker push registry.example.com/servermonitor-agent:0.1.9
```

The image reports its version from the compiled-in `pkg/version` constant.

## 2. Register the host to get its identity

Each agent needs a unique token. Register against the admin API:

```
curl -sS -X POST \
  -H "X-Admin-Token: $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"hostname":"web-07"}' \
  https://monitor.example.com/api/v1/admin/hosts
```

Response:

```
{"host_id":7,"token":"<agent-token>","sample_interval_s":10,"server_pubkey":"<hex>"}
```

Keep `token`. The response also returns `server_pubkey`, but this recipe doesn't set it: the agent pins it automatically from the server's first ingest ack (over the same TLS channel), and an externally-managed agent never self-upgrades — the only thing the pubkey gates.

## 3. Configure the endpoint

Copy `deploy/.env.agent.example` to `.env.agent` beside the compose file and fill it in:

```
SM_SERVER_URL=https://monitor.example.com
SM_TOKEN=<agent-token from step 2>
SM_AGENT_IMAGE=registry.example.com/servermonitor-agent:0.1.9
```

Treat `.env.agent` as a secret (`chmod 600`) — the token authenticates the agent. Do not commit it.

## 4. Run

The service declares both an `image:` and a `build:` section. With no `pull_policy` set, a bare `docker compose up -d` runs the local image if one is already present and otherwise **builds** it from source at `..` — Compose does not pull from the registry on its own once a build section exists. Pick the command that matches the host.

Registry install (image pushed in step 1, no source on the host) — pull and run, never build:

```
docker compose -f deploy/docker-compose.agent.yml --env-file .env.agent up -d --pull always --no-build
docker compose -f deploy/docker-compose.agent.yml logs -f sm-agent
```

`--pull always` fetches `SM_AGENT_IMAGE` from the registry; `--no-build` turns a failed pull into an error instead of letting Compose fall back to a build the host can't perform.

Build from source (run from a checkout of the repo, so `..` is a valid build context):

```
docker compose -f deploy/docker-compose.agent.yml --env-file .env.agent up -d --build
```

The recipe applies:
- `pid: host` + `network_mode: host` — the agent sees host processes and interfaces, not the container's namespace.
- `cap_drop: ALL` then `cap_add: DAC_READ_SEARCH, SYS_PTRACE` — the same capabilities the host unit grants under `--enable-port-owners`, nothing more; `no-new-privileges`.
- `read_only` rootfs; all state (spool, health file, deregistration sentinel) lives in the `sm-agent-state` volume.
- `/var/run/docker.sock` mounted at `:ro` so the `containers` collector can list the host's containers. **`:ro` is not a security boundary** — it marks the bind-mount read-only, not the Docker API: any process that can reach the socket can still issue write calls (start a privileged container, bind-mount `/`, add a host user), which is equivalent to root on the host. Treat socket access as full host trust. If that is unacceptable, run a read-only Docker socket proxy in front (e.g. `tecnativa/docker-socket-proxy` with only `CONTAINERS=1`) and set `DOCKER_HOST` to the proxy instead of bind-mounting the raw socket — the collector connects via `client.FromEnv` and honours it.
- the host root bind-mounted read-only at `/host` with `SM_HOST_FS_ROOT=/host`, so the `fs` collector reports the host's filesystem usage. `pid: host` exposes the host's mount table but not its mount namespace, so without this the agent would `statfs` the container overlay and report wrong or missing filesystems. The agent only `statfs`-es mountpoints — it does not read file contents — and the mount is read-only (the same approach `node-exporter` uses).

## 5. Verify

In the UI the new host appears within ~2 intervals and reports CPU / memory / network / disk plus container counts. Liveness from the host:

```
docker compose -f deploy/docker-compose.agent.yml exec sm-agent \
  /usr/local/bin/sm-agent healthz --config /var/lib/servermonitor/agent.toml
```

## Notes

- **Upgrades**: a containerized agent cannot replace its own binary (read-only rootfs, and the binary is reverted on the next image pull anyway), so it reports itself as *externally managed* — via `SM_EXTERNALLY_MANAGED=true`, and it also auto-detects `/.dockerenv`. The server then never offers remote or automatic upgrade for it: the host detail page shows "Managed externally — redeploy a new agent image to update" instead of an *Update now* button, the auto-update toggle is disabled, and the upgrade API returns `412`. To upgrade, build and push a new image tag, bump `SM_AGENT_IMAGE`, then `docker compose ... up -d --pull always --no-build` (a bare `up -d` would try to build the new tag from source rather than pull it). That guarantee only holds for agents running 0.1.9+ — the release that added `externally_managed` reporting. The server cannot tell that an *older* containerized agent (one new enough to accept remote upgrade, 0.1.1+, but too old to report the field) is read-only, so for those it may still show *Update now* and honour an auto-upgrade toggle; the agent then attempts a self-replace that fails on the read-only rootfs and retries with backoff (harmless but noisy). For such images set `SM_AUTO_UPGRADE=false` as defense in depth and don't click *Update now* until you redeploy onto a 0.1.9+ image, which closes the gap. Host-installed agents still self-upgrade normally.
- **Interval**: a sampling interval pushed from the server applies immediately but is not persisted across container restarts (config is env-only). Set `SM_INTERVAL_S` in `.env.agent` to pin it across restarts; the compose recipe forwards it into the container.
- **SMART / RAID / Wi-Fi**: not covered by this recipe — they need extra device access and host capabilities. Use the host install where you need them.
- **Port / process owner attribution**: the Ports tab lists every listening socket, but the PID and process columns are blank in this recipe. Mapping a socket to its process means `readlink`-ing `/proc/<pid>/fd`, which is ptrace-gated, and Docker's `docker-default` AppArmor profile blocks the agent from reading unconfined host processes **even with** `CAP_SYS_PTRACE`. For owner attribution, **prefer the host install** and pass `--enable-port-owners` — a targeted grant of the two caps with full systemd hardening kept and no AppArmor profile to strip. If it must run from a container, ship a **scoped AppArmor profile** (clone `docker-default`, add a narrow `ptrace (read)` for unconfined peers, select via `security_opt: apparmor=<profile>`) — **not `apparmor=unconfined`**, which strips the whole profile on a container that already has `pid: host`, `CAP_SYS_PTRACE`, host-FS, and the Docker socket. (Earlier revisions recommended `apparmor=unconfined`; if you applied it, remove it.)
- **Container metrics** come from the Docker socket; on hosts using Podman/containerd without a Docker-compatible socket they are skipped, and every other collector still works.
