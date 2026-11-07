package mtls_test

import (
	"context"
	"net"
	"sync"
)

type mtlsPipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
	accepted    []net.Conn
}

func (listener *mtlsPipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-listener.connections:
		return conn, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *mtlsPipeListener) Close() error {
	listener.closeOnce.Do(func() {
		close(listener.closed)
		listener.mu.Lock()
		defer listener.mu.Unlock()
		for _, conn := range listener.accepted {
			_ = conn.Close()
		}
	})
	return nil
}

func (listener *mtlsPipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 443}
}

func (listener *mtlsPipeListener) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	server, client := net.Pipe()
	listener.mu.Lock()
	listener.accepted = append(listener.accepted, server)
	listener.mu.Unlock()
	select {
	case listener.connections <- server:
		return client, nil
	case <-ctx.Done():
		_ = server.Close()
		_ = client.Close()
		return nil, ctx.Err()
	case <-listener.closed:
		_ = server.Close()
		_ = client.Close()
		return nil, net.ErrClosed
	}
}
