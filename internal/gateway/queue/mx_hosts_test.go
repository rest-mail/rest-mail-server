package queue

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// fakeResolver answers MX and address lookups from maps; a name in neither is
// "no such host", as Go reports both NXDOMAIN and a name with no records.
type fakeResolver struct {
	mx      map[string][]*net.MX
	hosts   map[string][]string
	failing error
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (r fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if r.failing != nil {
		return nil, r.failing
	}
	if mx, ok := r.mx[name]; ok {
		return mx, nil
	}
	return nil, notFound(name)
}

func (r fakeResolver) LookupHost(_ context.Context, name string) ([]string, error) {
	if r.failing != nil {
		return nil, r.failing
	}
	if hosts, ok := r.hosts[name]; ok {
		return hosts, nil
	}
	return nil, notFound(name)
}

// RFC 5321 §5.1, as #713 reads it: a domain that does not exist bounces at
// once with 5.1.2; one with no MX but an address is its own implicit MX; a
// lookup that fails for now is retried.
func TestMXHosts(t *testing.T) {
	resolver := fakeResolver{
		mx:    map[string][]*net.MX{"mx.test": {{Host: "in.mx.test.", Pref: 10}}},
		hosts: map[string][]string{"implicit.test": {"192.0.2.25"}},
	}
	w := &Worker{resolver: resolver}

	if mx, err := w.mxHosts(context.Background(), "mx.test"); err != nil || len(mx) != 1 || mx[0].Host != "in.mx.test." {
		t.Errorf("a domain with an MX: %v, %v", mx, err)
	}
	if mx, err := w.mxHosts(context.Background(), "implicit.test"); err != nil || len(mx) != 1 || mx[0].Host != "implicit.test" {
		t.Errorf("a domain with an address and no MX: %v, %v; want itself as the implicit MX", mx, err)
	}

	_, err := w.mxHosts(context.Background(), "nowhere.test")
	var reply *SMTPError
	if !errors.As(err, &reply) || reply.Code != 550 || reply.Enhanced != "5.1.2" {
		t.Fatalf("a domain that does not exist: %v; want 550 5.1.2", err)
	}
	if c := ClassifyFailure(err); c != models.FailureBadDomain || !c.Permanent() {
		t.Errorf("a domain that does not exist is %q, want a permanent bad domain", c)
	}

	w.resolver = fakeResolver{failing: &net.DNSError{Err: "server misbehaving", Name: "mx.test", IsTemporary: true}}
	_, err = w.mxHosts(context.Background(), "mx.test")
	if err == nil || errors.As(err, &reply) {
		t.Fatalf("a SERVFAIL: %v; want a lookup error to retry, not a reply", err)
	}
	if c := ClassifyFailure(err); c.Permanent() {
		t.Errorf("a SERVFAIL is %q, a permanent failure; want it retried", c)
	}
}
