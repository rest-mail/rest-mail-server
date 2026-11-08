package api

import (
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestInvitationExistingCredentialsOwnDataHook(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("contract hooks require python3")
	}
	useRealGrants(t)
	fixture := newCredentialFixtureWith(t, &config.Config{
		Environment: "test", TOTP2FAEnabled: true, MasterKey: accessMasterKey,
	})
	restmailTestDomain(fixture, true)
	server := httptest.NewServer(fixture.router)
	defer server.Close()
	hookFile, err := filepath.Abs("../../tests/contract/hooks.py")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", invitationExistingCredentialsHookScript,
		hookFile, server.URL, strings.TrimPrefix(twoFactorSession(fixture), "Bearer "))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("invitation own-data hook: %v\n%s", err, output)
	}
	var result struct {
		AccountID uint   `json:"account_id"`
		Password  string `json:"password"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode hook result: %v\n%s", err, output)
	}
	var account models.Mailbox
	if err := fixture.db.First(&account, result.AccountID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(account.Address, "credentialed-") || !strings.HasSuffix(account.Address, "@restmail.test") {
		t.Fatalf("the hook did not create its disposable account: %s", account.Address)
	}
	var credential models.Credential
	if err := fixture.db.Where("account_id = ? AND kind = ? AND revoked_at IS NULL",
		account.ID, models.CredentialKindPassword).First(&credential).Error; err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckPassword(result.Password, credential.SecretHash); err != nil {
		t.Fatalf("the fixture account does not hold the hook's password: %v", err)
	}
	var invitations int64
	if err := fixture.db.Model(&models.OneTimeToken{}).Where("account_id = ? AND purpose = ?",
		account.ID, models.OneTimeTokenInvitation).Count(&invitations).Error; err != nil {
		t.Fatal(err)
	}
	if invitations != 0 {
		t.Fatalf("the refused invitation stored %d invitation tokens", invitations)
	}
}

const invitationExistingCredentialsHookScript = `
import importlib.util, json, os, sys, types, urllib.error, urllib.parse, urllib.request
shim = types.ModuleType("vertrag_hooks")
shim.before = shim.after = lambda operation: lambda callback: callback
shim.stash = {}
shim.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
shim.set_json = lambda transaction, body: transaction["request"].__setitem__("body", json.dumps(body))
sys.modules["vertrag_hooks"] = shim
spec = importlib.util.spec_from_file_location("invitation_contract_hooks", sys.argv[1])
hooks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hooks)
origin, token = sys.argv[2:]
hooks.mfa_admin_token = lambda requested_origin: token
clean_starts = []
hooks.start_clean = lambda transaction: clean_starts.append(transaction)
os.environ["CONTRACT_RESET_SEED"] = "1"
url = urllib.parse.urlsplit(origin)
transaction = {
    "protocol": url.scheme + ":", "host": url.hostname, "port": url.port,
    "name": "POST /api/v1/admin/mailboxes/{id}/invitation > 409 > existing_credentials",
    "fullPath": "/api/v1/admin/mailboxes/12/invitation",
    "request": {"headers": {"Authorization": "Bearer " + token}},
}
try:
    hooks.invite_an_account_without_credentials(transaction)
except urllib.error.HTTPError as error:
    raise AssertionError(f"account setup returned HTTP {error.code}: {error.read().decode()}") from error
assert clean_starts == [transaction], clean_starts
assert not transaction.get("skip"), transaction
account_id = int(transaction["fullPath"].split("/")[-2])
request = urllib.request.Request(origin + transaction["fullPath"], data=b"", method="POST",
    headers=transaction["request"]["headers"])
try:
    with urllib.request.urlopen(request, timeout=10) as response:
        raise AssertionError(f"invitation unexpectedly returned HTTP {response.status}: {response.read()!r}")
except urllib.error.HTTPError as error:
    body = json.load(error)
    assert error.code == 409, (error.code, body)
    assert body["error"]["code"] == "account_not_invitable", body
print(json.dumps({"account_id": account_id, "password": hooks.CREDENTIALED_PASSWORD}))
`
