package sendlimits

import (
	"github.com/restmail/restmail/internal/db/models"
	"testing"
	"time"
)

func TestInheritedLimitsNeverAddAllowance(t *testing.T) {
	parent := models.SendLimitValues{MessagesPerHour: 2, MessagesPerDay: 5, RecipientsPerHour: 3, RecipientsPerDay: 10}
	child := models.SendLimitValues{MessagesPerHour: 1, MessagesPerDay: 9, RecipientsPerHour: 0, RecipientsPerDay: 4}
	got := Inherit(parent, &child)
	if got != (models.SendLimitValues{MessagesPerHour: 1, MessagesPerDay: 5, RecipientsPerHour: 3, RecipientsPerDay: 4}) {
		t.Fatalf("effective child = %+v", got)
	}
	if Inherit(parent, nil) != parent {
		t.Fatal("unset child did not inherit")
	}
	if Within(parent, child) {
		t.Fatal("child may not request unlimited recipients or exceed owner's messages")
	}
	if !Within(parent, got) {
		t.Fatal("lower limits should fit")
	}
	if !Within(models.SendLimitValues{}, child) {
		t.Fatal("unlimited parent may be capped")
	}
}

func TestBudgetChecksBothDimensionsAndWindows(t *testing.T) {
	cap := models.SendLimitValues{MessagesPerHour: 2, MessagesPerDay: 3, RecipientsPerHour: 4, RecipientsPerDay: 5}
	for _, tc := range []struct {
		name      string
		hour, day models.SendLimitCounter
		rcpts     int64
		want      string
	}{
		{"admitted", models.SendLimitCounter{Messages: 1, Recipients: 2}, models.SendLimitCounter{Messages: 2, Recipients: 3}, 2, ""},
		{"hour messages", models.SendLimitCounter{Messages: 2}, models.SendLimitCounter{}, 1, "messages_per_hour"},
		{"day messages", models.SendLimitCounter{}, models.SendLimitCounter{Messages: 3}, 1, "messages_per_day"},
		{"hour recipients", models.SendLimitCounter{Recipients: 3}, models.SendLimitCounter{}, 2, "recipients_per_hour"},
		{"day recipients", models.SendLimitCounter{}, models.SendLimitCounter{Recipients: 4}, 2, "recipients_per_day"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Exceeded(cap, tc.hour, tc.day, tc.rcpts); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	now := time.Date(2026, 10, 28, 10, 59, 59, 0, time.UTC)
	if Bucket(now, time.Hour) != time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC) || Bucket(now, 24*time.Hour) != time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC) {
		t.Fatal("buckets must use UTC hour/day boundaries")
	}
}
