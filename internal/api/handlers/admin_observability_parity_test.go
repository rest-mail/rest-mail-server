package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// The API-owned types preserve the complete existing JSON, including omitted
// expiry, explicit null ids/times, and the audit target's dynamic keys.
func TestAdminObservabilityJSONParity(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 30, 0, 123456789, time.UTC)
	id := uint(17)
	ban := models.Ban{ID: 9, IP: "2001:db8::1", Reason: "refused", Protocol: "all", CreatedBy: "admin", ExpiresAt: &now, CreatedAt: now}
	delivery := models.OutboundQueue{ID: 8, MessageID: &id, Sender: "a@example.com", Recipient: "b@example.net", Domain: "example.net", RawMessage: "private", BodyType: "8BITMIME", Status: "deferred", Attempts: 2, LastAttempt: &now, NextAttempt: now, LastError: "busy", LastErrorCode: 451, FailureCategory: models.FailureCategory("greylisted"), MaxRetries: 30, CreatedAt: now, ExpiresAt: now}
	entry := models.AuditEntry{Seq: 3, OccurredAt: now, ActorKind: "account", ActorID: &id, ActorName: "admin", CredentialID: &id, SessionID: "session", Action: "audit.read", Outcome: "denied", ErrorCode: "forbidden", Method: "GET", Route: "/api/v1/admin/audit", Status: 403, Target: models.AuditTarget{"id": "17"}, IP: "::1", UserAgent: "test", RequestID: "request", PrevHash: "previous", Hash: "hash"}
	cases := []struct {
		name          string
		before, after any
	}{
		{"permanent ban", models.Ban{}, banOf(models.Ban{})}, {"expiring ban", ban, banOf(ban)},
		{"empty delivery", models.OutboundQueue{}, deliveryLogEntryOf(models.OutboundQueue{})}, {"delivery", delivery, deliveryLogEntryOf(delivery)},
		{"empty audit", models.AuditEntry{}, auditEntryOf(models.AuditEntry{})}, {"audit", entry, auditEntryOf(entry)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.before)
			if err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(tc.after)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("JSON changed:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// Recording wraps capability checks, so refused audit reads remain evidence.
func TestRegisteredAuditReadsRecordCapabilityRefusals(t *testing.T) {
	for _, path := range []string{"/api/v1/admin/audit", "/api/v1/admin/audit/verify"} {
		t.Run(path, func(t *testing.T) {
			r := chi.NewRouter()
			var events []string
			checks := AdminLogRouteChecks{
				AuditRead: endpoint.Middleware{Wrap: func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						events = append(events, "audit start")
						next.ServeHTTP(w, r)
						events = append(events, "audit finish")
					})
				}},
				At: func(string) endpoint.Middleware {
					return endpoint.Middleware{Wrap: func(http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							events = append(events, "capability refused")
							respond.Error(w, 403, "forbidden", "Forbidden")
						})
					}}
				},
			}
			NewAuditHandler(nil).Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, checks)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != 403 || !reflect.DeepEqual(events, []string{"audit start", "capability refused", "audit finish"}) {
				t.Fatalf("%d: %v", rr.Code, events)
			}
		})
	}
}

// A list is never null (#622): with no recent activity the dashboard answers [], whether the
// handler left the list nil or empty, and populated activity is unchanged. This replaces the
// null a pointer to the slice used to preserve through endpoint registration, which nothing
// read: the admin dashboard treats null and [] alike, and webmail does not read the field.
func TestDashboardStatsRecentActivityIsNeverNull(t *testing.T) {
	event := RecentActivity{ID: "event", Type: "created", Description: "domain created", Timestamp: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	for _, tc := range []struct {
		name       string
		activities []RecentActivity
		want       string
	}{
		{"nil", nil, `"recentActivity":[]`},
		{"empty", []RecentActivity{}, `"recentActivity":[]`},
		{"populated", []RecentActivity{event}, `"recentActivity":[{"id":"event","type":"created","description":"domain created","timestamp":"2026-10-23T12:00:00Z"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := chi.NewRouter()
			stats := DashboardStats{MessageVolume: []MessageVolumeData{}, RecentActivity: tc.activities}
			endpoint.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, endpoint.Operation{Method: http.MethodGet, Path: "/stats"}, func(*http.Request, struct{}) (DashboardStats, error) { return stats, nil })
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stats", nil))
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), tc.want) {
				t.Fatalf("got %d %s, want %s", rr.Code, rr.Body, tc.want)
			}
		})
	}
}
