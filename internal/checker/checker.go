package checker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/model"
)

type Options struct {
	Policy                Policy
	ConnectTimeout        time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	ReadTimeout           time.Duration
	TotalTimeout          time.Duration
	MaxResponseBytes      int64
	MaxRedirects          int
	Retries               int
	AllowHTTPSDowngrade   bool
	UserAgent             string
}

type Checker struct {
	options    Options
	transports *transportCache
}

func New(options Options) *Checker {
	return &Checker{
		options: options,
		transports: &transportCache{
			items: make(map[string]*http.Transport),
		},
	}
}

func (c *Checker) Close() {
	c.transports.close()
}

func (c *Checker) Check(ctx context.Context, rawURL string) model.CheckResult {
	startedAt := time.Now()
	checkContext, cancel := context.WithTimeout(ctx, c.options.TotalTimeout)
	defer cancel()

	var result model.CheckResult
	for attempt := 0; attempt <= c.options.Retries; attempt++ {
		result = c.checkOnce(checkContext, rawURL)
		if !retryable(result) || attempt == c.options.Retries {
			break
		}
		delay := time.Duration(250*(1<<attempt)+rand.IntN(250)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-checkContext.Done():
			timer.Stop()
			result = failureResult(time.Now().UTC(), "REQUEST_TIMEOUT", true)
			attempt = c.options.Retries
		case <-timer.C:
		}
	}
	latency := int(time.Since(startedAt).Milliseconds())
	result.LatencyMs = &latency
	return result
}

func (c *Checker) checkOnce(ctx context.Context, rawURL string) model.CheckResult {
	checkedAt := time.Now().UTC()
	currentURL := rawURL
	resolved := make([]model.ResolvedIP, 0)
	var connectedIP string

	for hop := 0; hop <= c.options.MaxRedirects; hop++ {
		destination, err := c.options.Policy.Resolve(ctx, currentURL)
		if err != nil {
			var policyError *PolicyError
			if errors.As(err, &policyError) {
				outcome := model.OutcomeHardFailure
				if policyError.Code == "SSRF_BLOCKED" || policyError.Code == "INVALID_URL" || policyError.Code == "INVALID_HOSTNAME" || policyError.Code == "UNSUPPORTED_SCHEME" {
					outcome = model.OutcomeUnknown
				}
				return model.CheckResult{CheckedAt: checkedAt, Outcome: outcome, ErrorCode: policyError.Code, ResolvedIPs: resolved}
			}
			return failureResult(checkedAt, "DNS_RESOLUTION_FAILED", false)
		}

		selected := destination.Addresses[0]
		connectedIP = selected.String()
		for _, address := range destination.Addresses {
			family := 6
			if address.Is4() {
				family = 4
			}
			resolved = append(resolved, model.ResolvedIP{
				Hop: hop, Hostname: destination.Hostname, Address: address.String(), Family: family, Selected: address == selected,
			})
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, destination.URL.String(), nil)
		if err != nil {
			return model.CheckResult{CheckedAt: checkedAt, Outcome: model.OutcomeUnknown, ErrorCode: "INVALID_URL", ResolvedIPs: resolved}
		}
		request.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("User-Agent", c.options.UserAgent)

		client := &http.Client{
			Transport: c.transports.get(destination.URL.Scheme, destination.Hostname, destination.URL.Port(), selected, c.options),
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		response, err := client.Do(request)
		if err != nil {
			result := classifyRequestError(checkedAt, err)
			result.ResolvedIPs = resolved
			result.ConnectedIP = connectedIP
			return result
		}

		if isRedirect(response.StatusCode) && response.Header.Get("Location") != "" {
			_, _ = io.CopyN(io.Discard, response.Body, 4096)
			_ = response.Body.Close()
			if hop == c.options.MaxRedirects {
				return model.CheckResult{CheckedAt: checkedAt, Outcome: model.OutcomeHardFailure, HTTPStatus: intPointer(response.StatusCode), ErrorCode: "REDIRECT_LIMIT_EXCEEDED", ConnectedIP: connectedIP, ResolvedIPs: resolved}
			}
			nextURL, locationErr := destination.URL.Parse(response.Header.Get("Location"))
			if locationErr != nil || nextURL.Scheme == "" || nextURL.Host == "" {
				return model.CheckResult{CheckedAt: checkedAt, Outcome: model.OutcomeHardFailure, HTTPStatus: intPointer(response.StatusCode), ErrorCode: "INVALID_REDIRECT", ConnectedIP: connectedIP, ResolvedIPs: resolved}
			}
			if destination.URL.Scheme == "https" && nextURL.Scheme == "http" && !c.options.AllowHTTPSDowngrade {
				return model.CheckResult{CheckedAt: checkedAt, Outcome: model.OutcomeUnknown, HTTPStatus: intPointer(response.StatusCode), ErrorCode: "HTTPS_DOWNGRADE_BLOCKED", ConnectedIP: connectedIP, ResolvedIPs: resolved}
			}
			currentURL = nextURL.String()
			continue
		}

		_, readErr := io.CopyN(io.Discard, response.Body, c.options.MaxResponseBytes+1)
		_ = response.Body.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			result := classifyRequestError(checkedAt, readErr)
			if result.ErrorCode == "REQUEST_TIMEOUT" {
				result.ErrorCode = "READ_TIMEOUT"
			}
			result.HTTPStatus = intPointer(response.StatusCode)
			result.ResolvedIPs = resolved
			result.ConnectedIP = connectedIP
			return result
		}
		return classifyHTTPStatus(checkedAt, response.StatusCode, connectedIP, resolved)
	}
	return model.CheckResult{CheckedAt: checkedAt, Outcome: model.OutcomeHardFailure, ErrorCode: "REDIRECT_LIMIT_EXCEEDED", ConnectedIP: connectedIP, ResolvedIPs: resolved}
}

func classifyHTTPStatus(checkedAt time.Time, status int, connectedIP string, resolved []model.ResolvedIP) model.CheckResult {
	result := model.CheckResult{CheckedAt: checkedAt, HTTPStatus: intPointer(status), ConnectedIP: connectedIP, ResolvedIPs: resolved}
	switch {
	case status >= 200 && status <= 399:
		result.Outcome = model.OutcomeSuccess
	case status == 401 || status == 403 || status == 405 || status == 429:
		result.Outcome = model.OutcomeDegraded
		result.ErrorCode = fmt.Sprintf("HTTP_%d", status)
	case status >= 500 && status <= 599:
		result.Outcome = model.OutcomeHardFailure
		result.ErrorCode = "HTTP_5XX"
	default:
		result.Outcome = model.OutcomeHardFailure
		result.ErrorCode = fmt.Sprintf("HTTP_%d", status)
	}
	return result
}

func classifyRequestError(checkedAt time.Time, err error) model.CheckResult {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return failureResult(checkedAt, "REQUEST_TIMEOUT", true)
	}
	var certificateError *tls.CertificateVerificationError
	if errors.As(err, &certificateError) {
		return failureResult(checkedAt, "INVALID_CERTIFICATE", false)
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return failureResult(checkedAt, "REQUEST_TIMEOUT", true)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return failureResult(checkedAt, "CONNECTION_REFUSED", false)
	}
	if strings.Contains(strings.ToLower(err.Error()), "tls") || strings.Contains(strings.ToLower(err.Error()), "certificate") {
		return failureResult(checkedAt, "TLS_ERROR", false)
	}
	return failureResult(checkedAt, "CONNECTION_FAILED", false)
}

func failureResult(checkedAt time.Time, code string, timedOut bool) model.CheckResult {
	return model.CheckResult{CheckedAt: checkedAt, Outcome: model.OutcomeHardFailure, ErrorCode: code, TimedOut: timedOut}
}

func retryable(result model.CheckResult) bool {
	if result.Outcome != model.OutcomeHardFailure {
		return false
	}
	return result.ErrorCode == "HTTP_5XX" || result.ErrorCode == "DNS_RESOLUTION_FAILED" || result.ErrorCode == "DNS_TIMEOUT" || result.ErrorCode == "CONNECTION_REFUSED" || result.ErrorCode == "CONNECTION_FAILED" || result.ErrorCode == "REQUEST_TIMEOUT" || result.ErrorCode == "READ_TIMEOUT"
}

func isRedirect(status int) bool {
	return status == http.StatusMovedPermanently || status == http.StatusFound || status == http.StatusSeeOther || status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect
}

func intPointer(value int) *int { return &value }

type readDeadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *readDeadlineConn) Read(buffer []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Read(buffer)
}

type transportCache struct {
	mutex sync.Mutex
	items map[string]*http.Transport
}

func (c *transportCache) get(scheme, hostname, requestedPort string, address netip.Addr, options Options) *http.Transport {
	key := scheme + "|" + hostname + "|" + requestedPort + "|" + address.String()
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if transport := c.items[key]; transport != nil {
		return transport
	}
	if len(c.items) >= 1024 {
		for oldKey, transport := range c.items {
			transport.CloseIdleConnections()
			delete(c.items, oldKey)
			break
		}
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	if requestedPort != "" {
		port = requestedPort
	}
	dialer := &net.Dialer{Timeout: options.ConnectTimeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   options.TLSHandshakeTimeout,
		ResponseHeaderTimeout: options.ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
			if err != nil {
				return nil, err
			}
			return &readDeadlineConn{Conn: connection, timeout: options.ReadTimeout}, nil
		},
	}
	c.items[key] = transport
	return transport
}

func (c *transportCache) close() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for key, transport := range c.items {
		transport.CloseIdleConnections()
		delete(c.items, key)
	}
}
