package fixtureplan

import (
	"encoding/json"
	"testing"
)

func TestDefaultFixturePlan(t *testing.T) {
	plan, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Principals["owned"].Owner != "admin" || plan.Principals["owned"].Password || plan.Principals["app_password"].Account != "ordinary" || plan.Queue["retry"].Sender != plan.Principals["admin"].Address {
		t.Fatal("declared ownership, credential or queue relationships are inconsistent")
	}
	delete(plan.Principals, "ordinary")
	fresh, err := Default()
	if err != nil || fresh.Principals["ordinary"].Address == "" {
		t.Fatal("one setup phase mutated the shared declaration")
	}
}

func TestFixturePlanRefusesUnsafeOrDanglingDeclarations(t *testing.T) {
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Domain = "production.example" },
		func(p *Plan) { delete(p.Principals, "ordinary") },
		func(p *Plan) {
			v := p.Principals["ordinary"]
			v.Address = p.Principals["admin"].Address
			p.Principals["ordinary"] = v
		},
		func(p *Plan) {
			v := p.Principals["ordinary"]
			v.Address = "user@production.example"
			p.Principals["ordinary"] = v
		},
		func(p *Plan) { v := p.Principals["owned"]; v.Owner = "missing"; p.Principals["owned"] = v },
		func(p *Plan) {
			v := p.Principals["app_password"]
			v.Account = "owned"
			p.Principals["app_password"] = v
		},
		func(p *Plan) { v := p.Principals["none"]; v.Password = true; p.Principals["none"] = v },
		func(p *Plan) { delete(p.Queue, "retry") },
		func(p *Plan) { v := p.Queue["retry"]; v.NextAttemptHours = 0; p.Queue["retry"] = v },
		func(p *Plan) { v := p.Queue["retry"]; v.ExpiresHours = v.NextAttemptHours; p.Queue["retry"] = v },
		func(p *Plan) {
			v := p.Queue["retry"]
			v.Domain = "example.com"
			v.Recipient = "real@example.com"
			p.Queue["retry"] = v
		},
		func(p *Plan) { v := p.Queue["retry"]; v.Subject = "injected\r\nheader"; p.Queue["retry"] = v },
	} {
		p, err := Default()
		if err != nil {
			t.Fatal(err)
		}
		mutate(&p)
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); err == nil {
			t.Fatalf("accepted unsafe declaration: %s", data)
		}
	}
	for _, data := range [][]byte{append(append([]byte{}, defaults...), []byte(" {}")...), []byte(`{"unexpected":true}`)} {
		if _, err := Parse(data); err == nil {
			t.Fatal("accepted an unknown field or trailing JSON")
		}
	}
}
