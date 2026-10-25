package endpoint

import (
	"net/http"
	"reflect"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
)

func TestConditionalDescriptionsPreserveOrderAndSiblingIsolation(t *testing.T) {
	base := Routes{Router: chi.NewRouter(), Registry: NewRegistry()}.Describing(oapifly.Middleware{Roles: []string{"before"}})
	conditional := base.DescribingWhen(func(method string) bool { return method == http.MethodPost },
		oapifly.Middleware{Roles: []string{"conditional"}}).Describing(oapifly.Middleware{Roles: []string{"after"}})
	for _, tc := range []struct {
		name, method string
		routes       Routes
		roles        []string
	}{
		{"safe", "GET", conditional, []string{"before", "after"}},
		{"unsafe", "POST", conditional, []string{"before", "conditional", "after"}},
		{"sibling", "POST", base, []string{"before"}},
	} {
		Register(tc.routes, Operation{Method: tc.method, Path: "/" + tc.name, OperationID: tc.name},
			func(*http.Request, struct{}) (Empty, error) { return Empty{}, nil })
		operation := base.Registry.Endpoints()[len(base.Registry.Endpoints())-1]
		var roles []string
		for _, description := range operation.Middleware {
			roles = append(roles, description.Roles...)
		}
		if !reflect.DeepEqual(roles, tc.roles) {
			t.Fatalf("%s middleware order = %v, want %v", tc.name, roles, tc.roles)
		}
	}
}
