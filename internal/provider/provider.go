package provider

import (
	"context"
	"time"
)

type ICPResult struct {
	Registered   bool
	License      string
	Organization string
	CheckedAt    time.Time
	FailureCode  string
}

type ICP interface {
	Lookup(context.Context, string) (ICPResult, error)
}

type IPLocationResult struct {
	Region      string
	Provider    string
	CheckedAt   time.Time
	FailureCode string
}

type IPLocation interface {
	Lookup(context.Context, string) (IPLocationResult, error)
}

type DisabledICP struct{}

func (DisabledICP) Lookup(_ context.Context, _ string) (ICPResult, error) {
	return ICPResult{CheckedAt: time.Now().UTC(), FailureCode: "PROVIDER_NOT_CONFIGURED"}, nil
}

type DisabledIPLocation struct{}

func (DisabledIPLocation) Lookup(_ context.Context, _ string) (IPLocationResult, error) {
	return IPLocationResult{CheckedAt: time.Now().UTC(), FailureCode: "PROVIDER_NOT_CONFIGURED"}, nil
}
