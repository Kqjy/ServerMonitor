# Backup & Restore

The `backup` service in `docker-compose.yml` runs `pg_dump` every 24h and writes gzipped SQL to the `backups` named volume. Retention is `BACKUP_RETENTION_DAYS` (default 14).

## List backups

```bash
docker compose -f deploy/docker-compose.yml exec backup ls -lh /backups
```

## Pull a backup to the host

```bash
docker compose -f deploy/docker-compose.yml cp backup:/backups/servermonitor-YYYYMMDDTHHMMSSZ.sql.gz ./
```

## Restore from a backup

Stop the app so no writes race the restore, then pipe the dump back into Postgres:

```bash
docker compose -f deploy/docker-compose.yml stop app
gunzip -c servermonitor-YYYYMMDDTHHMMSSZ.sql.gz | \
  docker compose -f deploy/docker-compose.yml exec -T postgres \
    psql -U "$POSTGRES_USER" -d servermonitor
docker compose -f deploy/docker-compose.yml start app
```

## Off-host copies

The named `backups` volume lives on the docker host. For real disaster recovery:

- Mount a bind volume instead: replace `- backups:/backups` with `- /mnt/nas/sm-backups:/backups` (or similar) and remove `backups:` from the `volumes:` block.
- Or copy the volume contents to S3 from cron on the host:
  ```bash
  docker run --rm -v deploy_backups:/src -v ~/.aws:/root/.aws \
    amazon/aws-cli s3 sync /src s3://your-bucket/sm-backups/
  ```

## What's NOT in the dump

`pg_dump` covers all tables and the schema. It does NOT cover:
- TimescaleDB compressed chunks older than the compression threshold are dumped as-is and will need re-compression after restore (Timescale handles this on first access; no manual step needed for 30d-of-raw deployments).
- Cold-tier Parquet archives on S3 — those live in the bucket configured via `ARCHIVE_S3_BUCKET` (`S3_BUCKET` remains a compatibility alias) and need a separate S3 lifecycle/replication policy.
