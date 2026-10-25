package handlers

import (
	"encoding/json"
	"reflect"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// Captured from mobile client.dart at origin/main and the admin/src forms/stores.
// In particular, webmail PATCH sends domain_id and pipeline direction even though those
// fields have always been ignored. Config and filter JSON remain open to filter plugins.
func TestPipelineClientBodiesBind(t *testing.T) {
	for _, tc := range []struct {
		name string
		dst  any
		body string
	}{
		{"mobile pipeline create", &createPipelineRequestBody{}, `{"domain_id":1,"direction":"inbound","active":false,"filters":[{"name":"header_validate","type":"action","enabled":true,"unskippable":true,"config":{}}]}`},
		{"webmail pipeline patch", &updatePipelineRequest{}, `{"domain_id":1,"direction":"inbound","active":false,"filters":[{"name":"javascript","type":"transform","enabled":true,"config":{"script":"noop"}}]}`},
		{"mobile pipeline patch", &updatePipelineRequest{}, `{"active":true,"filters":[]}`},
		{"custom filter create", &createCustomFilterRequestBody{}, `{"domain_id":1,"name":"custom","description":"test","filter_type":"action","direction":"both","config":{"script":"noop"},"enabled":false}`},
		{"webmail custom filter patch", &updateCustomFilterRequest{}, `{"domain_id":1,"name":"custom","description":"test","filter_type":"transform","direction":"outbound","config":{"script":"noop"},"enabled":false}`},
		{"mobile pipeline preview", &testPipelineRequestBody{}, `{"pipeline_id":1,"email":{"envelope":{"mail_from":"a@example.com","rcpt_to":["b@example.com"]},"headers":{"subject":"test"},"body":{"content_type":"text/plain","content":"hi"}}}`},
		{"built-in filter preview", &testFilterRequestBody{}, `{"filter_name":"header_validate","config":{},"email":{}}`},
		{"custom filter preview", &testCustomFilterRequest{}, `{"email":{"from":"test@example.com","to":["a@example.com"],"subject":"test","body":"hi"}}`},
		{"custom filter validation", &validateCustomFilterRequestBody{}, `{"script":"function filter(e){return e}","email":{"subject":"test"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := oapifly.Bind(tc.dst, func(in, name string) (string, bool) { return "1", in == "path" && name == "id" }, []byte(tc.body))
			if err != nil {
				t.Fatalf("client body refused: %v", err)
			}
		})
	}
}

func TestPartialSampleEmailKeepsDecoderSemantics(t *testing.T) {
	const body = `{"envelope":{"mail_from":null,"rcpt_to":[]},"headers":{"subject":"test","to":null},"body":{"parts":[{"content":"hello"}]},"attachments":[],"metadata":{"key":"value"}}`
	var input sampleEmail
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatal(err)
	}
	var before pipeline.EmailJSON
	if err := json.Unmarshal([]byte(body), &before); err != nil {
		t.Fatal(err)
	}
	if got := input.canonical(); !reflect.DeepEqual(got, &before) {
		t.Fatalf("canonical email = %#v, want %#v", got, before)
	}
}

func TestPipelineAPIValuesKeepModelJSON(t *testing.T) {
	p := models.Pipeline{ID: 1, DomainID: 2, Direction: "inbound", Filters: json.RawMessage(`[{"name":"custom","type":"action","enabled":false,"config":{"key":null}}]`)}
	f := models.CustomFilter{ID: 3, DomainID: 2, Name: "custom", FilterType: "action", Direction: "both", Config: json.RawMessage(`{"script":"noop"}`)}
	trace, err := messageTraceOf(models.MessageTrace{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]any{{p, pipelineOf(p)}, {f, customFilterOf(f)}, {models.MessageTrace{}, trace}} {
		before, err := json.Marshal(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("API JSON changed:\n%s\n%s", before, after)
		}
	}
}
