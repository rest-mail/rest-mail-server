package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

func TestCreateFolder_ContractSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					MinLength int `yaml:"minLength"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Components.Schemas["CreateFolderRequest"].Properties["name"].MinLength; got != 1 {
		t.Fatalf("CreateFolderRequest.name minLength = %d, want 1: the handler refuses an empty name", got)
	}
}

// Exercise the real hook against the real handlers: repeated valid creates
// must reach 201, while the hook preserves every generated boundary byte.
func TestCreateFolder_ContractProbes(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("contract hooks require python3")
	}
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	if err := tx.Create(&models.Message{MailboxID: fx.mailbox.ID, Folder: "Existing", Sender: "system", IsDeleted: true}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageHandler(tx, nil, nil, "", nil)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox.ID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Post("/api/v1/auth/login", contractSetupLogin(t))
	router.Post("/api/v1/accounts/{id}/folders", h.CreateFolder)
	router.Get("/api/v1/accounts/{id}/folders", h.ListFolders)
	router.Delete("/api/v1/accounts/{id}/folders/{folder}", h.DeleteFolder)
	server := httptest.NewServer(router)
	defer server.Close()
	hookFile, err := filepath.Abs("../../../tests/contract/hooks.py")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-c", folderCoverageScript, hookFile, server.URL, uintToStr(fx.mailbox.ID))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("folder hook probes: %v\n%s", err, output)
	}
	var results []struct {
		Probe  string `json:"probe"`
		Status int    `json:"status"`
		Want   int    `json:"want"`
	}
	if err := json.Unmarshal(output, &results); err != nil {
		t.Fatalf("decode probes: %v\n%s", err, output)
	}
	if len(results) != 12 {
		t.Fatalf("ran %d probes, want 12", len(results))
	}
	for _, result := range results {
		t.Run(result.Probe, func(t *testing.T) {
			if result.Status != result.Want {
				t.Errorf("status %d, want %d", result.Status, result.Want)
			}
		})
	}
}

// These handler-only servers inject claims instead of running authentication.
// Give the real Python hook a setup session through the same login exchange it
// uses in E2E; scripts/tests/test_contract_setup_auth.py checks credential isolation.
func contractSetupLogin(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode setup sign-in: %v", err)
			http.Error(w, "invalid sign-in", http.StatusBadRequest)
			return
		}
		if body["email"] != "admin@restmail.test" || body["password"] != "admin123!@" || body["client"] != "native" {
			t.Errorf("setup sign-in did not use the fixture admin")
			http.Error(w, "invalid sign-in", http.StatusUnauthorized)
			return
		}
		respond.Data(w, http.StatusOK, map[string]string{"access_token": "handler-test-session"})
	}
}

// The shim supplies vertrag's registration and JSON helpers; preparation and
// HTTP requests run unchanged from hooks.py. No contract-runner installation
// is needed to test the hook's fixture lifecycle.
const folderCoverageScript = `
import copy, json, runpy, sys, types, urllib.error, urllib.parse, urllib.request
hooks = types.ModuleType("vertrag_hooks")
callbacks = {"before": {}, "after": {}}
def register(phase):
    def decorator(operation):
        def keep(function):
            callbacks[phase][operation] = function
            return function
        return keep
    return decorator
hooks.before, hooks.after = register("before"), register("after")
hooks.stash = {}
hooks.get_json = lambda transaction, which="request": json.loads(transaction.get(which, {}).get("body") or "{}")
def set_json(transaction, value):
    transaction["request"]["body"] = json.dumps(value, separators=(",", ":"))
hooks.set_json = set_json
sys.modules["vertrag_hooks"] = hooks
runpy.run_path(sys.argv[1])
origin = sys.argv[2]
url = urllib.parse.urlsplit(origin)
account = sys.argv[3]
probes = [
    ("example", account, '{"name":"Receipts"}', 201),
    ("required name missing", account, '{}', 422),
    ("empty name", account, '{"name":""}', 422),
    ("wrong name type", account, '{"name":12}', 422),
    ("minimum name length", account, '{"name":"a"}', 201),
    ("valid account ID repeated", account, '{"name":"Receipts"}', 201),
    ("minimum name repeated", account, '{"name":"a"}', 201),
    ("below minimum account ID", "0", '{"name":"Receipts"}', 403),
    ("wrong account ID type", "abc", '{"name":"Receipts"}', 400),
    ("fractional account ID", "1.5", '{"name":"Receipts"}', 400),
    ("preexisting folder is preserved", account, '{"name":"Existing"}', 409),
    ("failed create is not tracked", account, '{"name":"Existing"}', 409),
]
results = []
for probe, account_id, body, want in probes:
    transaction = {"fullPath": "/api/v1/accounts/" + account_id + "/folders",
                   "protocol": url.scheme + ":", "host": url.hostname, "port": str(url.port),
                   "request": {"body": body, "headers": {"Content-Type": "application/json"}}}
    original = copy.deepcopy(transaction)
    callbacks["before"].get("createFolder", lambda transaction: None)(transaction)
    assert transaction == original, "hook rewrote the generated probe: " + probe
    request = urllib.request.Request(origin + transaction["fullPath"], data=body.encode(),
                                     headers=transaction["request"]["headers"], method="POST")
    try:
        response = urllib.request.urlopen(request, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        status = response.code
        transaction["real"] = {"body": response.read().decode(), "statusCode": str(status)}
    results.append({"probe": probe, "status": status, "want": want})
print(json.dumps(results))
`
