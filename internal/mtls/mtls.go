// Package mtls builds the TLS configurations for the internal, gateway→API
// mutual-TLS channel.
//
// A few API endpoints (recipient existence checks and inbound message
// delivery) are called by the protocol gateways with no user token — they are
// machine-to-machine calls and were historically protected only by network
// isolation. This package hardens that trust boundary: an internal CA issues a
// client certificate to the gateways, the API is given a server certificate
// signed by the same CA, and the API's dedicated internal listener requires and
// verifies the client certificate (tls.RequireAndVerifyClientCert). The
// certificate is, in effect, the gateway's password.
//
// The two builders below are the only place TLS material is loaded, so both the
// API (server side) and the gateways (client side) agree on cipher/version
// policy and on which CA anchors the internal trust domain. They are pure with
// respect to the filesystem inputs and hold no global state, so they are unit
// testable with fixtures.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// ServerTLSConfig builds the *tls.Config for the API's internal mTLS listener.
//
// It presents serverCert/serverKey to connecting gateways and REQUIRES every
// client to present a certificate that verifies against caCert. A missing,
// self-signed, wrong-CA, or expired client certificate fails the TLS handshake
// before any HTTP request is dispatched — the strongest available rejection.
//
// caCert must be the internal CA that signs the gateway client certificates; it
// should NOT be a broad/public CA, or any holder of a cert from that CA could
// authenticate as a gateway.
func ServerTLSConfig(caCert, serverCert, serverKey string) (*tls.Config, error) {
	if caCert == "" || serverCert == "" || serverKey == "" {
		return nil, fmt.Errorf("internal mTLS server config requires a CA cert, server cert, and server key (got ca=%q cert=%q key=%q)", caCert, serverCert, serverKey)
	}
	cert, err := tls.LoadX509KeyPair(serverCert, serverKey)
	if err != nil {
		return nil, fmt.Errorf("load internal mTLS server keypair: %w", err)
	}
	pool, err := certPoolFromFile(caCert)
	if err != nil {
		return nil, fmt.Errorf("load internal mTLS client CA: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ClientTLSConfig builds the *tls.Config the gateways use when calling the API's
// internal listener. It presents clientCert/clientKey (the gateway's machine
// identity) and verifies the API's server certificate against caCert, so the
// gateway will not deliver mail to an impostor API.
func ClientTLSConfig(caCert, clientCert, clientKey string) (*tls.Config, error) {
	if caCert == "" || clientCert == "" || clientKey == "" {
		return nil, fmt.Errorf("internal mTLS client config requires a CA cert, client cert, and client key (got ca=%q cert=%q key=%q)", caCert, clientCert, clientKey)
	}
	cert, err := tls.LoadX509KeyPair(clientCert, clientKey)
	if err != nil {
		return nil, fmt.Errorf("load internal mTLS client keypair: %w", err)
	}
	pool, err := certPoolFromFile(caCert)
	if err != nil {
		return nil, fmt.Errorf("load internal mTLS server CA: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// certPoolFromFile reads a PEM file of one or more certificates into a fresh
// pool. Unlike x509.SystemCertPool-based helpers it starts empty: the internal
// trust domain is exactly the certificates in this file and nothing else. It
// errors if the file contains no parseable certificate, so a truncated or
// wrong-format CA file fails loudly instead of trusting nothing silently.
func certPoolFromFile(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no PEM certificates found in %s", path)
	}
	return pool, nil
}
