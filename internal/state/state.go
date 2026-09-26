package state

import (
	"fmt"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/model"
)

type Thresholds struct {
	Successes int
	Failures  int
}

func Apply(link model.Link, result model.CheckResult, thresholds Thresholds, eventID string) (model.StatusEvent, bool, error) {
	if thresholds.Successes < 1 || thresholds.Failures < 1 {
		return model.StatusEvent{}, false, fmt.Errorf("state thresholds must be positive")
	}
	if result.CheckedAt.IsZero() || eventID == "" {
		return model.StatusEvent{}, false, fmt.Errorf("check result is incomplete")
	}

	previousStatus := link.CurrentStatus
	if previousStatus == "" {
		previousStatus = model.StatusUnknown
	}
	currentStatus := previousStatus
	lastSuccessAt := link.LastSuccessAt
	lastFailureAt := link.LastFailureAt
	failureStartedAt := link.FailureStreakStartedAt
	offlineSince := link.OfflineSince
	consecutiveFailures := link.ConsecutiveFailures
	consecutiveSuccesses := link.ConsecutiveSuccesses

	switch result.Outcome {
	case model.OutcomeSuccess:
		timestamp := result.CheckedAt
		lastSuccessAt = &timestamp
		failureStartedAt = nil
		consecutiveFailures = 0
		consecutiveSuccesses++
		if consecutiveSuccesses >= thresholds.Successes {
			currentStatus = model.StatusUp
			offlineSince = nil
		}
	case model.OutcomeDegraded:
		currentStatus = model.StatusDegraded
		failureStartedAt = nil
		offlineSince = nil
		consecutiveFailures = 0
		consecutiveSuccesses = 0
	case model.OutcomeHardFailure:
		timestamp := result.CheckedAt
		lastFailureAt = &timestamp
		consecutiveSuccesses = 0
		consecutiveFailures++
		if consecutiveFailures == 1 || failureStartedAt == nil {
			failureStartedAt = &timestamp
		}
		if consecutiveFailures >= thresholds.Failures {
			currentStatus = model.StatusDown
			if offlineSince == nil {
				offlineSince = cloneTime(failureStartedAt)
			}
		}
	case model.OutcomeUnknown:
		currentStatus = model.StatusUnknown
		failureStartedAt = nil
		offlineSince = nil
		consecutiveFailures = 0
		consecutiveSuccesses = 0
	default:
		return model.StatusEvent{}, false, fmt.Errorf("unsupported check outcome %q", result.Outcome)
	}

	var errorCode *string
	if result.ErrorCode != "" {
		value := result.ErrorCode
		errorCode = &value
	}
	event := model.StatusEvent{
		EventID:                eventID,
		FriendLinkID:           link.ID,
		MonitorRevision:        link.MonitorRevision,
		CheckedAt:              result.CheckedAt.UTC(),
		CurrentStatus:          currentStatus,
		LastSuccessAt:          utcTime(lastSuccessAt),
		LastFailureAt:          utcTime(lastFailureAt),
		FailureStreakStartedAt: utcTime(failureStartedAt),
		OfflineSince:           utcTime(offlineSince),
		ConsecutiveFailures:    consecutiveFailures,
		ConsecutiveSuccesses:   consecutiveSuccesses,
		LatencyMs:              result.LatencyMs,
		HTTPStatus:             result.HTTPStatus,
		TimedOut:               result.TimedOut,
		ErrorCode:              errorCode,
	}
	return event, previousStatus != currentStatus, nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
