package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/imapmail"
)

type fakeAccountSession struct {
	fakeSession
	input        imapmail.AccountInput
	accounts     []imapmail.Account
	removed      uint
	accountError error
}

func (fake *fakeAccountSession) AddIMAPAccount(_ context.Context, input imapmail.AccountInput) (imapmail.Account, error) {
	fake.input = input
	account := imapmail.Account{ID: imapmail.IDBase + 1, Type: "imap", Address: input.Address, Host: input.Host, Username: input.Username}
	if fake.accountError == nil {
		fake.accounts = append(fake.accounts, account)
	}
	return account, fake.accountError
}
func (fake *fakeAccountSession) ListAccounts() []imapmail.Account { return fake.accounts }
func (fake *fakeAccountSession) RemoveIMAPAccount(id uint) error {
	fake.removed = id
	fake.accounts = []imapmail.Account{}
	return fake.accountError
}

func TestIMAPAccountMCPToolsNeverReturnOrLogPasswords(t *testing.T) {
	const password = "MCP-IMAP-secret<&>\\quoted"
	manager := &fakeAccountSession{}
	domain := NewDomain(manager, &fakeMail{})
	server, discovery := startTestServer(t, Options{Domain: domain, Secrets: func() []string { return []string{password} }})
	client := connect(t, discovery)
	input := imapmail.AccountInput{Host: "mail.test", Port: 993, Username: "reader", Password: password, SMTPHost: "smtp.test", SMTPPort: 587, Address: "reader@example.test"}
	result := call(t, client, "add_imap_account", input)
	if result.IsError || manager.input.Password != password {
		t.Fatal("account credentials not passed to native store")
	}
	for _, name := range []string{"list_accounts", "remove_imap_account"} {
		args := any(map[string]any{})
		if name == "remove_imap_account" {
			args = AccountArgs{AccountID: imapmail.IDBase + 1}
		}
		result = call(t, client, name, args)
		if result.IsError || strings.Contains(resultText(t, result), password) || strings.Contains(resultText(t, result), "password") {
			t.Fatalf("unsafe %s result", name)
		}
	}
	if manager.removed != imapmail.IDBase+1 {
		t.Fatal("remove did not reach store")
	}
	manager.accountError = errors.New("upstream echoed " + password)
	result = call(t, client, "add_imap_account", input)
	if !result.IsError || strings.Contains(resultText(t, result), password) {
		t.Fatal("unsafe account error")
	}
	server.Logs().Write([]byte("SMTP refused " + password + "\n"))
	logs := resultText(t, call(t, client, "app_logs", LogsArgs{Lines: 100}))
	if strings.Contains(logs, password) || strings.Contains(logs, "MCP-IMAP-secret") {
		t.Fatal("password reached MCP logs")
	}
}

func TestIMAPAccountMCPValidationAndSelection(t *testing.T) {
	manager := &fakeAccountSession{accounts: []imapmail.Account{{ID: imapmail.IDBase + 1, Address: "reader@example.test"}}}
	manager.status.SignedIn = true
	mail := &fakeMail{}
	domain := NewDomain(manager, mail)
	for _, test := range []struct{ name, args string }{{"add_imap_account", "{"}, {"remove_imap_account", `{}`}, {"remove_imap_account", `{"account_id":-1}`}, {"list_folders", `{"account_id":3}`}} {
		if _, err := domain.Call(t.Context(), test.name, json.RawMessage(test.args)); err == nil {
			t.Fatalf("accepted invalid %s", test.name)
		}
	}
	if _, err := domain.Call(t.Context(), "list_folders", json.RawMessage(`{"account_id":4503599627370497}`)); err != nil {
		t.Fatal(err)
	}
	if mail.method != "ListFolders" || mail.args[0] != imapmail.IDBase+1 {
		t.Fatal("MCP ignored selected IMAP account")
	}
	plain := NewDomain(&fakeSession{}, mail)
	for _, name := range []string{"add_imap_account", "list_accounts", "remove_imap_account"} {
		if _, err := plain.Call(t.Context(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("missing account service accepted %s", name)
		}
	}
}
