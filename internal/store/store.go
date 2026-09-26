package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/model"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
	_ "modernc.org/sqlite"
)

var ErrStaleLink = errors.New("friend link revision changed while check was running")

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}
	directory := filepath.Dir(path)
	if directory != "." {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	store := &Store{db: database}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := database.ExecContext(ctx, pragma); err != nil {
			_ = database.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if err := store.migrate(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) ApplyCatalogPage(ctx context.Context, syncID string, items []model.CatalogItem, now time.Time, interval time.Duration) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, item := range items {
		host, domain, err := catalogIdentity(item.URL)
		if err != nil {
			return fmt.Errorf("catalog item %s is invalid: %w", item.ID, err)
		}
		var existingURL string
		var existingRevision int
		var existingEnabled int
		err = transaction.QueryRowContext(ctx, "SELECT url, monitor_revision, enabled FROM links WHERE id = ?", item.ID).Scan(&existingURL, &existingRevision, &existingEnabled)
		nextCheck := spreadSchedule(now, interval, item.ID+syncID)
		switch {
		case err == sql.ErrNoRows:
			_, err = transaction.ExecContext(ctx, `
				INSERT INTO links (
					id, url, host, registrable_domain, monitor_revision, enabled,
					last_sync_id, current_status, next_check_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, 'UNKNOWN', ?, ?)
			`, item.ID, item.URL, host, domain, item.MonitorRevision, boolInt(item.MonitorEnabled), syncID, formatTime(nextCheck), formatTime(now))
		case err != nil:
			return err
		case existingURL != item.URL || existingRevision != item.MonitorRevision || (existingEnabled == 0 && item.MonitorEnabled):
			_, err = transaction.ExecContext(ctx, `
				UPDATE links SET
					url = ?, host = ?, registrable_domain = ?, monitor_revision = ?, enabled = ?,
					last_sync_id = ?, current_status = 'UNKNOWN', last_checked_at = NULL,
					last_success_at = NULL, last_failure_at = NULL,
					failure_streak_started_at = NULL, offline_since = NULL,
					consecutive_failures = 0, consecutive_successes = 0,
					next_check_at = ?, lease_until = NULL, updated_at = ?
				WHERE id = ?
			`, item.URL, host, domain, item.MonitorRevision, boolInt(item.MonitorEnabled), syncID, formatTime(nextCheck), formatTime(now), item.ID)
		default:
			_, err = transaction.ExecContext(ctx, `
				UPDATE links SET enabled = ?, last_sync_id = ?, lease_until = CASE WHEN ? = 0 THEN NULL ELSE lease_until END, updated_at = ? WHERE id = ?
			`, boolInt(item.MonitorEnabled), syncID, boolInt(item.MonitorEnabled), formatTime(now), item.ID)
		}
		if err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (s *Store) CompleteCatalogSync(ctx context.Context, syncID string, now time.Time) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "UPDATE links SET enabled = 0, lease_until = NULL, updated_at = ? WHERE last_sync_id <> ?", formatTime(now), syncID); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO sync_state(key, value, updated_at) VALUES('catalog_sync_id', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, syncID, formatTime(now)); err != nil {
		return err
	}
	return transaction.Commit()
}

func (s *Store) DueLinks(ctx context.Context, now time.Time, limit int, leaseDuration time.Duration) ([]model.Link, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, url, host, registrable_domain, monitor_revision, enabled,
			current_status, last_checked_at, last_success_at, last_failure_at,
			failure_streak_started_at, offline_since,
			consecutive_failures, consecutive_successes
		FROM links
		WHERE enabled = 1 AND next_check_at <= ? AND (lease_until IS NULL OR lease_until <= ?)
		ORDER BY next_check_at, id
		LIMIT ?
	`, formatTime(now), formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	links := make([]model.Link, 0, limit)
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	leaseUntil := formatTime(now.Add(leaseDuration))
	for _, link := range links {
		if _, err := transaction.ExecContext(ctx, "UPDATE links SET lease_until = ? WHERE id = ?", leaseUntil, link.ID); err != nil {
			return nil, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return links, nil
}

func (s *Store) CompleteCheck(ctx context.Context, link model.Link, result model.CheckResult, event model.StatusEvent, statusChanged bool, nextCheck time.Time) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode status event: %w", err)
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	updateResult, err := transaction.ExecContext(ctx, `
		INSERT INTO check_results (
			event_id, friend_link_id, monitor_revision, checked_at, outcome,
			latency_ms, http_status, timed_out, error_code, connected_ip, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.EventID, link.ID, link.MonitorRevision, formatTime(result.CheckedAt), result.Outcome, nullableInt(result.LatencyMs), nullableInt(result.HTTPStatus), boolInt(result.TimedOut), nullableString(result.ErrorCode), nullableString(result.ConnectedIP), formatTime(time.Now()))
	if err != nil {
		return err
	}
	for _, address := range result.ResolvedIPs {
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO resolved_ips(event_id, hop, hostname, address, family, selected) VALUES(?, ?, ?, ?, ?, ?)
		`, event.EventID, address.Hop, address.Hostname, address.Address, address.Family, boolInt(address.Selected)); err != nil {
			return err
		}
	}
	if statusChanged {
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO status_events(event_id, friend_link_id, previous_status, current_status, occurred_at, offline_since)
			VALUES(?, ?, ?, ?, ?, ?)
		`, event.EventID, link.ID, link.CurrentStatus, event.CurrentStatus, formatTime(event.CheckedAt), nullableTime(event.OfflineSince)); err != nil {
			return err
		}
	}
	_, err = transaction.ExecContext(ctx, `
		UPDATE links SET
			current_status = ?, last_checked_at = ?, last_success_at = ?, last_failure_at = ?,
			failure_streak_started_at = ?, offline_since = ?, consecutive_failures = ?,
			consecutive_successes = ?, next_check_at = ?, lease_until = NULL, updated_at = ?
		WHERE id = ? AND monitor_revision = ?
	`, event.CurrentStatus, formatTime(event.CheckedAt), nullableTime(event.LastSuccessAt), nullableTime(event.LastFailureAt), nullableTime(event.FailureStreakStartedAt), nullableTime(event.OfflineSince), event.ConsecutiveFailures, event.ConsecutiveSuccesses, formatTime(nextCheck), formatTime(time.Now()), link.ID, link.MonitorRevision)
	if err != nil {
		return err
	}
	updated, err := updateResult.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrStaleLink
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO status_outbox(event_id, payload, next_attempt_at, created_at) VALUES(?, ?, ?, ?)
	`, event.EventID, payload, formatTime(time.Now()), formatTime(time.Now())); err != nil {
		return err
	}
	return transaction.Commit()
}

func (s *Store) ReleaseLease(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE links SET lease_until = NULL WHERE id = ?", id)
	return err
}

func (s *Store) PendingOutbox(ctx context.Context, now time.Time, limit int) ([]model.OutboxRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, payload, attempt_count FROM status_outbox
		WHERE delivered_at IS NULL AND next_attempt_at <= ?
		ORDER BY next_attempt_at, created_at
		LIMIT ?
	`, formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]model.OutboxRecord, 0, limit)
	for rows.Next() {
		var record model.OutboxRecord
		if err := rows.Scan(&record.EventID, &record.Payload, &record.AttemptCount); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) MarkOutboxDelivered(ctx context.Context, eventIDs []string, now time.Time) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, eventID := range eventIDs {
		if _, err := transaction.ExecContext(ctx, "UPDATE status_outbox SET delivered_at = ?, last_error = NULL WHERE event_id = ?", formatTime(now), eventID); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (s *Store) MarkOutboxRetry(ctx context.Context, records []model.OutboxRecord, now time.Time, errorCode string) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, record := range records {
		attempt := record.AttemptCount + 1
		delay := time.Duration(1<<min(attempt, 10)) * time.Second
		if delay > 15*time.Minute {
			delay = 15 * time.Minute
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE status_outbox SET attempt_count = ?, next_attempt_at = ?, last_error = ? WHERE event_id = ?
		`, attempt, formatTime(now.Add(delay)), errorCode, record.EventID); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (s *Store) CleanupAndRollup(ctx context.Context, now time.Time, rawRetention, hourlyRetention, dailyRetention time.Duration) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for table, bucket := range map[string]string{
		"hourly_rollups": "%Y-%m-%dT%H:00:00Z",
		"daily_rollups":  "%Y-%m-%dT00:00:00Z",
	} {
		statement := fmt.Sprintf(`
			INSERT INTO %s (
				friend_link_id, bucket_start, total_count, success_count, degraded_count,
				failure_count, unknown_count, latency_sum_ms, latency_count
			)
			SELECT friend_link_id, strftime('%s', checked_at), COUNT(*),
				SUM(CASE WHEN outcome = 'SUCCESS' THEN 1 ELSE 0 END),
				SUM(CASE WHEN outcome = 'DEGRADED' THEN 1 ELSE 0 END),
				SUM(CASE WHEN outcome = 'HARD_FAILURE' THEN 1 ELSE 0 END),
				SUM(CASE WHEN outcome = 'UNKNOWN' THEN 1 ELSE 0 END),
				COALESCE(SUM(latency_ms), 0), COUNT(latency_ms)
			FROM check_results GROUP BY friend_link_id, strftime('%s', checked_at)
			ON CONFLICT(friend_link_id, bucket_start) DO UPDATE SET
				total_count = excluded.total_count, success_count = excluded.success_count,
				degraded_count = excluded.degraded_count, failure_count = excluded.failure_count,
				unknown_count = excluded.unknown_count, latency_sum_ms = excluded.latency_sum_ms,
				latency_count = excluded.latency_count
		`, table, bucket, bucket)
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM check_results WHERE checked_at < ?", formatTime(now.Add(-rawRetention))); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM hourly_rollups WHERE bucket_start < ?", formatTime(now.Add(-hourlyRetention))); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM daily_rollups WHERE bucket_start < ?", formatTime(now.Add(-dailyRetention))); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM status_events WHERE occurred_at < ?", formatTime(now.Add(-dailyRetention))); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM status_outbox WHERE delivered_at IS NOT NULL AND delivered_at < ?", formatTime(now.Add(-24*time.Hour))); err != nil {
		return err
	}
	return transaction.Commit()
}

func (s *Store) Counts(ctx context.Context) (links int, checks int, err error) {
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM links").Scan(&links); err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM check_results").Scan(&checks)
	return
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionText := strings.SplitN(entry.Name(), "_", 2)[0]
		version, err := strconv.Atoi(versionText)
		if err != nil {
			return fmt.Errorf("invalid migration filename %s", entry.Name())
		}
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		script, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		transaction, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, string(script)); err != nil {
			transaction.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := transaction.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", version, formatTime(time.Now())); err != nil {
			transaction.Rollback()
			return err
		}
		if err := transaction.Commit(); err != nil {
			return err
		}
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func scanLink(row scanner) (model.Link, error) {
	var link model.Link
	var enabled int
	var lastChecked, lastSuccess, lastFailure, failureStarted, offline sql.NullString
	if err := row.Scan(
		&link.ID, &link.URL, &link.Host, &link.RegistrableDomain, &link.MonitorRevision,
		&enabled, &link.CurrentStatus, &lastChecked, &lastSuccess, &lastFailure,
		&failureStarted, &offline, &link.ConsecutiveFailures, &link.ConsecutiveSuccesses,
	); err != nil {
		return model.Link{}, err
	}
	link.Enabled = enabled == 1
	var err error
	if link.LastCheckedAt, err = parseNullTime(lastChecked); err != nil {
		return model.Link{}, err
	}
	if link.LastSuccessAt, err = parseNullTime(lastSuccess); err != nil {
		return model.Link{}, err
	}
	if link.LastFailureAt, err = parseNullTime(lastFailure); err != nil {
		return model.Link{}, err
	}
	if link.FailureStreakStartedAt, err = parseNullTime(failureStarted); err != nil {
		return model.Link{}, err
	}
	if link.OfflineSince, err = parseNullTime(offline); err != nil {
		return model.Link{}, err
	}
	return link, nil
}

func catalogIdentity(rawURL string) (string, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return "", "", fmt.Errorf("invalid URL")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		return address.Unmap().String(), address.Unmap().String(), nil
	}
	host, err = idna.Lookup.ToASCII(host)
	if err != nil {
		return "", "", fmt.Errorf("invalid hostname")
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", "", fmt.Errorf("invalid registrable domain")
	}
	return host, domain, nil
}

func spreadSchedule(now time.Time, interval time.Duration, seed string) time.Time {
	if interval <= 0 {
		return now.UTC()
	}
	digest := sha256.Sum256([]byte(seed))
	offset := time.Duration(binary.BigEndian.Uint64(digest[:8]) % uint64(interval))
	return now.UTC().Add(offset)
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func parseNullTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
