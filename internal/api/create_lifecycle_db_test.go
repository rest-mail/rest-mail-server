package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmcrypto "github.com/rest-mail/rest-mail-server/internal/crypto"
)

// Each create is followed through the links the generated description gives it, on the real
// router and a real database: what it made reads back, a change persists, the delete takes it
// away, and the read then answers 404 (#1749, inherited from #656). So a link that names the
// wrong route, parameter or field fails here, not only a route that loses its data.
//
// A resource with no read of its own proves all that through the list that does read it, and
// where its delete of something absent is an open question on #672 this asserts nothing about
// that status.
type lifecycleCase struct {
	name      string
	create    string // the operation; its path and method come from the description
	path      string // the create's parameters filled, when it has any
	body      string
	persisted string   // text the read shows after the create
	list      string   // the list that reads it, when no link does
	update    string   // the update's body, when it has an update link
	changed   string   // text the read shows after the update
	repeat404 bool     // no read route, and a repeated delete answers 404
	gone      string   // what the list no longer shows, %d for the id, when changed is not unique
	trashed   bool     // the first delete moves it to Trash, and the next removes it
	noDelete  bool     // no route deletes it: the reason is in convention-exceptions.json
	unlinked  []string // the operations, taking the made id, its recorded reasons leave unlinked
}

func TestCreatesPersistAndEndAt404(t *testing.T) {
	// The push routes are off unless configured. gapGenerators turns them on only for the first
	// test in the process that asks, so this router asks for itself.
	configureAuthReadPush(t)
	generator := gapGenerators(t)[len(gapGenerators(t))-1]
	doc := contractDocument(t, generator)
	exceptions := readConventionExceptions(t)
	f := newGrantsFixture(t)
	// As accessRouter makes it, and able to take a recovery address, which is refused until the
	// server knows the origin of the links it emails.
	f.router = NewRouter(f.db, auth.NewJWTService("grants-access-secret", 15*time.Minute, 24*time.Hour), &config.Config{
		CORSAllowedOrigins: []string{"http://localhost:3000"}, Environment: "test", TOTP2FAEnabled: true,
		MasterKey: accessMasterKey, RecoveryEmailBaseURL: "https://mail.access.test",
	}, nil)
	owner := f.admins["owner"]
	token := f.token(t, "owner")
	account := strconv.FormatUint(uint64(owner.user.ID), 10)
	mine := "/api/v1/accounts/" + account
	domain := strconv.FormatUint(uint64(f.a.ID), 10)
	certificate, sealedKey := freshCertificate(t, "contract.example.org")
	key, err := rmcrypto.DecryptString(sealedKey, accessMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	certified := doRequest(f.router, http.MethodPost, "/api/v1/admin/domains", token, `{"name":"contract.example.org","server_type":"restmail"}`)
	if certified.Code != http.StatusCreated {
		t.Fatalf("the certificate's domain: %d %s", certified.Code, certified.Body)
	}
	certifiedID := strconv.FormatInt(int64(lifecycleField(t, certified.Body.Bytes(), "/data/id").(float64)), 10)
	certificateBody, _ := json.Marshal(map[string]any{"domain_id": json.Number(certifiedID), "cert_pem": certificate, "key_pem": key})

	for _, tc := range []lifecycleCase{
		{name: "domain", create: "createDomain", body: `{"name":"lifecycle.test","server_type":"restmail"}`, persisted: `"name":"lifecycle.test"`,
			update: `{"active":false}`, changed: `"active":false`},
		{name: "alias", create: "createAlias", body: `{"source_address":"sales@a.test","destination_address":"one@elsewhere.test"}`, persisted: `one@elsewhere.test`,
			update: `{"destination_address":"two@elsewhere.test"}`, changed: `two@elsewhere.test`},
		{name: "account", create: "createAccount", body: `{"address":"lifecycle@a.test","display_name":"Made","password":"correct-horse-battery"}`, persisted: `lifecycle@a.test`,
			update: `{"display_name":"Changed"}`, changed: `"display_name":"Changed"`},
		{name: "custom filter", create: "createCustomFilter", body: `{"name":"lifecycle","filter_type":"action","direction":"inbound","config":{"script":"function filter(email) {\n  return { action: \"continue\" };\n}\n"},"domain_id":` + domain + `}`, persisted: `"name":"lifecycle"`,
			update: `{"enabled":false}`, changed: `"enabled":false`},
		{name: "certificate", create: "createCertificate", body: string(certificateBody), persisted: `contract.example.org`},
		{name: "pipeline", create: "createPipeline", list: "/api/v1/admin/pipelines", body: `{"domain_id":` + domain + `,"direction":"inbound","filters":[{"name":"header_validate","config":{}}]}`, persisted: `header_validate`,
			update: `{"active":false}`, changed: `"active":false`, gone: `{"id":%d,"domain_id"`},
		{name: "contact", create: "createContact", path: mine + "/contacts", list: mine + "/contacts", body: `{"email":"lifecycle@example.com"}`, persisted: `lifecycle@example.com`,
			update: `{"name":"Changed"}`, changed: `"name":"Changed"`, repeat404: true},
		{name: "blocked sender", create: "blockSender", path: mine + "/contacts/block", list: mine + "/contacts", body: `{"email":"blocked@example.com"}`, persisted: `blocked@example.com`,
			update: `{"name":"Changed"}`, changed: `"name":"Changed"`, repeat404: true},
		{name: "renamed folder", create: "createFolder", path: mine + "/folders", list: mine + "/folders", body: `{"name":"Lifecycle"}`, persisted: `"name":"Lifecycle"`,
			update: `{"name":"Renamed"}`, changed: `"name":"Renamed"`},
		{name: "folder", create: "createFolder", path: mine + "/folders", list: mine + "/folders", body: `{"name":"Kept"}`, persisted: `"name":"Kept"`},
		{name: "ban", create: "createBan", list: "/api/v1/admin/bans", body: `{"ip":"192.0.2.10","reason":"lifecycle","protocol":"all","duration":"1h","created_by":"admin@a.test"}`, persisted: `192.0.2.10`},
		{name: "allowlist entry", create: "addToAllowlist", path: "/api/v1/admin/domains/" + domain + "/allowlist", list: "/api/v1/admin/domains/" + domain + "/allowlist", body: `{"pattern":"@partner.example.com","reason":"lifecycle"}`, persisted: `@partner.example.com`, repeat404: true},
		{name: "blocklist entry", create: "addToBlocklist", path: "/api/v1/admin/domains/" + domain + "/blocklist", list: "/api/v1/admin/domains/" + domain + "/blocklist", body: `{"pattern":"spam@spammer.example.com","reason":"lifecycle"}`, persisted: `spam@spammer.example.com`, repeat404: true},
		{name: "api key", create: "createAPIKey", path: mine + "/api-keys", list: mine + "/api-keys", body: fmt.Sprintf(`{"name":"Lifecycle key","password":%q,"permissions":[{"capability":"mail:read","scope":"mailbox:owner@access.test"}]}`, accessPassword), persisted: `Lifecycle key`,
			update: fmt.Sprintf(`{"password":%q,"permissions":[{"capability":"mail:send","scope":"mailbox:owner@access.test"}]}`, accessPassword), changed: `mail:send`},
		{name: "client secret", create: "createClientSecret", path: mine + "/client-secrets", list: mine + "/client-secrets", body: fmt.Sprintf(`{"name":"Lifecycle secret","password":%q,"permissions":[{"capability":"mail:read","scope":"mailbox:owner@access.test"}]}`, accessPassword), persisted: `Lifecycle secret`},
		{name: "app password", create: "createAppPassword", path: mine + "/credentials", list: mine + "/credentials", body: fmt.Sprintf(`{"name":"Lifecycle mail","password":%q}`, accessPassword), persisted: `Lifecycle mail`},
		{name: "grant", create: "createGrant", list: "/api/v1/admin/grants", body: fmt.Sprintf(`{"holder":{"kind":"account","id":%d},"role":"admin","scope":"domain:lifecycle-grant.test","mode":"eligible"}`, f.ma.ID), persisted: `domain:lifecycle-grant.test`},
		{name: "draft", create: "saveDraft", body: `{"from":"owner@access.test","to":["owner@access.test"],"subject":"Made","body_text":"Saved."}`, persisted: `"subject":"Made"`,
			update: `{"from":"owner@access.test","to":["owner@access.test"],"subject":"Changed","body_text":"Saved again."}`, changed: `"subject":"Changed"`, trashed: true, unlinked: []string{"getMessage", "deleteMessage"}},
		{name: "sent message", create: "sendMessage", body: `{"from":"owner@access.test","to":["owner@access.test"],"subject":"Sent lifecycle","body_text":"Sent."}`, persisted: `"subject":"Sent lifecycle"`,
			update: `{"is_flagged":true}`, changed: `"is_flagged":true`, trashed: true},
		{name: "sieve script", create: "putSieveScript", path: mine + "/sieve", body: `{"script":"keep;"}`, persisted: `keep;`},
		{name: "mta-sts policy", create: "setMtaStsPolicy", path: "/api/v1/admin/domains/" + domain + "/mta-sts", body: `{"active":false,"max_age":0,"mode":"enforce","mx_hosts":"mail.a.test"}`, persisted: `mail.a.test`},
		{name: "recovery address", create: "registerRecoveryEmail", body: fmt.Sprintf(`{"email":"backup@external.test","current_password":%q}`, accessPassword), persisted: `backup@external.test`, noDelete: true},
		{name: "push device", create: "registerPushDevice", list: "/api/v1/devices", body: `{"platform":"apns","token":"5c1d7e0f3a9b2c4d6e8f0a1b3c5d7e9f1a2b4c6d8e0f2a3b5c7d9e1f3a4b6c8d","environment":"production","app_id":"com.antimatterstudios.restmail"}`, persisted: `"platform":"apns"`, repeat404: true},
	} {
		t.Run(tc.name, func(t *testing.T) { runLifecycle(t, f.router, doc, token, tc, exceptions) })
	}
}

// lifecycleOperation is one operation of the description, by its id.
type lifecycleOperation struct {
	method, path string
	links        func(status string) map[string]any
}

func lifecycleOperationNamed(t *testing.T, doc map[string]any, id string) lifecycleOperation {
	t.Helper()
	var found *lifecycleOperation
	eachConventionOperation(doc, func(op conventionOperation) {
		if op.id != id {
			return
		}
		found = &lifecycleOperation{method: op.method, path: op.path, links: func(status string) map[string]any {
			response, _ := op.responses[status].(map[string]any)
			links, _ := response["links"].(map[string]any)
			return links
		}}
	})
	if found == nil {
		t.Fatalf("the description has no operation %s", id)
	}
	return *found
}

func lifecycleField(t *testing.T, body []byte, pointer string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("no %s in %s", pointer, body)
		}
		if value, ok = object[part]; !ok {
			t.Fatalf("no %s in %s", pointer, body)
		}
	}
	return value
}

var lifecycleParameter = regexp.MustCompile(`\{([^{}]+)\}`)

// followed is a link's target path with the link's own runtime expressions filled from the
// exchange it hangs off, so a wrong expression sends the request somewhere it answers for.
func followed(t *testing.T, doc map[string]any, link map[string]any, requestPath string, requestTemplate string, body []byte) (lifecycleOperation, string) {
	t.Helper()
	target := lifecycleOperationNamed(t, doc, link["operationId"].(string))
	parameters, _ := link["parameters"].(map[string]any)
	return target, lifecycleParameter.ReplaceAllStringFunc(target.path, func(placeholder string) string {
		name := placeholder[1 : len(placeholder)-1]
		expression, ok := parameters[name].(string)
		if !ok {
			t.Fatalf("link to %s does not fill %s", target.path, name)
		}
		if pointer, ok := strings.CutPrefix(expression, "$response.body#"); ok {
			switch value := lifecycleField(t, body, pointer).(type) {
			case float64:
				return strconv.FormatInt(int64(value), 10)
			case string:
				return value
			default:
				t.Fatalf("%s is %v", expression, value)
			}
		}
		if requested, ok := strings.CutPrefix(expression, "$request.path."); ok {
			templateParts, pathParts := strings.Split(requestTemplate, "/"), strings.Split(requestPath, "/")
			for i, part := range templateParts {
				if part == "{"+requested+"}" {
					return pathParts[i]
				}
			}
		}
		t.Fatalf("cannot fill %s from %s", name, expression)
		return ""
	})
}

func runLifecycle(t *testing.T, router http.Handler, doc map[string]any, token string, tc lifecycleCase, exceptions conventionExceptions) {
	t.Helper()
	create := lifecycleOperationNamed(t, doc, tc.create)
	createPath := tc.path
	if createPath == "" {
		createPath = create.path
	}
	made := doRequest(router, create.method, createPath, token, tc.body)
	if made.Code < 200 || made.Code > 299 {
		t.Fatalf("%s: %d %s", tc.create, made.Code, made.Body)
	}
	status := strconv.Itoa(made.Code)
	links := create.links(status)
	if len(links) == 0 {
		t.Fatalf("%s %s has no links", tc.create, status)
	}

	// The link that does a verb, from the latest answer that offers one: a rename's answer names
	// the folder as it now is, which the create's no longer does.
	type way struct {
		link     map[string]any
		path     string
		template string
		body     []byte
	}
	ways := map[string]way{}
	learn := func(links map[string]any, path, template string, body []byte) {
		for _, value := range links {
			link := value.(map[string]any)
			target := lifecycleOperationNamed(t, doc, link["operationId"].(string))
			verb, lifecycle := lifecycleVerbs[target.method]
			if !lifecycle || !madeBy(target.path, create.path, exceptions.CreateResource[tc.create].Path) {
				continue
			}
			ways[verb] = way{link, path, template, append([]byte(nil), body...)}
		}
	}
	learn(links, createPath, create.path, made.Body.Bytes())
	for _, operation := range tc.unlinked {
		link := map[string]any{"operationId": operation, "parameters": map[string]any{"id": "$response.body#/data/id"}}
		ways[lifecycleVerbs[lifecycleOperationNamed(t, doc, operation).method]] = way{link, createPath, create.path, made.Body.Bytes()}
	}

	send := func(verb, body string) *httptest.ResponseRecorder {
		t.Helper()
		w, ok := ways[verb]
		if !ok {
			t.Fatalf("%s offers no %s link", tc.create, verb)
		}
		target, path := followed(t, doc, w.link, w.path, w.template, w.body)
		rr := doRequest(router, target.method, path, token, body)
		if verb == "update" && rr.Code >= 200 && rr.Code <= 299 {
			learn(target.links(strconv.Itoa(rr.Code)), path, target.path, rr.Body.Bytes())
		}
		return rr
	}
	read := func() *httptest.ResponseRecorder {
		t.Helper()
		if tc.list != "" {
			return doRequest(router, http.MethodGet, tc.list, token, "")
		}
		return send("read", "")
	}
	shows := func(step, want string) {
		t.Helper()
		rr := read()
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("%s: the read answered %d %s, want %s", step, rr.Code, rr.Body, want)
		}
	}

	shows("after the create", tc.persisted)
	gone := tc.persisted
	if tc.update != "" {
		if rr := send("update", tc.update); rr.Code < 200 || rr.Code > 299 {
			t.Fatalf("update: %d %s", rr.Code, rr.Body)
		}
		shows("after the update", tc.changed)
		gone = tc.changed
	}
	if tc.gone != "" {
		gone = fmt.Sprintf(tc.gone, int64(lifecycleField(t, made.Body.Bytes(), "/data/id").(float64)))
	}
	if tc.noDelete {
		return
	}
	if rr := send("delete", ""); rr.Code < 200 || rr.Code > 299 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	if tc.trashed {
		shows("after the first delete", `"folder":"Trash"`)
		if rr := send("delete", ""); rr.Code < 200 || rr.Code > 299 {
			t.Fatalf("delete from Trash: %d %s", rr.Code, rr.Body)
		}
	}
	if tc.list == "" {
		if rr := read(); rr.Code != http.StatusNotFound {
			t.Fatalf("the read after the delete answered %d %s, want 404", rr.Code, rr.Body)
		}
		return
	}
	if rr := read(); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), gone) {
		t.Fatalf("the list after the delete answered %d and still shows %s: %s", rr.Code, gone, rr.Body)
	}
	if tc.repeat404 {
		if rr := send("delete", ""); rr.Code != http.StatusNotFound {
			t.Fatalf("the second delete answered %d %s, want 404", rr.Code, rr.Body)
		}
	}
}

// madeBy leaves out the links that lead onward from a create, such as a domain's to its
// allowlist: a verb here means the verb on what the create made.
func madeBy(target, create, stated string) bool {
	return target == stated || target == create ||
		(strings.HasPrefix(target, create+"/") && childOfCreate.MatchString(strings.TrimPrefix(target, create)))
}
