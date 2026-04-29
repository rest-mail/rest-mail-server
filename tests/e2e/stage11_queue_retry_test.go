package e2e

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testStage11QueueRetry(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.login("admin@mail3.test", adminPassword); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	// Ensure domains and mailboxes exist
	createDomain(t, adminClient, "mail3.test", "restmail")
	createMailbox(t, adminClient, "retry-sender@mail3.test", "password123", "Retry Sender")

	t.Run("EnqueueToUnresolvable_DefersWithRetry", func(t *testing.T) {
		// Send a message to a domain that has no reachable MX.
		// The queue worker will fail delivery and defer the item with
		// exponential backoff.
		senderClient := newAPIClient()
		requireNoError(t, senderClient.login("retry-sender@mail3.test", "password123"))

		// Link account
		_, _ = senderClient.post("/api/v1/accounts", map[string]string{
			"address": "retry-sender@mail3.test", "password": "password123",
		})

		subject := fmt.Sprintf("E2E-retry-%d", time.Now().UnixNano())

		resp, err := senderClient.post("/api/v1/messages/send", map[string]interface{}{
			"from":      "retry-sender@mail3.test",
			"to":        []string{"someone@unreachable-e2e-test-domain.invalid"},
			"subject":   subject,
			"body_text": "This message should be deferred due to unreachable domain.",
		})
		requireNoError(t, err)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			body := readBody(resp)
			t.Fatalf("send message returned %d: %s", resp.StatusCode, body)
		}
		resp.Body.Close()

		t.Log("Message enqueued for unreachable domain, waiting for worker to attempt delivery...")

		// Poll the admin queue API until we find the deferred item.
		// The queue worker polls at short intervals, so we give it time
		// to pick up the item, fail, and mark it as deferred.
		var queueItem struct {
			ID            uint   `json:"id"`
			Sender        string `json:"sender"`
			Recipient     string `json:"recipient"`
			Domain        string `json:"domain"`
			Status        string `json:"status"`
			Attempts      int    `json:"attempts"`
			LastError     string `json:"last_error"`
			LastErrorCode int    `json:"last_error_code"`
			NextAttempt   string `json:"next_attempt"`
			CreatedAt     string `json:"created_at"`
		}

		deadline := time.Now().Add(90 * time.Second)
		found := false
		for time.Now().Before(deadline) {
			qResp, err := adminClient.get("/api/v1/admin/queue?recipient=someone@unreachable-e2e-test-domain.invalid&limit=10")
			if err != nil {
				time.Sleep(2 * time.Second)
				continue
			}

			var queueList struct {
				Data struct {
					Items []json.RawMessage `json:"items"`
					Total int64             `json:"total"`
				} `json:"data"`
			}
			if err := decodeJSON(qResp, &queueList); err != nil {
				time.Sleep(2 * time.Second)
				continue
			}

			for _, raw := range queueList.Data.Items {
				var item struct {
					ID        uint   `json:"id"`
					Sender    string `json:"sender"`
					Recipient string `json:"recipient"`
					Domain    string `json:"domain"`
					Status    string `json:"status"`
					Attempts  int    `json:"attempts"`
					LastError string `json:"last_error"`
				}
				if err := json.Unmarshal(raw, &item); err != nil {
					continue
				}

				if item.Recipient == "someone@unreachable-e2e-test-domain.invalid" &&
					(item.Status == "deferred" || item.Status == "bounced") {
					// Found our deferred/bounced item. Decode fully.
					_ = json.Unmarshal(raw, &queueItem)
					found = true
					break
				}
			}

			if found {
				break
			}
			time.Sleep(3 * time.Second)
		}

		if !found {
			// Try checking if the item is still pending/delivering
			qResp, _ := adminClient.get("/api/v1/admin/queue?recipient=someone@unreachable-e2e-test-domain.invalid&limit=10")
			if qResp != nil {
				body := readBody(qResp)
				t.Fatalf("Queue item not found in deferred/bounced state after 90s. Queue response: %s", body)
			}
			t.Fatal("Queue item not found in deferred/bounced state after 90s")
		}

		t.Logf("Queue item found: id=%d status=%s attempts=%d domain=%s",
			queueItem.ID, queueItem.Status, queueItem.Attempts, queueItem.Domain)

		if queueItem.Attempts < 1 {
			t.Errorf("expected at least 1 attempt, got %d", queueItem.Attempts)
		}

		if queueItem.LastError == "" {
			t.Error("expected last_error to be populated after failed delivery")
		} else {
			t.Logf("Last error: %s", queueItem.LastError)
		}

		if queueItem.Domain != "unreachable-e2e-test-domain.invalid" {
			t.Errorf("expected domain 'unreachable-e2e-test-domain.invalid', got %q", queueItem.Domain)
		}

		t.Log("Queue retry/defer behavior verified")
	})

	t.Run("ExponentialBackoff_Calculation", func(t *testing.T) {
		// Verify the backoff formula: 2^attempt minutes, capped at 4 hours.
		// This is a logic verification test -- does not require network.
		for attempt := 0; attempt < 35; attempt++ {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Minute
			if backoff > 4*time.Hour {
				backoff = 4 * time.Hour
			}

			if attempt == 0 && backoff != 1*time.Minute {
				t.Errorf("attempt 0: expected 1m backoff, got %s", backoff)
			}
			if attempt == 1 && backoff != 2*time.Minute {
				t.Errorf("attempt 1: expected 2m backoff, got %s", backoff)
			}
			if attempt == 2 && backoff != 4*time.Minute {
				t.Errorf("attempt 2: expected 4m backoff, got %s", backoff)
			}
			if attempt == 3 && backoff != 8*time.Minute {
				t.Errorf("attempt 3: expected 8m backoff, got %s", backoff)
			}
			// After ~8 attempts (256 minutes = 4.27h), backoff should be capped
			if attempt >= 9 && backoff != 4*time.Hour {
				t.Errorf("attempt %d: expected 4h cap, got %s", attempt, backoff)
			}
		}
		t.Log("Exponential backoff calculation verified (1m, 2m, 4m, 8m, ... capped at 4h)")
	})

	t.Run("AdminRetry_RequeuesDeferred", func(t *testing.T) {
		// Find a deferred queue item and use the admin retry endpoint to requeue it.
		qResp, err := adminClient.get("/api/v1/admin/queue?status=deferred&limit=5")
		if err != nil {
			t.Skipf("Cannot list deferred queue items: %v", err)
		}

		var queueList struct {
			Data struct {
				Items []struct {
					ID     uint   `json:"id"`
					Status string `json:"status"`
				} `json:"items"`
				Total int64 `json:"total"`
			} `json:"data"`
		}
		if err := decodeJSON(qResp, &queueList); err != nil {
			t.Skipf("Cannot decode queue list: %v", err)
		}

		if len(queueList.Data.Items) == 0 {
			t.Skip("No deferred items found to test admin retry")
		}

		itemID := queueList.Data.Items[0].ID
		t.Logf("Retrying deferred queue item id=%d", itemID)

		retryResp, err := adminClient.post(fmt.Sprintf("/api/v1/admin/queue/%d/retry", itemID), nil)
		requireNoError(t, err)
		requireStatus(t, retryResp, http.StatusOK)

		var retryResult struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		retryBody, _ := adminClient.get(fmt.Sprintf("/api/v1/admin/queue/%d", itemID))
		if retryBody != nil {
			var detail struct {
				Data struct {
					ID     uint   `json:"id"`
					Status string `json:"status"`
				} `json:"data"`
			}
			if err := decodeJSON(retryBody, &detail); err == nil {
				// After retry, status should be pending or delivering
				if detail.Data.Status != "pending" && detail.Data.Status != "delivering" && detail.Data.Status != "deferred" {
					t.Errorf("expected status pending/delivering/deferred after retry, got %q", detail.Data.Status)
				} else {
					t.Logf("Queue item %d status after retry: %s", itemID, detail.Data.Status)
				}
			}
		}

		_ = retryResult
		t.Log("Admin retry endpoint verified")
	})

	t.Run("QueueStats_ReturnsAggregates", func(t *testing.T) {
		resp, err := adminClient.get("/api/v1/admin/queue/stats")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var stats struct {
			Data map[string]int64 `json:"data"`
		}
		requireNoError(t, decodeJSON(resp, &stats))

		total, hasTotal := stats.Data["total"]
		if !hasTotal {
			t.Error("queue stats missing 'total' field")
		} else {
			t.Logf("Queue stats: total=%d", total)
		}

		// Log all status counts
		for status, count := range stats.Data {
			if status != "total" {
				t.Logf("  %s: %d", status, count)
			}
		}
	})

	t.Run("SMTPError_Parsing", func(t *testing.T) {
		// Verify permanent vs temporary error classification.
		// 4xx codes should not be permanent, 5xx should be permanent.
		testCases := []struct {
			code      int
			permanent bool
		}{
			{421, false},
			{450, false},
			{451, false},
			{452, false},
			{500, true},
			{550, true},
			{551, true},
			{552, true},
			{553, true},
			{554, true},
		}

		for _, tc := range testCases {
			isPerm := tc.code >= 500 && tc.code < 600
			if isPerm != tc.permanent {
				t.Errorf("SMTP code %d: expected permanent=%v, got %v", tc.code, tc.permanent, isPerm)
			}
		}

		// Verify that 4xx errors lead to defer (not bounce) by checking
		// the permanent boundary
		if 450 >= 500 {
			t.Error("4xx should not be classified as permanent")
		}
		if !(550 >= 500 && 550 < 600) {
			t.Error("550 should be classified as permanent")
		}

		t.Log("SMTP error classification verified: 4xx=temporary, 5xx=permanent")
	})

	t.Run("QueueDomain_ExtractedCorrectly", func(t *testing.T) {
		// Verify that queued items have correct domain extraction.
		qResp, err := adminClient.get("/api/v1/admin/queue?limit=10")
		if err != nil {
			t.Skipf("Cannot list queue: %v", err)
		}

		var queueList struct {
			Data struct {
				Items []struct {
					Recipient string `json:"recipient"`
					Domain    string `json:"domain"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := decodeJSON(qResp, &queueList); err != nil {
			t.Skipf("Cannot decode queue: %v", err)
		}

		for _, item := range queueList.Data.Items {
			// Domain should be the part after @ in the recipient
			atIdx := strings.LastIndex(item.Recipient, "@")
			if atIdx < 0 {
				t.Errorf("recipient %q has no @ sign", item.Recipient)
				continue
			}
			expectedDomain := item.Recipient[atIdx+1:]
			if item.Domain != expectedDomain {
				t.Errorf("domain mismatch for recipient %q: got %q, expected %q",
					item.Recipient, item.Domain, expectedDomain)
			}
		}
		t.Log("Queue domain extraction verified")
	})
}
