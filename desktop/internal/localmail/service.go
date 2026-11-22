package localmail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/apiproxy"
	"github.com/rest-mail/rest-mail-server/desktop/internal/imapmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localstore"
	"github.com/rest-mail/rest-mail-server/desktop/internal/mailsync"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
)

type Backend interface {
	apiproxy.Manager
	ListAccounts() []imapmail.Account
}

type accountStore struct {
	id       uint
	store    *localstore.Store
	syncer   *mailsync.Synchronizer
	kick     chan struct{}
	events   chan apiproxy.MailEvent
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	offline  bool
	lastSync time.Time
}

type Service struct {
	Backend
	root      string
	temporary bool
	mu        sync.Mutex
	identity  session.SessionStatus
	accounts  map[uint]*accountStore
	closed    bool
}

type IndexStatus struct {
	localstore.Status
	AccountID uint      `json:"account_id"`
	Offline   bool      `json:"offline"`
	LastSync  time.Time `json:"last_sync"`
}

type upstream struct {
	apiproxy.Manager
	identity session.SessionStatus
}

func (source upstream) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if !sameSession(source.identity, source.Manager.Status()) {
		return nil, session.ErrSignedOut
	}
	request, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	response, err := source.DoRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	if !sameSession(source.identity, source.Manager.Status()) {
		response.Body.Close()
		return nil, session.ErrSignedOut
	}
	if session.IsEventStream(response.Header.Get("Content-Type")) {
		return response, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<20)+1))
	response.Body.Close()
	if err != nil || len(data) > 64<<20 {
		return nil, errors.New("mail sync response too large or incomplete")
	}
	response.Body = io.NopCloser(bytes.NewReader(source.Redact(data)))
	return response, nil
}

func New(manager Backend, config, instance string) (*Service, error) {
	root, err := localstore.Root(config, instance)
	if err != nil {
		return nil, err
	}
	return &Service{Backend: manager, root: root, temporary: instance != "", accounts: make(map[uint]*accountStore)}, nil
}

func sameSession(left, right session.SessionStatus) bool {
	return left.SignedIn == right.SignedIn && left.Server == right.Server && left.Email == right.Email && left.AccountID == right.AccountID && slices.Equal(left.SetupRequired, right.SetupRequired)
}

func (service *Service) Activate() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return errors.New("local store closed")
	}
	status := service.Backend.Status()
	if !sameSession(status, service.identity) {
		if err := service.stopLocked(false); err != nil {
			return err
		}
		service.identity = status
	}
	if !status.SignedIn || len(status.SetupRequired) > 0 {
		return nil
	}
	for _, account := range service.Backend.ListAccounts() {
		if err := service.openLocked(account.ID); err != nil {
			return err
		}
	}
	if primary, exists := service.accounts[status.AccountID]; exists {
		catalog, err := primary.store.Catalog(context.Background())
		if err != nil {
			return err
		}
		for _, account := range catalog {
			if err = service.openLocked(account.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *Service) openLocked(id uint) error {
	if id == 0 || uint64(id) >= 1<<53 {
		return errors.New("invalid local account identity")
	}
	if _, exists := service.accounts[id]; exists {
		return nil
	}
	key := localstore.AccountKey(service.identity.Server, service.identity.Email, id)
	store, err := localstore.Open(service.root, key)
	if err != nil {
		return err
	}
	if err = store.Identity(context.Background(), key); err != nil {
		store.Close()
		return err
	}
	source := upstream{Manager: service.Backend, identity: service.identity}
	ctx, cancel := context.WithCancel(session.BindServer(context.Background(), service.identity.Server))
	account := &accountStore{id: id, store: store, syncer: &mailsync.Synchronizer{Client: apiclient.New(source), Transport: source}, kick: make(chan struct{}, 1), events: make(chan apiproxy.MailEvent, 64), cancel: cancel, done: make(chan struct{})}
	service.accounts[id] = account
	go account.run(ctx, service.Backend)
	return nil
}

func (account *accountStore) run(ctx context.Context, manager apiproxy.Manager) {
	defer close(account.done)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		apiproxy.MailEvents(ctx, manager, int64(account.id), func(event apiproxy.MailEvent) {
			select {
			case account.events <- event:
			default:
				account.wake()
			}
		})
	}()
	defer func() { <-streamDone }()
	timer := time.NewTicker(10 * time.Second)
	defer timer.Stop()
	for {
		passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := account.syncer.Sync(passCtx, account.store, account.id, mailsync.DefaultBodyBudget)
		cancel()
		account.mu.Lock()
		account.offline = err != nil
		if err == nil {
			account.lastSync = time.Now().UTC()
		}
		account.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-account.kick:
		case event := <-account.events:
			id := eventMessageID(event.Data)
			if id > 0 {
				applyCtx, stopApply := context.WithTimeout(ctx, 30*time.Second)
				applyError := account.syncer.Apply(applyCtx, account.store, account.id, id)
				stopApply()
				account.mu.Lock()
				account.offline = applyError != nil
				account.mu.Unlock()
			}
		case <-timer.C:
		}
	}
}

func (account *accountStore) wake() {
	select {
	case account.kick <- struct{}{}:
	default:
	}
}

func eventMessageID(data map[string]any) uint {
	value, exists := data["message_id"]
	if !exists {
		value = data["id"]
	}
	if number, ok := value.(float64); ok {
		if math.IsNaN(number) || number <= 0 || number >= 1<<53 || math.Trunc(number) != number {
			return 0
		}
		return uint(number)
	}
	number, err := strconv.ParseUint(fmt.Sprint(value), 10, 53)
	if err != nil {
		return 0
	}
	return uint(number)
}

func (service *Service) Run(ctx context.Context) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		if err := service.Activate(); err != nil {
			log.Print("Local mail store could not be opened")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (service *Service) stopLocked(remove bool) error {
	for _, account := range service.accounts {
		account.cancel()
	}
	var failures []error
	for id, account := range service.accounts {
		<-account.done
		if remove {
			failures = append(failures, account.store.Delete())
		} else {
			failures = append(failures, account.store.Close())
		}
		delete(service.accounts, id)
	}
	return errors.Join(failures...)
}

func (service *Service) Close() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return nil
	}
	service.closed = true
	err := service.stopLocked(false)
	if service.temporary {
		err = errors.Join(err, os.RemoveAll(service.root))
	}
	return err
}

func (service *Service) Forget(id uint) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, active := range service.accounts {
		if err := active.store.RemoveCatalog(context.Background(), id); err != nil {
			return err
		}
	}
	account, found := service.accounts[id]
	if !found {
		return nil
	}
	account.cancel()
	<-account.done
	delete(service.accounts, id)
	return account.store.Delete()
}

func (service *Service) ForgetAll() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.stopLocked(true)
}

func (service *Service) account(id uint) (*accountStore, error) {
	if err := service.Activate(); err != nil {
		return nil, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.identity.SignedIn {
		return nil, session.ErrSignedOut
	}
	if id == 0 {
		id = service.identity.AccountID
	}
	account, found := service.accounts[id]
	if !found {
		return nil, errors.New("account is not active in the local store")
	}
	return account, nil
}

func (service *Service) IndexStatus(ctx context.Context, id uint) (IndexStatus, error) {
	account, err := service.account(id)
	if err != nil {
		return IndexStatus{}, err
	}
	status, err := account.store.Status(ctx)
	account.mu.Lock()
	defer account.mu.Unlock()
	return IndexStatus{Status: status, AccountID: account.id, Offline: account.offline, LastSync: account.lastSync}, err
}

func (service *Service) LocalSearch(ctx context.Context, id uint, query, folder string) (localstore.Results, error) {
	account, err := service.account(id)
	if err != nil {
		return localstore.Results{}, err
	}
	result, err := account.store.Search(ctx, query, folder, 50, 0)
	account.mu.Lock()
	result.Offline = account.offline
	account.mu.Unlock()
	return result, err
}

func (service *Service) Reindex(ctx context.Context, id uint) error {
	account, err := service.account(id)
	if err != nil {
		return err
	}
	err = account.syncer.Write(ctx, func() error { return account.store.Reset(ctx) })
	account.wake()
	return err
}

func (service *Service) Wake(id uint) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if account, ok := service.accounts[id]; ok {
		account.wake()
	}
}

func (service *Service) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return service.DoRequest(ctx, request)
}

func (service *Service) Root() string { return service.root }

func (service *Service) String() string {
	return fmt.Sprintf("local mail (%d)", service.Backend.Status().AccountID)
}
