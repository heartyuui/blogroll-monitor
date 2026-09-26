package syncclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/heartyuui/blogroll-monitor/internal/model"
	"github.com/heartyuui/blogroll-monitor/internal/signing"
)

const maxResponseBytes = 2 << 20

type CatalogPage struct {
	SchemaVersion int                 `json:"schemaVersion"`
	SyncID        string              `json:"syncId"`
	Items         []model.CatalogItem `json:"items"`
	NextCursor    *string             `json:"nextCursor"`
	Complete      bool                `json:"complete"`
}

type StatusResult struct {
	EventID  string `json:"eventId"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

type statusResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	Results       []StatusResult `json:"results"`
}

type Client struct {
	baseURL *url.URL
	signer  signing.Signer
	client  *http.Client
	nodeID  string
	retries int
}

func New(rawBaseURL, nodeID string, signer signing.Signer, allowInsecureLocal bool) (*Client, error) {
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil || baseURL.Hostname() == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, fmt.Errorf("BLOG_BASE_URL is invalid")
	}
	if baseURL.Scheme != "https" {
		if !allowInsecureLocal || baseURL.Scheme != "http" || !isLocalDevelopmentHost(baseURL.Hostname()) {
			return nil, fmt.Errorf("BLOG_BASE_URL must use HTTPS")
		}
	}
	transport := &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		DisableCompression:  true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &Client{
		baseURL: baseURL,
		signer:  signer,
		nodeID:  nodeID,
		retries: 2,
		client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Close() {
	if transport, ok := c.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (c *Client) Catalog(ctx context.Context, cursor string) (CatalogPage, error) {
	target := c.resolve("/internal/friend-link-monitor/catalog")
	query := target.Query()
	query.Set("limit", "200")
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	target.RawQuery = query.Encode()
	var page CatalogPage
	if err := c.doJSON(ctx, http.MethodGet, target, nil, &page); err != nil {
		return CatalogPage{}, err
	}
	if page.SchemaVersion != 1 || page.SyncID == "" || (!page.Complete && (page.NextCursor == nil || *page.NextCursor == "")) {
		return CatalogPage{}, fmt.Errorf("catalog response is invalid")
	}
	return page, nil
}

func (c *Client) SendStatus(ctx context.Context, events []model.StatusEvent) ([]StatusResult, error) {
	body, err := json.Marshal(struct {
		NodeID string              `json:"nodeId"`
		Events []model.StatusEvent `json:"events"`
	}{NodeID: c.nodeID, Events: events})
	if err != nil {
		return nil, err
	}
	var response statusResponse
	if err := c.doJSON(ctx, http.MethodPost, c.resolve("/internal/friend-link-monitor/status-batch"), body, &response); err != nil {
		return nil, err
	}
	if response.SchemaVersion != 1 || len(response.Results) != len(events) {
		return nil, fmt.Errorf("status response is invalid")
	}
	return response.Results, nil
}

func (c *Client) doJSON(ctx context.Context, method string, target *url.URL, body []byte, output any) error {
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<attempt) * 200 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		headers, err := c.signer.Headers(method, target, body, time.Now())
		if err != nil {
			return err
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("X-Monitor-Protocol-Version", "1")
		request.Header.Set("User-Agent", "blogroll-monitor/1")
		if len(body) > 0 {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := c.client.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		_ = response.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if len(responseBody) > maxResponseBytes {
			return fmt.Errorf("blog response exceeded size limit")
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			lastErr = fmt.Errorf("blog API returned HTTP %d", response.StatusCode)
			if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
				return lastErr
			}
			continue
		}
		if err := json.Unmarshal(responseBody, output); err != nil {
			return fmt.Errorf("decode blog response: %w", err)
		}
		return nil
	}
	return fmt.Errorf("blog API request failed after %s: %w", strconv.Itoa(c.retries+1)+" attempts", lastErr)
}

func (c *Client) resolve(path string) *url.URL {
	target := *c.baseURL
	target.Path = path
	target.RawPath = ""
	target.RawQuery = ""
	return &target
}

func isLocalDevelopmentHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || host == "host.docker.internal" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
