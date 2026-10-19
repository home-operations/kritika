package worker

import (
	"fmt"
	"testing"
	"time"
)

func TestSlotWaitElapsed(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	metadata := func(since time.Time) string {
		return fmt.Sprintf(`{"slot_wait":"provider/model","slot_wait_since":%q}`, since.Format(time.RFC3339Nano))
	}
	for _, tt := range []struct {
		name, metadata string
		want           time.Duration
		ok             bool
	}{
		{"early wake measures elapsed rather than requested backoff", metadata(now.Add(-2 * time.Second)), 2 * time.Second, true},
		{"late pickup includes queue delay", metadata(now.Add(-90 * time.Second)), 90 * time.Second, true},
		{"clock skew", metadata(now.Add(time.Second)), 0, true},
		{"old job without timestamp", `{"slot_wait":"provider/model"}`, 0, false},
		{"cleared wait", `{"slot_wait":null,"slot_wait_since":"2026-10-19T11:59:00Z"}`, 0, false},
		{"another snooze", `{}`, 0, false},
		{"bad timestamp", `{"slot_wait":"provider/model","slot_wait_since":"bad"}`, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model, elapsed, ok := slotWaitElapsed([]byte(tt.metadata), now)
			if ok != tt.ok || elapsed != tt.want || ok && model != "provider/model" {
				t.Fatalf("slot wait = %q, %s, %v; want %s, %v", model, elapsed, ok, tt.want, tt.ok)
			}
		})
	}
}
