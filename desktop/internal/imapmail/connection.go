package imapmail

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	keyring "github.com/zalando/go-keyring"
)

func (store *Store) tls(host string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if store.tlsConfig != nil {
		config = store.tlsConfig.Clone()
	}
	if config.InsecureSkipVerify {
		return nil, errors.New("certificate verification cannot be disabled")
	}
	config.ServerName = host
	if config.MinVersion < tls.VersionTLS12 {
		config.MinVersion = tls.VersionTLS12
	}
	return config, nil
}

func deadline(ctx context.Context) time.Time {
	end := time.Now().Add(30 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(end) {
		return requested
	}
	return end
}

func (store *Store) dial(ctx context.Context, account Account, password string, handlers ...*imapclient.UnilateralDataHandler) (*imapclient.Client, net.Conn, error) {
	config, err := store.tls(account.Host)
	if err != nil {
		return nil, nil, err
	}
	address := net.JoinHostPort(account.Host, strconv.Itoa(account.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if account.IMAPSecurity == "starttls" {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	} else {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: config}).DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, nil, errors.New("IMAP connection with verified TLS failed")
	}
	_ = conn.SetDeadline(deadline(ctx))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var client *imapclient.Client
	options := &imapclient.Options{TLSConfig: config}
	if len(handlers) > 0 {
		options.UnilateralDataHandler = handlers[0]
	}
	if account.IMAPSecurity == "starttls" {
		client, err = imapclient.NewStartTLS(conn, options)
	} else {
		client = imapclient.New(conn, options)
	}
	if err != nil {
		_ = conn.Close()
		return nil, nil, errors.New("IMAP requires verified STARTTLS")
	}
	if err = client.WaitGreeting(); err == nil {
		err = client.Login(account.Username, password).Wait()
	}
	if err != nil {
		_ = conn.Close()
		_ = client.Close()
		return nil, nil, errors.New("IMAP sign-in failed")
	}
	return client, conn, nil
}

func (store *Store) withClient(ctx context.Context, id uint, operation func(*imapclient.Client) error) error {
	account, exists := store.Account(id)
	if !exists {
		return errors.New("IMAP account not found")
	}
	store.mu.Lock()
	state := store.connections[id]
	if state == nil {
		state = &connection{}
		store.connections[id] = state
	}
	store.mu.Unlock()
	return store.useConnection(ctx, account, state, operation)
}

func keyringPassword(id uint) (string, error) {
	password, err := keyring.Get(keychainService, passwordKey(id))
	if err != nil || password == "" {
		return "", errors.New("IMAP password unavailable in keychain")
	}
	return password, nil
}

func (store *Store) useConnection(ctx context.Context, account Account, state *connection, operation func(*imapclient.Client) error) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return errors.New("IMAP account removed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state.client != nil {
		_ = state.conn.SetDeadline(deadline(ctx))
		if state.client.Noop().Wait() != nil {
			_ = state.client.Close()
			state.client = nil
		}
	}
	if state.client == nil {
		password, err := keyringPassword(account.ID)
		if err != nil {
			return err
		}
		store.remember(password)
		state.client, state.conn, err = store.dial(ctx, account, password)
		if err != nil {
			return err
		}
	}
	_ = state.conn.SetDeadline(deadline(ctx))
	conn := state.conn
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	err := operation(state.client)
	stop()
	if err != nil {
		_ = state.conn.Close()
		_ = state.client.Close()
		state.client = nil
		return errors.New("IMAP operation failed")
	}
	_ = state.conn.SetDeadline(time.Time{})
	return nil
}
