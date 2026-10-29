package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func resetTestAuth(id, provider string, now, reset time.Time) *Auth {
	return &Auth{ID: id, Provider: provider, Status: StatusActive, Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
		"X-Codex-Secondary-Window-Minutes":     "10080",
		"X-Codex-Secondary-Reset-At":           strconv.FormatInt(reset.Unix(), 10),
		"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(reset.Unix(), 10),
	}}}
}

func TestWeeklyQuotaReset(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mutate func(*Auth)
		want   time.Time
	}{
		{"codex weekly", func(a *Auth) {}, now.Add(time.Hour)},
		{"claude weekly", func(a *Auth) { a.Provider = "claude" }, now.Add(time.Hour)},
		{"devin weekly", func(a *Auth) {
			a.Provider = "devin"
			a.Quota.Signals["weekly_quota_reset_at"] = now.Add(time.Hour).Format(time.RFC3339)
		}, now.Add(time.Hour)},
		{"relative reset uses observation time", func(a *Auth) {
			a.Quota.ObservedAt = now.Add(-time.Hour)
			delete(a.Quota.Signals, "X-Codex-Secondary-Reset-At")
			a.Quota.Signals["X-Codex-Secondary-Reset-After-Seconds"] = "7200"
		}, now.Add(time.Hour)},
		{"primary can be weekly", func(a *Auth) {
			a.Quota.Signals = map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-At": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}
		}, now.Add(time.Hour)},
		{"short window ignored", func(a *Auth) { a.Quota.Signals["X-Codex-Secondary-Window-Minutes"] = "300" }, time.Time{}},
		{"stale", func(a *Auth) { a.Quota.ObservedAt = now.Add(-25 * time.Hour) }, time.Time{}},
		{"no observation", func(a *Auth) { a.Quota.ObservedAt = time.Time{} }, time.Time{}},
		{"expired", func(a *Auth) { a.Quota.Signals["X-Codex-Secondary-Reset-At"] = strconv.FormatInt(now.Unix(), 10) }, time.Time{}},
		{"malformed", func(a *Auth) { a.Quota.Signals["X-Codex-Secondary-Reset-At"] = "oops" }, time.Time{}},
		{"overflow duration", func(a *Auth) {
			delete(a.Quota.Signals, "X-Codex-Secondary-Reset-At")
			a.Quota.Signals["X-Codex-Secondary-Reset-After-Seconds"] = "9223372036854775807"
		}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := resetTestAuth("a", "codex", now, now.Add(time.Hour))
			tc.mutate(a)
			if got := weeklyQuotaReset(a, now); !got.Equal(tc.want) {
				t.Fatalf("reset = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEarliestResetFirstSelectorAndScheduler(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	opts := cliproxyexecutor.Options{}
	late := resetTestAuth("reset-a", "codex", now, now.Add(48*time.Hour))
	early := resetTestAuth("reset-b", "codex", now, now.Add(time.Hour))
	unknown := &Auth{ID: "reset-c", Provider: "codex", Status: StatusActive}
	registerSchedulerModels(t, "codex", "reset-model", late.ID, early.ID, unknown.ID)
	for _, schedulerPath := range []bool{false, true} {
		t.Run(strconv.FormatBool(schedulerPath), func(t *testing.T) {
			selector := &EarliestResetFirstSelector{}
			scheduler := newSchedulerForTest(selector, late, early, unknown)
			pick := func(auths []*Auth, tried map[string]struct{}) *Auth {
				t.Helper()
				var got *Auth
				var err error
				if schedulerPath {
					scheduler.rebuild(auths)
					got, err = scheduler.pickSingle(ctx, "codex", "reset-model", opts, tried)
				} else {
					got, err = selector.Pick(ctx, "codex", "reset-model", opts, auths)
				}
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			for i := 0; i < 3; i++ {
				if got := pick([]*Auth{late, early, unknown}, nil); got.ID != early.ID {
					t.Fatalf("pick=%s, want early", got.ID)
				}
			}
			cooled := early.Clone()
			cooled.Unavailable = true
			cooled.Quota.Exceeded = true
			cooled.Quota.Reason = "credential_quota"
			cooled.Quota.NextRecoverAt = now.Add(time.Hour)
			if got := pick([]*Auth{late, cooled, unknown}, nil); got.ID != late.ID {
				t.Fatalf("cooldown pick=%s", got.ID)
			}
			priority := late.Clone()
			priority.Attributes = map[string]string{"priority": "10"}
			if got := pick([]*Auth{priority, early, unknown}, nil); got.ID != late.ID {
				t.Fatalf("priority pick=%s", got.ID)
			}
			renewed := early.Clone()
			renewed.Quota.Signals["X-Codex-Secondary-Reset-At"] = strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10)
			if got := pick([]*Auth{late, renewed, unknown}, nil); got.ID != late.ID {
				t.Fatalf("renewal pick=%s", got.ID)
			}
			if schedulerPath {
				if got := pick([]*Auth{late, early, unknown}, map[string]struct{}{early.ID: {}}); got.ID != late.ID {
					t.Fatalf("retry pick=%s", got.ID)
				}
			}
			noLate := late.Clone()
			noEarly := early.Clone()
			noLate.Quota = QuotaState{}
			noEarly.Quota = QuotaState{}
			if !schedulerPath {
				first := pick([]*Auth{noLate, noEarly}, nil)
				second := pick([]*Auth{noLate, noEarly}, nil)
				if first.ID == second.ID {
					t.Fatalf("missing reset did not rotate: %s", first.ID)
				}
			}
		})
	}
}

func TestEarliestResetFirstMixedProviders(t *testing.T) {
	now := time.Now()
	late := resetTestAuth("mixed-late", "codex", now, now.Add(24*time.Hour))
	early := resetTestAuth("mixed-early", "claude", now, now.Add(time.Hour))
	registerSchedulerModels(t, "codex", "mixed-reset-model", late.ID)
	registerSchedulerModels(t, "claude", "mixed-reset-model", early.ID)
	scheduler := newSchedulerForTest(&EarliestResetFirstSelector{}, late, early)
	got, provider, err := scheduler.pickMixed(context.Background(), []string{"codex", "claude"}, "mixed-reset-model", cliproxyexecutor.Options{}, nil)
	if err != nil || got.ID != early.ID || provider != "claude" {
		t.Fatalf("pick=%v provider=%s err=%v", got, provider, err)
	}
}

func TestEarliestResetSchedulerFallbackRotates(t *testing.T) {
	a := &Auth{ID: "fallback-a", Provider: "codex", Status: StatusActive}
	b := &Auth{ID: "fallback-b", Provider: "codex", Status: StatusActive}
	registerSchedulerModels(t, "codex", "fallback-reset-model", a.ID, b.ID)
	scheduler := newSchedulerForTest(&EarliestResetFirstSelector{}, a, b)
	first, err := scheduler.pickSingle(context.Background(), "codex", "fallback-reset-model", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.pickSingle(context.Background(), "codex", "fallback-reset-model", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("fallback did not rotate: %s", first.ID)
	}
}

func TestEarliestResetFirstRespectsSessionAffinityCache(t *testing.T) {
	for _, mode := range []string{"derived-session", "prompt-cache-key"} {
		t.Run(mode, func(t *testing.T) {
			options := func(id string) cliproxyexecutor.Options {
				if mode == "prompt-cache-key" {
					return cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"` + id + `","input":"hello"}`)}
				}
				return cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: id}}
			}
			now := time.Now()
			early := resetTestAuth("sticky-early", "codex", now, now.Add(time.Hour))
			late := resetTestAuth("sticky-late", "codex", now, now.Add(24*time.Hour))
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &EarliestResetFirstSelector{}, TTL: time.Hour})
			defer selector.Stop()
			firstOpts := options("session-a")
			first, err := selector.Pick(context.Background(), "codex", "sticky-model", firstOpts, []*Auth{early, late})
			if err != nil || first.ID != early.ID {
				t.Fatalf("initial pick = %v, err=%v", first, err)
			}
			// The account's reset order changes after the binding. The cached session
			// must stay on its account while a new session reevaluates routing.
			early.Quota.Signals["X-Codex-Secondary-Reset-At"] = strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10)
			late.Quota.Signals["X-Codex-Secondary-Reset-At"] = strconv.FormatInt(now.Add(30*time.Minute).Unix(), 10)
			bound, err := selector.Pick(context.Background(), "codex", "sticky-model", firstOpts, []*Auth{early, late})
			if err != nil || bound.ID != early.ID {
				t.Fatalf("cached pick = %v, err=%v", bound, err)
			}
			newOpts := options("session-b")
			fresh, err := selector.Pick(context.Background(), "codex", "sticky-model", newOpts, []*Auth{early, late})
			if err != nil || fresh.ID != late.ID {
				t.Fatalf("new-session pick = %v, err=%v", fresh, err)
			}

		})
	}
}
