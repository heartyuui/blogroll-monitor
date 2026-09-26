package state

import (
	"testing"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/model"
)

func TestFailureThresholdTracksFirstFailureAndRecovery(t *testing.T) {
	base := time.Date(2026, 9, 20, 1, 0, 0, 0, time.UTC)
	link := model.Link{ID: "friend-1", MonitorRevision: 1, CurrentStatus: model.StatusUp}
	thresholds := Thresholds{Successes: 2, Failures: 3}
	for index := 0; index < 3; index++ {
		result := model.CheckResult{CheckedAt: base.Add(time.Duration(index) * time.Minute), Outcome: model.OutcomeHardFailure, ErrorCode: "HTTP_5XX"}
		event, changed, err := Apply(link, result, thresholds, eventID(index))
		if err != nil {
			t.Fatal(err)
		}
		if index < 2 && (event.CurrentStatus != model.StatusUp || changed) {
			t.Fatalf("failure %d changed status early: %#v", index+1, event)
		}
		link = applyEvent(link, event)
	}
	if link.CurrentStatus != model.StatusDown || link.OfflineSince == nil || !link.OfflineSince.Equal(base) {
		t.Fatalf("down transition did not retain first failure: %#v", link)
	}
	for index := 0; index < 2; index++ {
		result := model.CheckResult{CheckedAt: base.Add(time.Duration(3+index) * time.Minute), Outcome: model.OutcomeSuccess}
		event, _, err := Apply(link, result, thresholds, eventID(3+index))
		if err != nil {
			t.Fatal(err)
		}
		link = applyEvent(link, event)
	}
	if link.CurrentStatus != model.StatusUp || link.OfflineSince != nil {
		t.Fatalf("link did not recover after threshold: %#v", link)
	}
}

func TestDegradedAndUnknownNeverAccumulateOfflineTime(t *testing.T) {
	checkedAt := time.Now().UTC()
	for _, outcome := range []model.Outcome{model.OutcomeDegraded, model.OutcomeUnknown} {
		started := checkedAt.Add(-time.Hour)
		link := model.Link{
			ID: "friend-1", MonitorRevision: 1, CurrentStatus: model.StatusDown,
			OfflineSince: &started, FailureStreakStartedAt: &started, ConsecutiveFailures: 4,
		}
		event, _, err := Apply(link, model.CheckResult{CheckedAt: checkedAt, Outcome: outcome}, Thresholds{Successes: 2, Failures: 3}, eventID(9))
		if err != nil {
			t.Fatal(err)
		}
		if event.OfflineSince != nil || event.CurrentStatus == model.StatusDown || event.ConsecutiveFailures != 0 {
			t.Fatalf("outcome %s retained offline state: %#v", outcome, event)
		}
	}
}

func applyEvent(link model.Link, event model.StatusEvent) model.Link {
	link.CurrentStatus = event.CurrentStatus
	link.LastSuccessAt = event.LastSuccessAt
	link.LastFailureAt = event.LastFailureAt
	link.FailureStreakStartedAt = event.FailureStreakStartedAt
	link.OfflineSince = event.OfflineSince
	link.ConsecutiveFailures = event.ConsecutiveFailures
	link.ConsecutiveSuccesses = event.ConsecutiveSuccesses
	return link
}

func eventID(index int) string {
	return []string{
		"00000000-0000-4000-8000-000000000000",
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000004",
		"00000000-0000-4000-8000-000000000005",
		"00000000-0000-4000-8000-000000000006",
		"00000000-0000-4000-8000-000000000007",
		"00000000-0000-4000-8000-000000000008",
		"00000000-0000-4000-8000-000000000009",
	}[index]
}
