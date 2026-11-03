package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// GET /api/v1/accounts/{id}/addresses lists the aliases that deliver to the
// account and nothing about any other account's, and its sends_as list is
// exactly what sendMessage accepts as From (#1611).
func TestListAccountAddresses(t *testing.T) {
	base := openAccountsTestDB(t)
	if err := base.AutoMigrate(&models.Alias{}, &models.Message{}); err != nil {
		t.Skipf("account addresses DB test skipped: migrate failed (%v)", err)
	}
	// The sends below store real Sent messages; a transaction keeps them, and
	// everything else this test writes, out of the database other tests read.
	db := base.Begin()
	t.Cleanup(func() { db.Rollback() })
	fx := seedAccountsFixture(t, db)
	// Aliases deliver only on a live domain.
	if err := db.Model(&fx.domain).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	mk := func(local string) models.Mailbox {
		mb := models.Mailbox{DomainID: fx.domain.ID, LocalPart: local, Address: local + "@" + fx.domain.Name, Active: true}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatalf("create %s: %v", local, err)
		}
		return mb
	}
	shared, stranger := mk("shared"), mk("stranger")
	var owner models.Role
	if err := db.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: fx.user.ID, RoleID: owner.ID,
		Scope: grants.Mailbox(shared.Address), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	// mail:send on another mailbox is not mail:send_as: the stranger's mailbox
	// is worth asking about, and the send check refuses it.
	sendOnly := models.Role{Name: "send-only-" + fx.domain.Name}
	if err := db.Create(&sendOnly).Error; err != nil {
		t.Fatal(err)
	}
	// A mail capability counts only beside mailbox:session at its scope.
	for _, name := range []string{models.CapMailSend, models.CapMailboxSession} {
		var capability models.Capability
		if err := db.Where("name = ?", name).First(&capability).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.RoleCapability{RoleID: sendOnly.ID, CapabilityID: capability.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Where("role_id = ?", sendOnly.ID).Delete(&models.RoleCapability{})
		db.Where("role_id = ?", sendOnly.ID).Delete(&models.Grant{})
		db.Delete(&sendOnly)
	})
	if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: fx.user.ID, RoleID: sendOnly.ID,
		Scope: grants.Mailbox(stranger.Address), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	alias := func(source, destination string) {
		if err := db.Create(&models.Alias{DomainID: fx.domain.ID, SourceAddress: source + "@" + fx.domain.Name, DestinationAddress: destination, Active: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	alias("info", fx.user.Address)
	alias("sales", strings.ToUpper(fx.user.Address)) // another spelling of the same mailbox
	alias("billing", fx.admin.Address)
	t.Cleanup(func() { db.Where("domain_id = ?", fx.domain.ID).Delete(&models.Alias{}) })

	h := NewAccountHandler(db, nil, "")
	get := func(as, id uint) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodGet, "/api/v1/accounts/"+uintToStr(id)+"/addresses", as, nil, ""))
		return rr
	}

	rr := get(fx.user.ID, fx.user.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("own addresses: %d %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Data accountAddresses `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantReceives := []receivingAddress{
		{Address: fx.user.Address, Primary: true},
		{Address: "info@" + fx.domain.Name},
		{Address: "sales@" + fx.domain.Name},
	}
	if len(got.Data.Receives) != len(wantReceives) {
		t.Fatalf("receives = %+v, want %+v", got.Data.Receives, wantReceives)
	}
	for i := range wantReceives {
		if got.Data.Receives[i] != wantReceives[i] {
			t.Fatalf("receives = %+v, want %+v", got.Data.Receives, wantReceives)
		}
	}
	if strings.Contains(rr.Body.String(), "billing@") || strings.Contains(rr.Body.String(), fx.admin.Address) {
		t.Fatalf("another account's alias or address leaked: %s", rr.Body.String())
	}

	listed := map[string]sendingAddressMode{}
	for _, mode := range got.Data.SendsAs {
		listed[mode.Address] = mode
	}
	if own := listed[fx.user.Address]; !own.Primary || !own.From || len(got.Data.SendsAs) == 0 || got.Data.SendsAs[0].Address != fx.user.Address {
		t.Fatalf("own address not first with from: %+v", got.Data.SendsAs)
	}
	if mode := listed[shared.Address]; !mode.From || !mode.OnBehalf || mode.Primary {
		t.Fatalf("shared mailbox modes = %+v, want from and on_behalf", mode)
	}

	// Each mailbox in this domain is sent from as the route says: a listed mode
	// passes sendMessage's sender check and an unlisted one is refused.
	mh := NewMessageHandler(db, nil, nil, "", nil)
	send := func(from string, onBehalf bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"from": from, "on_behalf": onBehalf, "to": []string{fx.user.Address}, "subject": "hi", "body_text": "hi"})
		rr := httptest.NewRecorder()
		composeRoutes(mh).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/messages/send", fx.user.ID, nil, string(body)))
		return rr
	}
	refusedSender := func(rr *httptest.ResponseRecorder) bool {
		return rr.Code == http.StatusForbidden && strings.Contains(rr.Body.String(), "not authorized to send from this address")
	}
	for _, mb := range []models.Mailbox{fx.user, fx.admin, shared, stranger} {
		for _, onBehalf := range []bool{false, true} {
			mode, ok := listed[mb.Address]
			want := ok && (mode.From && !onBehalf || mode.OnBehalf && onBehalf)
			rr := send(mb.Address, onBehalf)
			if refused := refusedSender(rr); refused == want {
				t.Errorf("send from %s on_behalf=%v: listed=%v but sendMessage answered %d %s", mb.Address, onBehalf, want, rr.Code, rr.Body.String())
			}
		}
	}
	if _, ok := listed[fx.admin.Address]; ok {
		t.Fatalf("listed a mailbox the account holds no send capability on: %+v", got.Data.SendsAs)
	}
	if _, ok := listed[stranger.Address]; ok {
		t.Fatalf("listed a mailbox held with mail:send only, which is not send_as: %+v", got.Data.SendsAs)
	}

	// Another account may not read this one: 404, as for every account route.
	if rr := get(stranger.ID, fx.user.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("stranger: expected 404, got %d %s", rr.Code, rr.Body.String())
	}
}
