# ServerMonitor

A single Go application that replaces the **Glances + InfluxDB + Grafana** stack.

One binary collects metrics through a lightweight agent, stores them in **PostgreSQL + TimescaleDB**, optionally archives cold data to **S3 as Parquet**, and serves a modern **SvelteKit** dashboard from the same process.

```
agent (Linux/Windows)  ──HTTPS push (gzipped JSON)──▶  server (Go)
                                                          │
                                                          ├─ PostgreSQL + TimescaleDB
                                                          │     hypertable + 5-min CA + compression + retention
                                                          ├─ S3 (Parquet, nightly export)
                                                          └─ embedded SvelteKit UI + SSE live stream
```

---

## Why

Three loosely coupled services for a job that should be one process. Glances has no real persistence, InfluxDB is a separate database with its own schema and query language, Grafana is yet another service with its own auth and dashboard model. ServerMonitor pulls all of that into one self-contained binary backed by ordinary PostgreSQL.

- **Single binary** for the server, single binary for the agent.
- **PostgreSQL** is the only database — no Influx, no Prometheus.
- **Modern web UI** embedded into the server (no Grafana, no separate frontend service).
- **Light agent** — pure Go, runs as `systemd` on Linux or as a service on Windows.
- **Exportable** — old data is archived to S3 as standard Parquet files queryable by Athena, DuckDB, Spark, anything that reads Parquet.

---

## Quick start (Docker)

```bash
cp deploy/.env.example deploy/.env
$EDITOR deploy/.env
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.publish.yml --env-file deploy/.env up -d
```

Then open `http://localhost:8080`. The first visit takes you to a setup wizard that creates the admin account.

`docker-compose.yml` on its own publishes **no host ports** — the app and Postgres are reachable only over the Compose network, which is what a reverse proxy (Traefik, Caddy, Coolify, Dokploy) wants. `docker-compose.publish.yml` is what binds `${HTTP_BIND:-127.0.0.1}:${HTTP_PORT:-8080}` on the host for the UI and `127.0.0.1:5432` for Postgres; drop it from the command when a proxy fronts the app, and the host-port collisions that break redeploys go away with it. Tunnel backups need their own UDP port on the host — add `-f deploy/docker-compose.wg.yml` when `BACKUP_WG_PORT` is set.

The `.env` you must set:

| Variable           | Purpose |
|--------------------|---------|
| `POSTGRES_PASSWORD`| Postgres password |
| `ADMIN_TOKEN`      | Bearer token used by the agent install scripts to register a host |
| `ARCHIVE_S3_BUCKET` *(opt)* | Cold-metrics Parquet archive bucket. Leave empty to disable archiving. |
| `ARCHIVE_S3_REGION`, `ARCHIVE_S3_PREFIX`, `ARCHIVE_S3_ENDPOINT` *(opt)* | Cold-archive location; `S3_*` remains a compatibility alias. |
| `ARCHIVE_S3_ACCESS_KEY_ID`, `ARCHIVE_S3_SECRET_ACCESS_KEY` *(opt)* | Archive-only credentials. If omitted, the AWS credential chain is used. |
| `BACKUP_DIR` or `BACKUP_S3_BUCKET` *(opt)* | Storage behind ServerMonitor's central restic gateway. This is separate from the cold archive. |
| `BACKUP_SECRETS_KEY` *(opt)* | Stable 32-byte key used to encrypt centrally managed direct-S3 credentials in PostgreSQL. |

---

## Installing an agent on a remote host

After the server is running, you can install the agent on any Linux or Windows host that should be monitored. The install scripts register the host with the server and write `agent.toml` automatically. Supply the admin token through the `SM_ADMIN_TOKEN` environment variable (as shown below) or a file (`--admin-token-file` on Linux, `-AdminTokenFile` on Windows) — it is never accepted as a command-line argument, since arguments are visible to other local users while the script runs. On Linux, put the assignment **after** `sudo` so it survives into the elevated environment.

**Linux**
```bash
sudo SM_ADMIN_TOKEN="$ADMIN_TOKEN" ./scripts/install-agent-linux.sh \
  --server https://monitor.example.com \
  --binary ./sm-agent
```

**Windows (PowerShell, admin)**
```powershell
$env:SM_ADMIN_TOKEN = $env:ADMIN_TOKEN
.\scripts\install-agent-windows.ps1 `
  -ServerUrl https://monitor.example.com `
  -BinaryPath .\sm-agent.exe
```

Both scripts wrap `sm-agent register` (which calls `POST /api/v1/admin/hosts`), then enable the service (`systemd` on Linux, `sc.exe` on Windows). Within ~10 seconds the host appears in the dashboard with a live CPU sparkline.

**Reconfiguring an installed agent.** Re-running an installer on a host that already has `agent.toml` reconfigures the service in place rather than registering again: it re-derives capabilities and group memberships (Linux) or the service account and privileges (Windows), refreshes drifted binaries, and restarts — no admin token, re-registration, or identity change. Since 0.3.7 it also installs a root/SYSTEM reconciliation job that independently verifies the resident update's server signature before synchronizing the privileged backup-agent copy. Existing installations need one reconfigure run to bootstrap that job; later self-upgrades update both copies automatically. To flip a capability (e.g. add NVMe SMART with `--enable-smart-nvme`), just re-run with the flag added. Pass `--reinstall` / `-Reinstall` (or `SM_REINSTALL=1`) to force a full fresh install instead.

**Managed backups.** `--enable-backup` (`-EnableBackup` on Windows) additionally provisions scheduled, encrypted restic backups of the host to one or more endpoints you supply, with monitoring, per-repo alerts, and a printed recovery kit. It reads every file on the host at backup time, so it is a separate opt-in and — like `--enable-smart-nvme` — not part of `--enable-all`. Full guide, including the bare-machine restore runbook: **[deploy/BACKUPS.md](deploy/BACKUPS.md)**.

**Reactive IP banning.** `--enable-ipban` (`SM_ENABLE_IPBAN=1`) turns the Linux agent into a fail2ban-style banner: it follows sshd login failures in the journal (or `/var/log/auth.log`), bans an address after the configured number of failures with its own nftables table (`inet sm_agent`, prerouting hook, so Docker-published ports are covered too), and reports every ban to the server. The installer persists `enable_ipban = true` as a local safety gate; server policy cannot start detection or firewall writes on a host that was not explicitly opted in. The server keeps fleet defaults — thresholds, ban durations, the allowlist, and promotion rules — on the **Security** page, with optional per-host overrides for detection, enforcement, contribution, and applying the fleet list. Detection starts in observe mode; enforcement is a separate switch that refuses to turn on until the allowlist has at least one entry. The flag grants the resident agent `CAP_NET_ADMIN` plus `systemd-journal`/`adm` group membership, which is firewall-write authority and log-read access, so it is a separate opt-in and not part of `--enable-all`. `sm-agent ipban status` lists the kernel sets; `sm-agent ipban teardown` removes the table (the uninstaller and a reconfigure without the flag do this automatically).

**Docker (containerized agent)**

For hosts that run everything in containers, deploy the agent as a container instead of installing it on the host — register the host, drop its token into an env file, and `docker compose up`. Full steps in **[deploy/AGENT-DOCKER.md](deploy/AGENT-DOCKER.md)**. The container reads its identity from `SM_SERVER_URL` / `SM_TOKEN` / `SM_SERVER_PUBKEY`, so no on-disk `agent.toml` is required.

A host that only runs *Glances* in a container is not itself containerized — the host install above is simpler and still reports that host's containers via the Docker socket.

---

## Uninstalling an agent

To remove an agent that was installed directly on a host, run the matching uninstall script. Each stops and deletes the service, removes the binaries, config, spool, and the dedicated service account.

**Linux**
```bash
sudo ./scripts/uninstall-agent-linux.sh
```

**Windows (PowerShell, admin)**
```powershell
.\scripts\uninstall-agent-windows.ps1
```

Pass `--keep-data` (Linux) / `-KeepData` (Windows) to preserve `agent.toml` and the disk spool so a later reinstall keeps the same host identity. Uninstalling stops the host from reporting but does **not** drop its stored history — use **Settings → Remove** in the web UI for that. (For a containerized agent, `docker compose -f deploy/docker-compose.agent.yml down -v` instead.)

---

## What the agent collects

Everything Glances reports, gated by platform:

| Family | Linux | Windows | macOS | Source |
|---|:-:|:-:|:-:|---|
| CPU per-core + total + freq | ✓ | ✓ | ✓ | gopsutil |
| Load average | ✓ | — | ✓ | gopsutil |
| Memory + swap | ✓ | ✓ | ✓ | gopsutil |
| Disk I/O (per device, rates) | ✓ | ✓ | ✓ | gopsutil |
| Filesystems | ✓ | ✓ | ✓ | gopsutil |
| Network (per iface, rates) | ✓ | ✓ | ✓ | gopsutil |
| TCP connection states + open ports | ✓ | ✓ | ✓ | gopsutil |
| Process counts + top-N snapshot | ✓ | ✓ | ✓ | gopsutil |
| Containers (Docker) | ✓ | ✓ | ✓ | docker/docker — no-op if daemon unreachable |
| Sensors (temperatures) | ✓ | ✓ | ✓ | gopsutil |
| NVIDIA GPU | ✓ | ✓ | — | shells `nvidia-smi`; no-op if absent |
| SMART | ✓ | ✓ | ✓ | background `smartctl --json` sampling every 300s by default (`smart_sample_s` / `SM_SMART_SAMPLE_S`); no-op if absent. Linux NVMe SMART needs `CAP_SYS_ADMIN` (`--enable-smart-nvme`); `--enable-smart`/`CAP_SYS_RAWIO` covers SATA/SAS only |
| RAID arrays | ✓ | — | — | parses `/proc/mdstat` |
| Wi-Fi signal | ✓ | — | — | parses `/proc/net/wireless` |
| Uptime | ✓ | ✓ | ✓ | gopsutil |
| IP bans (sshd failures, bans, fleet list) | ✓ | — | — | `journalctl -f` / auth-log tail + nftables via netlink; needs `--enable-ipban` |

Run `sm-agent --list-collectors` to see what's active on the current platform.

---

## Web UI

- **Hosts** grid with live CPU sparklines (Server-Sent Events).
- **Host detail** with tabs: Overview, Memory, Disk, Network, Processes, Containers, Sensors, GPU.
- **Range selector** per host (15m / 1h / 6h / 24h / 7d).
- **Alerts** page with threshold rules and recent history.
- **Settings** for notification channels (SMTP + webhook).
- **Setup wizard** runs once on first boot.

Charts use [uPlot](https://github.com/leeoniya/uPlot) — small, fast, no canvas tearing. Styling is Tailwind v4, dark-first, semantic accents (emerald = good, amber = warn, rose = bad).

---

## Alerting

Rules evaluate every 15s against `metric_points` (or the 5-minute continuous aggregate for longer windows). A rule fires when the aggregate (avg/max/min/last) over the window crosses the threshold and stays crossed for `for_s`. Channels:

- **SMTP** — host, port, optional auth, comma-separated recipients.
- **Generic webhook** — POST JSON to any URL. Built-in adapters for Discord, Slack, ntfy. Falls back to generic JSON for everything else (PagerDuty Events v2 etc).

Dedup is per `(rule, host, label_key)`. Re-notification is suppressed within `cooldown_s`. Resolved fires call the channel again so you know it's clear.

---

## Storage tiers

| Tier | What | Retention |
|---|---|---|
| Hot | `metric_points` hypertable (raw 10s samples) | 30 days, compressed after 7 days |
| Warm | `metric_points_5m` continuous aggregate (avg/min/max/last per 5 min) | 6 months |
| Cold | S3 Parquet files via nightly job (`gocron` at 03:00 local) | indefinite |

`GET /api/v1/series` transparently merges all three sources for any time range. Cold-tier reads pull only the Parquet objects whose manifest range overlaps the query.

When `ARCHIVE_S3_BUCKET` is configured, the archive job—not a separate Timescale retention policy—owns expiry of `metric_points_5m`. It uploads and records manifests for every host first, and calls `drop_chunks` only after the entire pass succeeds. Any upload or manifest failure leaves all eligible chunks in TimescaleDB for the next run. Existing `S3_*` settings continue to work as lower-precedence aliases. This prevents future archive/retention races after upgrade; aggregate chunks already deleted by the old independent policy cannot be reconstructed unless another backup contains them.

---

## API surface

All routes under `/api/v1/*` except the public `GET /healthz` and `GET /auth/status`.

| Method & path | Auth | Purpose |
|---|---|---|
| `GET  /healthz` | none | liveness probe |
| `GET  /auth/status` | none | does an admin exist? |
| `POST /auth/setup` | none | one-time admin creation |
| `POST /auth/login` | none | session cookie |
| `POST /auth/logout` | cookie | drop session |
| `POST /ingest` | `X-Agent-Token` | agent push (gzipped JSON, batched) |
| `POST /admin/hosts` | cookie or `X-Admin-Token` | register a host, returns agent token |
| `DELETE /admin/hosts/{id}` | cookie / admin | unregister |
| `GET  /hosts`, `/hosts/{id}` | cookie / admin | list / detail |
| `GET  /hosts/{id}/processes` | cookie / admin | top-N process snapshot |
| `GET  /hosts/{id}/containers` | cookie / admin | container snapshot |
| `POST /hosts/{id}/backups/browse` | cookie / admin | queue a snapshot directory listing |
| `GET  /hosts/{id}/backups/browse/{jobID}` | cookie / admin | poll a snapshot directory listing |
| `GET  /hosts/{id}/labels?key=device` | cookie / admin | distinct label values |
| `GET  /series`, `/series/multi`, `/series/group` | cookie / admin | time-series query (single / per-label / bundled metrics) |
| `GET  /metrics` | cookie / admin | canonical metric registry |
| `GET  /stream` | cookie / admin | Server-Sent Events live feed |
| `GET  /stats` | cookie / admin | batcher queue depth, dropped count |
| `GET/POST/PUT/DELETE /alerts[/{id}]` | cookie / admin | alert rule CRUD |
| `GET  /alerts/history` | cookie / admin | recent fires; `format=csv` exports matching history |
| `DELETE /alerts/history` | cookie / admin | clear resolved history, optionally with `older_than_days` |
| `GET/POST/PUT/DELETE /channels[/{id}]` | cookie / admin | notification channel CRUD |
| `GET/POST /backup-repositories`, mutations under `/backup-repositories/{id}` | cookie / admin | repository namespaces, assignments, quotas, revocation, and write-only scoped credential replacement |
| `GET/POST /backup-destinations`, mutations under `/backup-destinations/{id}` | cookie / admin | centrally managed direct S3/R2 storage backends; responses expose only whether credentials exist |
| `GET /agent/backup-config` | `X-Agent-Token` | calling host's direct repositories in an agent-token-bound AES-GCM envelope; never plaintext secret JSON |
| `GET  /agent/ipban` | `X-Agent-Token` | effective ban policy, allowlist, fleet blocklist and pending unbans for the calling agent |
| `GET  /ipban/settings` | cookie | fleet-wide ban policy and allowlist |
| `PUT  /ipban/settings` | cookie / admin | update fleet-wide ban policy and allowlist (bumps the policy version every agent re-fetches) |
| `GET  /ipban/summary`, `/ipban/hosts`, `/ipban/active`, `/ipban/fleet`, `/ipban/events`, `/ipban/stats` | cookie | fleet overview, per-host state, tracked bans, fleet blocklist, paged/CSV ban history, and retention-clipped statistics (`/ipban/events?format=csv` is gzip encoded) |
| `PUT  /ipban/hosts/{id}` | cookie / admin | per-host detect / enforce / share / apply-fleet overrides |
| `POST /ipban/fleet`, `DELETE /ipban/fleet/{ip}`, `POST /ipban/unban` | cookie / admin | manual fleet ban, lift a fleet ban, unban on one host |

---

## Run from source (no Docker)

You need Go 1.26+, Node 22+, and a Postgres with TimescaleDB extension reachable on `localhost:5432`.

```powershell
$env:DATABASE_URL = "postgres://servermonitor:dev@localhost:5432/servermonitor?sslmode=disable"
$env:ADMIN_TOKEN  = "dev-admin-token"

cd web ; npm install ; npm run build ; cd ..
go run ./scripts/buildagents
go build -o bin/server.exe ./cmd/server
go build -o bin/agent.exe  ./cmd/agent

.\bin\server.exe
```

Then in another shell, register and run a local agent:
```powershell
$env:SM_ADMIN_TOKEN = "dev-admin-token"
.\bin\agent.exe register --server http://localhost:8080 --insecure
.\bin\agent.exe
```

For UI development without rebuilding the server, run `npm run dev` in `web/` — it proxies `/api/*` to `localhost:8080`.

---

## Project layout

```
ServerMonitor/
  cmd/
    server/                  server entrypoint
    agent/                   agent entrypoint (also has the register subcommand)
  internal/
    server/
      api/                   chi router + handlers + middleware
      ingest/                channel-fed batcher → pgx.CopyFrom
      storage/               pgxpool + embedded migrations + host cache
      sse/                   live-stream hub
      auth/                  bcrypt + sessions
      alerting/              rule evaluator
      notify/                SMTP + webhook senders
      archive/               Parquet writer/reader + S3 client
      tasks/                 nightly archive scheduler
      web/                   embedded SvelteKit dist
      config/                env loader
    agent/
      collectors/            one file per metric family
      config/                TOML loader
      transport/             HTTPS push client + spool drainer
      spool/                 BoltDB disk spool for offline buffering
      runner/                tick loop
      install/               install helpers
  pkg/
    wire/                    cross-binary payload types
    metrics/                 canonical metric IDs + units
  web/                       SvelteKit 2 + Svelte 5 + Tailwind v4 + uPlot
  deploy/                    Dockerfile + docker-compose + .env.example
  scripts/                   Linux + Windows agent install + uninstall scripts
  migrations/                see internal/server/storage/migrations/
```

---

## Hardening notes

- **CSP, X-Frame-Options DENY, X-Content-Type-Options nosniff, Referrer-Policy no-referrer** on every response.
- **Auth rate limit** of 5/min/IP on `/auth/login` and `/auth/setup`.
- **Audit log** for every mutating `/api/v1/*` request — actor, method, path, IP, status. Stored in the `audit_log` table.
- **Per-token ingest rate limit** at 10 batches/s (burst 30) per agent token.
- **Session cookies** are `Secure` when served over HTTPS, always `HttpOnly`, `SameSite=Lax`.
- **Agent tokens** are stored as SHA-256 hashes; the raw token is shown once at registration.
- **IP banning guardrails**: agents never ban loopback, link-local, multicast, their own addresses, the server's address, or anything on the allowlist, regardless of what the server sends; private ranges are only bannable when explicitly enabled and never propagate fleet-wide; enforcement cannot be switched on with an empty allowlist; every manual ban/unban is recorded with its actor in `ipban_events` on top of the audit log.

---

## Status

End-to-end functional: agent → server → Postgres → UI → SSE → alerts → S3 archive. All six implementation milestones (thin slice, auth + full collector set, host detail tabs, alerting, archive, hardening) are merged into the codebase.
