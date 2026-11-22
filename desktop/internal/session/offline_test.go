package session

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type unavailableTransport struct{}

func (unavailableTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

func TestKeychainIdentityRestoresForOfflineReadingWithoutAccessToken(t *testing.T) {
	fixture := newAuthFixture(t, false)
	manager := New()
	signIn(t, manager, fixture)
	restarted := New()
	restarted.client = &http.Client{Transport: unavailableTransport{}}
	if err := restarted.Restore(context.Background()); err == nil {
		t.Fatal("offline refresh should report unavailable")
	}
	status := restarted.Status()
	if !status.SignedIn || status.AccountID != 42 || status.Email != "admin@example.test" || restarted.access != "" {
		t.Fatalf("offline identity: %+v", status)
	}
	if _, err := restarted.Do(context.Background(), http.MethodGet, "/api/v1/accounts", nil); err == nil {
		t.Fatal("offline session sent an unauthenticated request")
	}
	if err := restarted.SignOut(); err != nil {
		t.Fatal(err)
	}
	if restarted.Status().SignedIn {
		t.Fatal("signout retained offline access")
	}
}
