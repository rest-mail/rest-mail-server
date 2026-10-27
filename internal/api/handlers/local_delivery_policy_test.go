package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

func missingGatewayScript() deliveryReadStep {
	return deliveryReadStep{table: "sieve_scripts", columns: []string{"id"}}
}

func gatewayPolicyRoutes(t *testing.T, d *deliveryReadDB, filter pipeline.Filter) http.Handler {
	t.Helper()
	registry := pipeline.NewRegistry()
	if filter != nil {
		registry.Register(filter.Name(), func([]byte) (pipeline.Filter, error) { return filter, nil })
		for _, step := range pipeline.DefaultInboundPipeline(1).Filters {
			registry.Register(step.Name, func([]byte) (pipeline.Filter, error) { return filter, nil })
		}
	}
	h := NewMessageHandler(deliveryReadGorm(t, d), nil, pipeline.NewEngine(registry, nil), "", nil)
	return deliveryRoute(h)
}

// A policy read/decode failure must stop before defaults, account scripts,
// pipeline execution, message storage, redirects or notification work begins.
func TestGatewayInboundPolicyFailuresDeferBeforeSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []deliveryReadStep
	}{
		{"policy lookup", []deliveryReadStep{{table: "pipelines", err: errors.New("private policy backend")}}},
		{"invalid stored JSON", []deliveryReadStep{deliveryPipelineStep(`{private`)}},
		{"wrong stored shape", []deliveryReadStep{deliveryPipelineStep(`{"private":"not a filter list"}`)}},
		{"account script lookup", []deliveryReadStep{deliveryPipelineStep("[]"), {table: "sieve_scripts", err: errors.New("private script backend")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &deliveryReadDB{steps: append([]deliveryReadStep{deliveryMailboxStep("one@example.test")}, tc.steps...)}
			rr := httptest.NewRecorder()
			gatewayPolicyRoutes(t, d, nil).ServeHTTP(rr, deliveryRequest(t, `{"mailbox_id":2,"subject":"policy"}`, "/api/v1/messages/deliver"))
			assertDeliveryReadRefusal(t, d, rr, 451, "deferred")
			if !strings.Contains(rr.Body.String(), `"message":"Try again later"`) {
				t.Fatalf("temporary refusal changed: %s", rr.Body)
			}
		})
	}
}

func TestGatewayInboundPolicyCancellationReachesEveryRead(t *testing.T) {
	for _, stage := range []string{"pipelines", "sieve_scripts"} {
		t.Run(stage, func(t *testing.T) {
			steps := []deliveryReadStep{deliveryMailboxStep("one@example.test")}
			if stage == "sieve_scripts" {
				steps = append(steps, deliveryPipelineStep("[]"))
			}
			steps = append(steps, deliveryReadStep{table: stage, wait: true})
			d := &deliveryReadDB{steps: steps, started: make(chan struct{})}
			route := gatewayPolicyRoutes(t, d, nil)
			r := deliveryRequest(t, `{"address":"one@example.test"}`, "/api/v1/messages/deliver")
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			rr := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); route.ServeHTTP(rr, r.WithContext(ctx)) }()
			select {
			case <-d.started:
				cancel()
			case <-time.After(time.Second):
				cancel()
				t.Error("policy read did not start")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("delivery ignored cancellation")
			}
			assertDeliveryReadRefusal(t, d, rr, 451, "deferred")
		})
	}
}

// A missing row retains the domain defaults; a stored empty policy stays empty.
// This distinction prevents hardening failures from enabling extra filters.
func TestGatewayInboundPolicyDefaultsRequireAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, filters string
		missing       bool
	}{
		{name: "absent policy", missing: true}, {name: "configured empty", filters: "[]"}, {name: "configured null", filters: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := deliveryPipelineStep(tc.filters)
			if tc.missing {
				policy = deliveryReadStep{table: "pipelines", columns: []string{"id"}}
			}
			d := &deliveryReadDB{steps: []deliveryReadStep{policy, missingGatewayScript()}}
			h := NewMessageHandler(deliveryReadGorm(t, d), nil, nil, "", nil)
			r := deliveryRequest(t, "{}", "/api/v1/messages/deliver")
			got, err := h.localInboundPolicy(r.Context(), models.Mailbox{ID: 2, DomainID: 1})
			if err != nil {
				t.Fatal(err)
			}
			if tc.missing {
				want := pipeline.DefaultInboundPipeline(1)
				// Default construction stamps the current time; compare the policy.
				want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("default policy changed: %+v", got)
				}
			} else if got.ID != 3 || got.DomainID != 1 || got.Direction != "inbound" || !got.Active || len(got.Filters) != 0 {
				t.Fatalf("stored policy changed: %+v", got)
			}
			if d.reads != 2 || d.writes != 0 || d.lostContext != 0 {
				t.Fatalf("reads=%d writes=%d lost contexts=%d", d.reads, d.writes, d.lostContext)
			}
		})
	}
}

type gatewayPolicyReject struct {
	calls       int
	lostContext bool
}

func (*gatewayPolicyReject) Name() string              { return "gateway_policy_reject" }
func (*gatewayPolicyReject) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (f *gatewayPolicyReject) Execute(ctx context.Context, _ *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	f.calls++
	_, deadline := ctx.Deadline()
	f.lostContext = !deadline || ctx.Value(deliveryContextKey{}) != true
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionReject, RejectMsg: "configured policy rejection"}, nil
}

func TestGatewayInboundPolicyExecutesConfiguredFilters(t *testing.T) {
	d := &deliveryReadDB{steps: []deliveryReadStep{deliveryMailboxStep("one@example.test"), deliveryPipelineStep(`[{"name":"gateway_policy_reject","type":"action","enabled":true}]`), missingGatewayScript()}}
	f := &gatewayPolicyReject{}
	rr := httptest.NewRecorder()
	gatewayPolicyRoutes(t, d, f).ServeHTTP(rr, deliveryRequest(t, `{"mailbox_id":2}`, "/api/v1/messages/deliver"))
	assertDeliveryReadRefusal(t, d, rr, 550, "rejected")
	if f.calls != 1 || f.lostContext || !strings.Contains(rr.Body.String(), "configured policy rejection") {
		t.Fatalf("calls=%d lost context=%v body=%s", f.calls, f.lostContext, rr.Body)
	}
}

// A configured dependency fault must not execute even a valid default filter.
func TestGatewayInboundPolicyFaultsCannotSelectDefaults(t *testing.T) {
	for _, kind := range []string{"lookup", "invalid JSON", "wrong shape"} {
		t.Run(kind, func(t *testing.T) {
			step := deliveryPipelineStep(`{"private":"not a list"}`)
			if kind == "lookup" {
				step = deliveryReadStep{table: "pipelines", err: errors.New("private database unavailable")}
			}
			if kind == "invalid JSON" {
				step = deliveryPipelineStep(`{private`)
			}
			d := &deliveryReadDB{steps: []deliveryReadStep{deliveryMailboxStep("one@example.test"), step, missingGatewayScript()}}
			f := &gatewayPolicyReject{}
			rr := httptest.NewRecorder()
			gatewayPolicyRoutes(t, d, f).ServeHTTP(rr, deliveryRequest(t, `{"mailbox_id":2}`, "/api/v1/messages/deliver"))
			if rr.Code != 451 || !strings.Contains(rr.Body.String(), `"code":"deferred"`) || strings.Contains(rr.Body.String(), "private") || f.calls != 0 || d.reads != 2 || d.writes != 0 || d.lostContext != 0 {
				t.Fatalf("status=%d body=%s default executions=%d reads=%d writes=%d lost contexts=%d", rr.Code, rr.Body, f.calls, d.reads, d.writes, d.lostContext)
			}
		})
	}
}
