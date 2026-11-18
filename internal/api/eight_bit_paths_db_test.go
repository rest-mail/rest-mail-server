package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// holdEverythingFromRemote makes the domain's pipeline hold mail its sender's SPF refuses, as
// a real policy does, and has remote.test's SPF refuse everyone.
func (s *eightBitServer) holdEverythingFromRemote(t *testing.T) {
	t.Helper()
	serveTXT(t, map[string][]string{"remote.test": {"v=spf1 -all"}})
	config, _ := json.Marshal(map[string]string{"fail_action": "quarantine"})
	filters, _ := json.Marshal([]pipeline.FilterConfig{{Name: "spf_check", Type: pipeline.FilterTypeAction, Enabled: true, Config: config}})
	if err := s.db.Model(&models.Pipeline{}).Where("direction = ?", "inbound").Update("filters", filters).Error; err != nil {
		t.Fatal(err)
	}
}

// release lists the mailbox's quarantine, expects exactly one held message whose source is message,
// and releases it through the API.
func (s *eightBitServer) release(t *testing.T, message string) {
	t.Helper()
	var messages int64
	if err := s.db.Model(&models.Message{}).Where("mailbox_id = ?", s.mailboxID).Count(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if messages != 0 {
		t.Fatalf("%d messages in the mailbox before release", messages)
	}
	listed := s.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/quarantine", s.accountID), s.session, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list quarantine: %d %s", listed.Code, listed.Body)
	}
	var list struct {
		Data []struct {
			ID          uint   `json:"id"`
			Subject     string `json:"subject"`
			BodyPreview string `json:"body_preview"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatalf("%v: %s", err, listed.Body)
	}
	if len(list.Data) != 1 {
		t.Fatalf("held messages = %d: %s", len(list.Data), listed.Body)
	}
	var held models.Quarantine
	if err := s.db.First(&held, list.Data[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(held.RawMessage, message) {
		t.Fatalf("held message changed:\n got %q\nwant suffix %q", held.RawMessage, message)
	}
	var columns struct {
		RawMessage string
		RawOctets  []byte
	}
	if err := s.db.Raw("SELECT raw_message, raw_octets FROM quarantine WHERE id = ?", held.ID).Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if models.SourceFitsText(message) != (columns.RawOctets == nil) {
		t.Fatalf("raw_message %d octets, raw_octets %d octets, for %q", len(columns.RawMessage), len(columns.RawOctets), message)
	}
	released := s.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/quarantine/%d/release", s.accountID, held.ID), s.session, "")
	if released.Code != http.StatusOK {
		t.Fatalf("release: %d %s", released.Code, released.Body)
	}
	listed = s.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/quarantine", s.accountID), s.session, "")
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil || len(list.Data) != 0 {
		t.Fatalf("still held after release: %v: %s", err, listed.Body)
	}
}

// resetMailbox empties what one shape left behind, so each starts from nothing.
func (s *eightBitServer) resetMailbox(t *testing.T) {
	t.Helper()
	for _, table := range []string{"quarantine", "messages"} {
		if err := s.db.Exec("DELETE FROM "+table+" WHERE mailbox_id = ?", s.mailboxID).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestEightBitMailHeldThenReleased: mail that policy holds, by real SMTP and by RESTMAIL, is held
// and released through the real routes, then reads back with exact octets through the API, IMAP
// and POP3, as a message that was never held does.
func TestEightBitMailHeldThenReleased(t *testing.T) {
	useRealGrants(t)
	s := newEightBitServer(t)
	s.own(s.mailboxID, s.address)
	s.holdEverythingFromRemote(t)
	password := s.createAppPassword("reader").Password
	for _, shape := range eightBitShapes() {
		t.Run("SMTP "+shape.name, func(t *testing.T) {
			s.t = t
			s.resetMailbox(t)
			reply, status, body := s.deliver("sender@remote.test", s.address, shape.message)
			if reply != "250" {
				t.Fatalf("SMTP %s, API %v %s", reply, status, body)
			}
			s.release(t, shape.message)
			s.proveStored(shape.message, password)
		})
		t.Run("RESTMAIL "+shape.name, func(t *testing.T) {
			s.t = t
			s.resetMailbox(t)
			payload, _ := json.Marshal(map[string]any{"from": "sender@remote.test", "to": []string{s.address}, "raw_message": []byte(shape.message)})
			rr := s.do(http.MethodPost, "/restmail/messages", "", string(payload))
			if rr.Code != http.StatusCreated {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
			var answer struct {
				Data struct {
					Delivered []string `json:"delivered"`
					Failed    []string `json:"failed"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			if len(answer.Data.Delivered) != 1 || len(answer.Data.Failed) != 0 {
				t.Fatalf("a hold told the peer: %s", rr.Body)
			}
			s.release(t, shape.message)
			s.proveStored(shape.message, password)
		})
	}
}
