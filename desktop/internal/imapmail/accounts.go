package imapmail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"sort"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/instance"
	keyring "github.com/zalando/go-keyring"
)

const IDBase uint = 1 << 52
const maxFolderSlot uint = (1 << 20) - 1

var keychainService = instance.KeychainService()

const metadataKey = "imap-accounts-v1"

type Account struct {
	Type         string `json:"type"`
	ID           uint   `json:"id"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Username     string `json:"username"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	Address      string `json:"address"`
	IMAPSecurity string `json:"imap_security"`
	SMTPSecurity string `json:"smtp_security"`
}

type AccountInput struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	Address      string `json:"address"`
	IMAPSecurity string `json:"imap_security,omitempty"`
	SMTPSecurity string `json:"smtp_security,omitempty"`
}

type folderIdentity struct {
	Account  uint   `json:"account"`
	Name     string `json:"name"`
	Validity uint32 `json:"validity"`
	Slot     uint   `json:"slot"`
}

type metadata struct {
	Accounts    []Account        `json:"accounts"`
	Folders     []folderIdentity `json:"folders"`
	NextAccount uint             `json:"next_account"`
	NextFolder  uint             `json:"next_folder"`
}

type connection struct {
	mu     sync.Mutex
	client *imapclient.Client
	conn   net.Conn
	closed bool
}

type Store struct {
	mu           sync.Mutex
	data         metadata
	connections  map[uint]*connection
	secrets      map[string]struct{}
	attachments  map[uint]attachmentRef
	tlsConfig    *tls.Config
	watchContext context.Context
	stopWatch    context.CancelFunc
}

func New() (*Store, error) {
	store := &Store{connections: map[uint]*connection{}, secrets: map[string]struct{}{}, attachments: map[uint]attachmentRef{}}
	store.watchContext, store.stopWatch = context.WithCancel(context.Background())
	encoded, err := keyring.Get(keychainService, metadataKey)
	if errors.Is(err, keyring.ErrNotFound) {
		return store, nil
	}
	if err != nil {
		return nil, errors.New("could not read IMAP account keychain")
	}
	if json.Unmarshal([]byte(encoded), &store.data) != nil {
		return nil, errors.New("invalid IMAP account metadata")
	}
	for _, account := range store.data.Accounts {
		if password, err := keyring.Get(keychainService, passwordKey(account.ID)); err == nil {
			store.secrets[password] = struct{}{}
		}
	}
	return store, nil
}

func passwordKey(id uint) string { return fmt.Sprintf("imap:%d", id) }

func validate(input AccountInput) (Account, error) {
	account := Account{Type: "imap", Host: input.Host, Port: input.Port, Username: input.Username, SMTPHost: input.SMTPHost, SMTPPort: input.SMTPPort, Address: input.Address, IMAPSecurity: input.IMAPSecurity, SMTPSecurity: input.SMTPSecurity}
	if account.Port == 0 {
		account.Port = 993
	}
	if account.SMTPPort == 0 {
		account.SMTPPort = 587
	}
	if account.IMAPSecurity == "" {
		account.IMAPSecurity = "tls"
		if account.Port == 143 {
			account.IMAPSecurity = "starttls"
		}
	}
	if account.SMTPSecurity == "" {
		account.SMTPSecurity = "starttls"
		if account.SMTPPort == 465 {
			account.SMTPSecurity = "tls"
		}
	}
	for _, value := range []string{account.Host, account.SMTPHost} {
		if value == "" || strings.ContainsAny(value, "\r\n\x00 /\\@") || strings.Contains(value, ":") && net.ParseIP(value) == nil {
			return Account{}, errors.New("invalid mail server hostname")
		}
	}
	if account.Port < 1 || account.Port > 65535 || account.SMTPPort < 1 || account.SMTPPort > 65535 {
		return Account{}, errors.New("invalid mail server port")
	}
	if (account.IMAPSecurity != "tls" && account.IMAPSecurity != "starttls") || (account.SMTPSecurity != "tls" && account.SMTPSecurity != "starttls") {
		return Account{}, errors.New("TLS is required for IMAP and SMTP")
	}
	address, err := mail.ParseAddress(account.Address)
	if err != nil || address.Address != account.Address || strings.ContainsAny(account.Address, "\r\n\x00") || account.Username == "" || input.Password == "" || strings.ContainsAny(account.Username+input.Password, "\r\n\x00") {
		return Account{}, errors.New("valid address, username and password required")
	}
	return account, nil
}

func (store *Store) remember(password string) {
	if password == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.secrets[password] = struct{}{}
}

func (store *Store) SensitiveValues() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	values := make([]string, 0, len(store.secrets))
	for value := range store.secrets {
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool { return len(values[left]) > len(values[right]) })
	return values
}

func (store *Store) Redact(data []byte) []byte {
	for _, secret := range store.SensitiveValues() {
		data = bytes.ReplaceAll(data, []byte(secret), []byte("[redacted]"))
	}
	return data
}

func (store *Store) ListAccounts() []Account {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]Account{}, store.data.Accounts...)
}

func (store *Store) Account(id uint) (Account, bool) {
	for _, account := range store.ListAccounts() {
		if account.ID == id {
			return account, true
		}
	}
	return Account{}, false
}

func (store *Store) AddIMAPAccount(ctx context.Context, input AccountInput) (Account, error) {
	store.remember(input.Password)
	account, err := validate(input)
	if err != nil {
		return Account{}, err
	}
	client, conn, err := store.dial(ctx, account, input.Password)
	if err != nil {
		return Account{}, err
	}
	_ = client.Close()
	_ = conn.Close()
	if err := store.checkSMTP(ctx, account, input.Password); err != nil {
		return Account{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.data.Accounts {
		if strings.EqualFold(existing.Address, account.Address) {
			account.ID = existing.ID
			if account != existing {
				return Account{}, errors.New("remove the existing IMAP account before changing its server settings")
			}
			if keyring.Set(keychainService, passwordKey(existing.ID), input.Password) != nil {
				return Account{}, errors.New("could not save IMAP password in keychain")
			}
			return existing, nil
		}
	}
	if store.data.NextAccount >= IDBase-1 {
		return Account{}, errors.New("IMAP account ID space exhausted")
	}
	account.ID = IDBase + store.data.NextAccount + 1
	if keyring.Set(keychainService, passwordKey(account.ID), input.Password) != nil {
		return Account{}, errors.New("could not save IMAP password in keychain")
	}
	next := store.data
	next.Accounts = append(append([]Account{}, store.data.Accounts...), account)
	next.NextAccount++
	if err := saveMetadata(next); err != nil {
		_ = keyring.Delete(keychainService, passwordKey(account.ID))
		return Account{}, err
	}
	store.data = next
	return account, nil
}

func saveMetadata(data metadata) error {
	encoded, err := json.Marshal(data)
	if err != nil || keyring.Set(keychainService, metadataKey, string(encoded)) != nil {
		return errors.New("could not save IMAP account metadata in keychain")
	}
	return nil
}

func (store *Store) RemoveIMAPAccount(id uint) error {
	store.mu.Lock()
	index := -1
	for offset, account := range store.data.Accounts {
		if account.ID == id {
			index = offset
			break
		}
	}
	if index < 0 {
		store.mu.Unlock()
		return errors.New("IMAP account not found")
	}
	next := store.data
	next.Accounts = append(append([]Account{}, store.data.Accounts[:index]...), store.data.Accounts[index+1:]...)
	if err := saveMetadata(next); err != nil {
		store.mu.Unlock()
		return err
	}
	store.data = next
	connection := store.connections[id]
	delete(store.connections, id)
	err := keyring.Delete(keychainService, passwordKey(id))
	store.mu.Unlock()
	if connection != nil {
		connection.mu.Lock()
		connection.closed = true
		if connection.client != nil {
			_ = connection.client.Close()
		}
		connection.mu.Unlock()
	}
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return errors.New("account removed but password could not be deleted from keychain")
	}
	return nil
}

func (store *Store) Close() {
	store.mu.Lock()
	connections := store.connections
	store.connections = map[uint]*connection{}
	store.stopWatch()
	store.watchContext, store.stopWatch = context.WithCancel(context.Background())
	store.mu.Unlock()
	for _, connection := range connections {
		connection.mu.Lock()
		if connection.client != nil {
			_ = connection.client.Close()
			connection.client = nil
		}
		connection.mu.Unlock()
	}
}

func (store *Store) messageID(account uint, folder string, validity uint32, uid uint32) (uint, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, identity := range store.data.Folders {
		if identity.Account == account && identity.Name == folder && identity.Validity == validity {
			return IDBase + identity.Slot<<32 + uint(uid), nil
		}
	}
	if validity == 0 || uid == 0 || store.data.NextFolder >= maxFolderSlot {
		return 0, errors.New("invalid or exhausted IMAP message identity")
	}
	next := store.data
	next.NextFolder++
	identity := folderIdentity{Account: account, Name: folder, Validity: validity, Slot: next.NextFolder}
	next.Folders = append(append([]folderIdentity{}, store.data.Folders...), identity)
	if err := saveMetadata(next); err != nil {
		return 0, err
	}
	store.data = next
	return IDBase + identity.Slot<<32 + uint(uid), nil
}

func (store *Store) resolve(id uint) (folderIdentity, uint32, bool) {
	if id < IDBase || id >= 2*IDBase {
		return folderIdentity{}, 0, false
	}
	slot := (id - IDBase) >> 32
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, identity := range store.data.Folders {
		if identity.Slot == slot {
			return identity, uint32(id), uint32(id) != 0
		}
	}
	return folderIdentity{}, 0, false
}
