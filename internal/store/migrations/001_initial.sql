CREATE TABLE links (
    id TEXT PRIMARY KEY,
    url TEXT NOT NULL,
    host TEXT NOT NULL,
    registrable_domain TEXT NOT NULL,
    monitor_revision INTEGER NOT NULL,
    enabled INTEGER NOT NULL,
    last_sync_id TEXT NOT NULL,
    current_status TEXT NOT NULL DEFAULT 'UNKNOWN',
    last_checked_at TEXT,
    last_success_at TEXT,
    last_failure_at TEXT,
    failure_streak_started_at TEXT,
    offline_since TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    consecutive_successes INTEGER NOT NULL DEFAULT 0,
    next_check_at TEXT NOT NULL,
    lease_until TEXT,
    updated_at TEXT NOT NULL
);

CREATE INDEX links_due_idx ON links(enabled, next_check_at, lease_until);
CREATE INDEX links_domain_idx ON links(registrable_domain, enabled);

CREATE TABLE check_results (
    event_id TEXT PRIMARY KEY,
    friend_link_id TEXT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    monitor_revision INTEGER NOT NULL,
    checked_at TEXT NOT NULL,
    outcome TEXT NOT NULL,
    latency_ms INTEGER,
    http_status INTEGER,
    timed_out INTEGER NOT NULL,
    error_code TEXT,
    connected_ip TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX check_results_link_checked_idx ON check_results(friend_link_id, checked_at);
CREATE INDEX check_results_checked_idx ON check_results(checked_at);

CREATE TABLE resolved_ips (
    event_id TEXT NOT NULL REFERENCES check_results(event_id) ON DELETE CASCADE,
    hop INTEGER NOT NULL,
    hostname TEXT NOT NULL,
    address TEXT NOT NULL,
    family INTEGER NOT NULL,
    selected INTEGER NOT NULL,
    PRIMARY KEY(event_id, hop, address)
);

CREATE TABLE status_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    friend_link_id TEXT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    previous_status TEXT NOT NULL,
    current_status TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    offline_since TEXT
);

CREATE INDEX status_events_link_occurred_idx ON status_events(friend_link_id, occurred_at);

CREATE TABLE status_outbox (
    event_id TEXT PRIMARY KEY,
    payload BLOB NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    last_error TEXT,
    created_at TEXT NOT NULL,
    delivered_at TEXT
);

CREATE INDEX status_outbox_pending_idx ON status_outbox(delivered_at, next_attempt_at);

CREATE TABLE hourly_rollups (
    friend_link_id TEXT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    bucket_start TEXT NOT NULL,
    total_count INTEGER NOT NULL,
    success_count INTEGER NOT NULL,
    degraded_count INTEGER NOT NULL,
    failure_count INTEGER NOT NULL,
    unknown_count INTEGER NOT NULL,
    latency_sum_ms INTEGER NOT NULL,
    latency_count INTEGER NOT NULL,
    PRIMARY KEY(friend_link_id, bucket_start)
);

CREATE TABLE daily_rollups (
    friend_link_id TEXT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    bucket_start TEXT NOT NULL,
    total_count INTEGER NOT NULL,
    success_count INTEGER NOT NULL,
    degraded_count INTEGER NOT NULL,
    failure_count INTEGER NOT NULL,
    unknown_count INTEGER NOT NULL,
    latency_sum_ms INTEGER NOT NULL,
    latency_count INTEGER NOT NULL,
    PRIMARY KEY(friend_link_id, bucket_start)
);

CREATE TABLE sync_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
