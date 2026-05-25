CREATE INDEX IF NOT EXISTS processes_host_pid_time_idx ON processes (host_id, pid, time DESC);
