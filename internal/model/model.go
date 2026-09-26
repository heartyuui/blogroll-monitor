package model

import "time"

type Status string

const (
	StatusUp       Status = "UP"
	StatusDegraded Status = "DEGRADED"
	StatusDown     Status = "DOWN"
	StatusUnknown  Status = "UNKNOWN"
)

type Outcome string

const (
	OutcomeSuccess     Outcome = "SUCCESS"
	OutcomeDegraded    Outcome = "DEGRADED"
	OutcomeHardFailure Outcome = "HARD_FAILURE"
	OutcomeUnknown     Outcome = "UNKNOWN"
)

type CatalogItem struct {
	ID              string `json:"id"`
	URL             string `json:"url"`
	UpdatedAt       string `json:"updatedAt"`
	MonitorRevision int    `json:"monitorRevision"`
	MonitorEnabled  bool   `json:"monitorEnabled"`
}

type Link struct {
	ID                     string
	URL                    string
	Host                   string
	RegistrableDomain      string
	MonitorRevision        int
	Enabled                bool
	CurrentStatus          Status
	LastCheckedAt          *time.Time
	LastSuccessAt          *time.Time
	LastFailureAt          *time.Time
	FailureStreakStartedAt *time.Time
	OfflineSince           *time.Time
	ConsecutiveFailures    int
	ConsecutiveSuccesses   int
}

type ResolvedIP struct {
	Hop      int    `json:"hop"`
	Hostname string `json:"hostname"`
	Address  string `json:"address"`
	Family   int    `json:"family"`
	Selected bool   `json:"selected"`
}

type CheckResult struct {
	CheckedAt   time.Time
	Outcome     Outcome
	LatencyMs   *int
	HTTPStatus  *int
	TimedOut    bool
	ErrorCode   string
	ConnectedIP string
	ResolvedIPs []ResolvedIP
}

type StatusEvent struct {
	EventID                string     `json:"eventId"`
	FriendLinkID           string     `json:"friendLinkId"`
	MonitorRevision        int        `json:"monitorRevision"`
	CheckedAt              time.Time  `json:"checkedAt"`
	CurrentStatus          Status     `json:"currentStatus"`
	LastSuccessAt          *time.Time `json:"lastSuccessAt"`
	LastFailureAt          *time.Time `json:"lastFailureAt"`
	FailureStreakStartedAt *time.Time `json:"failureStreakStartedAt"`
	OfflineSince           *time.Time `json:"offlineSince"`
	ConsecutiveFailures    int        `json:"consecutiveFailures"`
	ConsecutiveSuccesses   int        `json:"consecutiveSuccesses"`
	LatencyMs              *int       `json:"latencyMs"`
	HTTPStatus             *int       `json:"httpStatus"`
	TimedOut               bool       `json:"timedOut"`
	ErrorCode              *string    `json:"errorCode"`
}

type OutboxRecord struct {
	EventID      string
	Payload      []byte
	AttemptCount int
}
