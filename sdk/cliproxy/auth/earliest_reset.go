package auth

import (
	"context"
	"strconv"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// EarliestResetFirstSelector spends quota on the earliest weekly reset first.
// Explicit priorities, availability and transport preferences still take precedence.
// Missing, expired or observations older than 24 hours fall back to round-robin.
// Session affinity, when enabled, keeps existing bindings until failover.
type EarliestResetFirstSelector struct{ fallback RoundRobinSelector }

func (s *EarliestResetFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	var best *Auth
	var bestReset time.Time
	for _, candidate := range available {
		reset := weeklyQuotaReset(candidate, now)
		if !reset.IsZero() && (best == nil || reset.Before(bestReset) || (reset.Equal(bestReset) && candidate.ID < best.ID)) {
			best, bestReset = candidate, reset
		}
	}
	if best != nil {
		return best, nil
	}
	return s.fallback.Pick(ctx, provider, model, opts, available)
}

// weeklyQuotaReset reads only shared weekly limits. Short request limits and
// model-specific additional limits must not be mistaken for account renewals.
func weeklyQuotaReset(auth *Auth, now time.Time) time.Time {
	if auth == nil {
		return time.Time{}
	}
	q := auth.Quota
	if q.ObservedAt.IsZero() || q.ObservedAt.After(now) || now.Sub(q.ObservedAt) > 24*time.Hour {
		return time.Time{}
	}
	var earliest time.Time
	keep := func(reset time.Time) {
		if reset.After(now) && (earliest.IsZero() || reset.Before(earliest)) {
			earliest = reset
		}
	}
	switch auth.Provider {
	case "codex":
		for _, window := range []string{"Primary", "Secondary"} {
			prefix := "X-Codex-" + window + "-"
			minutes, err := strconv.ParseInt(q.Signals[prefix+"Window-Minutes"], 10, 64)
			if err != nil || minutes != 10080 {
				continue
			}
			if epoch, err := strconv.ParseInt(q.Signals[prefix+"Reset-At"], 10, 64); err == nil && epoch > 0 {
				keep(time.Unix(epoch, 0))
			} else if seconds, err := strconv.ParseInt(q.Signals[prefix+"Reset-After-Seconds"], 10, 64); err == nil && seconds >= 0 && seconds <= 7*24*60*60 {
				keep(q.ObservedAt.Add(time.Duration(seconds) * time.Second))
			}
		}
	case "claude":
		if epoch, err := strconv.ParseInt(q.Signals["Anthropic-Ratelimit-Unified-7d-Reset"], 10, 64); err == nil && epoch > 0 {
			keep(time.Unix(epoch, 0))
		}
	case "devin":
		if reset, err := time.Parse(time.RFC3339, q.Signals["weekly_quota_reset_at"]); err == nil {
			keep(reset)
		}
	}
	return earliest
}

func (v *readyView) pickEarliestReset(predicate func(*scheduledAuth) bool, now time.Time) *scheduledAuth {
	var best *scheduledAuth
	var bestReset time.Time
	for _, entry := range v.flat {
		if entry == nil || entry.auth == nil || (predicate != nil && !predicate(entry)) {
			continue
		}
		reset := weeklyQuotaReset(entry.auth, now)
		if !reset.IsZero() && (best == nil || reset.Before(bestReset) || (reset.Equal(bestReset) && entry.auth.ID < best.auth.ID)) {
			best, bestReset = entry, reset
		}
	}
	if best != nil {
		return best
	}
	return v.pickRoundRobin(predicate)
}
