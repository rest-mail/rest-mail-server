package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/examplepairs"
)

// Grant and activation rows are controlled; production authorization, fresh
// second factors, transitions and transactional audit writes run unchanged.
type grantRequirementConnection struct {
	*authRequirementConnection
	mode                     string
	live                     bool
	created, ended, eligible bool
	activation               map[string]any
}

func (c *grantRequirementConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *grantRequirementConnection) Driver() driver.Driver                        { return c }
func (c *grantRequirementConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *grantRequirementConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(q, "JOIN role_capabilities"):
		capability, scope := "domains:read", "domain:example.com"
		if c.op == "createGrant" {
			capability, scope = "*", "*"
		}
		if c.name == "exceeds_held" {
			capability = "users:write"
		}
		return invitationRows([]string{"scope", "capability", "expires_at", "activation_expires_at"}, []driver.Value{scope, capability, nil, nil}), nil
	case strings.Contains(q, `FROM "mailboxes"`):
		if strings.Contains(q, "owner_id") {
			return invitationRows([]string{"address", "owner_id"}, []driver.Value{"admin@example.com", nil}), nil
		}
		if c.name == "no_holder" {
			return invitationRows([]string{"id"}), nil
		}
		return invitationRows([]string{"id"}, []driver.Value{int64(2)}), nil
	case strings.Contains(q, "FROM role_capabilities"):
		capability := "domains:read"
		if c.name == "exceeds_held" {
			capability = "*"
		}
		return invitationRows([]string{"name"}, []driver.Value{capability}), nil
	case strings.Contains(q, `FROM "roles"`):
		return invitationRows([]string{"id", "name"}, []driver.Value{int64(2), "admin"}), nil
	case strings.Contains(q, `FROM "grants"`):
		return importRows(map[string]any{"id": int64(7), "holder_kind": "account", "holder_id": int64(2), "role_id": int64(2), "scope": "domain:example.com", "mode": c.mode, "granted_by_kind": "account", "granted_by_id": int64(1), "granted_at": time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}), nil
	case strings.HasPrefix(q, `INSERT INTO "grants"`):
		if c.name == "exists" {
			return invitationRows([]string{"id"}), nil
		}
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		if f["holder_id"] != int64(2) || f["role_id"] != int64(2) || f["mode"] != "eligible" || f["scope"] != "domain:example.com" || f["granted_by_id"] != int64(1) {
			return nil, fmt.Errorf("incorrect grant insert: %v", f)
		}
		c.created = true
		return invitationRows([]string{"id"}, []driver.Value{int64(7)}), nil
	case strings.Contains(q, `FROM "grant_activations"`):
		if strings.Contains(q, "expires_at <=") || !c.live {
			return invitationRows([]string{"id"}), nil
		}
		return importRows(c.activation), nil
	case strings.HasPrefix(q, `INSERT INTO "grant_activations"`):
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		if f["grant_id"] != int64(7) || f["reason"] != "Rotate the DKIM key" {
			return nil, fmt.Errorf("incorrect activation: %v", f)
		}
		c.activation = f
		c.activation["id"] = int64(3)
		c.live = true
		c.created = true
		return invitationRows([]string{"id"}, []driver.Value{int64(3)}), nil
	}
	return c.authRequirementConnection.QueryContext(ctx, q, args)
}
func (c *grantRequirementConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.HasPrefix(q, `UPDATE "grant_activations"`):
		c.live = false
		c.ended = true
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(q, `UPDATE "grants"`):
		c.mode = "eligible"
		c.eligible = true
		return driver.RowsAffected(1), nil
	}
	return c.authRequirementConnection.ExecContext(ctx, q, args)
}

func TestGrantStateRequirementExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, tc := range []struct{ op, name string }{
		{"createGrant", "granted"}, {"createGrant", "exists"}, {"createGrant", "no_holder"}, {"createGrant", "exceeds_held"},
		{"activateGrant", "activated"}, {"activateGrant", "already_active"}, {"activateGrant", "invalid_code"},
		{"deactivateGrant", "not_active"}, {"deactivateGrant", "switched_off"},
		{"makeGrantEligible", "already_eligible"}, {"makeGrantEligible", "invalid_code"}, {"makeGrantEligible", "now_eligible"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			encrypted, err := crypto.EncryptString(requirementTOTPSecret, accessMasterKey)
			if err != nil {
				t.Fatal(err)
			}
			c := &grantRequirementConnection{authRequirementConnection: &authRequirementConnection{t: t, op: tc.op, name: tc.name, confirmed: true, encrypted: encrypted}, mode: "eligible"}
			if tc.op == "makeGrantEligible" && tc.name != "already_eligible" {
				c.mode = "active"
			}
			if tc.name == "already_active" || tc.name == "switched_off" {
				c.live = true
			}
			now := time.Now()
			c.activation = map[string]any{"id": int64(3), "grant_id": int64(7), "activated_at": now.Add(-time.Minute), "expires_at": now.Add(time.Hour), "reason": "Rotate the DKIM key"}
			jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
			r := NewRouters(requirementDB(t, c), jwt, &config.Config{Environment: "test", MasterKey: accessMasterKey, TOTP2FAEnabled: true}, nil)
			t.Cleanup(r.Close)
			actor := uint(2)
			if tc.op == "createGrant" {
				actor = testAdminHolding([]string{"users:write"})
			}
			session, err := jwt.GenerateSession(actor, "admin@example.com", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
			if err != nil {
				t.Fatal(err)
			}
			// The example's granter is account 1; give that identity the same middleware grants.
			if tc.op == "createGrant" {
				testGrants.Lock()
				previous, existed := testGrants.held[1]
				testGrants.held[1] = testGrants.held[actor]
				testGrants.Unlock()
				t.Cleanup(func() {
					testGrants.Lock()
					defer testGrants.Unlock()
					if existed {
						testGrants.held[1] = previous
					} else {
						delete(testGrants.held, 1)
					}
				})
				session, err = jwt.GenerateSession(1, "admin@example.com", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			pair := pairs[tc.op][tc.name]
			var body map[string]any
			if len(pair.Request.Body) > 0 {
				if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
					t.Fatal(err)
				}
			}
			if body["totp_code"] == "123456" {
				body["totp_code"], err = auth.GenerateTOTPCode(requirementTOTPSecret, time.Now())
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(body) > 0 {
				pair.Request.Body, err = json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
			}
			req := requirementRequest(routes[tc.op], pair)
			req.Header.Set("Authorization", "Bearer "+session.AccessToken)
			rr := httptest.NewRecorder()
			started := time.Now()
			r.Public.ServeHTTP(rr, req)
			finished := time.Now()
			if rr.Code < 300 && rr.Code >= 200 {
				var got, want map[string]map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				if tc.op == "createGrant" {
					stamp, err := time.Parse(time.RFC3339, got["data"]["granted_at"].(string))
					if err != nil || stamp.Before(started.Truncate(time.Second)) || stamp.After(finished) {
						t.Fatal("incorrect grant time")
					}
					got["data"]["granted_at"] = want["data"]["granted_at"]
				}
				if tc.name == "activated" {
					activation := got["data"]["grant"].(map[string]any)["activation"].(map[string]any)
					start, err := time.Parse(time.RFC3339, activation["activated_at"].(string))
					if err != nil || start.Before(started.Truncate(time.Second)) || start.After(finished) {
						t.Fatal("incorrect activation time")
					}
					end, err := time.Parse(time.RFC3339, activation["expires_at"].(string))
					if err != nil || end.Sub(start) != time.Hour {
						t.Fatal("incorrect activation lifetime")
					}
					left := activation["seconds_left"].(float64)
					if left < 3598 || left > 3600 {
						t.Fatal("incorrect seconds left")
					}
					expected := want["data"]["grant"].(map[string]any)["activation"].(map[string]any)
					for _, key := range []string{"activated_at", "expires_at", "seconds_left"} {
						activation[key] = expected[key]
					}
				}
				data, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				rr.Body.Reset()
				rr.Body.Write(data)
			}
			assertRequirementResponse(t, pair, rr)
			switch tc.name {
			case "granted", "activated":
				if !c.created {
					t.Fatal("no persisted grant/activation")
				}
			case "switched_off":
				if !c.ended || c.live {
					t.Fatal("activation not ended")
				}
			case "now_eligible":
				if !c.eligible {
					t.Fatal("grant not made eligible")
				}
			default:
				if c.created || c.ended || c.eligible {
					t.Fatal("refused grant operation wrote state")
				}
			}
		})
	}
}
