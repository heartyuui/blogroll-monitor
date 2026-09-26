package checker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/model"
)

type fixedResolver struct {
	addresses []netip.Addr
	err       error
}

func (r fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, r.err
}

func TestPolicyRejectsUnsafeDestinationsAndMixedDNS(t *testing.T) {
	tests := []string{
		"http://127.0.0.1/", "http://[::1]/", "http://[::ffff:127.0.0.1]/",
		"http://169.254.169.254/latest/meta-data/", "ftp://example.com/", "http://user@example.com/",
	}
	policy := Policy{LookupTimeout: time.Second}
	for _, target := range tests {
		if _, err := policy.Resolve(context.Background(), target); err == nil {
			t.Errorf("Resolve(%q) unexpectedly succeeded", target)
		}
	}
	policy.Resolver = fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.1")}}
	if _, err := policy.Resolve(context.Background(), "https://example.com/"); err == nil {
		t.Fatal("mixed public/private DNS response was accepted")
	}
}

func TestPolicyNormalizesIDNAndRegistrableDomain(t *testing.T) {
	policy := Policy{
		LookupTimeout: time.Second,
		Resolver:      fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
	}
	destination, err := policy.Resolve(context.Background(), "https://www.食狮.com.cn/path")
	if err != nil {
		t.Fatal(err)
	}
	if destination.Hostname != "www.xn--85x722f.com.cn" || destination.RegistrableDomain != "xn--85x722f.com.cn" {
		t.Fatalf("unexpected normalized identity: %#v", destination)
	}
}

func TestCheckerClassifiesControlledHTTPResponses(t *testing.T) {
	statuses := []struct {
		code    int
		outcome model.Outcome
	}{
		{http.StatusOK, model.OutcomeSuccess},
		{http.StatusForbidden, model.OutcomeDegraded},
		{http.StatusTooManyRequests, model.OutcomeDegraded},
		{http.StatusServiceUnavailable, model.OutcomeHardFailure},
	}
	for _, test := range statuses {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(test.code)
			_, _ = response.Write([]byte("controlled"))
		}))
		target, _ := url.Parse(server.URL)
		address := net.ParseIP(target.Hostname())
		checker := New(Options{
			Policy:         Policy{LookupTimeout: time.Second, AllowTestLoopback: true},
			ConnectTimeout: time.Second, TLSHandshakeTimeout: time.Second,
			ResponseHeaderTimeout: time.Second, ReadTimeout: time.Second, TotalTimeout: 2 * time.Second,
			MaxResponseBytes: 32, MaxRedirects: 2, UserAgent: "test",
		})
		result := checker.Check(context.Background(), server.URL)
		checker.Close()
		server.Close()
		if result.Outcome != test.outcome {
			t.Errorf("status %d from %s yielded %s, want %s (IP %s)", test.code, server.URL, result.Outcome, test.outcome, address)
		}
	}
}

func TestCheckerRevalidatesRedirectDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Location", "http://169.254.169.254/latest/meta-data/")
		response.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	checker := New(Options{
		Policy:         Policy{LookupTimeout: time.Second, AllowTestLoopback: true},
		ConnectTimeout: time.Second, TLSHandshakeTimeout: time.Second,
		ResponseHeaderTimeout: time.Second, ReadTimeout: time.Second, TotalTimeout: 2 * time.Second,
		MaxResponseBytes: 32, MaxRedirects: 2, UserAgent: "test",
	})
	defer checker.Close()
	result := checker.Check(context.Background(), server.URL)
	if result.Outcome != model.OutcomeUnknown || result.ErrorCode != "SSRF_BLOCKED" {
		t.Fatalf("unsafe redirect result = %#v", result)
	}
}
