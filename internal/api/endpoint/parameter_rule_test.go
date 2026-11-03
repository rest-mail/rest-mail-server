package endpoint

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type ruledQuery struct {
	Limit *int   `query:"limit"`
	Mode  string `query:"mode"`
}

func (q *ruledQuery) CheckParameters() map[string]string {
	if q.Mode == "refused" {
		return map[string]string{"mode": "says the rule"}
	}
	return nil
}

// A parameter rule's fields join Bind's in one refusal, and a request both let through binds.
func TestParameterRuleJoinsBindsRefusal(t *testing.T) {
	for _, c := range []struct {
		name, target string
		want         map[string]string
	}{
		{"both pass", "/?mode=fine&limit=1", nil},
		{"the rule alone", "/?mode=refused", map[string]string{"mode": "says the rule"}},
		{"Bind alone", "/?limit=x", map[string]string{"limit": "must be an integer"}},
		{"both refuse", "/?mode=refused&limit=x", map[string]string{"mode": "says the rule", "limit": "must be an integer"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var in ruledQuery
			err := BindRequest(httptest.NewRequest(http.MethodGet, c.target, nil), &in, nil)
			if c.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var refusal *Error
			if !errors.As(err, &refusal) || refusal.Status != http.StatusUnprocessableEntity {
				t.Fatalf("got %v", err)
			}
			if !reflect.DeepEqual(refusal.Fields, c.want) {
				t.Fatalf("fields %v, want %v", refusal.Fields, c.want)
			}
			if refusal.Message != queryFailedValidation {
				t.Fatalf("message %q", refusal.Message)
			}
		})
	}
}
