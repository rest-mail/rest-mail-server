package outbound

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"time"

	arc "github.com/rest-mail/go-arc/pkg/arc"
	"gorm.io/gorm"
)

// SealARC adds an ARC set to a message this server forwards (RFC 8617 §5.1), sealed as
// sendingDomain with its DKIM key. results is the Authentication-Results value this server
// recorded when it accepted the message, which the set's ARC-Authentication-Results carries;
// chain is the cv= of the ARC chain the message arrived with (none, pass or fail), or empty
// for the seal to verify the message and find it. It runs on the final bytes: every change,
// the DKIM signature included, comes before the seal (§5.1 step 1).
//
// The message is relayed as it is when the domain has no key, has an Ed25519 one (go-arc
// seals with rsa-sha256 alone), or arrived with a chain whose latest seal says cv=fail,
// which no one continues (§5.1 step 2). A key that cannot be loaded fails closed, as
// SignDKIM does.
func SealARC(db *gorm.DB, masterKey, sendingDomain, raw, results, chain string) (string, error) {
	domain, key, asciiDomain, err := domainKey(db, masterKey, sendingDomain, "arc seal")
	if err != nil {
		return "", err
	}
	if key == nil {
		return raw, nil
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		slog.Info("outbound arc seal: the domain's key is not RSA, the forward goes unsealed", "domain", sendingDomain)
		return raw, nil
	}
	return sealARC(raw, arc.SealOptions{
		Domain:          asciiDomain,
		Selector:        domain.DKIMSelector,
		PrivateKey:      rsaKey,
		AuthResults:     results,
		Time:            time.Now().Unix(),
		ChainValidation: chain,
	})
}

// sealARC seals raw with opt. A chain status that does not fit the message (none on one
// that carries an ARC chain, say, once a hop between the verdict and the seal added one) is
// not trusted: the seal verifies the message itself instead.
func sealARC(raw string, opt arc.SealOptions) (string, error) {
	res, err := arc.Seal(context.Background(), []byte(raw), opt)
	if errors.Is(err, arc.ErrChainValidation) {
		opt.ChainValidation = ""
		res, err = arc.Seal(context.Background(), []byte(raw), opt)
	}
	if errors.Is(err, arc.ErrChainFailed) {
		slog.Info("outbound arc seal: the chain arrived failed, the forward goes without a new set", "domain", opt.Domain)
		return raw, nil
	}
	if err != nil {
		slog.Error("outbound arc seal failed (fail-closed)", "domain", opt.Domain, "error", err)
		return "", fmt.Errorf("arc: seal for %s: %w", opt.Domain, err)
	}
	return string(res.Message), nil
}
