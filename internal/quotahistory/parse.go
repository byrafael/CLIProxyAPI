package quotahistory

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const (
	fiveHourSeconds = 5 * 60 * 60
	weekSeconds     = 7 * 24 * 60 * 60
	minMonthSeconds = 28 * 24 * 60 * 60
	maxMonthSeconds = 31 * 24 * 60 * 60
)

// limits holds the panel's two classified windows for one credential.
type limits struct {
	weekly  *Window
	session *Window
}

// primary is the panel's primaryLimit: the weekly limit when there is one, else the short one.
func (l limits) primary() (Window, bool) {
	if l.weekly != nil {
		return *l.weekly, true
	}
	if l.session != nil {
		return *l.session, true
	}
	return Window{}, false
}

// ClaudePrimary reads an Anthropic /api/oauth/usage body. Claude names its windows,
// so five_hour is the session window and seven_day the weekly one.
func ClaudePrimary(body []byte) (Window, bool) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return Window{}, false
	}
	read := func(key, name string, periodHours float64) *Window {
		var window map[string]any
		if raw, ok := payload[key]; !ok || json.Unmarshal(raw, &window) != nil || window == nil {
			return nil
		}
		if _, ok := window["utilization"]; !ok {
			return nil
		}
		used, okUsed := number(window["utilization"])
		resets, okReset := instant(window["resets_at"])
		if !okUsed || !okReset {
			return nil
		}
		return &Window{Name: name, Remaining: clampPct(100 - used), ResetsAt: resets, PeriodHours: periodHours}
	}
	return limits{
		weekly:  read("seven_day", "weekly", 24*7),
		session: read("five_hour", "session", 5),
	}.primary()
}

// CodexPrimary reads a ChatGPT /backend-api/wham/usage body, following the panel's
// codex window classification: windows are matched by length, monthly windows never
// count as weekly, and legacy payloads fall back to primary/secondary order.
func CodexPrimary(body []byte, now time.Time) (Window, bool) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return Window{}, false
	}
	rateLimit, _ := pick(payload, "rate_limit", "rateLimit").(map[string]any)
	if rateLimit == nil {
		return Window{}, false
	}
	primaryRaw, _ := pick(rateLimit, "primary_window", "primaryWindow").(map[string]any)
	secondaryRaw, _ := pick(rateLimit, "secondary_window", "secondaryWindow").(map[string]any)

	seconds := func(window map[string]any) (float64, bool) {
		if window == nil {
			return 0, false
		}
		return number(pick(window, "limit_window_seconds", "limitWindowSeconds"))
	}
	isMonthly := func(window map[string]any) bool {
		s, ok := seconds(window)
		return ok && s >= minMonthSeconds && s <= maxMonthSeconds
	}

	var fiveHour, weekly map[string]any
	for _, window := range []map[string]any{primaryRaw, secondaryRaw} {
		if window == nil {
			continue
		}
		s, ok := seconds(window)
		switch {
		case ok && s == fiveHourSeconds && fiveHour == nil:
			fiveHour = window
		case ok && (s == weekSeconds || isMonthly(window)) && weekly == nil:
			weekly = window
		}
	}
	if fiveHour == nil && primaryRaw != nil && !sameMap(primaryRaw, weekly) {
		fiveHour = primaryRaw
	}
	if weekly == nil && secondaryRaw != nil && !sameMap(secondaryRaw, fiveHour) {
		weekly = secondaryRaw
	}

	limitReached, _ := pick(rateLimit, "limit_reached", "limitReached").(bool)
	allowed, hasAllowed := rateLimit["allowed"].(bool)
	reached := limitReached || (hasAllowed && !allowed)

	read := func(window map[string]any, name string, defaultHours float64) *Window {
		if window == nil {
			return nil
		}
		resets, okReset := instant(pick(window, "reset_at", "resetAt"))
		if !okReset {
			if after, ok := number(pick(window, "reset_after_seconds", "resetAfterSeconds")); ok && after > 0 {
				resets, okReset = now.Add(time.Duration(after*float64(time.Second))), true
			}
		}
		used, okUsed := number(pick(window, "used_percent", "usedPercent"))
		if !okUsed && reached && okReset {
			used, okUsed = 100, true
		}
		if !okUsed || !okReset {
			return nil
		}
		hours := defaultHours
		if s, ok := seconds(window); ok && s > 0 {
			hours = s / 3600
		}
		return &Window{Name: name, Remaining: clampPct(100 - used), ResetsAt: resets, PeriodHours: hours}
	}

	result := limits{session: read(fiveHour, "session", 5)}
	// A monthly window is the panel's "monthly" id, which never fills the weekly column.
	if !isMonthly(weekly) {
		result.weekly = read(weekly, "weekly", 24*7)
	}
	return result.primary()
}

func pick(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

// sameMap is the panel's object identity check (window === other): the same decoded map.
func sameMap(a, b map[string]any) bool {
	return a != nil && b != nil && reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

func clampPct(v float64) float64 { return math.Min(100, math.Max(0, v)) }

// number accepts JSON numbers and numeric strings, like the panel's normalizeNumberValue.
func number(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// instant reads an ISO timestamp or a Unix epoch in seconds or milliseconds,
// like the panel's resolveResetMs.
func instant(value any) (time.Time, bool) {
	if s, ok := value.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return time.Time{}, false
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t, true
		}
	}
	n, ok := number(value)
	if !ok || n <= 0 {
		return time.Time{}, false
	}
	if n < 1e12 {
		return time.UnixMilli(int64(n * 1000)), true
	}
	return time.UnixMilli(int64(n)), true
}
