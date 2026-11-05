package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestDraftThreadLinkRemainsUsable(t *testing.T) {
	fixture := newCredentialFixture(t)
	created := fixture.do(http.MethodPost, "/api/v1/messages/draft", fixture.session,
		fmt.Sprintf(`{"from":%q,"subject":"draft thread link"}`, fixture.address))
	if created.Code != http.StatusCreated {
		t.Fatalf("save draft: %d %s", created.Code, created.Body.String())
	}
	var draft struct {
		Data struct {
			ID uint `json:"id"`
		}
	}
	if err := json.Unmarshal(created.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	var firstThread string
	for _, update := range []bool{false, true} {
		if update {
			response := fixture.do(http.MethodPut, fmt.Sprintf("/api/v1/messages/draft/%d", draft.Data.ID), fixture.session, `{"in_reply_to":"","references":""}`)
			if response.Code != http.StatusOK {
				t.Fatalf("update draft: %d %s", response.Code, response.Body.String())
			}
		}
		message := fixture.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", draft.Data.ID), fixture.session, "")
		if message.Code != http.StatusOK {
			t.Fatalf("get message: %d %s", message.Code, message.Body.String())
		}
		var linked struct {
			Data struct {
				MailboxID uint   `json:"mailbox_id"`
				ThreadID  string `json:"thread_id"`
			}
		}
		if err := json.Unmarshal(message.Body.Bytes(), &linked); err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/api/v1/accounts/%d/threads/%s", linked.Data.MailboxID, url.PathEscape(linked.Data.ThreadID))
		thread := fixture.do(http.MethodGet, path, fixture.session, "")
		if thread.Code != http.StatusOK {
			t.Fatalf("follow getMessage -> getThread: %s returned %d %s", path, thread.Code, thread.Body.String())
		}
		var messages struct {
			Data []struct {
				ID uint `json:"id"`
			}
		}
		if err := json.Unmarshal(thread.Body.Bytes(), &messages); err != nil {
			t.Fatal(err)
		}
		if len(messages.Data) != 1 || messages.Data[0].ID != draft.Data.ID {
			t.Fatalf("linked thread does not contain its draft: %s", thread.Body.String())
		}
		if update && linked.Data.ThreadID != firstThread {
			t.Fatalf("clearing absent reply headers changed thread: %q -> %q", firstThread, linked.Data.ThreadID)
		}
		firstThread = linked.Data.ThreadID
	}
}
