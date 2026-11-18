package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"gorm.io/gorm"
)

const engineLimitHeaders = "From: old@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"

// sieveDelivery is /deliver to a mailbox with a Sieve script, through the real pipeline engine.
type sieveDelivery struct {
	db      *gorm.DB
	mailbox models.Mailbox
	route   http.HandlerFunc
}

func newSieveDelivery(t *testing.T) *sieveDelivery {
	t.Helper()
	db := importProofDB(t)
	if err := db.AutoMigrate(&models.Pipeline{}, &models.OutboundQueue{}, &models.SieveScript{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "engine-limit.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "to", Address: "to@engine-limit.test", Active: true, QuotaBytes: 1 << 40}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.QuotaUsage{MailboxID: mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, db, mailbox)
	encoded, err := json.Marshal([]pipeline.FilterConfig{sieveStep("")})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: encoded, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	delivery := &sieveDelivery{db: db, mailbox: mailbox}
	delivery.route = deliveryRoute(NewMessageHandler(db, nil, pipeline.NewEngine(withQuietVacation(pipeline.DefaultRegistry), slog.Default()), "", nil))
	if err := db.Create(&models.SieveScript{MailboxID: mailbox.ID, Script: "keep;", ScriptHash: filters.HashSieveScript("keep;"), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	// What the first delivery in a process builds and keeps isn't the message's to answer for.
	if w, _ := delivery.send("Message-ID: <warm@example.test>\r\n" + engineLimitHeaders + "\r\nb\r\n"); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return delivery
}

// use makes script the mailbox's active one.
func (d *sieveDelivery) use(t *testing.T, script string) {
	t.Helper()
	update := map[string]any{"script": script, "script_hash": filters.HashSieveScript(script)}
	if err := d.db.Model(&models.SieveScript{}).Where("mailbox_id = ?", d.mailbox.ID).Updates(update).Error; err != nil {
		t.Fatal(err)
	}
}

// send delivers message and answers what that allocated, from empty pools.
func (d *sieveDelivery) send(message string) (*httptest.ResponseRecorder, uint64) {
	body := fmt.Sprintf(`{"address":"%[1]s","sender":"old@example.test","sender_name":"Old","subject":"s","message_id":"m@example.test","recipients_to":["%[1]s"],"client_ip":"192.0.2.1","helo_name":"mail.example.test","received_tls":true,"tls_version":"TLS1.3","tls_cipher":"TLS_AES_128_GCM_SHA256","raw_message":"%[2]s"}`, d.mailbox.Address, base64.StdEncoding.EncodeToString([]byte(message)))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	var before, after runtime.MemStats
	emptyPools()
	runtime.ReadMemStats(&before)
	d.route(w, r)
	runtime.ReadMemStats(&after)
	return w, after.TotalAlloc - before.TotalAlloc
}

// cost delivers message three times over and answers the least and the most that allocated, and the
// length of what was delivered.
func (d *sieveDelivery) cost(t *testing.T, label, message string) (best, worst, length uint64) {
	t.Helper()
	best = 1 << 63
	for attempt := range 3 {
		stored := fmt.Sprintf("Message-ID: <engine-%s-%d@example.test>\r\n", strings.NewReplacer(" ", "-", ",", "", "*", "").Replace(label), attempt) + message
		length = uint64(len(stored))
		w, allocated := d.send(stored)
		if w.Code != http.StatusCreated {
			t.Fatalf("%d %.300s", w.Code, w.Body)
		}
		best, worst = min(best, allocated), max(worst, allocated)
	}
	return best, worst, length
}

// latest is the message last stored in the mailbox.
func (d *sieveDelivery) latest(t *testing.T) models.Message {
	t.Helper()
	var stored models.Message
	if err := d.db.Where("mailbox_id = ?", d.mailbox.ID).Order("id DESC").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	return stored
}

// engineAddressLists are the shapes of a very long To field, by name.
func engineAddressLists() map[string]string {
	lists := map[string]string{}
	for name, message := range hostileFieldShapes(engineLimitHeaders) {
		if strings.HasPrefix(name, "address list") {
			lists[name] = message
		}
	}
	return lists
}

var lastAddressInField = regexp.MustCompile(`[A-Za-z0-9.]+@[A-Za-z0-9.]+`)

// lastListedAddress is the last address the message's To field spells, found in its text and not by
// any reader of address lists.
func lastListedAddress(t *testing.T, message string) string {
	t.Helper()
	_, rest, found := strings.Cut(message, "\r\nTo: ")
	if !found {
		t.Fatalf("no To field in %.100q", message)
	}
	field, _, _ := strings.Cut(rest, "\r\nContent-Type:")
	addresses := lastAddressInField.FindAllString(field, -1)
	if len(addresses) < 2 {
		t.Fatalf("%d addresses in %.100q", len(addresses), field)
	}
	return addresses[len(addresses)-1]
}

// An address test reads a list one address at a time, so a message of a very long list holds the store
// path's limit through the real engine (#1915). It still has to read all of it: the last address of each
// list is one the script can find, and the message is filed by that. "address list, many charsets" is the
// closest, at 7.7x to 7.9x; every other row is at 7x or less.
func TestDeliverHoldsTheLimitThroughTheEngine(t *testing.T) {
	delivery := newSieveDelivery(t)
	for name, message := range engineAddressLists() {
		t.Run(name, func(t *testing.T) {
			delivery.use(t, `require ["fileinto"]; if address :is "to" "nobody@nowhere.test" { fileinto "Never"; }`)
			best, worst, length := delivery.cost(t, name, message)
			t.Logf("delivering %d bytes allocated %.2f to %.2f times over", length, float64(best)/float64(length), float64(worst)/float64(length))
			if best > 8*length {
				t.Errorf("delivering %d bytes allocated %d", length, best)
			}
			stored := delivery.latest(t)
			if stored.Folder != "INBOX" {
				t.Errorf("a list without the address was filed in %q", stored.Folder)
			}
			if digest, err := stored.SealDigest(); err != nil || digest != stored.SealedHash {
				t.Errorf("seal %q for %q (%v)", stored.SealedHash, digest, err)
			}

			last := lastListedAddress(t, message)
			delivery.use(t, fmt.Sprintf(`require ["fileinto"]; if address :is "to" %q { fileinto "Found"; }`, last))
			w, _ := delivery.send(fmt.Sprintf("Message-ID: <engine-found-%s@example.test>\r\n", strings.NewReplacer(" ", "-", ",", "").Replace(name)) + message)
			if w.Code != http.StatusCreated {
				t.Fatalf("%d %.300s", w.Code, w.Body)
			}
			if stored := delivery.latest(t); stored.Folder != "Found" {
				t.Errorf("a list ending in %s was filed in %q", last, stored.Folder)
			}
		})
	}
}

// sieveWholeListScripts are the tests that still take a whole address list from go-sieve: header and
// exists have every address of the field formatted, and size adds up all four fields.
var sieveWholeListScripts = map[string]string{
	"header": `require ["fileinto"]; if header :contains "to" "nobody@nowhere.test" { fileinto "Never"; }`,
	"exists": `require ["fileinto"]; if not exists "to" { fileinto "Never"; }`,
	"size":   `require ["fileinto"]; if size :under 1 { fileinto "Never"; }`,
}

// wholeListCost is what a list may cost through each of those scripts, as a multiple of the message.
type wholeListCost struct{ header, exists, size uint64 }

func (c wholeListCost) of(kind string) uint64 {
	switch kind {
	case "header":
		return c.header
	case "exists":
		return c.exists
	}
	return c.size
}

// sieveWholeListCeilings are the most each row cost on darwin/arm64, linux/arm64 and linux/amd64 —
// the platform CI runs — each with and without the race detector, plus a quarter and rounded up.
// They count bytes allocated, not elapsed time, so a slower CPU does not raise them. On amd64 the
// address rows peak at 7.90x, the closest being "many charsets" at 7.87x-7.94x under -race, which
// leaves about 63 KB before the 8x limit.
var sieveWholeListCeilings = map[string]wholeListCost{
	"address list, 8-bit names":        {header: 14, exists: 11, size: 11},
	"address list, base64 words read":  {header: 11, exists: 9, size: 9},
	"address list, comments":           {header: 11, exists: 11, size: 11},
	"address list, encoded names":      {header: 12, exists: 9, size: 10},
	"address list, many charsets":      {header: 13, exists: 11, size: 11},
	"address list, names no one reads": {header: 13, exists: 9, size: 10},
	"address list, quoted words":       {header: 12, exists: 10, size: 10},
	"address list, short unread names": {header: 15, exists: 14, size: 14},
	"address list, short words read":   {header: 13, exists: 12, size: 12},
	"address list, top level":          {header: 12, exists: 10, size: 10},
	"address list, very short":         {header: 36, exists: 33, size: 31},
	"address list, words in comments":  {header: 12, exists: 10, size: 10},
	"address list, words read":         {header: 11, exists: 9, size: 9},
}

// The store path's limit is 8x, and a script that reads a whole address field is still over it: the test
// has every address formatted. Each is held to what it costs, and fails once it's under 8x.
func TestStillOverTheLimit_SieveWholeListThroughTheEngine(t *testing.T) {
	delivery := newSieveDelivery(t)
	lists := engineAddressLists()
	for kind, script := range sieveWholeListScripts {
		t.Run(kind, func(t *testing.T) {
			delivery.use(t, script)
			for name, message := range lists {
				t.Run(name, func(t *testing.T) {
					best, worst, length := delivery.cost(t, kind+"-"+name, message)
					t.Logf("delivering %d bytes allocated %.2f to %.2f times over", length, float64(best)/float64(length), float64(worst)/float64(length))
					cost, measured := sieveWholeListCeilings[name]
					if !measured {
						t.Fatalf("this row needs a measured ceiling")
					}
					ceiling := cost.of(kind)
					if best > ceiling*length {
						t.Errorf("delivering %d bytes allocated %d, over the %dx this row was measured at", length, best, ceiling)
					}
					if 2*worst <= 15*length {
						t.Errorf("delivering %d bytes allocated at most %d, under 8x: this row belongs back under the limit", length, worst)
					}
				})
			}
		})
	}
}
