package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment            string
	ListenAddress          string
	DatabasePath           string
	BlogBaseURL            string
	BlogAllowInsecureLocal bool
	HMACKeyID              string
	HMACSecret             []byte
	NodeID                 string
	SyncInterval           time.Duration
	CheckInterval          time.Duration
	SchedulerPollInterval  time.Duration
	GlobalConcurrency      int
	PerDomainConcurrency   int
	SuccessThreshold       int
	FailureThreshold       int
	MaxRedirects           int
	TargetRetries          int
	DNSLookupTimeout       time.Duration
	ConnectTimeout         time.Duration
	TLSHandshakeTimeout    time.Duration
	ResponseHeaderTimeout  time.Duration
	ReadTimeout            time.Duration
	TotalTimeout           time.Duration
	MaxResponseBytes       int64
	AllowHTTPSDowngrade    bool
	TestAllowLoopback      bool
	RawRetention           time.Duration
	HourlyRetention        time.Duration
	DailyRetention         time.Duration
}

func Load() (Config, error) {
	secret, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(os.Getenv("MONITOR_HMAC_SECRET")))
	if err != nil || len(secret) < 32 {
		return Config{}, fmt.Errorf("MONITOR_HMAC_SECRET must be at least 32 random bytes encoded as base64url")
	}
	configuration := Config{
		Environment:            envString("MONITOR_ENV", "production"),
		ListenAddress:          envString("MONITOR_LISTEN_ADDRESS", "127.0.0.1:8080"),
		DatabasePath:           envString("MONITOR_DATABASE_PATH", "/data/friend-link-monitor.db"),
		BlogBaseURL:            strings.TrimRight(envString("BLOG_BASE_URL", ""), "/"),
		BlogAllowInsecureLocal: envBool("BLOG_ALLOW_INSECURE_LOCAL", false),
		HMACKeyID:              envString("MONITOR_HMAC_KEY_ID", ""),
		HMACSecret:             secret,
		NodeID:                 envString("MONITOR_NODE_ID", "monitor-1"),
		SyncInterval:           envDuration("MONITOR_SYNC_INTERVAL", time.Minute),
		CheckInterval:          envDuration("MONITOR_CHECK_INTERVAL", 5*time.Minute),
		SchedulerPollInterval:  envDuration("MONITOR_SCHEDULER_POLL_INTERVAL", time.Second),
		GlobalConcurrency:      envInt("MONITOR_GLOBAL_CONCURRENCY", 32),
		PerDomainConcurrency:   envInt("MONITOR_PER_DOMAIN_CONCURRENCY", 1),
		SuccessThreshold:       envInt("MONITOR_SUCCESS_THRESHOLD", 2),
		FailureThreshold:       envInt("MONITOR_FAILURE_THRESHOLD", 3),
		MaxRedirects:           envInt("MONITOR_MAX_REDIRECTS", 5),
		TargetRetries:          envInt("MONITOR_TARGET_RETRIES", 1),
		DNSLookupTimeout:       envDuration("MONITOR_DNS_TIMEOUT", 2*time.Second),
		ConnectTimeout:         envDuration("MONITOR_CONNECT_TIMEOUT", 3*time.Second),
		TLSHandshakeTimeout:    envDuration("MONITOR_TLS_TIMEOUT", 3*time.Second),
		ResponseHeaderTimeout:  envDuration("MONITOR_RESPONSE_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:            envDuration("MONITOR_READ_TIMEOUT", 3*time.Second),
		TotalTimeout:           envDuration("MONITOR_TOTAL_TIMEOUT", 10*time.Second),
		MaxResponseBytes:       int64(envInt("MONITOR_MAX_RESPONSE_BYTES", 65536)),
		AllowHTTPSDowngrade:    envBool("MONITOR_ALLOW_HTTPS_DOWNGRADE", false),
		TestAllowLoopback:      envBool("MONITOR_TEST_ALLOW_LOOPBACK", false),
		RawRetention:           envDuration("MONITOR_RAW_RETENTION", 7*24*time.Hour),
		HourlyRetention:        envDuration("MONITOR_HOURLY_RETENTION", 180*24*time.Hour),
		DailyRetention:         envDuration("MONITOR_DAILY_RETENTION", 730*24*time.Hour),
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, err
	}
	return configuration, nil
}

func (c Config) Validate() error {
	if c.BlogBaseURL == "" || c.HMACKeyID == "" {
		return fmt.Errorf("BLOG_BASE_URL and MONITOR_HMAC_KEY_ID are required")
	}
	if _, _, err := net.SplitHostPort(c.ListenAddress); err != nil {
		return fmt.Errorf("MONITOR_LISTEN_ADDRESS is invalid: %w", err)
	}
	if c.GlobalConcurrency < 1 || c.GlobalConcurrency > 256 || c.PerDomainConcurrency < 1 || c.PerDomainConcurrency > 16 {
		return fmt.Errorf("monitor concurrency is outside the supported range")
	}
	if c.SuccessThreshold < 1 || c.FailureThreshold < 1 || c.MaxRedirects < 0 || c.MaxRedirects > 10 || c.TargetRetries < 0 || c.TargetRetries > 3 {
		return fmt.Errorf("monitor threshold or retry configuration is invalid")
	}
	if c.TotalTimeout < time.Second || c.TotalTimeout > time.Minute || c.MaxResponseBytes < 1024 || c.MaxResponseBytes > 1024*1024 {
		return fmt.Errorf("monitor timeout or response limit is invalid")
	}
	if c.TestAllowLoopback && c.Environment == "production" {
		return fmt.Errorf("MONITOR_TEST_ALLOW_LOOPBACK is forbidden in production")
	}
	if c.BlogAllowInsecureLocal && c.Environment == "production" {
		return fmt.Errorf("BLOG_ALLOW_INSECURE_LOCAL is forbidden in production")
	}
	return nil
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return fallback
	}
	return value
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
