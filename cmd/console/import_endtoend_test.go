package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/rest-mail/rest-mail-server/internal/api"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
)

// These tests are the two "done when" of migrating in (#734). Everything an
// operator's migration goes through is the real thing: the restmail commands,
// the API's public router on PostgreSQL, and the IMAP gateway's server and
// backend on an implicit TLS port, as cmd/imap-gateway wires them, with an IMAP
// client signing in over the network. The other tests of these commands answer
// from a fake server, so they cannot show that a user can sign in afterwards or
// that a second run stores nothing.

// Dovecot 2.3.21 wrote these hashes, with `doveadm pw -s <scheme> -p <password>`.
const (
	dovecotSHA512Password = "sha512-old-password"
	dovecotSHA512Hash     = "{SHA512-CRYPT}$6$p4akVbS.Pzk1AkhO$VKVlV1TusYSvJsEy5W5sf51w.wbOj7ueubKJ51nP9BgJbCA85aquFzQbW65nk9eluMTG4jgP5MlqJ2bgs5MOa0"
	dovecotBLFPassword    = "blf-crypt-old-password"
	dovecotBLFHash        = "{BLF-CRYPT}$2y$05$te/jwHF6ODrzWhEucu6EwOjy8fqiTiQRNnkS5te0d/v1ioEMKAzVu"
	dovecotMD5Password    = "md5-old-password"
	dovecotMD5Hash        = "{MD5-CRYPT}$1$7kDwMYu8$ZNLfiti8aTYan.NHyTZqm0"
	dovecotPlainPassword  = "plain-old-password"
	dovecotPlainHash      = "{PLAIN}plain-old-password"
)

const migrationDomain = "example.test"

// imapGatewayCN names the IMAP gateway in its internal mTLS client certificate.
const imapGatewayCN = "rest-mail-imap-gateway"

// migrationServer is the destination of a migration, with what an administrator
// of its domain would use: a session signed in with a second factor to make
// accounts, and an API key to import mail.
type migrationServer struct {
	db       *gorm.DB
	apiURL   string
	imapAddr string
	imapTLS  *tls.Config
	session  string
	apiKey   string
	client   *http.Client
}

func newMigrationServer(t *testing.T) *migrationServer {
	t.Helper()
	db := migrationDatabase(t)

	jwt := auth.NewJWTService("migration-test-signing-key-of-no-value", 5*time.Minute, time.Hour)
	routers := api.NewRouters(db, jwt, &config.Config{Environment: "test", SMTPMaxMessageSize: 1 << 20, APIMaxBodyBytes: 4 << 20,
		InternalMTLSEnabled: true}, nil)
	t.Cleanup(routers.Close)
	apiServer := httptest.NewServer(routers.Public)
	t.Cleanup(apiServer.Close)

	s := &migrationServer{db: db, apiURL: apiServer.URL, client: apiServer.Client()}
	s.session, s.apiKey = migrationCredentials(t, db, jwt)
	s.imapAddr, s.imapTLS = startIMAPGateway(t, apiServer.URL, routers.Internal)
	return s
}

// asIMAPGateway serves internal as the internal listener does to the IMAP
// gateway: every request carries the gateway's verified client certificate.
// The TLS handshake that checks it is the listener's, not under test here.
func asIMAPGateway(internal http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaf := &x509.Certificate{Subject: pkix.Name{CommonName: imapGatewayCN}}
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
		internal.ServeHTTP(w, r)
	})
}

// migrationDatabase is a database of the test's own, migrated as the server
// migrates it: audit entries cannot be deleted once written, so they must not
// stay in one that other tests read.
func migrationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	port, err := strconv.Atoi(migrationEnv("DB_PORT", "5432"))
	if err != nil {
		t.Fatalf("DB_PORT: %v", err)
	}
	cfg := config.Config{
		DBHost: migrationEnv("DB_HOST", "localhost"),
		DBPort: port,
		DBName: migrationEnv("DB_NAME", "restmail"),
		DBUser: migrationEnv("DB_USER", "restmail"),
		DBPass: migrationEnv("DB_PASS", "restmail"),
	}
	admin, err := rmdb.Connect(&cfg)
	if err != nil {
		testdb.Unreachable(t, "skipped: no database reachable (%v)", err)
	}
	name := fmt.Sprintf("restmail_migration_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatalf("create database: %v", err)
	}
	cfg.DBName = name
	db, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Fatalf("connect to the new database: %v", err)
	}
	t.Cleanup(func() {
		closeMigrationDB(db)
		admin.Exec("DROP DATABASE IF EXISTS " + name)
		closeMigrationDB(admin)
	})
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatalf("roles: %v", err)
	}
	return db
}

func migrationEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func closeMigrationDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// migrationCredentials makes the domain and its administrator, and returns that
// administrator's session, as signing in with a second factor makes it, and a
// key holding only what the import commands ask for there.
func migrationCredentials(t *testing.T, db *gorm.DB, jwt *auth.JWTService) (string, string) {
	t.Helper()
	domain := models.Domain{Name: migrationDomain, Active: true, DefaultQuotaBytes: 1 << 30}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	holder := models.Mailbox{DomainID: domain.ID, LocalPart: "migrator", Address: "migrator@" + migrationDomain, Active: true}
	if err := db.Create(&holder).Error; err != nil {
		t.Fatalf("create the administrator: %v", err)
	}
	var adminRole models.Role
	if err := db.Where("name = ?", "admin").First(&adminRole).Error; err != nil {
		t.Fatalf("find the admin role: %v", err)
	}
	if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: holder.ID, RoleID: adminRole.ID,
		Scope: grants.Domain(migrationDomain), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatalf("grant the admin role: %v", err)
	}
	_, key, err := repositories.NewCredentialRepository(db).CreateAPIKey(holder.ID, holder.ID, "migration", []auth.ScopedPermission{
		{Capability: "accounts:write", Scope: grants.Domain(migrationDomain)},
		{Capability: models.CapMailImport, Scope: grants.Domain(migrationDomain)},
	}, nil)
	if err != nil {
		t.Fatalf("create the API key: %v", err)
	}
	session, err := jwt.GenerateSession(holder.ID, holder.Address, holder.CredentialEpoch, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatalf("make the session: %v", err)
	}
	return session.AccessToken, key
}

// startIMAPGateway serves REST Mail's IMAP as cmd/imap-gateway does: the
// gateway's backend over the API client, on an implicit TLS port. Sign-in
// opens a gateway session on the internal listener, as the IMAP gateway
// (#1586).
func startIMAPGateway(t *testing.T, apiURL string, internal http.Handler) (string, *tls.Config) {
	t.Helper()
	certificate, roots := migrationCertificate(t)
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 20, MaxGlobal: 1000})
	limiter.SetProtocol("imap")
	internalServer := httptest.NewServer(asIMAPGateway(internal))
	t.Cleanup(internalServer.Close)
	client := apiclient.New(apiURL, apiclient.WithMessageDeadline(30*time.Second),
		apiclient.WithInternalMTLS(internalServer.URL, nil))
	server := imapserver.New("imap."+migrationDomain, imapgw.NewBackend(client, limiter),
		&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}, limiter)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Serve(imapserver.Listeners{IMAPTLS: listener})
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "imap." + migrationDomain, MinVersion: tls.VersionTLS12}
}

func migrationCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "imap." + migrationDomain},
		DNSNames:     []string{"imap." + migrationDomain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},

		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

// importPasswdFile runs restmail import-passwd-file on the file's contents,
// with token as RESTMAIL_API_KEY.
func (s *migrationServer) importPasswdFile(t *testing.T, token, contents string) (int, map[string][]passwdFileResult) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := runImportPasswdFile(context.Background(), []string{"--server", s.apiURL, "--domain", migrationDomain, "--json", path},
		strings.NewReader(""), token, s.client, &out, &errs)
	var outcome map[string][]passwdFileResult
	if err := json.Unmarshal(out.Bytes(), &outcome); err != nil {
		t.Fatalf("not one JSON object: %v\nstdout %s\nstderr %s", err, &out, &errs)
	}
	return code, outcome
}

func (s *migrationServer) storedHash(t *testing.T, address string) models.Credential {
	t.Helper()
	var mailbox models.Mailbox
	if err := s.db.Where("address = ?", address).First(&mailbox).Error; err != nil {
		t.Fatalf("find %s: %v", address, err)
	}
	passwords, err := repositories.NewCredentialRepository(s.db).Passwords(mailbox.ID, time.Now())
	if err != nil || len(passwords) != 1 {
		t.Fatalf("%s holds %d passwords: %v", address, len(passwords), err)
	}
	return passwords[0]
}

// signIn opens an IMAP connection and sends LOGIN with what it is given.
func (s *migrationServer) signIn(t *testing.T, address, password string) (*imapclient.Client, error) {
	t.Helper()
	client, err := imapclient.DialTLS(s.imapAddr, &imapclient.Options{TLSConfig: s.imapTLS})
	if err != nil {
		t.Fatalf("connect to IMAP: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Login(address, password).Wait(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// mustReadInbox proves a real session: LOGIN, then SELECT INBOX, then LOGOUT.
func (s *migrationServer) mustReadInbox(t *testing.T, address, password string) {
	t.Helper()
	client, err := s.signIn(t, address, password)
	if err != nil {
		t.Fatalf("%s did not sign in to IMAP: %v", address, err)
	}
	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("%s SELECT INBOX: %v", address, err)
	}
	if err := client.Logout().Wait(); err != nil {
		t.Fatalf("%s LOGOUT: %v", address, err)
	}
}

func addressesOf(results []passwdFileResult) []string {
	addresses := []string{}
	for _, result := range results {
		addresses = append(addresses, result.Address)
	}
	return addresses
}

func TestPasswdFileUsersSignInToIMAPWithTheirOldPasswords(t *testing.T) {
	s := newMigrationServer(t)
	alice, bob := "alice@"+migrationDomain, "bob@"+migrationDomain
	weak := map[string]string{"mallory@" + migrationDomain: dovecotMD5Password, "pat@" + migrationDomain: dovecotPlainPassword}

	// A file as Dovecot's passwd-file format has it: a comment, a blank line,
	// the optional fields of a user, and a user without a domain.
	passwdFile := "# users of " + migrationDomain + "\n" +
		alice + ":" + dovecotSHA512Hash + ":1000:1000::/home/alice::\n" +
		"\n" +
		"bob:" + dovecotBLFHash + ":1001:1001:Bob:/home/bob\n" +
		"mallory@" + migrationDomain + ":" + dovecotMD5Hash + ":1002:1002\n" +
		"pat@" + migrationDomain + ":" + dovecotPlainHash + "\n"

	code, outcome := s.importPasswdFile(t, s.session, passwdFile)
	if code != exitFailed {
		t.Fatalf("exit %d: the weak users failed, so the import must say so", code)
	}
	if got := addressesOf(outcome["created"]); !reflect.DeepEqual(got, []string{alice, bob}) {
		t.Fatalf("created %v: %+v", got, outcome)
	}
	if got := addressesOf(outcome["failed"]); len(got) != 2 || got[0] != "mallory@"+migrationDomain || got[1] != "pat@"+migrationDomain {
		t.Fatalf("refused %v: %+v", got, outcome)
	}

	// Both hashes arrive as Dovecot wrote them; nothing has rehashed them yet.
	originalAlice := s.storedHash(t, alice)
	if originalAlice.SecretHash != dovecotSHA512Hash {
		t.Fatalf("the SHA512-CRYPT hash changed on import: %s", originalAlice.SecretHash)
	}
	if got := s.storedHash(t, bob); got.SecretHash != dovecotBLFHash {
		t.Fatalf("the BLF-CRYPT hash changed on import: %s", got.SecretHash)
	}

	// A weak hash is refused outright: no account, by the command or by the API
	// behind it, and the old password opens nothing.
	for address, password := range weak {
		var count int64
		if err := s.db.Model(&models.Mailbox{}).Where("address = ?", address).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s has %d accounts: %v", address, count, err)
		}
		if _, err := s.signIn(t, address, password); err == nil {
			t.Fatalf("%s signed in with a weak scheme's password", address)
		}
	}
	for address, hash := range map[string]string{"mallory@" + migrationDomain: dovecotMD5Hash, "pat@" + migrationDomain: dovecotPlainHash} {
		if status := s.postAccount(t, address, hash); status != http.StatusUnprocessableEntity {
			t.Fatalf("the API answered %d to a weak hash for %s", status, address)
		}
	}

	// A wrong password is refused and leaves the hash alone.
	if _, err := s.signIn(t, alice, "not-"+dovecotSHA512Password); err == nil {
		t.Fatal("a wrong password signed in")
	}
	if got := s.storedHash(t, alice); got.SecretHash != dovecotSHA512Hash {
		t.Fatalf("a failed sign-in changed the hash: %s", got.SecretHash)
	}

	// Each user signs in to IMAP with their old password.
	s.mustReadInbox(t, alice, dovecotSHA512Password)
	s.mustReadInbox(t, bob, dovecotBLFPassword)

	// That first sign-in made the SHA512-CRYPT hash REST Mail's own bcrypt, on
	// the same credential and at the cost HashPassword uses.
	upgraded := s.storedHash(t, alice)
	if upgraded.ID != originalAlice.ID || !strings.HasPrefix(upgraded.SecretHash, "{BLF-CRYPT}") {
		t.Fatalf("the first sign-in did not rehash in place: credential %d, %.16s", upgraded.ID, upgraded.SecretHash)
	}
	if cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(upgraded.SecretHash, "{BLF-CRYPT}"))); err != nil || cost != auth.PasswordHashCost {
		t.Fatalf("rehashed at cost %d: %v", cost, err)
	}
	if err := auth.CheckPassword(dovecotSHA512Password, upgraded.SecretHash); err != nil {
		t.Fatalf("the rehash does not hold the old password: %v", err)
	}
	// A BLF-CRYPT hash is REST Mail's own scheme already, so only its cost is
	// held to the server's: this one came at cost 5.
	got := s.storedHash(t, bob)
	if cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(got.SecretHash, "{BLF-CRYPT}"))); err != nil || cost != auth.PasswordHashCost {
		t.Fatalf("a cheap BLF-CRYPT hash ended at cost %d: %v", cost, err)
	}
	if err := auth.CheckPassword(dovecotBLFPassword, got.SecretHash); err != nil {
		t.Fatalf("the rehash does not hold the old password: %v", err)
	}

	// The old passwords keep working on the next sign-in, the wrong one does not.
	s.mustReadInbox(t, alice, dovecotSHA512Password)
	s.mustReadInbox(t, bob, dovecotBLFPassword)
	if _, err := s.signIn(t, bob, "not-"+dovecotBLFPassword); err == nil {
		t.Fatal("a wrong password signed in after import")
	}

	// Running the file again finds both users and replaces nothing.
	rerun, again := s.importPasswdFile(t, s.session, alice+":"+dovecotSHA512Hash+"\nbob:"+dovecotBLFHash+"\n")
	if rerun != exitOK || len(again["created"]) != 0 || !reflect.DeepEqual(addressesOf(again["existing"]), []string{alice, bob}) {
		t.Fatalf("second run: exit %d, %+v", rerun, again)
	}
	if got := s.storedHash(t, alice); got.SecretHash != upgraded.SecretHash {
		t.Fatal("a second run replaced the password")
	}
}

// An API key cannot make an account, whatever it holds: since #1332 that takes
// a person who proved a second factor, and a key is no person. The commands that
// make accounts therefore need a session; this is the refusal a key gets.
func TestPasswdFileImportRefusesAnAPIKey(t *testing.T) {
	s := newMigrationServer(t)
	address := "alice@" + migrationDomain
	code, outcome := s.importPasswdFile(t, s.apiKey, address+":"+dovecotSHA512Hash+"\n")
	if code != exitFailed || len(outcome["created"]) != 0 || len(outcome["failed"]) != 1 || !strings.Contains(outcome["failed"][0].Reason, "HTTP 403") {
		t.Fatalf("exit %d, %+v", code, outcome)
	}
	var count int64
	if err := s.db.Model(&models.Mailbox{}).Where("address = ?", address).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("%d accounts made: %v", count, err)
	}
	if _, err := s.signIn(t, address, dovecotSHA512Password); err == nil {
		t.Fatal("a refused import can sign in")
	}
}

// postAccount asks the API directly, as the command's own check would otherwise
// keep a weak hash from ever reaching it.
func (s *migrationServer) postAccount(t *testing.T, address, hash string) int {
	t.Helper()
	body, err := json.Marshal(map[string]string{"address": address, "password_hash": hash})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, s.apiURL+"/api/v1/accounts", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+s.session)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode
}

// sourceMessage is one file of the source Maildir and what it must become.
type sourceMessage struct {
	name   string // relative to the Maildir's root
	raw    string
	folder string
	flags  []imap.Flag
}

// migrationMaildir is a Maildir as Dovecot leaves it: its own index and list
// files beside the mail, unfinished deliveries in tmp, filenames with their
// sizes, and lines ending in LF as well as in CRLF.
func migrationMaildir(t *testing.T) (string, []sourceMessage, time.Time) {
	t.Helper()
	root := t.TempDir()
	date := time.Date(2019, 6, 7, 8, 9, 10, 0, time.UTC)
	for _, folder := range []string{"", ".Archive", ".Archive.2020"} {
		for _, sub := range []string{"cur", "new", "tmp"} {
			if err := os.MkdirAll(filepath.Join(root, folder, sub), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, contents := range map[string]string{
		"dovecot-uidlist":                   "3 V1700000000 N9 G0\n",
		"dovecot-uidvalidity":               "65000000\n",
		"dovecot.index":                     "binary index",
		"dovecot.index.log":                 "binary log",
		"subscriptions":                     "Archive\nArchive.2020\n",
		"dovecot-keywords":                  "0 $Forwarded\n",
		".Archive/maildirfolder":            "",
		".Archive/dovecot.index":            "binary index",
		".Archive.2020/maildirfolder":       "",
		"tmp/1700000099.M9P9.mail":          "unfinished delivery",
		".Archive/tmp/1700000098.M8P8.mail": "unfinished delivery",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	message := func(id, subject, eol string) string {
		lines := []string{}
		if id != "" {
			lines = append(lines, "Message-ID: <"+id+"@old.example>")
		}
		lines = append(lines, "From: Old Sender <sender@old.example>", "To: dest@"+migrationDomain, "Subject: "+subject, "Date: Fri, 07 Jun 2019 08:09:10 +0000", "", "Body of "+subject+".", "")
		return strings.Join(lines, eol)
	}
	messages := []sourceMessage{
		{"cur/1700000001.M1P1.mail,S=200,W=210:2,S", message("read", "read", "\n"), "INBOX", []imap.Flag{imap.FlagSeen}},
		{"cur/1700000002.M2P2.mail,S=200,W=210:2,FR", message("answered", "answered", "\r\n"), "INBOX", []imap.Flag{imap.FlagAnswered, imap.FlagFlagged}},
		{"new/1700000003.M3P3.mail,S=200,W=210", message("unread", "unread", "\n"), "INBOX", nil},
		{"cur/1700000004.M4P4.mail,S=200,W=210:2,ST", message("deleted", "deleted", "\n"), "INBOX", []imap.Flag{imap.FlagSeen, imap.FlagDeleted}},
		{"cur/1700000005.M5P5.mail,S=200,W=210:2,Sa", message("keyword", "keyword", "\n"), "INBOX", []imap.Flag{imap.FlagSeen, "$Forwarded"}},
		{"cur/1700000006.M6P6.mail,S=200,W=210:2,S", message("", "no message id", "\n"), "INBOX", []imap.Flag{imap.FlagSeen}},
		// The same Message-ID in another folder is another copy, kept.
		{".Archive/cur/1700000007.M7P7.mail,S=200,W=210:2,S", message("read", "read", "\n"), "Archive", []imap.Flag{imap.FlagSeen}},
		{".Archive.2020/cur/1700000008.M8P8.mail,S=200,W=210:2,D", message("draft", "draft", "\n"), "Archive/2020", []imap.Flag{imap.FlagDraft}},
	}
	for _, source := range messages {
		path := filepath.Join(root, source.name)
		if err := os.WriteFile(path, []byte(source.raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, date, date); err != nil {
			t.Fatal(err)
		}
	}
	return root, messages, date
}

type maildirSummary struct {
	Messages int `json:"messages"`
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

func (s *migrationServer) importMaildir(t *testing.T, account uint, root string) maildirSummary {
	t.Helper()
	var out, errs bytes.Buffer
	code := runImportMail(context.Background(), "maildir", []string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(account), 10), "--path", root, "--json"},
		s.apiKey, s.client, &out, &errs)
	if code != exitOK {
		t.Fatalf("import-maildir: exit %d: %s", code, &errs)
	}
	var summary maildirSummary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, &out)
	}
	return summary
}

// storedMail is everything a run could add or change, read from PostgreSQL.
type storedMail struct {
	Messages []storedMessage
	Used     int64
	Counted  int64
}

type storedMessage struct {
	ID         uint
	Folder     string
	MessageID  string
	Raw        string
	IsRead     bool
	IsAnswered bool
	IsFlagged  bool
	IsDraft    bool
	IsDeleted  bool
	Keywords   string
	ReceivedAt time.Time
}

func (s *migrationServer) storedMail(t *testing.T, account uint) storedMail {
	t.Helper()
	var stored storedMail
	if err := s.db.Raw(`SELECT id, folder, message_id, raw_message AS raw, is_read, is_answered, is_flagged, is_draft, is_deleted,
		COALESCE(keywords::text, '') AS keywords, received_at FROM messages WHERE mailbox_id = ? ORDER BY id`, account).Scan(&stored.Messages).Error; err != nil {
		t.Fatal(err)
	}
	var mailbox models.Mailbox
	if err := s.db.First(&mailbox, account).Error; err != nil {
		t.Fatal(err)
	}
	stored.Used = mailbox.QuotaUsedBytes
	if err := s.db.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", account).Select("COALESCE(SUM(message_count), 0)").Scan(&stored.Counted).Error; err != nil {
		t.Fatal(err)
	}
	return stored
}

// fetchedMessage is one message as an IMAP client sees it.
type fetchedMessage struct {
	flags []imap.Flag
	date  time.Time
	raw   []byte
}

// readFolder fetches a folder over IMAP, by its source-independent Subject.
func readFolder(t *testing.T, client *imapclient.Client, folder string) map[string]fetchedMessage {
	t.Helper()
	selected, err := client.Select(folder, nil).Wait()
	if err != nil {
		t.Fatalf("SELECT %s: %v", folder, err)
	}
	got := map[string]fetchedMessage{}
	if selected.NumMessages == 0 {
		return got
	}
	var all imap.SeqSet
	all.AddRange(1, selected.NumMessages)
	section := &imap.FetchItemBodySection{Peek: true}
	buffers, err := client.Fetch(all, &imap.FetchOptions{Flags: true, InternalDate: true, Envelope: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		t.Fatalf("FETCH %s: %v", folder, err)
	}
	for _, buffer := range buffers {
		got[buffer.Envelope.Subject] = fetchedMessage{flags: buffer.Flags, date: buffer.InternalDate, raw: buffer.FindBodySection(section)}
	}
	return got
}

// checkMaildirOverIMAP compares what an IMAP client finds with the source.
func (s *migrationServer) checkMaildirOverIMAP(t *testing.T, address, password string, messages []sourceMessage, date time.Time) {
	t.Helper()
	client, err := s.signIn(t, address, password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	defer func() { _ = client.Logout().Wait() }()

	listed, err := client.List("", "*", nil).Collect()
	if err != nil {
		t.Fatalf("LIST: %v", err)
	}
	folders := map[string]bool{}
	for _, entry := range listed {
		folders[entry.Mailbox] = true
	}
	want := map[string][]sourceMessage{}
	for _, source := range messages {
		want[source.folder] = append(want[source.folder], source)
	}
	for folder, sources := range want {
		if !folders[folder] {
			t.Fatalf("LIST does not have %s: %v", folder, folders)
		}
		got := readFolder(t, client, folder)
		if len(got) != len(sources) {
			t.Fatalf("%s holds %d messages, want %d: %v", folder, len(got), len(sources), got)
		}
		for _, source := range sources {
			subject := subjectOf(source.raw)
			message, ok := got[subject]
			if !ok {
				t.Fatalf("%s has no message %q", folder, subject)
			}
			if !sameFlags(message.flags, source.flags) {
				t.Errorf("%s %q: flags %v, want %v", folder, subject, message.flags, source.flags)
			}
			if !message.date.Equal(date) {
				t.Errorf("%s %q: internal date %v, want %v", folder, subject, message.date, date)
			}
			if !bytes.Equal(toCRLF(message.raw), toCRLF([]byte(source.raw))) {
				t.Errorf("%s %q: body differs from the source\n got %q\nwant %q", folder, subject, message.raw, source.raw)
			}
		}
	}
}

func subjectOf(raw string) string {
	for line := range strings.SplitSeq(raw, "\n") {
		if subject, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "Subject: "); ok {
			return subject
		}
	}
	return ""
}

// toCRLF compares messages as IMAP carries them, whichever line ending the
// source file used.
func toCRLF(raw []byte) []byte {
	return bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
}

func sameFlags(got, want []imap.Flag) bool {
	normalize := func(flags []imap.Flag) []string {
		out := []string{}
		for _, flag := range flags {
			out = append(out, strings.ToLower(string(flag)))
		}
		slices.Sort(out)
		return out
	}
	return reflect.DeepEqual(normalize(got), normalize(want))
}

func TestMaildirImportsIntoTheRightMailboxesAndASecondRunAddsNothing(t *testing.T) {
	s := newMigrationServer(t)
	address := "dest@" + migrationDomain
	code, outcome := s.importPasswdFile(t, s.session, address+":"+dovecotSHA512Hash+"\n")
	if code != exitOK || len(outcome["created"]) != 1 {
		t.Fatalf("create the destination: exit %d, %+v", code, outcome)
	}
	account := outcome["created"][0].ID
	root, messages, date := migrationMaildir(t)

	first := s.importMaildir(t, account, root)
	if first != (maildirSummary{Messages: len(messages), Imported: len(messages)}) {
		t.Fatalf("first run: %+v, want all %d imported", first, len(messages))
	}
	afterFirst := s.storedMail(t, account)
	if len(afterFirst.Messages) != len(messages) || afterFirst.Used == 0 || afterFirst.Counted != int64(len(messages)) {
		t.Fatalf("first run stored %d messages, %d bytes, counted %d; want %d", len(afterFirst.Messages), afterFirst.Used, afterFirst.Counted, len(messages))
	}
	s.checkMaildirOverIMAP(t, address, dovecotSHA512Password, messages, date)

	// The second run, at cutover: every message is found and nothing is stored.
	second := s.importMaildir(t, account, root)
	if second != (maildirSummary{Messages: len(messages), Skipped: len(messages)}) {
		t.Fatalf("second run: %+v, want all %d skipped", second, len(messages))
	}
	if afterSecond := s.storedMail(t, account); !reflect.DeepEqual(afterSecond, afterFirst) {
		t.Fatalf("the second run changed what is stored:\nbefore %+v\nafter  %+v", afterFirst, afterSecond)
	}
	s.checkMaildirOverIMAP(t, address, dovecotSHA512Password, messages, date)

	// A user read a message on the new server between the runs. The run after
	// that adds nothing and takes the user's change back from nobody.
	client, err := s.signIn(t, address, dovecotSHA512Password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	selected, err := client.Select("INBOX", nil).Wait()
	if err != nil || selected.NumMessages == 0 {
		t.Fatalf("SELECT INBOX: %v", err)
	}
	var all imap.SeqSet
	all.AddRange(1, selected.NumMessages)
	if err := client.Store(all, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); err != nil {
		t.Fatalf("STORE: %v", err)
	}
	if err := client.Logout().Wait(); err != nil {
		t.Fatalf("LOGOUT: %v", err)
	}
	beforeThird := s.storedMail(t, account)
	if third := s.importMaildir(t, account, root); third != (maildirSummary{Messages: len(messages), Skipped: len(messages)}) {
		t.Fatalf("third run: %+v", third)
	}
	if afterThird := s.storedMail(t, account); !reflect.DeepEqual(afterThird, beforeThird) {
		t.Fatalf("a run overwrote what the user changed:\nbefore %+v\nafter  %+v", beforeThird, afterThird)
	}

	// Mail that arrived on the old server since: only that message is added.
	late := filepath.Join(root, "new", "1700000009.M9P9.mail,S=200,W=210")
	if err := os.WriteFile(late, []byte("Message-ID: <late@old.example>\nSubject: late\n\nArrived before cutover.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if fourth := s.importMaildir(t, account, root); fourth != (maildirSummary{Messages: len(messages) + 1, Imported: 1, Skipped: len(messages)}) {
		t.Fatalf("fourth run: %+v, want one imported", fourth)
	}
	if got := len(s.storedMail(t, account).Messages); got != len(messages)+1 {
		t.Fatalf("after the late message: %d stored", got)
	}
}
