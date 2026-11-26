package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

type fakeSession struct {
	status session.SessionStatus
	method string
	args   []string
	err    error
}

func (fake *fakeSession) Status() session.SessionStatus { return fake.status }
func (fake *fakeSession) SignIn(server, email, password string) (session.SignInResult, error) {
	fake.method, fake.args = "SignIn", []string{server, email, password}
	return session.SignInResult{NeedsTOTP: true}, fake.err
}
func (fake *fakeSession) SubmitTOTP(code string) error {
	fake.method, fake.args = "SubmitTOTP", []string{code}
	return fake.err
}
func (fake *fakeSession) SignOut() error {
	fake.method, fake.args = "SignOut", []string{}
	return fake.err
}

type fakeMail struct {
	method string
	args   []any
	err    error
	calls  int
}

func (fake *fakeMail) record(method string, args ...any) error {
	fake.method, fake.args = method, args
	fake.calls++
	return fake.err
}
func (fake *fakeMail) ListFolders(ctx context.Context, account uint) ([]apitypes.FolderSummary, error) {
	return []apitypes.FolderSummary{}, fake.record("ListFolders", account)
}
func (fake *fakeMail) ListMessages(ctx context.Context, account uint, folder string, page apiclient.Page, query string) (apiclient.MessagePage, error) {
	return apiclient.MessagePage{Data: []apitypes.Message{}, Pagination: &apiclient.Pagination{Cursor: "next", HasMore: true}}, fake.record("ListMessages", account, folder, page, query)
}
func (fake *fakeMail) GetMessage(ctx context.Context, id uint) (apitypes.Message, error) {
	return apitypes.Message{}, fake.record("GetMessage", id)
}
func (fake *fakeMail) Search(ctx context.Context, account uint, query, folder string, page apiclient.Page) (apiclient.MessagePage, error) {
	return apiclient.MessagePage{}, fake.record("Search", account, query, folder, page)
}
func (fake *fakeMail) SendMessage(ctx context.Context, message apitypes.SendMessageRequest) error {
	return fake.record("SendMessage", message)
}
func (fake *fakeMail) SaveDraft(ctx context.Context, id uint, draft apiclient.Draft) (apitypes.Message, error) {
	return apitypes.Message{}, fake.record("SaveDraft", id, draft)
}
func (fake *fakeMail) SetFlags(ctx context.Context, ids []uint, flags apitypes.UpdateMessageBody) error {
	return fake.record("SetFlags", ids, flags)
}
func (fake *fakeMail) MoveMessages(ctx context.Context, ids []uint, folder string) error {
	return fake.record("MoveMessages", ids, folder)
}
func (fake *fakeMail) DeleteMessages(ctx context.Context, ids []uint, permanent bool) error {
	return fake.record("DeleteMessages", ids, permanent)
}

func TestTypedDomainToolsOverHTTP(t *testing.T) {
	seen, flagged := false, true
	expectations := map[string]struct {
		method string
		args   any
	}{
		"sign_in":         {"SignIn", []string{"https://mail.test", "a@mail.test", "password-value"}},
		"submit_totp":     {"SubmitTOTP", []string{"123456"}},
		"sign_out":        {"SignOut", []string{}},
		"list_folders":    {"ListFolders", []any{uint(7)}},
		"list_messages":   {"ListMessages", []any{uint(7), "INBOX", apiclient.Page{Cursor: "next", Limit: 50}, "hello"}},
		"get_message":     {"GetMessage", []any{uint(42)}},
		"search":          {"Search", []any{uint(7), "hello", "", apiclient.Page{Limit: 50}}},
		"send_message":    {"SendMessage", []any{apitypes.SendMessageRequest{From: "from@mail.test", To: []string{"to@mail.test"}, Cc: []string{"cc@mail.test"}, Subject: "hello", BodyText: "plain", BodyHTML: "<p>html</p>"}}},
		"save_draft":      {"SaveDraft", []any{uint(0), apiclient.Draft{From: "from@mail.test", To: []string{"to@mail.test"}, Subject: "draft", BodyText: "plain"}}},
		"set_flags":       {"SetFlags", []any{[]uint{1, 2}, apitypes.UpdateMessageBody{IsRead: &seen, IsFlagged: &flagged}}},
		"move_messages":   {"MoveMessages", []any{[]uint{1, 2}, "Archive"}},
		"delete_messages": {"DeleteMessages", []any{[]uint{1, 2}, false}},
	}
	for _, test := range domainCases {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeSession{status: session.SessionStatus{SignedIn: true, Server: "https://mail.test", Email: "from@mail.test", AccountID: 7}}
			mail := &fakeMail{}
			_, discovery := startTestServer(t, Options{Domain: NewDomain(manager, mail)})
			client := connect(t, discovery)
			var args map[string]any
			json.Unmarshal([]byte(test.args), &args)
			result := call(t, client, test.name, args)
			if result.IsError {
				t.Fatal(resultText(t, result))
			}
			if test.name == "session_status" {
				if !strings.Contains(resultText(t, result), "from@mail.test") {
					t.Fatal("session status missing email")
				}
				return
			}
			expectation := expectations[test.name]
			actualMethod := mail.method
			var actualArgs any = mail.args
			if manager.method != "" {
				actualMethod, actualArgs = manager.method, manager.args
			}
			if actualMethod != expectation.method || !jsonEqual(actualArgs, expectation.args) {
				t.Fatalf("got %s %#v, want %s %#v", actualMethod, actualArgs, expectation.method, expectation.args)
			}
			if test.name == "list_messages" && mail.calls != 2 {
				t.Fatal("numbered paging did not follow cursor")
			}
			manager.err, mail.err = &session.APIError{Status: 401, Message: "Bearer SECRET"}, &apiclient.Error{Status: 401, Code: "SECRET"}
			result = call(t, client, test.name, args)
			if !result.IsError || !strings.Contains(resultText(t, result), "unauthorized") || strings.Contains(resultText(t, result), "SECRET") {
				t.Fatalf("unsafe typed error: %s", resultText(t, result))
			}
		})
	}
}

func jsonEqual(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	var leftValue, rightValue any
	json.Unmarshal(leftJSON, &leftValue)
	json.Unmarshal(rightJSON, &rightValue)
	return reflect.DeepEqual(leftValue, rightValue)
}

func TestDomainValidationAndSafeErrors(t *testing.T) {
	manager := &fakeSession{}
	domain := NewDomain(manager, &fakeMail{})
	if _, err := domain.Call(t.Context(), "list_folders", json.RawMessage(`{}`)); !errors.Is(err, session.ErrSignedOut) {
		t.Fatalf("signed-out call: %v", err)
	}
	manager.status.SignedIn = true
	for _, test := range []struct{ name, args string }{
		{"sign_in", `{`}, {"submit_totp", `{`}, {"list_messages", `{"folder":"INBOX","page":-1}`},
		{"list_messages", `{"folder":"INBOX","page":101}`}, {"list_messages", `{}`},
		{"get_message", `{"id":"0"}`}, {"get_message", `{"id":"-1"}`}, {"get_message", `{"id":"abc"}`},
		{"set_flags", `{"ids":["1"]}`}, {"set_flags", `{"ids":[],"seen":true}`},
		{"move_messages", `{"ids":["1"]}`}, {"delete_messages", `{"ids":["bad"]}`},
	} {
		if _, err := domain.Call(t.Context(), test.name, json.RawMessage(test.args)); !errors.Is(err, errInvalidArguments) {
			t.Fatalf("%s %s: %v", test.name, test.args, err)
		}
	}
	for _, test := range []struct {
		err  error
		code string
	}{
		{session.ErrSignedOut, "signed_out"}, {errInvalidArguments, "invalid_arguments"},
		{context.DeadlineExceeded, "domain_timeout"}, {context.Canceled, "cancelled"},
		{&apiclient.Error{Status: 400}, "invalid_arguments"}, {&apiclient.Error{Status: 422}, "invalid_arguments"},
		{&apiclient.Error{Status: 403}, "forbidden"}, {&apiclient.Error{Status: 404}, "not_found"},
		{&apiclient.Error{Status: 409}, "conflict"}, {&apiclient.Error{Status: 429}, "rate_limited"},
		{&apiclient.Error{Status: 500}, "domain_error"},
	} {
		if got := domainErrorCode(test.err); got != test.code {
			t.Fatalf("error %v: %s, want %s", test.err, got, test.code)
		}
	}
}
