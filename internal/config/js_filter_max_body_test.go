package config

import (
	"os"
	"testing"
)

// JSFilterMaxBodyBytes reads JS_FILTER_MAX_BODY_BYTES as the sidecar does
// (projects/js-filter-sidecar/server.js toPositiveInt): a positive number with
// its fraction dropped, otherwise the 32 MiB default.
func TestJSFilterMaxBodyBytes(t *testing.T) {
	for _, tc := range []struct {
		env  string
		set  bool
		want int64
	}{
		{set: false, want: 32 << 20},
		{env: "", set: true, want: 32 << 20},
		{env: "2048", set: true, want: 2048},
		{env: " 65536 ", set: true, want: 65536},
		{env: "1.9", set: true, want: 1},
		{env: "0", set: true, want: 32 << 20},
		{env: "-5", set: true, want: 32 << 20},
		{env: "0.5", set: true, want: 32 << 20},
		{env: "lots", set: true, want: 32 << 20},
		{env: "Infinity", set: true, want: 32 << 20},
		{env: "NaN", set: true, want: 32 << 20},
	} {
		if tc.set {
			t.Setenv("JS_FILTER_MAX_BODY_BYTES", tc.env)
		} else {
			t.Setenv("JS_FILTER_MAX_BODY_BYTES", "")
			_ = os.Unsetenv("JS_FILTER_MAX_BODY_BYTES")
		}
		if got := (&Config{}).JSFilterMaxBodyBytes(); got != tc.want {
			t.Errorf("JS_FILTER_MAX_BODY_BYTES=%q (set %v): got %d, want %d", tc.env, tc.set, got, tc.want)
		}
	}
	if DefaultJSFilterMaxBodyBytes != 32*1024*1024 {
		t.Errorf("default %d, want 32 MiB", DefaultJSFilterMaxBodyBytes)
	}
}
