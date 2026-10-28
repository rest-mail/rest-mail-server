package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/examplepairs"
	"github.com/restmail/restmail/internal/version"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

const requirementTOTPSecret = "JBSWY3DPEHPK3PXP"

// This ledger supplies storage, not handler answers. Password hashing/checking,
// TOTP, signed owner challenges, token spending, session signing and revocation
// all run in the production router and repositories.
type authRequirementConnection struct {
	capabilityExampleConnection
	t                                                  *testing.T
	op, name, encrypted, password, recovery, publicKey string
	confirmed, revoked, recoveryUsed, tokenUsed        bool
	refreshRevoked                                     bool
	epoch                                              int64
	twoFactorRequired                                  bool
	token                                              map[string]any
	refresh                                            map[string]any
	writes                                             []string
}

func (c *authRequirementConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *authRequirementConnection) Driver() driver.Driver                        { return c }
func (c *authRequirementConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *authRequirementConnection) Begin() (driver.Tx, error)                    { return bulkQueueExampleTx{}, nil }

func (c *authRequirementConnection) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(q, "FROM audit_log"):
		return invitationRows([]string{"seq", "hash"}), nil
	case strings.Contains(q, `FROM "owner_keys"`):
		return invitationRows([]string{"public_key"}, []driver.Value{c.publicKey}), nil
	case strings.Contains(q, "JOIN role_capabilities"):
		return invitationRows([]string{"scope", "capability", "expires_at", "activation_expires_at"}), nil
	case strings.Contains(q, `FROM "mailboxes"`):
		if strings.Contains(q, "owner_id") { // Real grants.Load includes the account's own rights.
			return invitationRows([]string{"address", "owner_id"}, []driver.Value{"admin@restmail.test", nil}), nil
		}
		for _, arg := range args {
			if arg.Value == "nobody@restmail.test" {
				return invitationRows([]string{"id"}), nil
			}
		}
		return importRows(map[string]any{"id": int64(1), "address": "admin@restmail.test", "display_name": "Admin", "active": true,
			"credential_epoch": c.epoch, "two_factor_required": c.twoFactorRequired}), nil
	case strings.HasPrefix(q, `UPDATE "credentials"`) && strings.Contains(q, "RETURNING"):
		c.writes = append(c.writes, q)
		for _, arg := range args {
			if arg.Value == models.CredentialKindTOTP {
				c.revoked = true
			}
		}
		return invitationRows([]string{"id"}), nil
	case strings.Contains(q, `FROM "credentials"`):
		kind := ""
		accountID := int64(1)
		if len(args) > 0 {
			if id, ok := args[0].Value.(int64); ok {
				accountID = id
			}
		}
		for _, arg := range args {
			switch arg.Value {
			case models.CredentialKindTOTP, models.CredentialKindPassword, models.CredentialKindRecoveryCode:
				kind, _ = arg.Value.(string)
			}
		}
		if strings.HasPrefix(q, "SELECT count(*)") {
			n := int64(0)
			if kind == models.CredentialKindTOTP && c.confirmed && !c.revoked {
				n = 1
			}
			return invitationRows([]string{"count"}, []driver.Value{n}), nil
		}
		switch kind {
		case models.CredentialKindPassword:
			return importRows(map[string]any{"id": int64(5), "account_id": accountID, "kind": kind, "secret_hash": c.password}), nil
		case models.CredentialKindTOTP:
			if c.revoked || (strings.Contains(q, "confirmed_at IS NOT NULL") && !c.confirmed) {
				return invitationRows([]string{"id"}), nil
			}
			var confirmed any
			if c.confirmed {
				confirmed = time.Now().Add(-time.Hour)
			}
			return importRows(map[string]any{"id": int64(6), "account_id": accountID, "kind": kind, "encrypted_secret": c.encrypted, "confirmed_at": confirmed}), nil
		case models.CredentialKindRecoveryCode:
			if c.recoveryUsed {
				return invitationRows([]string{"id"}), nil
			}
			return importRows(map[string]any{"id": int64(8), "account_id": accountID, "kind": kind, "secret_hash": c.recovery}), nil
		}
	case strings.HasPrefix(q, `INSERT INTO "credentials"`):
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		if f["kind"] != models.CredentialKindPassword || f["account_id"] != int64(1) {
			return nil, fmt.Errorf("unexpected credential insert: %v", f)
		}
		c.password, _ = f["secret_hash"].(string)
		c.writes = append(c.writes, q)
		return invitationRows([]string{"id"}, []driver.Value{int64(9)}), nil
	case strings.HasPrefix(q, `INSERT INTO "refresh_tokens"`):
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		c.refresh = f
		return invitationRows([]string{"id"}, []driver.Value{int64(10)}), nil
	case strings.HasPrefix(q, `INSERT INTO "one_time_tokens"`):
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		if f["purpose"] == models.OneTimeTokenResetAdmin {
			c.token = f
			c.tokenUsed = false
		}
		return invitationRows([]string{"id"}, []driver.Value{int64(11)}), nil
	case strings.Contains(q, `FROM "one_time_tokens"`):
		if c.token == nil || len(args) == 0 || args[0].Value != c.token["token_hash"] {
			return invitationRows([]string{"id"}), nil
		}
		row := map[string]any{"id": int64(11)}
		for k, v := range c.token {
			row[k] = v
		}
		if c.tokenUsed {
			row["used_at"] = time.Now()
		}
		return importRows(row), nil
	}
	c.t.Errorf("unexpected auth requirement query: %s %v", q, args)
	return nil, fmt.Errorf("unexpected auth requirement query: %s", q)
}

func (c *authRequirementConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.writes = append(c.writes, q)
	switch {
	case strings.HasPrefix(q, "SAVEPOINT "), strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT "), strings.HasPrefix(q, "SELECT pg_advisory_xact_lock"), strings.HasPrefix(q, `INSERT INTO "audit_log"`), strings.HasPrefix(q, `DELETE FROM "one_time_tokens"`):
	case strings.HasPrefix(q, `UPDATE "mailboxes"`):
		if strings.Contains(q, "credential_epoch") {
			c.epoch++
		}
		if strings.Contains(q, "two_factor_required") {
			c.twoFactorRequired = true
		}
	case strings.HasPrefix(q, `UPDATE "credentials"`):
		if strings.Contains(q, "confirmed_at") {
			c.confirmed = true
		} else {
			c.recoveryUsed = true
		}
	case strings.HasPrefix(q, `UPDATE "refresh_tokens"`):
		if len(args) != 4 || args[0].Value != models.RefreshTokenRevoked || args[2].Value != int64(1) || args[3].Value != models.RefreshTokenActive {
			return nil, fmt.Errorf("unexpected refresh transition")
		}
		c.refreshRevoked = true
	case strings.HasPrefix(q, `UPDATE "one_time_tokens"`):
		if c.tokenUsed || c.token == nil || len(args) != 4 || args[1].Value != c.token["token_hash"] || args[2].Value != models.OneTimeTokenResetAdmin || !c.token["expires_at"].(time.Time).After(time.Now()) {
			return driver.RowsAffected(0), nil
		}
		c.tokenUsed = true
	default:
		c.t.Errorf("unexpected auth requirement write: %s %v", q, args)
		return nil, fmt.Errorf("unexpected auth requirement write: %s", q)
	}
	return driver.RowsAffected(1), nil
}

func requirementHash(t *testing.T, value string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return "{BLF-CRYPT}" + string(hash)
}

func newAuthRequirement(t *testing.T, op, name string) (*authRequirementConnection, *Routers, *auth.JWTService, string) {
	t.Helper()
	encrypted, err := crypto.EncryptString(requirementTOTPSecret, accessMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	c := &authRequirementConnection{t: t, op: op, name: name, encrypted: encrypted, confirmed: name != "confirmed" && name != "invalid_code", password: requirementHash(t, "the current password"), recovery: requirementHash(t, auth.NormalizeRecoveryCode("AAAA-BBBB-CCCC"))}
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	c.publicKey = string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, 7*24*time.Hour)
	r := NewRouters(requirementDB(t, c), jwt, &config.Config{Environment: "test", MasterKey: accessMasterKey, TOTP2FAEnabled: true, JWTSecret: routerTestSecret}, nil)
	t.Cleanup(r.Close)
	session, err := jwt.GenerateSession(1, "admin@restmail.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	return c, r, jwt, session.AccessToken
}

func TestAuthStateRequirementExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, tc := range []struct{ op, name string }{
		{"changeOwnPassword", "changed"}, {"changeOwnPassword", "same_password"},
		{"confirmTwoFactor", "already_enabled"}, {"confirmTwoFactor", "confirmed"}, {"confirmTwoFactor", "invalid_code"},
		{"disableTwoFactor", "disabled"}, {"disableTwoFactor", "wrong_recovery_code"}, {"enrollTwoFactor", "already_enabled"},
		{"recoverAccount", "recovered"}, {"createAPIKey", "password_only"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			pair := pairs[tc.op][tc.name]
			c, r, jwt, token := newAuthRequirement(t, tc.op, tc.name)
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
			if tc.name == "wrong_recovery_code" {
				c.recovery = requirementHash(t, auth.NormalizeRecoveryCode("DDDD-EEEE-FFFF"))
			}
			pair.Request.Body, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req := requirementRequest(routes[tc.op], pair)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, req)
			if tc.op == "recoverAccount" && rr.Code == 200 {
				var got map[string]map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				claims, err := jwt.ValidateAccessToken(got["data"]["access_token"].(string))
				if err != nil || claims.CredentialEpoch != int(c.epoch) || !claims.HasAMR(auth.AMROTP) {
					t.Fatalf("invalid recovered session: %v", err)
				}
				refresh, err := jwt.ValidateRefreshToken(got["data"]["refresh_token"].(string))
				if err != nil || refresh.ID != c.refresh["jti"] {
					t.Fatalf("invalid persisted refresh: %v", err)
				}
				left := got["data"]["refresh_expires_in"].(float64)
				if left < 604798 || left > 604800 {
					t.Fatal("incorrect refresh lifetime")
				}
				got["data"]["refresh_expires_in"] = float64(604800)
				got["data"]["access_token"] = "example-access-token"
				got["data"]["refresh_token"] = "example-refresh-token"
				out, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				rr.Body.Reset()
				rr.Body.Write(out)
			}
			assertRequirementResponse(t, pair, rr)
			switch tc.name {
			case "changed", "recovered":
				if c.epoch != 1 || !c.refreshRevoked || auth.CheckPassword("a new password of my own", c.password) != nil {
					t.Fatal("password change did not replace credential and end sessions")
				}
				if tc.name == "recovered" && !c.recoveryUsed {
					t.Fatal("recovery code was not consumed")
				}
			case "confirmed":
				if !c.confirmed {
					t.Fatal("enrollment stayed pending")
				}
			case "disabled":
				if !c.revoked || c.epoch != 1 || !c.refreshRevoked {
					t.Fatal("authenticator or sessions survived disable")
				}
			default:
				if c.epoch != 0 || c.revoked || c.recoveryUsed || c.refreshRevoked {
					t.Fatal("refusal changed credentials")
				}
			}
		})
	}
}

func TestOwnerStateRequirementExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, tc := range []struct{ op, name string }{
		{"ownerChallenge", "issued"}, {"ownerStatus", "status"}, {"ownerStatus", "not_status_key"},
		{"ownerToken", "minted"}, {"ownerToken", "no_account"}, {"ownerToken", "not_owner"}, {"resetAdmin", "reset"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			c, r, jwt, _ := newAuthRequirement(t, tc.op, tc.name)
			rr := doRequest(r.Public, http.MethodPost, routes["ownerChallenge"].path, "", "")
			var challenge struct {
				Data struct {
					Challenge string
					ExpiresAt time.Time `json:"expires_at"`
				}
			}
			if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &challenge) != nil || time.Until(challenge.Data.ExpiresAt) < time.Minute || time.Until(challenge.Data.ExpiresAt) > 2*time.Minute {
				t.Fatalf("invalid challenge: %d %s", rr.Code, rr.Body.String())
			}
			if tc.op != "ownerChallenge" {
				pair := pairs[tc.op][tc.name]
				var body map[string]any
				if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
					t.Fatal(err)
				}
				purpose, address := "reset-admin", "admin@restmail.test"
				if tc.op == "ownerStatus" {
					purpose, address = "status", ""
				}
				if tc.name == "no_account" {
					address = "nobody@restmail.test"
				}
				seed := byte(2)
				if tc.name == "not_owner" || tc.name == "not_status_key" {
					seed = 3
				}
				signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
				if err != nil {
					t.Fatal(err)
				}
				sig, err := signer.Sign(rand.Reader, bootstrap.OwnerMessage(challenge.Data.Challenge, purpose, address))
				if err != nil {
					t.Fatal(err)
				}
				signed := map[string]any{"challenge": challenge.Data.Challenge, "public_key": string(ssh.MarshalAuthorizedKey(signer.PublicKey())), "signature": base64.StdEncoding.EncodeToString(ssh.Marshal(sig))}
				if tc.op != "ownerStatus" {
					signed["purpose"], signed["address"] = purpose, address
				}
				data, err := json.Marshal(signed)
				if err != nil {
					t.Fatal(err)
				}
				path := routes[tc.op].path
				if tc.op == "resetAdmin" {
					path = routes["ownerToken"].path
				}
				rr = doRequest(r.Public, http.MethodPost, path, "", string(data))
				if tc.op == "resetAdmin" {
					var minted struct{ Data struct{ Token string } }
					if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &minted) != nil || minted.Data.Token == "" {
						t.Fatalf("mint reset prerequisite: %d %s", rr.Code, rr.Body.String())
					}
					body["token"] = minted.Data.Token
					data, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					rr = doRequest(r.Public, http.MethodPost, routes[tc.op].path, "", string(data))
					if rr.Code == 200 {
						var got map[string]map[string]any
						if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						claims, err := jwt.ValidateAccessToken(got["data"]["access_token"].(string))
						if err != nil || !claims.SetupPending || claims.CredentialEpoch != 1 || !c.tokenUsed || !c.revoked || !c.refreshRevoked {
							t.Fatalf("reset did not revoke credentials and require setup: %v", err)
						}
						if auth.CheckPassword("a new password of my own", c.password) != nil {
							t.Fatal("reset did not set chosen password")
						}
						refresh, err := jwt.ValidateRefreshToken(got["data"]["refresh_token"].(string))
						if err != nil || refresh.ID != c.refresh["jti"] {
							t.Fatalf("invalid reset refresh: %v", err)
						}
						left := got["data"]["refresh_expires_in"].(float64)
						if left < 604798 || left > 604800 {
							t.Fatal("incorrect refresh lifetime")
						}
						got["data"]["refresh_expires_in"] = float64(604800)
						got["data"]["access_token"] = "example-access-token"
						got["data"]["refresh_token"] = "example-refresh-token"
						data, err := json.Marshal(got)
						if err != nil {
							t.Fatal(err)
						}
						rr.Body.Reset()
						rr.Body.Write(data)
					}
				}
			}
			pair := pairs[tc.op][tc.name]
			if rr.Code == 200 && tc.op != "resetAdmin" {
				var got, want map[string]map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				switch tc.op {
				case "ownerChallenge":
					got["data"]["challenge"], got["data"]["expires_at"] = want["data"]["challenge"], want["data"]["expires_at"]
				case "ownerToken":
					plain, err := base64.RawURLEncoding.DecodeString(got["data"]["token"].(string))
					if err != nil || len(plain) != 32 || c.token == nil {
						t.Fatal("reset token was not minted into ledger")
					}
					sum := sha256.Sum256([]byte(got["data"]["token"].(string)))
					if hex.EncodeToString(sum[:]) != c.token["token_hash"] {
						t.Fatal("minted token does not match stored hash")
					}
					expires, err := time.Parse(time.RFC3339, got["data"]["expires_at"].(string))
					if err != nil || time.Until(expires) < 14*time.Minute || time.Until(expires) > 15*time.Minute {
						t.Fatal("invalid reset token lifetime")
					}
					got["data"]["token"], got["data"]["expires_at"] = want["data"]["token"], want["data"]["expires_at"]
				case "ownerStatus":
					for key, value := range map[string]string{"version": version.Version, "commit": version.Commit, "built_at": version.BuildDate, "started_at": version.Started.UTC().Format(time.RFC3339)} {
						if got["data"][key] != value {
							t.Fatalf("incorrect running %s: %v", key, got["data"][key])
						}
						got["data"][key] = want["data"][key]
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
		})
	}
}
