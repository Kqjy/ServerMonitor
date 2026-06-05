CREATE INDEX IF NOT EXISTS metric_points_5m_host_metric_bucket_idx
    ON metric_points_5m (host_id, metric, bucket DESC);
