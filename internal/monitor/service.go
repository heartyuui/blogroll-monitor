package monitor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/checker"
	"github.com/heartyuui/blogroll-monitor/internal/config"
	"github.com/heartyuui/blogroll-monitor/internal/model"
	"github.com/heartyuui/blogroll-monitor/internal/state"
	"github.com/heartyuui/blogroll-monitor/internal/store"
	"github.com/heartyuui/blogroll-monitor/internal/syncclient"
)

type Service struct {
	config  config.Config
	store   *store.Store
	checker *checker.Checker
	client  *syncclient.Client
	logger  *slog.Logger
	ready   atomic.Bool
	domains domainLimiter
}

func New(configuration config.Config, database *store.Store, targetChecker *checker.Checker, client *syncclient.Client, logger *slog.Logger) *Service {
	return &Service{
		config:  configuration,
		store:   database,
		checker: targetChecker,
		client:  client,
		logger:  logger,
		domains: domainLimiter{limit: configuration.PerDomainConcurrency, semaphores: make(map[string]chan struct{})},
	}
}

func (s *Service) Ready() bool { return s.ready.Load() }

func (s *Service) Run(ctx context.Context) {
	jobs := make(chan model.Link, s.config.GlobalConcurrency*2)
	var workers sync.WaitGroup
	for index := 0; index < s.config.GlobalConcurrency; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.worker(ctx, jobs)
		}()
	}

	var loops sync.WaitGroup
	loops.Add(4)
	go func() { defer loops.Done(); s.syncLoop(ctx) }()
	go func() { defer loops.Done(); s.schedulerLoop(ctx, jobs) }()
	go func() { defer loops.Done(); s.outboxLoop(ctx) }()
	go func() { defer loops.Done(); s.cleanupLoop(ctx) }()
	loops.Wait()
	close(jobs)
	workers.Wait()
}

func (s *Service) syncLoop(ctx context.Context) {
	s.runCatalogSync(ctx)
	ticker := time.NewTicker(s.config.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCatalogSync(ctx)
		}
	}
}

func (s *Service) runCatalogSync(ctx context.Context) {
	cursor := ""
	syncID := ""
	itemCount := 0
	for pageNumber := 0; pageNumber < 10000; pageNumber++ {
		page, err := s.client.Catalog(ctx, cursor)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Error("catalog sync failed", "error", err)
			}
			return
		}
		if syncID == "" {
			syncID = page.SyncID
		} else if page.SyncID != syncID {
			s.logger.Error("catalog sync changed identity between pages")
			return
		}
		if err := s.store.ApplyCatalogPage(ctx, syncID, page.Items, time.Now().UTC(), s.config.CheckInterval); err != nil {
			s.logger.Error("catalog persistence failed", "error", err)
			return
		}
		itemCount += len(page.Items)
		if page.Complete {
			if err := s.store.CompleteCatalogSync(ctx, syncID, time.Now().UTC()); err != nil {
				s.logger.Error("catalog reconciliation failed", "error", err)
				return
			}
			s.ready.Store(true)
			s.logger.Info("catalog sync completed", "items", itemCount)
			return
		}
		if page.NextCursor == nil || *page.NextCursor == cursor {
			s.logger.Error("catalog sync returned an invalid cursor")
			return
		}
		cursor = *page.NextCursor
	}
	s.logger.Error("catalog sync exceeded page limit")
}

func (s *Service) schedulerLoop(ctx context.Context, jobs chan<- model.Link) {
	ticker := time.NewTicker(s.config.SchedulerPollInterval)
	defer ticker.Stop()
	for {
		if err := s.enqueueDue(ctx, jobs); err != nil && ctx.Err() == nil {
			s.logger.Error("load due checks failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) enqueueDue(ctx context.Context, jobs chan<- model.Link) error {
	links, err := s.store.DueLinks(ctx, time.Now().UTC(), s.config.GlobalConcurrency, s.config.TotalTimeout+time.Minute)
	if err != nil {
		return err
	}
	for _, link := range links {
		select {
		case <-ctx.Done():
			_ = s.store.ReleaseLease(context.Background(), link.ID)
			return ctx.Err()
		case jobs <- link:
		}
	}
	return nil
}

func (s *Service) worker(ctx context.Context, jobs <-chan model.Link) {
	for link := range jobs {
		if err := s.domains.acquire(ctx, link.RegistrableDomain); err != nil {
			_ = s.store.ReleaseLease(context.Background(), link.ID)
			return
		}
		result := s.checker.Check(ctx, link.URL)
		s.domains.release(link.RegistrableDomain)
		eventID, err := newEventID()
		if err != nil {
			s.logger.Error("event id generation failed", "friendLinkId", link.ID, "error", err)
			_ = s.store.ReleaseLease(context.Background(), link.ID)
			continue
		}
		event, changed, err := state.Apply(link, result, state.Thresholds{
			Successes: s.config.SuccessThreshold,
			Failures:  s.config.FailureThreshold,
		}, eventID)
		if err != nil {
			s.logger.Error("state transition failed", "friendLinkId", link.ID, "error", err)
			_ = s.store.ReleaseLease(context.Background(), link.ID)
			continue
		}
		nextCheck := jitteredNextCheck(result.CheckedAt, s.config.CheckInterval, eventID)
		if err := s.store.CompleteCheck(ctx, link, result, event, changed, nextCheck); err != nil {
			if !errors.Is(err, store.ErrStaleLink) && ctx.Err() == nil {
				s.logger.Error("persist check failed", "friendLinkId", link.ID, "error", err)
			}
			_ = s.store.ReleaseLease(context.Background(), link.ID)
			continue
		}
		s.logger.Info("friend link checked", "friendLinkId", link.ID, "outcome", result.Outcome, "status", event.CurrentStatus, "latencyMs", event.LatencyMs)
	}
}

func (s *Service) outboxLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		s.flushOutbox(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) flushOutbox(ctx context.Context) {
	records, err := s.store.PendingOutbox(ctx, time.Now().UTC(), 100)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("load outbox failed", "error", err)
		}
		return
	}
	if len(records) == 0 {
		return
	}
	events := make([]model.StatusEvent, 0, len(records))
	validRecords := make([]model.OutboxRecord, 0, len(records))
	for _, record := range records {
		var event model.StatusEvent
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			s.logger.Error("outbox payload is invalid", "eventId", record.EventID)
			_ = s.store.MarkOutboxDelivered(ctx, []string{record.EventID}, time.Now().UTC())
			continue
		}
		events = append(events, event)
		validRecords = append(validRecords, record)
	}
	if len(events) == 0 {
		return
	}
	results, err := s.client.SendStatus(ctx, events)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("status batch delivery failed", "events", len(events), "error", err)
			_ = s.store.MarkOutboxRetry(ctx, validRecords, time.Now().UTC(), "DELIVERY_FAILED")
		}
		return
	}
	expected := make(map[string]struct{}, len(validRecords))
	for _, record := range validRecords {
		expected[record.EventID] = struct{}{}
	}
	delivered := make([]string, 0, len(results))
	for _, result := range results {
		if _, ok := expected[result.EventID]; ok {
			delivered = append(delivered, result.EventID)
			delete(expected, result.EventID)
		}
	}
	if len(delivered) > 0 {
		if err := s.store.MarkOutboxDelivered(ctx, delivered, time.Now().UTC()); err != nil {
			s.logger.Error("mark outbox delivered failed", "error", err)
			return
		}
	}
	if len(expected) > 0 {
		missing := make([]model.OutboxRecord, 0, len(expected))
		for _, record := range validRecords {
			if _, ok := expected[record.EventID]; ok {
				missing = append(missing, record)
			}
		}
		_ = s.store.MarkOutboxRetry(ctx, missing, time.Now().UTC(), "INCOMPLETE_RESPONSE")
	}
}

func (s *Service) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.CleanupAndRollup(ctx, time.Now().UTC(), s.config.RawRetention, s.config.HourlyRetention, s.config.DailyRetention); err != nil && ctx.Err() == nil {
				s.logger.Error("retention cleanup failed", "error", err)
			}
		}
	}
}

func jitteredNextCheck(checkedAt time.Time, interval time.Duration, seed string) time.Time {
	if interval <= 0 {
		return checkedAt.UTC()
	}
	digest := sha256.Sum256([]byte(seed))
	window := interval / 5
	if window <= 0 {
		return checkedAt.UTC().Add(interval)
	}
	offset := time.Duration(binary.BigEndian.Uint64(digest[:8])%uint64(window)) - window/2
	return checkedAt.UTC().Add(interval + offset)
}

func newEventID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

type domainLimiter struct {
	limit      int
	mutex      sync.Mutex
	semaphores map[string]chan struct{}
}

func (l *domainLimiter) acquire(ctx context.Context, domain string) error {
	l.mutex.Lock()
	semaphore := l.semaphores[domain]
	if semaphore == nil {
		semaphore = make(chan struct{}, l.limit)
		l.semaphores[domain] = semaphore
	}
	l.mutex.Unlock()
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *domainLimiter) release(domain string) {
	l.mutex.Lock()
	semaphore := l.semaphores[domain]
	l.mutex.Unlock()
	if semaphore != nil {
		<-semaphore
	}
}
