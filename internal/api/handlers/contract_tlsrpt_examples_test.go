package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

const tlsReportPath = "/.well-known/smtp-tlsrpt"

// tlsReportBodyRefused is the 400 ReceiveReport sends for a body that does not
// decode as a report.
const tlsReportBodyRefused = `{"error":{"code":"bad_request","message":"Invalid TLS-RPT JSON"}}`

// receiveTlsReport's organization_missing request (#386) is refused by the
// real handler with exactly its documented 422, before any domain is looked up
// or any report stored: the handler has no database.
func TestTLSReportReceive_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, tlsReportPath, "receiveTlsReport", NewTLSReportHandler(nil).ReceiveReport)
}

// The pairs vertrag compiles for receiveTlsReport: stored with the 201,
// body_array with the 400 and organization_missing with the 422, nothing
// else. organization_missing is the stored report without its
// organization-name, so the name is the only reason it is refused.
func TestTLSReportReceive_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, tlsReportPath, "receiveTlsReport")

	want := map[string][]string{
		"request": {"body_array", "organization_missing", "stored"},
		"201":     {"stored"},
		"400":     {"body_array"},
		"422":     {"organization_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("receiveTlsReport's named examples are %v, want %v", got, want)
	}

	// The report the spec documented before its examples were named.
	var stored map[string]interface{}
	if err := json.Unmarshal([]byte(`{
		"organization-name": "Example Reporter",
		"contact-info": "tlsrpt@reporter.example.com",
		"report-id": "2026-10-17T00:00:00Z_restmail.test",
		"date-range": {
			"start-datetime": "2026-10-16T00:00:00Z",
			"end-datetime": "2026-10-16T23:59:59Z"
		},
		"policies": [{
			"policy": {
				"policy-type": "sts",
				"policy-string": ["version: STSv1", "mode: testing", "mx: mail.restmail.test", "max_age: 86400"],
				"policy-domain": "restmail.test",
				"mx-host": ["mail.restmail.test"]
			},
			"summary": {
				"total-successful-session-count": 12,
				"total-failure-session-count": 1
			},
			"failure-details": [{
				"result-type": "certificate-expired",
				"sending-mta-ip": "192.0.2.1",
				"receiving-mx-hostname": "mail.restmail.test",
				"failed-session-count": 1
			}]
		}]
	}`), &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(requests["stored"], stored) {
		t.Errorf("stored is %s, want the report documented before %s",
			mustJSON(requests["stored"]), mustJSON(stored))
	}
	missing := map[string]interface{}{}
	for k, v := range stored {
		if k != "organization-name" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["organization_missing"], missing) {
		t.Errorf("organization_missing is %s, want stored without its organization-name: %s",
			mustJSON(requests["organization_missing"]), mustJSON(missing))
	}

	// stored passes the checks that come before storage, and carries one
	// policy, so the only 201 it can get stores that one.
	var report tlsRPTReport
	if err := json.Unmarshal(mustJSON(requests["stored"]), &report); err != nil {
		t.Fatalf("decode stored: %v", err)
	}
	if _, _, verr := validateTLSRPTReport(&report); verr != nil {
		t.Fatalf("stored is refused before storage: %+v", verr)
	}
	if len(report.Policies) != 1 || report.Policies[0].Policy.PolicyDomain != "restmail.test" {
		t.Fatalf("stored's policies are %+v, want one for restmail.test", report.Policies)
	}
	if want := map[string]interface{}{"data": map[string]interface{}{"stored": float64(1)}}; !reflect.DeepEqual(responses["201"]["stored"], want) {
		t.Errorf("the 201 example is %s, want %s", mustJSON(responses["201"]["stored"]), mustJSON(want))
	}
}

// receiveTLSReportNoDB sends body to the real handler, which has no database,
// and reports whether it got as far as the database.
func receiveTLSReportNoDB(t *testing.T, body string) (rr *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, tlsReportPath, bytes.NewReader([]byte(body)))
	rr = httptest.NewRecorder()
	func() {
		defer func() {
			if recover() != nil {
				reached = true
			}
		}()
		NewTLSReportHandler(nil).ReceiveReport(rr, req)
	}()
	return rr, reached
}

// receiveTlsReport's body_array request (#386) is refused by the real handler,
// which has no database, with exactly the 400 the spec documents for it: an
// array is not a report, so it is refused as it is decoded, before any field
// is checked or any domain looked up.
func TestTLSReportReceive_BodyArrayRefused(t *testing.T) {
	requests, responses := postExamples(t, tlsReportPath, "receiveTlsReport")
	want := parseShapeJSON(t, tlsReportBodyRefused)
	if !reflect.DeepEqual(responses["400"]["body_array"], want) {
		t.Errorf("the spec documents body_array's 400 as %s, want %s",
			mustJSON(responses["400"]["body_array"]), tlsReportBodyRefused)
	}

	body := string(mustJSON(requests["body_array"]))
	if body != "[]" {
		t.Fatalf("body_array is sent as %s, want []", body)
	}
	rr, reached := receiveTLSReportNoDB(t, body)
	if reached {
		t.Fatalf("%s reached the database before it was refused", body)
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("%s: status %d, want 400; body %s", body, rr.Code, rr.Body)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: Content-Type %q, want application/json", body, ct)
	}
	if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered %s, want %s", body, rr.Body, tlsReportBodyRefused)
	}

	// Controls. An array is refused whatever it holds, the stored report in
	// one too: the 400 is the array's, not its emptiness'. And
	// organization_missing is decoded and refused on its field with its
	// documented 422, not body_array's 400.
	wrapped := "[" + string(mustJSON(requests["stored"])) + "]"
	if rr, reached := receiveTLSReportNoDB(t, wrapped); reached || rr.Code != http.StatusBadRequest ||
		!reflect.DeepEqual(decodeShapeBody(t, rr), want) {
		t.Errorf("%s answered %d %s, want body_array's 400", wrapped, rr.Code, rr.Body)
	}
	object := string(mustJSON(requests["organization_missing"]))
	if rr, reached := receiveTLSReportNoDB(t, object); reached || rr.Code != http.StatusUnprocessableEntity ||
		!reflect.DeepEqual(decodeShapeBody(t, rr), responses["422"]["organization_missing"]) {
		t.Errorf("organization_missing answered %d %s, want its documented 422, not body_array's 400",
			rr.Code, rr.Body)
	}
}
