package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// A mail client reaches the API only in a session its gateway opened (#1586).
// The fixed-row example fixtures answer one: the IMAP gateway's session for
// app password 90 of account 10, which the wrapped connection's rows describe.

// fixtureGatewayToken is the session's token, sent as Bearer.
const fixtureGatewayToken = "rmgs_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// gatewaySessionConnector answers the session's own rows, and everything else
// from the connector it wraps.
type gatewaySessionConnector struct{ inner driver.Connector }

func (c gatewaySessionConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return gatewaySessionConn{conn}, nil
}

func (c gatewaySessionConnector) Driver() driver.Driver { return c.inner.Driver() }

type gatewaySessionConn struct{ driver.Conn }

func (c gatewaySessionConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, `FROM "gateway_sessions"`):
		// Just used, so using it again writes nothing.
		now := time.Now()
		return &authReadSnapshotRows{
			columns: []string{"id", "token_hash", "account_id", "credential_id", "credential_kind", "service", "protocol", "created_at", "last_used_at", "closed_at"},
			rows: [][]driver.Value{{int64(7), "hash", int64(10), int64(90), models.CredentialKindAppPassword,
				models.GatewayServiceIMAP, "imap", now, now, nil}},
		}, nil
	case strings.Contains(query, `FROM "credentials" JOIN mailboxes`) && strings.Contains(query, "count("):
		// The session's credential is still usable.
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, errors.New("the wrapped fixture connection cannot query")
	}
	return queryer.QueryContext(ctx, query, args)
}

func (c gatewaySessionConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, errors.New("the wrapped fixture connection cannot write")
	}
	return execer.ExecContext(ctx, query, args)
}

// gatewaySessionScenario is the app password's scenario: the IMAP gateway,
// with its certificate, calls the internal listener of a router over
// connector in the fixture's session.
func gatewaySessionScenario(t *testing.T, connector driver.Connector, jwt *auth.JWTService, cfg config.Config, prepare ...func(*gorm.DB)) accountReadScenario {
	t.Helper()
	pool := sql.OpenDB(gatewaySessionConnector{connector})
	t.Cleanup(func() { _ = pool.Close() })
	db := openAccountReadDB(t, pool)
	for _, p := range prepare {
		p(db)
	}
	cfg.InternalMTLSEnabled = true
	routers := NewRouters(db, jwt, &cfg, nil)
	t.Cleanup(routers.Close)
	return accountReadScenario{asGateway(imapCN, routers.Internal), fixtureGatewayToken}
}
