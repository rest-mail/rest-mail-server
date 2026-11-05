package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// draftRecipientAnswers are what the description of saveDraftBody and updateDraftBody is held to
// (#1163), for a to or a cc, which the handlers treat alike. An address is what net/mail reads as
// one address. The description says idn-email, and the operations' words say a list may also hold
// the wider forms here.
var draftRecipientAnswers = []struct {
	name, item string
	accepted   bool
}{
	{"bare", `a@example.test`, true},
	{"internationalized", `用户@例子.广告`, true},
	{"internationalized domain", `user@bücher.example`, true},
	{"dotless domain", `a@localhost`, true},
	{"display name", `Someone <a@example.test>`, true},
	{"angle brackets", `<a@example.test>`, true},
	{"quoted local part", `\"a b\"@example.test`, true},
	{"surrounding space", ` a@example.test `, true},
	{"empty", ``, false},
	{"blank", `  `, false},
	{"not an address", `not-an-address`, false},
	{"two addresses", `a@example.test, b@example.test`, false},
	{"header injection", `a@example.test\r\nBcc: c@example.test`, false},
}

func TestDraftFields(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := ensureRBAC(db); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	own, foreign := fx.ownerMailbox.Address, fx.attackerMailbox.Address
	idn := models.Mailbox{DomainID: fx.ownerMailbox.DomainID, LocalPart: "用户", Address: "用户@idor.test", Active: true}
	if err := tx.Create(&idn).Error; err != nil {
		t.Fatal(err)
	}
	var role models.Role
	if err := tx.Where("name = ?", models.RoleOwner).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: fx.ownerMailbox.ID, RoleID: role.ID,
		Scope: grants.Mailbox(idn.Address), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageHandler(tx, nil, nil, "", nil)
	save := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		composeRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/messages/draft", fx.ownerMailbox.ID, nil, body))
		return rr
	}
	saved := models.Message{MailboxID: fx.ownerMailbox.ID, Folder: "Drafts", IsDraft: true, IsRead: true, Sender: own, Subject: "kept",
		RecipientsTo: models.JSONB(`["to@example.test"]`), RecipientsCc: models.JSONB(`["cc@example.test"]`)}
	if err := tx.Create(&saved).Error; err != nil {
		t.Fatal(err)
	}
	const originalTo, originalCc = `["to@example.test"]`, `["cc@example.test"]`
	updateAt := func(id string, accountID uint, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		composeRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPut, "/api/v1/messages/draft/"+id, accountID, nil, body))
		return rr
	}
	// update answers on the same draft each time, as it was saved, and returns what it holds after.
	update := func(t *testing.T, body string) (*httptest.ResponseRecorder, models.Message) {
		t.Helper()
		if err := tx.Model(&models.Message{}).Where("id = ?", saved.ID).Updates(map[string]any{"sender": own, "mailbox_id": fx.ownerMailbox.ID,
			"recipients_to": models.JSONB(originalTo), "recipients_cc": models.JSONB(originalCc)}).Error; err != nil {
			t.Fatal(err)
		}
		rr := updateAt(uintToStr(saved.ID), fx.ownerMailbox.ID, body)
		var after models.Message
		if err := tx.First(&after, saved.ID).Error; err != nil {
			t.Fatal(err)
		}
		return rr, after
	}
	refused := func(t *testing.T, rr *httptest.ResponseRecorder, status int, says string) {
		t.Helper()
		if rr.Code != status || !strings.Contains(rr.Body.String(), says) {
			t.Fatalf("got %d %s, want %d saying %q", rr.Code, rr.Body, status, says)
		}
	}
	const notAllowedToSend = "You are not authorized to send from this address"

	for _, field := range []string{"to", "cc"} {
		kept := map[string]string{"to": originalTo, "cc": originalCc}[field]
		stored := func(m models.Message) string {
			if field == "to" {
				return string(m.RecipientsTo)
			}
			return string(m.RecipientsCc)
		}
		for _, tc := range draftRecipientAnswers {
			body := `{"` + field + `":["` + tc.item + `"]}`
			t.Run("saveDraft/"+field+"/"+tc.name, func(t *testing.T) {
				rr := save(body)
				if tc.accepted {
					if rr.Code != http.StatusCreated {
						t.Fatalf("got %d %s, want 201", rr.Code, rr.Body)
					}
					return
				}
				refused(t, rr, http.StatusUnprocessableEntity, "invalid recipient address in "+field)
			})
			t.Run("updateDraft/"+field+"/"+tc.name, func(t *testing.T) {
				rr, after := update(t, body)
				if tc.accepted {
					if rr.Code != http.StatusOK || stored(after) == kept {
						t.Fatalf("got %d %s, stored %s", rr.Code, rr.Body, stored(after))
					}
					return
				}
				refused(t, rr, http.StatusUnprocessableEntity, "invalid recipient address in "+field)
				if stored(after) != kept {
					t.Errorf("a refused update stored %s", stored(after))
				}
			})
		}
		t.Run("saveDraft/"+field+"/omitted or empty list", func(t *testing.T) {
			for _, body := range []string{`{}`, `{"` + field + `":[]}`} {
				if rr := save(body); rr.Code != http.StatusCreated {
					t.Fatalf("%s: got %d %s, want 201", body, rr.Code, rr.Body)
				}
			}
		})
		t.Run("updateDraft/"+field+"/omitted leaves it, an empty list clears it", func(t *testing.T) {
			if rr, after := update(t, `{}`); rr.Code != http.StatusOK || stored(after) != kept {
				t.Fatalf("omitted: got %d, stored %s", rr.Code, stored(after))
			}
			if rr, after := update(t, `{"`+field+`":[]}`); rr.Code != http.StatusOK || stored(after) != "[]" {
				t.Fatalf("empty: got %d, stored %s", rr.Code, stored(after))
			}
		})
		t.Run(field+"/null and wrong types are refused while binding", func(t *testing.T) {
			for body, says := range map[string]string{
				`{"` + field + `":null}`:       field + `":"must not be null"`,
				`{"` + field + `":[null]}`:     field + `[0]":"must not be null"`,
				`{"` + field + `":[1]}`:        field + `[0]":"must be a string"`,
				`{"` + field + `":"a@b.test"}`: field + `":"must be an array"`,
			} {
				refused(t, save(body), http.StatusUnprocessableEntity, says)
				rr, _ := update(t, body)
				refused(t, rr, http.StatusUnprocessableEntity, says)
			}
		})
	}

	t.Run("bcc is refused while binding", func(t *testing.T) {
		const body, says = `{"bcc":["a@example.test"]}`, `"bcc":"not allowed"`
		refused(t, save(body), http.StatusUnprocessableEntity, says)
		rr, _ := update(t, body)
		refused(t, rr, http.StatusUnprocessableEntity, says)
	})

	t.Run("saveDraft/from", func(t *testing.T) {
		// Omitted and empty are the same: the caller's own mailbox, with no sender.
		for _, body := range []string{`{}`, `{"from":""}`} {
			rr := save(body)
			var response struct {
				Data struct {
					MailboxID uint   `json:"mailbox_id"`
					Sender    string `json:"sender"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || rr.Code != http.StatusCreated {
				t.Fatalf("%s: got %d %s (%v)", body, rr.Code, rr.Body, err)
			}
			if response.Data.MailboxID != fx.ownerMailbox.ID || response.Data.Sender != "" {
				t.Errorf("%s: saved %+v", body, response.Data)
			}
		}
		for _, address := range []string{own, idn.Address} {
			if rr := save(`{"from":"` + address + `"}`); rr.Code != http.StatusCreated {
				t.Errorf("from %s: got %d %s, want 201", address, rr.Code, rr.Body)
			}
		}
		for _, from := range []string{"   ", "not-an-address", "nobody@example.test", foreign, "Owner <" + own + ">"} {
			refused(t, save(`{"from":"`+from+`"}`), http.StatusForbidden, notAllowedToSend)
		}
		refused(t, save(`{"from":null}`), http.StatusUnprocessableEntity, `"from":"must not be null"`)
		refused(t, save(`{"from":1}`), http.StatusUnprocessableEntity, `"from":"must be a string"`)
	})

	t.Run("updateDraft/from", func(t *testing.T) {
		// Omitted and null leave the sender; empty clears it, and leaves the mailbox.
		for _, body := range []string{`{}`, `{"from":null}`} {
			if rr, after := update(t, body); rr.Code != http.StatusOK || after.Sender != own {
				t.Errorf("%s: got %d, sender %q", body, rr.Code, after.Sender)
			}
		}
		if rr, after := update(t, `{"from":""}`); rr.Code != http.StatusOK || after.Sender != "" || after.MailboxID != fx.ownerMailbox.ID {
			t.Errorf(`empty from: got %d, sender %q in mailbox %d`, rr.Code, after.Sender, after.MailboxID)
		}
		if rr, after := update(t, `{"from":"`+idn.Address+`"}`); rr.Code != http.StatusOK || after.Sender != idn.Address || after.MailboxID != idn.ID {
			t.Errorf("internationalized from: got %d, sender %q in mailbox %d", rr.Code, after.Sender, after.MailboxID)
		}
		for _, from := range []string{"   ", "not-an-address", "nobody@example.test", foreign, "Owner <" + own + ">"} {
			rr, after := update(t, `{"from":"`+from+`"}`)
			refused(t, rr, http.StatusForbidden, notAllowedToSend)
			if after.Sender != own {
				t.Errorf("a refused update stored sender %q", after.Sender)
			}
		}
		rr, _ := update(t, `{"from":1}`)
		refused(t, rr, http.StatusUnprocessableEntity, `"from":"must be a string"`)
	})

	// The order of the refusals, which the description must not move.
	t.Run("order", func(t *testing.T) {
		refused(t, save(`{"to":["x"],"cc":["y"],"from":"`+foreign+`"}`), http.StatusUnprocessableEntity, "invalid recipient address in to")
		refused(t, save(`{"cc":["y"],"from":"`+foreign+`"}`), http.StatusUnprocessableEntity, "invalid recipient address in cc")
		rr, _ := update(t, `{"to":["x"],"cc":["y"],"from":"`+foreign+`"}`)
		refused(t, rr, http.StatusUnprocessableEntity, "invalid recipient address in to")
		rr, _ = update(t, `{"cc":["y"],"from":"`+foreign+`"}`)
		refused(t, rr, http.StatusUnprocessableEntity, "invalid recipient address in cc")
		// A recipient is read only once the draft is found to be the caller's, and a wrong type
		// is refused before the draft is looked for at all.
		refused(t, updateAt("2147483000", fx.ownerMailbox.ID, `{"to":["x"]}`), http.StatusNotFound, "Draft not found")
		refused(t, updateAt(uintToStr(saved.ID), fx.attackerMailbox.ID, `{"to":["x"]}`), http.StatusNotFound, "Message not found")
		refused(t, updateAt("2147483000", fx.ownerMailbox.ID, `{"to":[1]}`), http.StatusUnprocessableEntity, "must be a string")
	})
}
