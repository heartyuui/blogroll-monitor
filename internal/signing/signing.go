package signing

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

const Version = "monitor-hmac-v1"

type Signer struct {
	KeyID  string
	Secret []byte
}

func (s Signer) Headers(method string, requestURL *url.URL, body []byte, now time.Time) (map[string]string, error) {
	if s.KeyID == "" || len(s.Secret) < 32 {
		return nil, fmt.Errorf("HMAC signer is not configured")
	}
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("generate HMAC nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	timestamp := fmt.Sprintf("%d", now.UTC().Unix())
	bodyDigest := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(bodyDigest[:])
	canonical := CanonicalRequest(s.KeyID, method, CanonicalTarget(requestURL), timestamp, nonce, bodyHash)
	mac := hmac.New(sha256.New, s.Secret)
	_, _ = mac.Write([]byte(canonical))
	return map[string]string{
		"X-Monitor-Key-Id":         s.KeyID,
		"X-Monitor-Timestamp":      timestamp,
		"X-Monitor-Nonce":          nonce,
		"X-Monitor-Content-SHA256": bodyHash,
		"X-Monitor-Signature":      base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	}, nil
}

func CanonicalRequest(keyID, method, target, timestamp, nonce, bodyHash string) string {
	return strings.Join([]string{
		Version,
		keyID,
		strings.ToUpper(method),
		target,
		timestamp,
		nonce,
		bodyHash,
	}, "\n")
}

func CanonicalTarget(value *url.URL) string {
	path := value.EscapedPath()
	if path == "" {
		path = "/"
	}
	type entry struct{ key, value string }
	entries := make([]entry, 0)
	for key, values := range value.Query() {
		for _, item := range values {
			entries = append(entries, entry{key: key, value: item})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key == entries[j].key {
			return entries[i].value < entries[j].value
		}
		return entries[i].key < entries[j].key
	})
	parts := make([]string, 0, len(entries))
	for _, item := range entries {
		parts = append(parts, encodeQuery(item.key)+"="+encodeQuery(item.value))
	}
	if len(parts) == 0 {
		return path
	}
	return path + "?" + strings.Join(parts, "&")
}

func encodeQuery(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
