package handlers

import (
	"fmt"
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

type DashboardStats struct {
	DomainCount    int                 `json:"domainCount"`
	MailboxCount   int                 `json:"mailboxCount"`
	QueueStats     QueueStats          `json:"queueStats"`
	MessageVolume  []MessageVolumeData `json:"messageVolume"`
	RecentActivity []RecentActivity    `json:"recentActivity"`
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

	stats.QueueStats = QueueStats{}
	for _, qc := range queueCounts {
		switch qc.Status {
		case "pending":
			stats.QueueStats.Pending = qc.Count
		case "processing":
			stats.QueueStats.Processing = qc.Count
		case "failed":
			stats.QueueStats.Failed = qc.Count
		}
	}

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

	respond.Data(w, http.StatusOK, stats)
}

// getMessageVolume returns message counts for the last 7 days
func (h *StatsHandler) getMessageVolume() ([]MessageVolumeData, error) {
	var results []struct {
		Date  time.Time
		Count int
	}

	// Query to get message counts grouped by day for the last 7 days
	err := h.db.Raw(`
		SELECT
			DATE(created_at) as date,
			COUNT(*) as count
		FROM outbound_queue
		WHERE created_at >= DATE_SUB(NOW(), INTERVAL 7 DAY)
		GROUP BY DATE(created_at)
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

	// Query recent messages sent
	var messages []models.OutboundQueue
	if err := h.db.Where("status = ?", "sent").
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
