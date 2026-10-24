package queue

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type contractNoDB struct{}

func (contractNoDB) Open(string) (driver.Conn, error) {
	return nil, errors.New("no database in this test")
}

type contractNoConn struct{}

func (contractNoConn) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("no database in this test")
}
func (contractNoConn) Driver() driver.Driver { return contractNoDB{} }

// answered is one request REST Mail's own router answered, as the outbound client sent it.
type answered struct {
	method, path, body string
	status             int
	answer             string
}

// The outbound RESTMAIL client sends what the RESTMAIL operations describe (#678). Its
// discovery and delivery requests go to REST Mail's own router, whose registered routes bind
// a request exactly as the description says (go-oapifly: what is described is what is bound),
// so a request the description refuses is a 422 validation_failed or a 400 bad_request there.
// With no database behind it, one the description accepts goes on to the handler and fails
// inside it instead.
func TestRESTMAILClientRequestsMatchTheDescription(t *testing.T) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(contractNoConn{})}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	router := api.NewRouter(gdb, auth.NewJWTService("restmail-contract-secret", time.Minute, time.Hour),
		&config.Config{Environment: "test", GatewayHostname: "mx.example.test"}, nil)

	var mu sync.Mutex
	var seen []answered
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		mu.Lock()
		seen = append(seen, answered{r.Method, r.URL.RequestURI(), string(body), rec.Code, rec.Body.String()})
		mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(srv.Close)

	w := &Worker{sendDeadline: defaultSendDeadline, allowPrivateDest: true, hostname: "test.local",
		restmailRoots: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}

	// Discovery, as a peer asks the MX host for a domain (restmailDiscover), then delivery to
	// the endpoint it would have named (restmailDeliver).
	_, _ = w.discoverRESTMAILEndpoint(srv.URL + "/.well-known/restmail?domain=example.net")
	_ = w.deliverRESTMAILHTTPS(srv.URL+"/restmail", testItem())

	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"GET /.well-known/restmail": false, "POST /restmail/messages": false}
	for _, a := range seen {
		route := a.method + " " + strings.SplitN(a.path, "?", 2)[0]
		if _, expected := want[route]; !expected {
			t.Errorf("the client sent %s %s, which is not a RESTMAIL operation it should use", a.method, a.path)
			continue
		}
		want[route] = true
		t.Logf("%s %s -> %d %s", a.method, a.path, a.status, strings.TrimSpace(a.answer))
		// The binding's refusals: a body or parameter the description refuses (422
		// validation_failed), or a body that is not the shape it reads, or a parameter the
		// handler cannot read (400 bad_request). Other answers, such as 422 undeliverable when
		// no recipient is hosted, are what the server made of a well-formed request.
		var refusal struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(a.answer), &refusal)
		if code := refusal.Error.Code; code == "validation_failed" || code == "bad_request" {
			t.Errorf("%s %s with %s was refused as the description reads it: %d %s", a.method, a.path, a.body, a.status, a.answer)
		}
	}
	for route, sent := range want {
		if !sent {
			t.Errorf("the client never sent %s", route)
		}
	}
}
