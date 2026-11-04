package main

import (
	"context"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestInvitationProbePreparesTheDrawnTokenWithoutReusingAnAccount(t *testing.T) {
	database := fixtureDB(t)
	hosted := models.Domain{Name: "restmail.test", ServerType: "traditional", Active: true}
	if err := database.Create(&hosted).Error; err != nil {
		t.Fatal(err)
	}
	var previous uint
	for range 2 {
		if err := prepareInvitationProbe(database, hosted.Name, "x"); err != nil {
			t.Fatal(err)
		}
		account, err := bootstrap.AcceptInvitation(context.Background(), database, "x", "twelve-characters")
		if err != nil {
			t.Fatal(err)
		}
		if account.ID == previous {
			t.Fatal("a consumed invitation's account was reused")
		}
		previous = account.ID
		if _, err := bootstrap.AcceptInvitation(context.Background(), database, "x", "twelve-characters"); err != bootstrap.ErrTokenInvalid {
			t.Fatalf("a second redemption = %v, want invalid token", err)
		}
	}
}

func TestInvitationProbeRefusesOtherTopologiesAndUnusableTokens(t *testing.T) {
	for _, request := range []struct{ domain, token string }{
		{"example.test", "x"}, {"restmail.test", ""}, {"restmail.test", " "},
	} {
		if err := prepareInvitationProbe(nil, request.domain, request.token); err == nil {
			t.Fatalf("accepted %q, %q", request.domain, request.token)
		}
	}
}
