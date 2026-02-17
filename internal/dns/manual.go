package dns

import (
	"context"
	"log/slog"
)

// ManualProvider returns the required DNS records for manual creation.
// It does not actually create or modify any records.
type ManualProvider struct{}

func NewManualProvider() *ManualProvider {
	return &ManualProvider{}
}

func (p *ManualProvider) EnsureRecords(ctx context.Context, domain string, records []DNSRecord) error {
	slog.Info("manual: DNS records need to be created manually",
		"domain", domain,
		"count", len(records),
	)
	for _, r := range records {
		slog.Info("manual: required record",
			"type", r.Type,
			"name", r.Name,
			"value", r.Value,
			"priority", r.Priority,
		)
	}
	return nil
}

func (p *ManualProvider) RemoveRecords(ctx context.Context, domain string) error {
	slog.Info("manual: DNS records need to be removed manually", "domain", domain)
	return nil
}

func (p *ManualProvider) VerifyRecords(ctx context.Context, domain string) ([]VerifyResult, error) {
	slog.Info("manual: DNS verification not supported for manual provider")
	return nil, nil
}
