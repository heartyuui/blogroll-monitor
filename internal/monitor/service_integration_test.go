package monitor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/checker"
	"github.com/heartyuui/blogroll-monitor/internal/config"
	"github.com/heartyuui/blogroll-monitor/internal/model"
	"github.com/heartyuui/blogroll-monitor/internal/signing"
	"github.com/heartyuui/blogroll-monitor/internal/store"
	"github.com/heartyuui/blogroll-monitor/internal/syncclient"
)

func TestServiceSyncsChecksPersistsAndReturnsStatus(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("controlled target"))
	}))
	defer target.Close()
	received := make(chan model.StatusEvent, 1)
	blog := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/internal/friend-link-monitor/catalog":
			_ = json.NewEncoder(response).Encode(syncclient.CatalogPage{
				SchemaVersion: 1, SyncID: "integration-sync", Complete: true,
				Items: []model.CatalogItem{{ID: "friend-1", URL: target.URL, MonitorRevision: 1, MonitorEnabled: true}},
			})
		case "/internal/friend-link-monitor/status-batch":
			var payload struct {
				Events []model.StatusEvent `json:"events"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload.Events) == 0 {
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case received <- payload.Events[0]:
			default:
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"schemaVersion": 1,
				"results":       []map[string]any{{"eventId": payload.Events[0].EventID, "accepted": true}},
			})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer blog.Close()

	ctx, cancel := context.WithCancel(context.Background())
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	configuration := config.Config{
		SyncInterval: 200 * time.Millisecond, CheckInterval: 100 * time.Millisecond,
		SchedulerPollInterval: 10 * time.Millisecond, GlobalConcurrency: 2, PerDomainConcurrency: 1,
		SuccessThreshold: 1, FailureThreshold: 3, TotalTimeout: time.Second,
		RawRetention: 7 * 24 * time.Hour, HourlyRetention: 180 * 24 * time.Hour, DailyRetention: 730 * 24 * time.Hour,
	}
	targetChecker := checker.New(checker.Options{
		Policy:         checker.Policy{LookupTimeout: time.Second, AllowTestLoopback: true},
		ConnectTimeout: time.Second, TLSHandshakeTimeout: time.Second,
		ResponseHeaderTimeout: time.Second, ReadTimeout: time.Second, TotalTimeout: time.Second,
		MaxResponseBytes: 1024, MaxRedirects: 2, UserAgent: "integration-test",
	})
	defer targetChecker.Close()
	client, err := syncclient.New(blog.URL, "integration-node", signing.Signer{
		KeyID: "integration", Secret: []byte("01234567890123456789012345678901"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	service := New(configuration, database, targetChecker, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()

	select {
	case event := <-received:
		if event.CurrentStatus != model.StatusUp || event.FriendLinkID != "friend-1" {
			t.Fatalf("unexpected status event: %#v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("monitor cycle did not return a status event")
	}
	if !service.Ready() {
		t.Fatal("service did not become ready after catalog sync")
	}
	links, checks, err := database.Counts(context.Background())
	if err != nil || links != 1 || checks < 1 {
		t.Fatalf("unexpected database counts links=%d checks=%d err=%v", links, checks, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("service did not stop gracefully")
	}
}
