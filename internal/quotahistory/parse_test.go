package quotahistory

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)

func TestClaudePrefersTheWeeklyWindow(t *testing.T) {
	body := `{"five_hour":{"utilization":38,"resets_at":"2026-11-14T10:00:00Z"},
	          "seven_day":{"utilization":59.5,"resets_at":"2026-11-17T09:00:00+00:00"}}`
	window, ok := ClaudePrimary([]byte(body))
	if !ok || window.Name != "weekly" || window.Remaining != 40.5 || window.PeriodHours != 168 {
		t.Fatalf("window = %+v ok=%v", window, ok)
	}
	if !window.ResetsAt.Equal(time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("resets = %v", window.ResetsAt)
	}
}

func TestClaudeFallsBackToTheSessionWindow(t *testing.T) {
	// A weekly window without a reset instant is not usable, as in the panel.
	body := `{"five_hour":{"utilization":"92","resets_at":1794650400},"seven_day":{"utilization":10,"resets_at":null}}`
	window, ok := ClaudePrimary([]byte(body))
	if !ok || window.Name != "session" || window.Remaining != 8 || window.PeriodHours != 5 {
		t.Fatalf("window = %+v ok=%v", window, ok)
	}
	if !window.ResetsAt.Equal(time.Unix(1794650400, 0)) {
		t.Fatalf("resets = %v", window.ResetsAt)
	}
	if _, ok := ClaudePrimary([]byte(`{"seven_day_opus":{"utilization":5,"resets_at":"2026-11-17T09:00:00Z"}}`)); ok {
		t.Fatal("model-only windows must not become the primary window")
	}
}

func TestCodexClassifiesWindowsByLength(t *testing.T) {
	// Secondary first on purpose: classification goes by length, not position.
	body := `{"rate_limit":{
	  "primary_window":{"used_percent":53,"limit_window_seconds":604800,"reset_at":1794830400},
	  "secondary_window":{"used_percent":17,"limit_window_seconds":18000,"reset_after_seconds":3600}}}`
	window, ok := CodexPrimary([]byte(body), now)
	if !ok || window.Name != "weekly" || window.Remaining != 47 || window.PeriodHours != 168 {
		t.Fatalf("window = %+v ok=%v", window, ok)
	}
}

func TestCodexMonthlyWindowIsNotWeekly(t *testing.T) {
	body := `{"rate_limit":{
	  "primary_window":{"used_percent":65,"limit_window_seconds":18000,"reset_after_seconds":3240},
	  "secondary_window":{"used_percent":29,"limit_window_seconds":2592000,"reset_at":1794830400}}}`
	window, ok := CodexPrimary([]byte(body), now)
	if !ok || window.Name != "session" || window.Remaining != 35 || window.PeriodHours != 5 {
		t.Fatalf("window = %+v ok=%v", window, ok)
	}
	if !window.ResetsAt.Equal(now.Add(3240 * time.Second)) {
		t.Fatalf("resets = %v", window.ResetsAt)
	}
}

func TestCodexLegacyOrderAndLimitReached(t *testing.T) {
	// No window lengths: primary is the session window, secondary the weekly one.
	// A reached limit without a percentage counts as fully used.
	body := `{"rateLimit":{"limitReached":true,
	  "primaryWindow":{"usedPercent":20,"resetAt":"2026-11-14T10:00:00Z"},
	  "secondaryWindow":{"resetAt":"2026-11-18T10:00:00Z"}}}`
	window, ok := CodexPrimary([]byte(body), now)
	if !ok || window.Name != "weekly" || window.Remaining != 0 || window.PeriodHours != 168 {
		t.Fatalf("window = %+v ok=%v", window, ok)
	}
}

func TestCodexRejectsUnusablePayloads(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"rate_limit":{}}`, `{"rate_limit":{"primary_window":{"used_percent":5}}}`} {
		if window, ok := CodexPrimary([]byte(body), now); ok {
			t.Fatalf("CodexPrimary(%s) = %+v", body, window)
		}
	}
}
