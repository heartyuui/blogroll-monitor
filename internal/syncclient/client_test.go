package syncclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/heartyuui/blogroll-monitor/internal/model"
	"github.com/heartyuui/blogroll-monitor/internal/signing"
)

func TestClientSignsCatalogAndStatusRequests(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		body, _ := io.ReadAll(request.Body)
		if request.Header.Get("X-Monitor-Protocol-Version") != "1" {
			t.Error("protocol version header is missing")
		}
		verifyRequest(t, request, body, secret)
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/internal/friend-link-monitor/catalog":
			_ = json.NewEncoder(response).Encode(CatalogPage{
				SchemaVersion: 1, SyncID: "sync-1", Complete: true,
				Items: []model.CatalogItem{{ID: "friend-1", URL: "https://example.com/", MonitorRevision: 1, MonitorEnabled: true}},
			})
		case "/internal/friend-link-monitor/status-batch":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"schemaVersion": 1,
				"results":       []StatusResult{{EventID: "00000000-0000-4000-8000-000000000001", Accepted: true}},
			})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, "test-node", signing.Signer{KeyID: "key-1", Secret: secret}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	page, err := client.Catalog(t.Context(), "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("catalog failed: %#v %v", page, err)
	}
	event := model.StatusEvent{
		EventID: "00000000-0000-4000-8000-000000000001", FriendLinkID: "friend-1",
		MonitorRevision: 1, CurrentStatus: model.StatusUp,
	}
	results, err := client.SendStatus(t.Context(), []model.StatusEvent{event})
	if err != nil || len(results) != 1 || !results[0].Accepted {
		t.Fatalf("status batch failed: %#v %v", results, err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d", requests)
	}
}

func TestClientAllowsExplicitDockerHostHTTPForLocalDevelopment(t *testing.T) {
	client, err := New(
		"http://host.docker.internal:3000",
		"test-node",
		signing.Signer{KeyID: "key-1", Secret: []byte("01234567890123456789012345678901")},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
}

func TestClientRejectsDockerHostHTTPWithoutExplicitOptIn(t *testing.T) {
	_, err := New(
		"http://host.docker.internal:3000",
		"test-node",
		signing.Signer{KeyID: "key-1", Secret: []byte("01234567890123456789012345678901")},
		false,
	)
	if err == nil {
		t.Fatal("expected insecure Docker host URL to be rejected")
	}
}

func verifyRequest(t *testing.T, request *http.Request, body, secret []byte) {
	t.Helper()
	digest := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(digest[:])
	if request.Header.Get("X-Monitor-Content-SHA256") != bodyHash {
		t.Fatal("signed body hash does not match")
	}
	target, _ := url.Parse(request.URL.RequestURI())
	canonical := signing.CanonicalRequest(
		request.Header.Get("X-Monitor-Key-Id"), request.Method, signing.CanonicalTarget(target),
		request.Header.Get("X-Monitor-Timestamp"), request.Header.Get("X-Monitor-Nonce"), bodyHash,
	)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(request.Header.Get("X-Monitor-Signature"))) {
		t.Fatal("request signature does not match")
	}
}
