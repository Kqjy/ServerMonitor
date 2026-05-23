#!/bin/sh
set -eu
stamp=$(date -u +%Y%m%dT%H%M%SZ)
out="/backups/servermonitor-${stamp}.sql.gz"
pg_dump --format=plain --no-owner --no-privileges | gzip -9 > "${out}.tmp"
mv "${out}.tmp" "${out}"
find /backups -name 'servermonitor-*.sql.gz' -mtime +"${BACKUP_RETENTION_DAYS:-14}" -delete
echo "wrote ${out}"
