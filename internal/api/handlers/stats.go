package handlers

import (
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type StatsHandler struct {
	db *gorm.DB
}

func NewStatsHandler(db *gorm.DB) *StatsHandler {
	return &StatsHandler{db: db}
}

type QueueStats struct {
	Pending    int `json:"pending"`
	Processing int `json:"processing"`
	Failed     int `json:"failed"`
}

type MessageVolumeData struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type RecentActivity struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Timestamp   time.Time `json:"timestamp"`
}

// InboundTransportSecurity summarizes, over all inbound-MX (port 25) mail, how
// much arrived encrypted vs plaintext and — for the plaintext slice — how much
// carried a passing sender-authentication result (SPF or DKIM). It answers the
// operator's standing question: "how much of my inbound is encrypted, and is my
// plaintext inbound legit or junk?". Collection is always on; there is no toggle.
//
// The denominator counts inbound-MX deliveries (one row per local recipient),
// identified by received_tls IS NOT NULL — the column is left NULL for local
// webmail sends, IMAP APPEND, authenticated submission, and pre-existing rows.
type InboundTransportSecurity struct {
	TotalInboundMX    int     `json:"totalInboundMX"`
	OverTLS           int     `json:"overTLS"`
	Plaintext         int     `json:"plaintext"`
	TLSPercent        float64 `json:"tlsPercent"`
	PlaintextPercent  float64 `json:"plaintextPercent"`
	PlaintextAuthPass int     `json:"plaintextAuthPass"`
	PlaintextAuthFail int     `json:"plaintextAuthFail"`
}

type DashboardStats struct {
	DomainCount              int                      `json:"domainCount"`
	MailboxCount             int                      `json:"mailboxCount"`
	QueueStats               QueueStats               `json:"queueStats"`
	MessageVolume            []MessageVolumeData      `json:"messageVolume"`
	RecentActivity           []RecentActivity         `json:"recentActivity"`
	InboundTransportSecurity InboundTransportSecurity `json:"inboundTransportSecurity"`
}

// GetDashboardStats returns statistics for the admin dashboard
// GET /api/v1/admin/stats
func (h *StatsHandler) GetDashboardStats(w http.ResponseWriter, r *http.Request) {
	stats := DashboardStats{}

	// Count total domains
	var domainCount int64
	if err := h.db.Model(&models.Domain{}).Count(&domainCount).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to count domains")
		return
	}
	stats.DomainCount = int(domainCount)

	// Count total mailboxes
	var mailboxCount int64
	if err := h.db.Model(&models.Mailbox{}).Count(&mailboxCount).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to count mailboxes")
		return
	}
	stats.MailboxCount = int(mailboxCount)

	// Queue statistics
	var queueCounts []struct {
		Status string
		Count  int
	}
	if err := h.db.Model(&models.OutboundQueue{}).
		Select("status, COUNT(*) as count").
		Group("status").
		Scan(&queueCounts).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to get queue stats")
		return
	}

	byStatus := make(map[string]int, len(queueCounts))
	for _, qc := range queueCounts {
		byStatus[qc.Status] += qc.Count
	}
	stats.QueueStats = bucketQueueStatuses(byStatus)

	// Message volume (last 7 days)
	messageVolume, err := h.getMessageVolume()
	if err != nil {
		// Log error but don't fail the request
		messageVolume = []MessageVolumeData{}
	}
	stats.MessageVolume = messageVolume

	// Recent activity (last 10 items)
	recentActivity, err := h.getRecentActivity()
	if err != nil {
		// Log error but don't fail the request
		recentActivity = []RecentActivity{}
	}
	stats.RecentActivity = recentActivity

	// Inbound transport-security (always-on). Never fail the whole dashboard on
	// an aggregate error — fall back to the zero value, as the other panels do.
	if its, err := h.getInboundTransportSecurity(); err == nil {
		stats.InboundTransportSecurity = its
	}

	respond.Data(w, http.StatusOK, stats)
}

// inboundTransportSecuritySQL is a single DB aggregate over inbound-MX mail.
// The predicates are deliberately portable (SUM(CASE ...) rather than the
// Postgres-only COUNT(*) FILTER) so the exact query the server runs is also
// exercisable against a lightweight test database.
//
// "Auth pass" is defined as a stored Authentication-Results carrying spf=pass or
// dkim=pass — the tokens the SPF and DKIM filters actually persist onto the raw
// message (the dmarc verdict is computed but not written to the header). These
// are precisely DMARC's inputs, so the split answers "did this plaintext message
// carry a passing sender authentication result, or none at all?".
const inboundTransportSecuritySQL = `SELECT
	COUNT(*) AS total_inbound_mx,
	COALESCE(SUM(CASE WHEN received_tls THEN 1 ELSE 0 END), 0) AS over_tls,
	COALESCE(SUM(CASE WHEN NOT received_tls THEN 1 ELSE 0 END), 0) AS plaintext,
	COALESCE(SUM(CASE WHEN NOT received_tls AND (raw_message LIKE '%spf=pass%' OR raw_message LIKE '%dkim=pass%') THEN 1 ELSE 0 END), 0) AS plaintext_auth_pass,
	COALESCE(SUM(CASE WHEN NOT received_tls AND NOT (raw_message LIKE '%spf=pass%' OR raw_message LIKE '%dkim=pass%') THEN 1 ELSE 0 END), 0) AS plaintext_auth_fail
FROM messages
WHERE received_tls IS NOT NULL`

// inboundTLSCounts holds the raw aggregate row before percentages are derived.
type inboundTLSCounts struct {
	TotalInboundMX    int
	OverTLS           int
	Plaintext         int
	PlaintextAuthPass int
	PlaintextAuthFail int
}

// getInboundTransportSecurity runs the aggregate and assembles the response DTO.
func (h *StatsHandler) getInboundTransportSecurity() (InboundTransportSecurity, error) {
	var c inboundTLSCounts
	if err := h.db.Raw(inboundTransportSecuritySQL).Scan(&c).Error; err != nil {
		return InboundTransportSecurity{}, err
	}
	return buildInboundTransportSecurity(c), nil
}

// buildInboundTransportSecurity derives the encrypted/plaintext percentages from
// the raw aggregate counts. Pure (no I/O) so it is unit-testable directly, and
// guards the zero-denominator case (no inbound-MX mail yet → 0% rather than NaN).
func buildInboundTransportSecurity(c inboundTLSCounts) InboundTransportSecurity {
	its := InboundTransportSecurity{
		TotalInboundMX:    c.TotalInboundMX,
		OverTLS:           c.OverTLS,
		Plaintext:         c.Plaintext,
		PlaintextAuthPass: c.PlaintextAuthPass,
		PlaintextAuthFail: c.PlaintextAuthFail,
	}
	if c.TotalInboundMX > 0 {
		its.TLSPercent = round1(float64(c.OverTLS) * 100 / float64(c.TotalInboundMX))
		its.PlaintextPercent = round1(float64(c.Plaintext) * 100 / float64(c.TotalInboundMX))
	}
	return its
}

// round1 rounds a percentage to one decimal place.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// bucketQueueStatuses folds raw outbound_queue status counts into the three
// dashboard buckets. The queue worker only ever writes pending, deferred,
// delivering, delivered and bounced (expired is reserved). The dashboard used
// to look for "processing"/"failed", which are never written, so the in-flight
// and failed counters were permanently stuck at zero.
func bucketQueueStatuses(byStatus map[string]int) QueueStats {
	var qs QueueStats
	for status, n := range byStatus {
		switch status {
		case "pending", "deferred":
			qs.Pending += n
		case "delivering":
			qs.Processing += n
		case "bounced", "expired":
			qs.Failed += n
		}
	}
	return qs
}

// getMessageVolume returns message counts for the last 7 days
func (h *StatsHandler) getMessageVolume() ([]MessageVolumeData, error) {
	var results []struct {
		Date  time.Time
		Count int
	}

	// Query to get message counts grouped by day for the last 7 days.
	// PostgreSQL syntax: DATE_SUB()/`INTERVAL 7 DAY` and DATE() are MySQL-isms
	// that error on Postgres, so this query used to fail on every call and the
	// dashboard silently fell back to all-zero volume.
	err := h.db.Raw(`
		SELECT
			created_at::date as date,
			COUNT(*) as count
		FROM outbound_queue
		WHERE created_at >= NOW() - INTERVAL '7 days'
		GROUP BY created_at::date
		ORDER BY date ASC
	`).Scan(&results).Error

	if err != nil {
		return nil, err
	}

	// Format the results
	volume := make([]MessageVolumeData, 0, len(results))
	for _, r := range results {
		volume = append(volume, MessageVolumeData{
			Date:  r.Date.Format("Jan 02"),
			Count: r.Count,
		})
	}

	// If no data, fill with zeros for last 7 days
	if len(volume) == 0 {
		now := time.Now()
		for i := 6; i >= 0; i-- {
			d := now.AddDate(0, 0, -i)
			volume = append(volume, MessageVolumeData{
				Date:  d.Format("Jan 02"),
				Count: 0,
			})
		}
	}

	return volume, nil
}

// getRecentActivity returns the last 10 admin actions
func (h *StatsHandler) getRecentActivity() ([]RecentActivity, error) {
	var activities []RecentActivity

	// Query recent domains
	var domains []models.Domain
	if err := h.db.Order("created_at DESC").Limit(3).Find(&domains).Error; err == nil {
		for _, d := range domains {
			if time.Since(d.CreatedAt) < 24*time.Hour {
				activities = append(activities, RecentActivity{
					ID:          fmt.Sprintf("domain_%d", d.ID),
					Type:        "domain_created",
					Description: fmt.Sprintf("Domain %s was created", d.Name),
					Timestamp:   d.CreatedAt,
				})
			}
		}
	}

	// Query recent mailboxes
	var mailboxes []models.Mailbox
	if err := h.db.Order("created_at DESC").Limit(3).Find(&mailboxes).Error; err == nil {
		for _, m := range mailboxes {
			if time.Since(m.CreatedAt) < 24*time.Hour {
				activities = append(activities, RecentActivity{
					ID:          fmt.Sprintf("mailbox_%d", m.ID),
					Type:        "mailbox_created",
					Description: fmt.Sprintf("Mailbox %s was created", m.Address),
					Timestamp:   m.CreatedAt,
				})
			}
		}
	}

	// Query recent messages sent. The queue worker marks successful deliveries
	// "delivered" (never "sent"), so the old filter matched nothing and no sent
	// activity ever appeared on the dashboard.
	var messages []models.OutboundQueue
	if err := h.db.Where("status = ?", "delivered").
		Order("created_at DESC").
		Limit(4).
		Find(&messages).Error; err == nil {
		for _, msg := range messages {
			if time.Since(msg.CreatedAt) < 24*time.Hour {
				activities = append(activities, RecentActivity{
					ID:          fmt.Sprintf("message_%d", msg.ID),
					Type:        "message_sent",
					Description: fmt.Sprintf("Message sent to %s", msg.Recipient),
					Timestamp:   msg.CreatedAt,
				})
			}
		}
	}

	// Sort by timestamp (most recent first)
	// Simple bubble sort since we have few items
	for i := 0; i < len(activities)-1; i++ {
		for j := 0; j < len(activities)-i-1; j++ {
			if activities[j].Timestamp.Before(activities[j+1].Timestamp) {
				activities[j], activities[j+1] = activities[j+1], activities[j]
			}
		}
	}

	// Limit to 10 items
	if len(activities) > 10 {
		activities = activities[:10]
	}

	return activities, nil
}
