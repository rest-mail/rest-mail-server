package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restmail/restmail/tests/contract/fixtureplan"
)

// The principals of the contract tests (#376): one of each kind of caller the
// API has, made here so the run after this suite can act as each of them. The
// contract run itself signs in as the admin; the others are for the
// authorization matrix, which vertrag will check from the spec's security
// roles once it has an authorization phase (antimatter-studios/vertrag#71).
// Until then TestContractPrincipals checks a small matrix of its own.
//
// There is no agent principal: the account model has none yet (#341), so
// there is nothing to make.
// contractPrincipalsFile is where the principals are written, under the
// repository root (mounted at /src by `chore test:e2e`), for
// tests/contract/hooks.py to read. go test runs in the package's directory,
// tests/e2e, so the root is two levels up.
var contractPrincipalsFile = envOr("CONTRACT_PRINCIPALS", "../../.cache/contract-principals.json")

type contractPrincipal struct {
	Address  string `json:"address,omitempty"`
	Password string `json:"password,omitempty"`
	Secret   string `json:"secret,omitempty"`
	ID       uint   `json:"id,omitempty"`
}

// TestContractPrincipals makes the principals and checks each reaches what its
// kind should and nothing more:
//
//	principal                 admin route   own mailbox   owned account
//	admin                     200           200           200 (its owner)
//	ordinary account          403           200           403
//	app password (Basic)      401           200           403
//	owned, no password        cannot sign in
//	no credentials            401           401           401
func TestContractPrincipals(t *testing.T) {
	plan, err := fixtureplan.Default()
	requireNoError(t, err)
	ordinaryPlan, ownedPlan := plan.Principals["ordinary"], plan.Principals["owned"]
	contractOrdinaryAddress, contractOwnedAddress := ordinaryPlan.Address, ownedPlan.Address
	if plan.Principals["admin"].Address != seedAdminAddress {
		t.Fatal("contract fixture plan does not name the topology's admin")
	}
	waitForAPI(t, 60*time.Second)

	admin := newAPIClient()
	if err := admin.loginAdmin(); err != nil {
		skipOrFail(t, "Cannot get admin token: %v", err)
	}
	adminAccount := getMailboxByAddress(t, admin, seedAdminAddress)

	// An ordinary account: a password, no grants. The admin makes it, as
	// only an account holding mailboxes:write can.
	ordinary := createMailbox(t, admin, contractOrdinaryAddress, accountPassword, ordinaryPlan.DisplayName)
	// An account with no password, reached only through its owner, the admin.
	resp, err := admin.post("/api/v1/accounts", map[string]string{
		"address":      contractOwnedAddress,
		"display_name": ownedPlan.DisplayName,
	})
	requireNoError(t, err)
	var owned mailboxInfo
	switch resp.StatusCode {
	case http.StatusCreated:
		var created struct {
			Data mailboxInfo `json:"data"`
		}
		requireNoError(t, decodeJSON(resp, &created))
		owned = created.Data
	case http.StatusConflict:
		resp.Body.Close()
		owned = getMailboxByAddress(t, admin, contractOwnedAddress)
	default:
		t.Fatalf("create the owned account: %d %s", resp.StatusCode, readBody(resp))
	}

	// An app password for the ordinary account: what it holds is fixed and
	// narrow, read, write and send on its own mailbox and nothing else.
	user := newAPIClient()
	requireNoError(t, loginRetrying(user, contractOrdinaryAddress, accountPassword))
	resp, err = user.post(fmt.Sprintf("/api/v1/accounts/%d/credentials", ordinary.ID),
		map[string]string{"name": plan.Principals["app_password"].Name, "password": accountPassword})
	requireNoError(t, err)
	requireStatus(t, resp, http.StatusCreated)
	var created struct {
		Data struct {
			Password string `json:"password"`
		} `json:"data"`
	}
	requireNoError(t, decodeJSON(resp, &created))
	appPassword := created.Data.Password

	principals := map[string]contractPrincipal{
		"admin":        {Address: seedAdminAddress, Password: seedAdminPassword, ID: adminAccount.ID},
		"ordinary":     {Address: contractOrdinaryAddress, Password: accountPassword, ID: ordinary.ID},
		"owned":        {Address: contractOwnedAddress, ID: owned.ID},
		"app_password": {Address: contractOrdinaryAddress, Secret: appPassword, ID: ordinary.ID},
		"none":         {},
	}
	writeContractPrincipals(t, principals)

	adminRoute := "/api/v1/admin/domains"
	folders := func(id uint) string { return fmt.Sprintf("/api/v1/accounts/%d/folders", id) }
	basic := &basicClient{address: contractOrdinaryAddress, password: appPassword}
	anonymous := newAPIClient()

	matrix := []struct {
		who    string
		get    func(string) (*http.Response, error)
		path   string
		status int
	}{
		{"admin", admin.get, adminRoute, http.StatusOK},
		{"admin", admin.get, folders(adminAccount.ID), http.StatusOK},
		{"admin", admin.get, folders(owned.ID), http.StatusOK},
		{"ordinary", user.get, adminRoute, http.StatusForbidden},
		{"ordinary", user.get, folders(ordinary.ID), http.StatusOK},
		{"ordinary", user.get, folders(owned.ID), http.StatusForbidden},
		{"app password", basic.get, adminRoute, http.StatusUnauthorized},
		{"app password", basic.get, folders(ordinary.ID), http.StatusOK},
		{"app password", basic.get, folders(owned.ID), http.StatusForbidden},
		{"no credentials", anonymous.get, adminRoute, http.StatusUnauthorized},
		{"no credentials", anonymous.get, folders(ordinary.ID), http.StatusUnauthorized},
		{"no credentials", anonymous.get, folders(owned.ID), http.StatusUnauthorized},
	}
	for _, c := range matrix {
		resp, err := c.get(c.path)
		requireNoError(t, err)
		if resp.StatusCode != c.status {
			t.Errorf("%s GET %s: %d, want %d: %s", c.who, c.path, resp.StatusCode, c.status, readBody(resp))
			continue
		}
		resp.Body.Close()
	}

	// The account with no password has nothing to sign in with.
	resp, err = anonymous.post("/api/v1/auth/login", map[string]string{"email": contractOwnedAddress, "password": accountPassword})
	requireNoError(t, err)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the owned account signed in with a password it does not have: %d %s", resp.StatusCode, readBody(resp))
	} else {
		resp.Body.Close()
	}
}

// loginRetrying signs in, waiting out the login rate limit: the stages share
// its budget, and it refills at one request a second.
func loginRetrying(c *apiClient, address, password string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = c.login(address, password); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return err
}

func writeContractPrincipals(t *testing.T, principals map[string]contractPrincipal) {
	t.Helper()
	data, err := json.MarshalIndent(principals, "", "  ")
	requireNoError(t, err)
	requireNoError(t, os.MkdirAll(filepath.Dir(contractPrincipalsFile), 0o755))
	// Readable by the job's user: the suite runs as root in its container.
	requireNoError(t, os.WriteFile(contractPrincipalsFile, data, 0o644))
}

// basicClient sends an app password with HTTP Basic, as a mail client's
// gateway does.
type basicClient struct {
	address, password string
}

func (c *basicClient) get(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, apiBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.address+":"+c.password)))
	return httpClient.Do(req)
}
