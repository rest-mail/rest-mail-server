package dnsbl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const eventWriteTimeout = 3 * time.Second

// EventObserver persists transitions before forwarding them to the logs and
// metrics observer. A failed write stops the monitor; it must be restarted
// after the database is repaired. Ordinary checks never write history.
type EventObserver struct {
	db   *gorm.DB
	next Observer
}

var _ Observer = (*EventObserver)(nil)

func NewEventObserver(db *gorm.DB, next Observer) *EventObserver {
	return &EventObserver{db: db, next: next}
}

func (o *EventObserver) Checked(obs Observation) { o.next.Checked(obs) }

func (o *EventObserver) Changed(tr Transition) error {
	kind, since := "listed", tr.At
	switch tr.Kind {
	case TransitionListed:
	case TransitionRecovered:
		kind, since = "delisted", tr.Since
	default:
		return fmt.Errorf("dnsbl: invalid transition kind %q", tr.Kind)
	}
	codes := make([]string, len(tr.Result.Codes))
	for i, code := range tr.Result.Codes {
		codes[i] = code.String()
	}
	encoded, err := json.Marshal(codes)
	if err != nil {
		return err
	}
	row := models.DNSBLListingEvent{
		IP: tr.Result.IP.String(), Provider: tr.Result.Provider, Zone: tr.Result.Zone,
		Kind: kind, At: tr.At, Since: since, Codes: encoded,
		Reason: tr.Result.Reason, Query: tr.Result.Query,
	}
	// A completed DNS answer still needs recording if shutdown arrives now.
	// Bound this independent write so shutdown cannot hang on the database.
	ctx, cancel := context.WithTimeout(context.Background(), eventWriteTimeout)
	defer cancel()
	if err := o.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("persist DNSBL event: %w", err)
	}
	return o.next.Changed(tr)
}

// Known loads only configured pairs. A changed zone is a different source of
// evidence even when the operator reuses its name. ID orders commits from the
// single monitor independently of clock adjustments. No event means unknown.
func (o *EventObserver) Known(ctx context.Context, cfg MonitorConfig) ([]Observation, error) {
	var known []Observation
	for _, ip := range cfg.IPs {
		for _, p := range cfg.Providers {
			var row models.DNSBLListingEvent
			q := o.db.WithContext(ctx).Where("ip = ? AND provider = ? AND zone = ?",
				ip.Unmap().String(), p.Name, strings.TrimSuffix(p.Zone, ".")).Order("id DESC").Limit(1).Find(&row)
			if q.Error != nil {
				return nil, fmt.Errorf("load DNSBL state: %w", q.Error)
			}
			if q.RowsAffected == 0 {
				continue
			}
			r := Result{IP: ip.Unmap(), Provider: row.Provider, Zone: row.Zone, Query: row.Query, Reason: row.Reason}
			switch row.Kind {
			case "listed":
				r.Status = StatusListed
			case "delisted":
				r.Status = StatusNotListed
			default:
				return nil, fmt.Errorf("dnsbl: invalid stored event kind %q", row.Kind)
			}
			var codes []string
			if err := json.Unmarshal(row.Codes, &codes); err != nil {
				return nil, fmt.Errorf("dnsbl: invalid stored codes: %w", err)
			}
			for _, code := range codes {
				a, err := netip.ParseAddr(code)
				if err != nil {
					return nil, fmt.Errorf("dnsbl: invalid stored code: %w", err)
				}
				r.Codes = append(r.Codes, a)
			}
			known = append(known, Observation{At: row.At, Result: r})
		}
	}
	return known, nil
}
