# Dockerized agent

For hosts where everything runs in containers (no host packages). For ordinary VPS/VMs — **including hosts that currently run Glances in a container** — prefer the host install via `scripts/install-agent-linux.sh`: the host itself isn't containerized, so the installed agent sees the host *and* its Docker containers without any of the setup below.

The agent is a single static binary. This image runs it with host-namespace visibility and the same least-privilege capability set as the systemd unit (`CAP_DAC_READ_SEARCH` + `CAP_SYS_PTRACE`, all others dropped).

## 1. Build and push the image (once, on a build host with the source)

```
docker build -f deploy/agent.Dockerfile -t registry.example.com/servermonitor-agent:0.1.7 .
docker push registry.example.com/servermonitor-agent:0.1.7
```

The image reports its version from the compiled-in `pkg/version` constant.

## 2. Register the host to get its identity

Each agent needs a unique token plus the server's signing pubkey. Register against the admin API:

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

Keep `token` and `server_pubkey`.

## 3. Configure the endpoint

Copy `deploy/.env.agent.example` to `.env.agent` beside the compose file and fill it in:

```
SM_SERVER_URL=https://monitor.example.com
SM_TOKEN=<agent-token from step 2>
SM_SERVER_PUBKEY=<hex from step 2>
SM_AGENT_IMAGE=registry.example.com/servermonitor-agent:0.1.7
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
- `cap_drop: ALL` then `cap_add: DAC_READ_SEARCH, SYS_PTRACE` — same capabilities the systemd unit grants, nothing more; `no-new-privileges`.
- `read_only` rootfs; all state (spool, health file, deregistration sentinel) lives in the `sm-agent-state` volume.
- `/var/run/docker.sock` mounted read-only so the `containers` collector reports the host's containers.
- the host root bind-mounted read-only at `/host` with `SM_HOST_FS_ROOT=/host`, so the `fs` collector reports the host's filesystem usage. `pid: host` exposes the host's mount table but not its mount namespace, so without this the agent would `statfs` the container overlay and report wrong or missing filesystems. The agent only `statfs`-es mountpoints — it does not read file contents — and the mount is read-only (the same approach `node-exporter` uses).

## 5. Verify

In the UI the new host appears within ~2 intervals and reports CPU / memory / network / disk plus container counts. Liveness from the host:

```
docker compose -f deploy/docker-compose.agent.yml exec sm-agent \
  /usr/local/bin/sm-agent healthz --config /var/lib/servermonitor/agent.toml
```

## Notes

- **Upgrades**: auto-upgrade is disabled in containers (`SM_AUTO_UPGRADE=false`). To upgrade, build and push a new image tag, bump `SM_AGENT_IMAGE`, then `docker compose ... up -d --pull always --no-build` (a bare `up -d` would try to build the new tag from source rather than pull it). Host-installed agents still self-upgrade normally.
- **Interval**: a sampling interval pushed from the server applies immediately but is not persisted across container restarts (config is env-only). Set `SM_INTERVAL_S` in `.env.agent` to pin it across restarts; the compose recipe forwards it into the container.
- **SMART / RAID / Wi-Fi**: not covered by this recipe — they need extra device access and host capabilities. Use the host install where you need them.
- **Container metrics** come from the Docker socket; on hosts using Podman/containerd without a Docker-compatible socket they are skipped, and every other collector still works.
