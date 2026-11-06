package handlers

import (
	"net/http"
	"sort"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// The reputation view brings together, per hosted domain, what the server
// already records about how its mail is received (#1590): the DMARC results
// receivers report back (#730), how its outbound mail fared (#142), and whether
// an operator has stopped it (#1590), with the blocklist listings of the
// server's sending IPs (#148), which every domain shares. It collects nothing
// new. It is the operator's: it needs queue:read at "*", the server's own scope,
// as reading the queue does.

const reputationDescription = "Per hosted domain, over a window of the last `days` days (default 7, at most 90) and the same length before it: DMARC pass and fail message counts from received aggregate reports whose period ends in the window, a message counting as a pass when DKIM or SPF passed; outbound delivered, bounced and deferred counts for mail queued in the window and sent as the domain, with bounces by failure category and the bounce rate (bounced over delivered plus bounced, expired counting as bounced); and whether the domain's outbound mail is stopped. Complaints are always null: the server does not record complaint reports yet. The current DNSBL listings of the server's sending IPs are given once, since every domain sends from them. Rates are null when there is nothing to divide. Domains come worst first: by bounce rate, then by name. Only data the server already stores is read."

// reputationDefaultDays is the window when none is asked for; the binder holds
// days to 1..90.
const reputationDefaultDays = 7

type ReputationHandler struct{ db *gorm.DB }

func NewReputationHandler(db *gorm.DB) *ReputationHandler { return &ReputationHandler{db: db} }

func (h *ReputationHandler) Register(rs endpoint.Routes, at func(string) endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/reputation",
		OperationID: "getReputation", Summary: "Read each domain's sending reputation", Description: reputationDescription,
		Tags: []string{"Admin: Domains"}, Middleware: []endpoint.Middleware{at(middleware.CapQueueRead)}}, h.get)
}

type reputationInput struct {
	Days *int `query:"days" minimum:"1" maximum:"90" description:"The window's length in days, ending now; 7 when left out."`
}

// Reputation is the reputation view: the window, the server's DNSBL listings,
// and each hosted domain's figures, worst first.
type Reputation struct {
	From    time.Time          `json:"from"`
	To      time.Time          `json:"to"`
	Days    int                `json:"days"`
	DNSBL   ReputationDNSBL    `json:"dnsbl"`
	Domains []DomainReputation `json:"domains"`
}

// ReputationDNSBL is what the DNSBL monitor last recorded for each of the
// server's sending IPs and blocklists: the pairs that stand listed.
type ReputationDNSBL struct {
	Listed   bool                     `json:"listed"`
	Listings []ReputationDNSBLListing `json:"listings"`
}

type ReputationDNSBLListing struct {
	IP       string    `json:"ip"`
	Provider string    `json:"provider"`
	Zone     string    `json:"zone"`
	Since    time.Time `json:"since"`
	Reason   string    `json:"reason"`
}

// DomainReputation is one domain's figures for the window and for the window
// before it, against which a spike shows.
type DomainReputation struct {
	DomainID     uint                         `json:"domain_id"`
	Domain       string                       `json:"domain"`
	OutboundStop *apitypes.DomainOutboundStop `json:"outbound_stop" description:"The operator's stop on the domain's outbound mail, or null while it sends"`
	Current      ReputationFigures            `json:"current"`
	Previous     ReputationFigures            `json:"previous"`
}

type ReputationFigures struct {
	DMARC    ReputationDMARC    `json:"dmarc"`
	Outbound ReputationOutbound `json:"outbound"`
	// Complaints is always null for now: complaint reports are not recorded yet (#733).
	Complaints *int64 `json:"complaints" description:"Always null: complaint reports are not recorded yet"`
}

type ReputationDMARC struct {
	Pass     int64    `json:"pass"`
	Fail     int64    `json:"fail"`
	PassRate *float64 `json:"pass_rate" description:"pass over pass plus fail, from 0 to 1; null without reported messages"`
}

type ReputationOutbound struct {
	Delivered  int64             `json:"delivered"`
	Bounced    int64             `json:"bounced"`
	Deferred   int64             `json:"deferred"`
	BounceRate *float64          `json:"bounce_rate" description:"bounced over delivered plus bounced, from 0 to 1; null with neither"`
	Bounces    ReputationBounces `json:"bounces"`
}

// ReputationBounces splits the bounced mail by why it bounced: the permanent
// failure categories (queue.ClassifyFailure), any other refusal, and mail that
// ran out of retries.
type ReputationBounces struct {
	BadMailbox     int64 `json:"bad_mailbox"`
	BadDomain      int64 `json:"bad_domain"`
	Policy         int64 `json:"policy"`
	OtherPermanent int64 `json:"other_permanent" description:"Bounced with any other or no recorded category"`
	Expired        int64 `json:"expired" description:"Retried until its queue lifetime ran out"`
}

func (h *ReputationHandler) get(r *http.Request, in reputationInput) (Reputation, error) {
	days := reputationDefaultDays
	if in.Days != nil {
		days = *in.Days
	}
	db := h.db.WithContext(r.Context())
	to := time.Now().UTC()
	span := time.Duration(days) * 24 * time.Hour
	from := to.Add(-span)

	var domains []models.Domain
	if err := db.Order("name").Find(&domains).Error; err != nil {
		return Reputation{}, err
	}
	current, err := reputationFigures(db, from, to)
	if err != nil {
		return Reputation{}, err
	}
	previous, err := reputationFigures(db, from.Add(-span), from)
	if err != nil {
		return Reputation{}, err
	}
	listings, err := dnsblListings(db)
	if err != nil {
		return Reputation{}, err
	}

	out := Reputation{From: from, To: to, Days: days, DNSBL: ReputationDNSBL{Listed: len(listings) > 0, Listings: listings},
		Domains: make([]DomainReputation, 0, len(domains))}
	for _, d := range domains {
		out.Domains = append(out.Domains, DomainReputation{DomainID: d.ID, Domain: d.Name, OutboundStop: outboundStopOf(d),
			Current: current.of(d), Previous: previous.of(d)})
	}
	sort.SliceStable(out.Domains, func(i, j int) bool {
		a, b := out.Domains[i].Current.Outbound.BounceRate, out.Domains[j].Current.Outbound.BounceRate
		switch {
		case a == nil && b == nil:
			return false
		case a == nil || b == nil:
			return b == nil
		}
		return *a > *b
	})
	return out, nil
}

// windowFigures is one window's DMARC counts by domain ID and outbound counts by
// domain name.
type windowFigures struct {
	dmarc    map[uint]ReputationDMARC
	outbound map[string]ReputationOutbound
}

func (w windowFigures) of(d models.Domain) ReputationFigures {
	dmarc := w.dmarc[d.ID]
	if total := dmarc.Pass + dmarc.Fail; total > 0 {
		dmarc.PassRate = ratio(dmarc.Pass, total)
	}
	outbound := w.outbound[d.Name]
	if total := outbound.Delivered + outbound.Bounced; total > 0 {
		outbound.BounceRate = ratio(outbound.Bounced, total)
	}
	return ReputationFigures{DMARC: dmarc, Outbound: outbound}
}

func ratio(part, total int64) *float64 {
	r := float64(part) / float64(total)
	return &r
}

// dmarcPassSQL is true, over one record of a received report, when the
// receiver evaluated it as a DMARC pass: DKIM or SPF passed aligned (RFC 7489 §4.2).
const dmarcPassSQL = `(rec->'row'->'policy_evaluated'->>'dkim' = 'pass' OR rec->'row'->'policy_evaluated'->>'spf' = 'pass')`

func reputationFigures(db *gorm.DB, from, to time.Time) (windowFigures, error) {
	w := windowFigures{dmarc: map[uint]ReputationDMARC{}, outbound: map[string]ReputationOutbound{}}

	var dmarcRows []struct {
		DomainID uint
		Pass     int64
		Fail     int64
	}
	err := db.Raw(`SELECT r.domain_id,
		COALESCE(SUM((rec->'row'->>'count')::bigint) FILTER (WHERE `+dmarcPassSQL+`), 0) AS pass,
		COALESCE(SUM((rec->'row'->>'count')::bigint) FILTER (WHERE NOT `+dmarcPassSQL+`), 0) AS fail
		FROM dmarc_reports_received r CROSS JOIN LATERAL jsonb_array_elements(r.records) rec
		WHERE r.end_date > ? AND r.end_date <= ?
		GROUP BY r.domain_id`, from, to).Scan(&dmarcRows).Error
	if err != nil {
		return w, err
	}
	for _, row := range dmarcRows {
		w.dmarc[row.DomainID] = ReputationDMARC{Pass: row.Pass, Fail: row.Fail}
	}

	var queueRows []struct {
		Domain         string
		Delivered      int64
		Deferred       int64
		BadMailbox     int64
		BadDomain      int64
		Policy         int64
		OtherPermanent int64
		Expired        int64
	}
	err = db.Raw(`SELECT `+models.OutboundQueueSendingDomainSQL+` AS domain,
		COUNT(*) FILTER (WHERE status = 'delivered') AS delivered,
		COUNT(*) FILTER (WHERE status = 'deferred') AS deferred,
		COUNT(*) FILTER (WHERE status = 'bounced' AND failure_category = 'bad_mailbox') AS bad_mailbox,
		COUNT(*) FILTER (WHERE status = 'bounced' AND failure_category = 'bad_domain') AS bad_domain,
		COUNT(*) FILTER (WHERE status = 'bounced' AND failure_category = 'policy') AS policy,
		COUNT(*) FILTER (WHERE status = 'bounced' AND COALESCE(failure_category, '') NOT IN ('bad_mailbox', 'bad_domain', 'policy')) AS other_permanent,
		COUNT(*) FILTER (WHERE status = 'expired') AS expired
		FROM outbound_queue
		WHERE created_at >= ? AND created_at < ?
		GROUP BY 1`, from, to).Scan(&queueRows).Error
	if err != nil {
		return w, err
	}
	for _, row := range queueRows {
		bounces := ReputationBounces{BadMailbox: row.BadMailbox, BadDomain: row.BadDomain, Policy: row.Policy,
			OtherPermanent: row.OtherPermanent, Expired: row.Expired}
		w.outbound[row.Domain] = ReputationOutbound{Delivered: row.Delivered, Deferred: row.Deferred, Bounces: bounces,
			Bounced: bounces.BadMailbox + bounces.BadDomain + bounces.Policy + bounces.OtherPermanent + bounces.Expired}
	}
	return w, nil
}

// dnsblListings is each IP and blocklist whose latest recorded event is a listing.
func dnsblListings(db *gorm.DB) ([]ReputationDNSBLListing, error) {
	var rows []models.DNSBLListingEvent
	err := db.Raw(`SELECT * FROM (SELECT DISTINCT ON (ip, provider, zone) * FROM dnsbl_listing_events
		ORDER BY ip, provider, zone, id DESC) latest WHERE kind = 'listed' ORDER BY ip, provider, zone`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	listings := make([]ReputationDNSBLListing, len(rows))
	for i, row := range rows {
		listings[i] = ReputationDNSBLListing{IP: row.IP, Provider: row.Provider, Zone: row.Zone, Since: row.Since, Reason: row.Reason}
	}
	return listings, nil
}
