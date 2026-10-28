package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

const vacationContractPath = "/api/v1/accounts/{id}/vacation"

type vacationContractContent struct {
	Example interface{} `yaml:"example"`
	Schema  struct {
		Ref        string   `yaml:"$ref"`
		Required   []string `yaml:"required"`
		Properties map[string]struct {
			Ref string `yaml:"$ref"`
		} `yaml:"properties"`
	} `yaml:"schema"`
	namedExamples `yaml:",inline"`
}

type vacationContractSpec struct {
	OperationID string `yaml:"operationId"`
	RequestBody struct {
		Required bool                               `yaml:"required"`
		Content  map[string]vacationContractContent `yaml:"content"`
	} `yaml:"requestBody"`
	Responses map[string]struct {
		Ref     string                             `yaml:"$ref"`
		Content map[string]vacationContractContent `yaml:"content"`
	} `yaml:"responses"`
}

func readVacationContractSpec(t *testing.T) vacationContractSpec {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Put vacationContractSpec `yaml:"put"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Paths[vacationContractPath].Put
}

// A named response must pair only with its own request; an unnamed success
// would also be expected for the invalid requests in the contract run.
func TestSetVacation_ExamplePairs(t *testing.T) {
	op := readVacationContractSpec(t)
	if op.OperationID != "setVacation" || !op.RequestBody.Required {
		t.Fatalf("unexpected operation: %+v", op)
	}
	body := op.RequestBody.Content["application/json"]
	if body.Example != nil || body.Schema.Ref != "#/components/schemas/VacationConfig" {
		t.Fatalf("unexpected request schema or unnamed example: %+v", body)
	}
	want := map[string][]string{
		"200": {"configured"},
		"422": {"body_array", "body_missing", "dates_reversed", "enabled_missing", "reply_missing", "subject_missing"},
	}
	statuses := names(op.Responses)
	if !reflect.DeepEqual(statuses, []string{"200", "400", "422"}) {
		t.Fatalf("response statuses %v, want 200, 400 and 422", statuses)
	}
	paired := map[string]bool{}
	for status, expected := range want {
		response := op.Responses[status]
		content := response.Content["application/json"]
		if response.Ref != "" || content.Example != nil || !reflect.DeepEqual(names(content.Examples), expected) {
			t.Errorf("%s must name exactly %v: %+v", status, expected, response)
		}
		for _, name := range expected {
			if _, ok := body.Examples[name]; !ok {
				t.Errorf("%s example %s has no matching request", status, name)
			}
			paired[name] = true
		}
	}
	if !reflect.DeepEqual(names(body.Examples), names(paired)) {
		t.Errorf("request examples %v, want %v", names(body.Examples), names(paired))
	}
	if got := jsonValue(t, body.Examples["body_array"].Value); !reflect.DeepEqual(got, []interface{}{}) {
		t.Errorf("body_array = %v, want []", got)
	}
	for status, ref := range map[string]string{
		"400": "#/components/schemas/ErrorResponse",
		"422": "#/components/schemas/ValidationErrorResponse",
	} {
		if got := op.Responses[status].Content["application/json"].Schema.Ref; got != ref {
			t.Errorf("%s schema = %q, want %q", status, got, ref)
		}
	}
	ok := op.Responses["200"].Content["application/json"].Schema
	if !reflect.DeepEqual(ok.Required, []string{"data"}) || ok.Properties["data"].Ref != "#/components/schemas/VacationConfig" {
		t.Errorf("unexpected success schema: %+v", ok)
	}
}

// Use the actual spec bytes against the real handler and database. Every
// refusal must preserve both an absent config and a previously saved reply.
func TestSetVacation_DocumentedExamples(t *testing.T) {
	op := readVacationContractSpec(t)
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.VacationConfig{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewVacationHandler(tx)
	put := func(t *testing.T, name string, status string) {
		t.Helper()
		request, found := op.RequestBody.Content["application/json"].Examples[name]
		if !found {
			t.Fatalf("missing request example %s", name)
		}
		body := string(mustJSON(yamlToJSON(request.Value)))
		rr := httptest.NewRecorder()
		serveVacation(h, rr, authzRequest(http.MethodPut, "/vacation", fx.ownerMailbox.ID,
			map[string]string{"id": uintToStr(fx.ownerMailbox.ID)}, body))
		code, err := strconv.Atoi(status)
		if err != nil {
			t.Fatal(err)
		}
		if rr.Code != code {
			t.Fatalf("%s answered %d, want %d: %s", name, rr.Code, code, rr.Body)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s Content-Type = %q, want application/json", name, ct)
		}
		var got interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := jsonValue(t, op.Responses[status].Content["application/json"].Examples[name].Value)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s answered %s; spec promises %s", name, rr.Body, mustJSON(want))
		}
	}
	refuse := func() {
		for _, status := range []string{"400", "422"} {
			for _, name := range names(op.Responses[status].Content["application/json"].Examples) {
				t.Run(status+"/"+name, func(t *testing.T) { put(t, name, status) })
			}
		}
	}
	refuse()
	var count int64
	if err := tx.Model(&models.VacationConfig{}).Where("mailbox_id = ?", fx.ownerMailbox.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("refused requests created %d vacation configs", count)
	}
	put(t, "configured", "200")
	var saved models.VacationConfig
	if err := tx.Where("mailbox_id = ?", fx.ownerMailbox.ID).First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	refuse()
	var after models.VacationConfig
	if err := tx.First(&after, saved.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, after) {
		t.Errorf("refused updates changed stored config: before %+v, after %+v", saved, after)
	}
	// The same success pair also works when the config already exists.
	put(t, "configured", "200")
}
