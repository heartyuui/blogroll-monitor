package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"testing"
	"time"
)

func TestCanonicalTargetSortsAndEncodesLikeBackend(t *testing.T) {
	target, err := url.Parse("https://example.com/internal/friend-link-monitor/catalog?z=2&a=hello+world&a=%21%2A")
	if err != nil {
		t.Fatal(err)
	}
	got := CanonicalTarget(target)
	want := "/internal/friend-link-monitor/catalog?a=%21%2A&a=hello%20world&z=2"
	if got != want {
		t.Fatalf("canonical target = %q, want %q", got, want)
	}
}

func TestHeadersSignBodyAndTarget(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	target, _ := url.Parse("https://example.com/internal/friend-link-monitor/status-batch")
	body := []byte(`{"nodeId":"monitor-1"}`)
	now := time.Unix(1_700_000_000, 0)
	headers, err := (Signer{KeyID: "key-1", Secret: secret}).Headers("POST", target, body, now)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if headers["X-Monitor-Content-SHA256"] != hex.EncodeToString(digest[:]) {
		t.Fatal("body digest header does not match")
	}
	canonical := CanonicalRequest("key-1", "POST", target.Path, headers["X-Monitor-Timestamp"], headers["X-Monitor-Nonce"], headers["X-Monitor-Content-SHA256"])
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(headers["X-Monitor-Signature"]), []byte(want)) {
		t.Fatal("signature does not match canonical request")
	}
}
