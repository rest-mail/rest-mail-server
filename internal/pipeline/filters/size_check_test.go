package filters

// Item 3 (issue #201): the size_check filter's compiled-in default was 25 MB,
// which is unreachable — the SMTP ingress rejects anything over
// DefaultSMTPMaxMessageSize (10 MB) before the pipeline ever runs, so the filter
// default could never fire. Align the default with the ingress limit.

import (
	"testing"

	"github.com/restmail/restmail/internal/config"
)

// TestSizeCheck_DefaultAlignsWithIngressLimit proves an unconfigured size_check
// defaults to the SMTP ingress limit rather than the old unreachable 25 MB.
func TestSizeCheck_DefaultAlignsWithIngressLimit(t *testing.T) {
	f, err := NewSizeCheck(nil)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}
	sc, ok := f.(*sizeCheckFilter)
	if !ok {
		t.Fatalf("unexpected filter type %T", f)
	}
	if sc.maxSize != config.DefaultSMTPMaxMessageSize {
		t.Errorf("default maxSize = %d, want %d (ingress DefaultSMTPMaxMessageSize)",
			sc.maxSize, config.DefaultSMTPMaxMessageSize)
	}
}
