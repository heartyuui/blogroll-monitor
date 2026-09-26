package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsInsecureLocalBlogURLInProduction(t *testing.T) {
	configuration := Config{
		Environment:            "production",
		ListenAddress:          "127.0.0.1:8080",
		BlogBaseURL:            "http://host.docker.internal:3000",
		BlogAllowInsecureLocal: true,
		HMACKeyID:              "key-1",
		GlobalConcurrency:      1,
		PerDomainConcurrency:   1,
		SuccessThreshold:       1,
		FailureThreshold:       1,
		MaxRedirects:           5,
		TargetRetries:          1,
		TotalTimeout:           10 * time.Second,
		MaxResponseBytes:       65536,
	}

	err := configuration.Validate()
	if err == nil || !strings.Contains(err.Error(), "BLOG_ALLOW_INSECURE_LOCAL") {
		t.Fatalf("expected production insecure-local validation error, got %v", err)
	}
}
