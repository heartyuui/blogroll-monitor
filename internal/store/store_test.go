package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/model"
)

func TestMigrationRestartSchedulingAndRetention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "monitor.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	item := model.CatalogItem{
		ID: "friend-1", URL: "https://example.com/", UpdatedAt: "2026-09-20T00:00:00Z",
		MonitorRevision: 1, MonitorEnabled: true,
	}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := database.ApplyCatalogPage(ctx, "sync-1", []model.CatalogItem{item}, now.Add(-2*time.Hour), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCatalogSync(ctx, "sync-1", now); err != nil {
		t.Fatal(err)
	}
	links, err := database.DueLinks(ctx, now, 10, time.Minute)
	if err != nil || len(links) != 1 {
		t.Fatalf("due links = %d, err = %v", len(links), err)
	}
	checkedAt := now.Add(-8 * 24 * time.Hour)
	latency := 125
	event := model.StatusEvent{
		EventID: "00000000-0000-4000-8000-000000000001", FriendLinkID: item.ID,
		MonitorRevision: 1, CheckedAt: checkedAt, CurrentStatus: model.StatusDown,
		LastFailureAt: &checkedAt, FailureStreakStartedAt: &checkedAt, OfflineSince: &checkedAt,
		ConsecutiveFailures: 3, LatencyMs: &latency, ErrorCode: stringPointer("HTTP_5XX"),
	}
	result := model.CheckResult{CheckedAt: checkedAt, Outcome: model.OutcomeHardFailure, LatencyMs: &latency, ErrorCode: "HTTP_5XX"}
	if err := database.CompleteCheck(ctx, links[0], result, event, true, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.CleanupAndRollup(ctx, now, 7*24*time.Hour, 180*24*time.Hour, 730*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var checks, statusEvents, pending, daily int
	for query, destination := range map[string]*int{
		"SELECT COUNT(*) FROM check_results":                            checksPointer(&checks),
		"SELECT COUNT(*) FROM status_events":                            checksPointer(&statusEvents),
		"SELECT COUNT(*) FROM status_outbox WHERE delivered_at IS NULL": checksPointer(&pending),
		"SELECT COUNT(*) FROM daily_rollups":                            checksPointer(&daily),
	} {
		if err := database.db.QueryRowContext(ctx, query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 0 || statusEvents != 1 || pending != 1 || daily != 1 {
		t.Fatalf("unexpected retention counts checks=%d status=%d pending=%d daily=%d", checks, statusEvents, pending, daily)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	linksCount, _, err := reopened.Counts(ctx)
	if err != nil || linksCount != 1 {
		t.Fatalf("restart lost catalog: count=%d err=%v", linksCount, err)
	}
}

func checksPointer(value *int) *int { return value }

func stringPointer(value string) *string { return &value }
