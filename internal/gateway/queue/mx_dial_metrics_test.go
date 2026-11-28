package queue

import (
	"context"
	"maps"
	"net"
	"net/http/httptrace"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const mxDialMetricName = "restmail_outbound_mx_dial_attempts_total"

type mxDialLabels struct{ family, outcome string }

func mxDialCounts(t *testing.T) map[mxDialLabels]float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[mxDialLabels]float64)
	for _, family := range families {
		if family.GetName() != mxDialMetricName {
			continue
		}
		for _, metric := range family.GetMetric() {
			var labels mxDialLabels
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "family":
					labels.family = label.GetValue()
				case "outcome":
					labels.outcome = label.GetValue()
				default:
					t.Fatalf("unexpected dial label %q", label.GetName())
				}
			}
			counts[labels] = metric.GetCounter().GetValue()
		}
	}
	return counts
}

func assertMXDialAttempts(t *testing.T, before, want map[mxDialLabels]float64) {
	t.Helper()
	delta := make(map[mxDialLabels]float64)
	for labels, count := range mxDialCounts(t) {
		if change := count - before[labels]; change != 0 {
			delta[labels] = change
		}
	}
	if !maps.Equal(delta, want) {
		t.Errorf("%s increments = %v, want %v", mxDialMetricName, delta, want)
	}
}

func TestMXDialMetrics_IPv6FailureThenIPv4Delivery(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		err     error
	}{
		{"unreachable", syscall.ENETUNREACH},
		{"refused", syscall.ECONNREFUSED},
		{"timeout", context.DeadlineExceeded},
		{"other", context.Canceled},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			listener, _, port := listenLoopback(t)
			finish := (scriptedMX{}).serve(t, listener)
			w := NewWorker(nil, "sender.test", 1, time.Second)
			w.allowPrivateDest = true
			w.mxDialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				// Model the first dial's result without opening an IPv6 socket.
				if trace := httptrace.ContextClientTrace(ctx); trace != nil && trace.ConnectDone != nil {
					trace.ConnectDone(network, net.JoinHostPort("2001:db8::25", port), os.NewSyscallError("connect", tc.err))
				}
				// The real IPv4 dial exercises net.Dialer's trace hook.
				return w.newGuardedDialer(smtpDialTimeout).DialContext(ctx, network, listener.Addr().String())
			}
			before := mxDialCounts(t)
			smtpBefore := smtpAttemptCounts()
			if err := w.deliverSMTP(t.Context(), net.JoinHostPort("mx.test", port), "mx.test", tlsTestItem(), nil, nil); err != nil {
				t.Fatal(err)
			}
			if got := finish().accepted; got != 1 {
				t.Fatalf("MX accepted %d messages, want 1", got)
			}
			assertSMTPAttempts(t, smtpBefore, map[[2]string]float64{{smtpIPv4, smtpAttemptDelivered}: 1})
			assertMXDialAttempts(t, before, map[mxDialLabels]float64{{"ipv6", tc.outcome}: 1, {"ipv4", "connected"}: 1})
		})
	}
}

func TestMXDialMetrics_GuardedCapabilityProbe(t *testing.T) {
	listener, _, _ := listenLoopback(t)
	finish := (scriptedMX{extensions: []string{"RESTMAIL"}}).serve(t, listener)
	addr := listener.Addr().String()
	w := NewWorker(nil, "sender.test", 1, time.Second)
	before := mxDialCounts(t)
	if got := len(before); got != 10 {
		t.Fatalf("got %d MX dial series after worker initialization, want 10", got)
	}
	smtpBefore := smtpAttemptCounts()
	if probed, _ := w.probeRESTMAIL(addr, "mx.test"); probed {
		t.Fatal("the outbound guard allowed a loopback probe")
	}
	w.allowPrivateDest = true
	if probed, supported := w.probeRESTMAIL(addr, "mx.test"); !probed || !supported {
		t.Fatal("probe did not see the RESTMAIL extension")
	}
	if got := finish().connections; got != 1 {
		t.Fatalf("MX accepted %d probe connections, want 1", got)
	}
	assertMXDialAttempts(t, before, map[mxDialLabels]float64{{"ipv4", "other"}: 1, {"ipv4", "connected"}: 1})
	assertSMTPAttempts(t, smtpBefore, nil)
}
