package filters

import (
	"context"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/sendlimits"
	"testing"
)

func TestRateLimitHumanReleaseIsExplicitAndLeavesOtherSendsLimited(t *testing.T) {
	f, err := NewRateLimit()([]byte(`{"max_per_hour":1,"max_per_minute":1}`))
	if err != nil {
		t.Fatal(err)
	}
	email := &pipeline.EmailJSON{Metadata: map[string]string{"auth_account": "7", "human_limit_release": "true"}}
	first, err := f.Execute(context.Background(), email)
	if err != nil || first.Action != pipeline.ActionContinue {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := f.Execute(context.Background(), email)
	if err != nil || second.Action != pipeline.ActionDefer {
		t.Fatalf("untrusted metadata bypassed a cap: %+v %v", second, err)
	}
	released, err := f.Execute(sendlimits.WithHumanRelease(context.Background()), email)
	if err != nil || released.Action != pipeline.ActionContinue {
		t.Fatalf("human release: %+v %v", released, err)
	}
	again, err := f.Execute(context.Background(), email)
	if err != nil || again.Action != pipeline.ActionDefer {
		t.Fatalf("release weakened subsequent submissions: %+v %v", again, err)
	}
}
