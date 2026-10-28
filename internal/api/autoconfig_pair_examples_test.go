package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"encoding/xml"
	"errors"
	"github.com/restmail/restmail/internal/examplepairs"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

type autoconfigExampleConnection struct {
	capabilityExampleConnection
	found bool
}

func (c autoconfigExampleConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c autoconfigExampleConnection) Driver() driver.Driver                        { return c }
func (c autoconfigExampleConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c autoconfigExampleConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `FROM "domains"`) || len(args) < 2 {
		return nil, errors.New("unexpected autoconfig lookup")
	}
	rows := &authReadSnapshotRows{columns: []string{"id", "name", "active"}}
	if c.found && args[0].Value == "example.com" && args[1].Value == true {
		rows.rows = [][]driver.Value{{int64(1), "example.com", true}}
	}
	return rows, nil
}

func autoconfigExampleDB(t *testing.T, found bool) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(autoconfigExampleConnection{found: found})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func autoconfigExampleRouters(t *testing.T) map[string]http.Handler {
	t.Helper()
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	makeRouter := func(db *gorm.DB, mailHost string) http.Handler {
		r := NewRouters(db, jwt, &config.Config{Environment: "test", GatewayHostname: mailHost}, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	available := makeRouter(autoconfigExampleDB(t, true), "mail.example.com")
	return map[string]http.Handler{
		"available": available, "invalid_address": available,
		"domain_not_found":       makeRouter(autoconfigExampleDB(t, false), "mail.example.com"),
		"internal_error":         makeRouter(newFailingGormDB(t), "mail.example.com"),
		"mail_host_unconfigured": makeRouter(newFailingGormDB(t), ""),
	}
}

func TestMozillaExamplesMatchRegisteredResponses(t *testing.T) {
	routers := autoconfigExampleRouters(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{"available": 200, "invalid_address": 400, "domain_not_found": 404, "internal_error": 500, "mail_host_unconfigured": 503}
	for operation, path := range mozillaExamplePaths {
		for name, status := range statuses {
			t.Run(operation+"/"+name, func(t *testing.T) {
				pair, ok := pairs[operation][name]
				examplepairs.Proven(t, operation, name)
				if !ok {
					t.Fatal("named autoconfig pair is missing")
				}
				email := pair.Request.Query["emailaddress"].(string)
				req := httptest.NewRequest(http.MethodGet, path+"?"+url.Values{"emailaddress": {email}}.Encode(), nil)
				rr := httptest.NewRecorder()
				routers[name].ServeHTTP(rr, req)
				if rr.Code != status {
					t.Fatalf("got %d, want %d: %s", rr.Code, status, rr.Body.String())
				}
				media, params, err := mime.ParseMediaType(rr.Header().Get("Content-Type"))
				if err != nil {
					t.Fatal(err)
				}
				if media != "application/json" && params["charset"] != "utf-8" {
					t.Fatal("text/XML charset is not UTF-8")
				}
				if status == 200 {
					var document struct {
						XMLName  xml.Name
						Version  string `xml:"version,attr"`
						Provider struct {
							ID       string `xml:"id,attr"`
							Domain   string `xml:"domain"`
							Incoming struct {
								Host   string `xml:"hostname"`
								Port   int    `xml:"port"`
								Socket string `xml:"socketType"`
							} `xml:"incomingServer"`
							Outgoing struct {
								Host   string `xml:"hostname"`
								Port   int    `xml:"port"`
								Socket string `xml:"socketType"`
							} `xml:"outgoingServer"`
						} `xml:"emailProvider"`
					}
					if err := xml.Unmarshal(rr.Body.Bytes(), &document); err != nil {
						t.Fatal(err)
					}
					p := document.Provider
					if document.XMLName.Local != "clientConfig" || document.Version != "1.1" || p.ID != "example.com" || p.Domain != "example.com" || p.Incoming.Host != "mail.example.com" || p.Incoming.Port != 993 || p.Incoming.Socket != "SSL" || p.Outgoing.Host != "mail.example.com" || p.Outgoing.Port != 465 || p.Outgoing.Socket != "SSL" {
						t.Fatalf("wrong discovery wiring: %+v", document)
					}
				}
				if pair.Response.Status != rr.Code || pair.Response.MediaType != media {
					t.Fatal("pair status or media type differs")
				}
				if media == "application/json" {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				} else {
					var expected string
					if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
						t.Fatal(err)
					}
					if expected != rr.Body.String() {
						t.Fatalf("text/XML differs: got %q, want %q", rr.Body.String(), expected)
					}
				}
			})
		}
	}
	for _, warning := range describedRouters(t).generator("Mozilla examples").Report() {
		if _, selected := mozillaExamplePaths[warning.OperationID]; selected {
			t.Errorf("autoconfig still has a generator warning: %v", warning)
		}
	}
}

func TestMozillaExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for the real autoconfig boundary check")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers := autoconfigExampleRouters(t)
	paths := []string{mozillaExamplePaths["mozillaAutoconfig"], mozillaExamplePaths["mozillaAutoconfigWellKnown"]}
	for name, status := range map[string]string{"available": "200", "invalid_address": "400", "domain_not_found": "404", "internal_error": "500", "mail_host_unconfigured": "503"} {
		t.Run(name, func(t *testing.T) { checkAuthReadWithVertrag(t, binary, routers[name], "", status, paths) })
	}
}
