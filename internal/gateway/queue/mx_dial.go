package queue

import (
	"context"
	"errors"
	"net"
	"net/http/httptrace"
	"net/netip"
	"syscall"

	"github.com/rest-mail/rest-mail-server/internal/metrics"
)

const (
	mxDialIPv4        = "ipv4"
	mxDialIPv6        = "ipv6"
	mxDialConnected   = "connected"
	mxDialRefused     = "refused"
	mxDialTimeout     = "timeout"
	mxDialUnreachable = "unreachable"
	mxDialOther       = "other"
)

// initMXDialAttemptSeries makes the first outcome visible as a rise from zero.
func initMXDialAttemptSeries() {
	for _, family := range []string{mxDialIPv4, mxDialIPv6} {
		for _, outcome := range []string{mxDialConnected, mxDialRefused, mxDialTimeout, mxDialUnreachable, mxDialOther} {
			metrics.OutboundMXDialAttempts.WithLabelValues(family, outcome)
		}
	}
}

func (w *Worker) dialMX(addr string) (net.Conn, error) {
	// net.Dialer reports each resolved address through ConnectDone, including
	// failed dials before a Happy Eyeballs fallback succeeds. The guarded
	// dialer still handles resolution, address selection, and timeouts.
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{ConnectDone: countMXDialAttempt})
	if w.mxDialContext != nil {
		return w.mxDialContext(ctx, "tcp", addr)
	}
	return w.newGuardedDialer(smtpDialTimeout).DialContext(ctx, "tcp", addr)
}

func countMXDialAttempt(_ string, address string, err error) {
	peer, parseErr := netip.ParseAddrPort(address)
	if parseErr != nil {
		return
	}
	family := mxDialIPv6
	if peer.Addr().Unmap().Is4() {
		family = mxDialIPv4
	}
	metrics.OutboundMXDialAttempts.WithLabelValues(family, mxDialOutcome(err)).Inc()
}

func mxDialOutcome(err error) string {
	switch {
	case err == nil:
		return mxDialConnected
	case errors.Is(err, syscall.ECONNREFUSED):
		return mxDialRefused
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, syscall.ENETDOWN), errors.Is(err, syscall.EHOSTDOWN):
		return mxDialUnreachable
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, syscall.ETIMEDOUT):
		return mxDialTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return mxDialTimeout
	}
	return mxDialOther
}
