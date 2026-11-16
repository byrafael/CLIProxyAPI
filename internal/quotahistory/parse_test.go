package quotahistory

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)

// byName indexes windows so a test does not depend on their order.
func byName(t *testing.T, windows []Window) map[string]Window {
	t.Helper()
	out := map[string]Window{}
	for _, window := range windows {
		if _, dup := out[window.Name]; dup {
			t.Fatalf("window %q reported twice in %+v", window.Name, windows)
		}
		out[window.Name] = window
	}
	return out
}

func TestClaudeReportsEveryWindow(t *testing.T) {
	body := `{"five_hour":{"utilization":38,"resets_at":"2026-11-14T10:00:00Z"},
	          "seven_day":{"utilization":59.5,"resets_at":"2026-11-17T09:00:00+00:00"}}`
	windows := byName(t, ClaudeWindows([]byte(body)))
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	weekly := windows["weekly"]
	if weekly.Remaining != 40.5 || weekly.PeriodHours != 168 || !weekly.ResetsAt.Equal(time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly = %+v", weekly)
	}
	session := windows["session"]
	if session.Remaining != 62 || session.PeriodHours != 5 || !session.ResetsAt.Equal(time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("session = %+v", session)
	}
}

func TestClaudeSkipsWindowsWithoutAResetInstant(t *testing.T) {
	// A weekly window without a reset instant is not usable, as in the panel.
	body := `{"five_hour":{"utilization":"92","resets_at":1794650400},"seven_day":{"utilization":10,"resets_at":null}}`
	windows := byName(t, ClaudeWindows([]byte(body)))
	session, ok := windows["session"]
	if len(windows) != 1 || !ok || session.Remaining != 8 || session.PeriodHours != 5 || !session.ResetsAt.Equal(time.Unix(1794650400, 0)) {
		t.Fatalf("windows = %+v", windows)
	}
	if got := ClaudeWindows([]byte(`{"seven_day_opus":{"utilization":5,"resets_at":"2026-11-17T09:00:00Z"}}`)); len(got) != 0 {
		t.Fatalf("model-only windows must not be recorded: %+v", got)
	}
}

func TestClaudeReadsTheScopedFableLimit(t *testing.T) {
	body := `{"seven_day":{"utilization":20,"resets_at":"2026-11-17T09:00:00Z"},
	  "limits":[
	    {"kind":"weekly_scoped","percent":10,"resets_at":"2026-11-18T09:00:00Z","is_active":false,"scope":{"model":{"display_name":"Fable"}}},
	    {"kind":"weekly_scoped","percent":64,"resets_at":"2026-11-17T10:00:00Z","is_active":true,"scope":{"model":{"display_name":"Fable 5"}}},
	    {"kind":"weekly_scoped","percent":99,"resets_at":"2026-11-17T10:00:00Z","is_active":true,"scope":{"model":{"display_name":"Opus"}}},
	    {"kind":"session","percent":5,"resets_at":"2026-11-14T10:00:00Z","scope":null}]}`
	windows := byName(t, ClaudeWindows([]byte(body)))
	fable, ok := windows["fable"]
	if !ok || fable.Remaining != 36 || fable.PeriodHours != 168 || !fable.ResetsAt.Equal(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("fable = %+v (all %+v)", fable, windows)
	}
	if _, ok := windows["weekly"]; !ok || len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}

	// Without an active entry the first Fable limit counts.
	first := byName(t, ClaudeWindows([]byte(`{"limits":[{"kind":"weekly_scoped","percent":30,"resets_at":"2026-11-17T10:00:00Z","scope":{"model":{"display_name":"fable"}}}]}`)))
	if first["fable"].Remaining != 70 {
		t.Fatalf("first fable = %+v", first)
	}
	// A scoped limit with no percentage or reset is not usable.
	if got := ClaudeWindows([]byte(`{"limits":[{"kind":"weekly_scoped","resets_at":"2026-11-17T10:00:00Z","scope":{"model":{"display_name":"Fable"}}}]}`)); len(got) != 0 {
		t.Fatalf("unusable fable recorded: %+v", got)
	}
}

func TestClaudeLegacyFableWindowUnlessItCountsDollars(t *testing.T) {
	legacy := `{"iguana_necktie":{"utilization":42,"resets_at":"2026-11-17T09:00:00Z"}}`
	if fable := byName(t, ClaudeWindows([]byte(legacy)))["fable"]; fable.Remaining != 58 || fable.PeriodHours != 168 {
		t.Fatalf("legacy fable = %+v", fable)
	}
	// Dollar-denominated, it is the panel's cloud session credits, not Fable.
	dollars := `{"iguana_necktie":{"utilization":5,"resets_at":"2026-11-17T09:00:00Z","limit_dollars":100,"used_dollars":5}}`
	if got := ClaudeWindows([]byte(dollars)); len(got) != 0 {
		t.Fatalf("credit pool recorded as Fable: %+v", got)
	}
	// A scoped limit wins over the legacy percentage.
	both := `{"iguana_necktie":{"utilization":42,"resets_at":"2026-11-17T09:00:00Z"},
	  "limits":[{"kind":"weekly_scoped","percent":64,"resets_at":"2026-11-17T10:00:00Z","scope":{"model":{"display_name":"Fable"}}}]}`
	if fable := byName(t, ClaudeWindows([]byte(both)))["fable"]; fable.Remaining != 36 {
		t.Fatalf("scoped fable should win: %+v", fable)
	}
}

func TestClaudeRejectsUnusablePayloads(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `[]`, `{"limits":"x"}`, `{"iguana_necktie":null}`} {
		if got := ClaudeWindows([]byte(body)); len(got) != 0 {
			t.Fatalf("ClaudeWindows(%s) = %+v", body, got)
		}
	}
}

func TestCodexClassifiesWindowsByLength(t *testing.T) {
	// Secondary first on purpose: classification goes by length, not position.
	body := `{"rate_limit":{
	  "primary_window":{"used_percent":53,"limit_window_seconds":604800,"reset_at":1794830400},
	  "secondary_window":{"used_percent":17,"limit_window_seconds":18000,"reset_after_seconds":3600}}}`
	windows := byName(t, CodexWindows([]byte(body), now))
	weekly, session := windows["weekly"], windows["session"]
	if len(windows) != 2 || weekly.Remaining != 47 || weekly.PeriodHours != 168 {
		t.Fatalf("weekly = %+v (all %+v)", weekly, windows)
	}
	if session.Remaining != 83 || session.PeriodHours != 5 || !session.ResetsAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("session = %+v", session)
	}
}

func TestCodexMonthlyWindowIsNotWeekly(t *testing.T) {
	body := `{"rate_limit":{
	  "primary_window":{"used_percent":65,"limit_window_seconds":18000,"reset_after_seconds":3240},
	  "secondary_window":{"used_percent":29,"limit_window_seconds":2592000,"reset_at":1794830400}}}`
	windows := byName(t, CodexWindows([]byte(body), now))
	session, ok := windows["session"]
	if len(windows) != 1 || !ok || session.Remaining != 35 || session.PeriodHours != 5 {
		t.Fatalf("windows = %+v", windows)
	}
	if !session.ResetsAt.Equal(now.Add(3240 * time.Second)) {
		t.Fatalf("resets = %v", session.ResetsAt)
	}
}

func TestCodexLegacyOrderAndLimitReached(t *testing.T) {
	// No window lengths: primary is the session window, secondary the weekly one.
	// A reached limit without a percentage counts as fully used.
	body := `{"rateLimit":{"limitReached":true,
	  "primaryWindow":{"usedPercent":20,"resetAt":"2026-11-14T10:00:00Z"},
	  "secondaryWindow":{"resetAt":"2026-11-18T10:00:00Z"}}}`
	windows := byName(t, CodexWindows([]byte(body), now))
	weekly, session := windows["weekly"], windows["session"]
	if len(windows) != 2 || weekly.Remaining != 0 || weekly.PeriodHours != 168 || session.Remaining != 80 {
		t.Fatalf("windows = %+v", windows)
	}
}

func TestCodexRejectsUnusablePayloads(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"rate_limit":{}}`, `{"rate_limit":{"primary_window":{"used_percent":5}}}`} {
		if got := CodexWindows([]byte(body), now); len(got) != 0 {
			t.Fatalf("CodexWindows(%s) = %+v", body, got)
		}
	}
}
