package auth

import (
	"testing"
	"time"
)

func TestCodexSessionBindingDoesNotExpire(t *testing.T) {
	c := NewSessionCache(time.Hour)
	defer c.Stop()
	codex := "codex::session::gpt-6.1"
	claude := "claude::session::claude-sonnet"
	c.Set(codex, "codex-account")
	c.Set(claude, "claude-account")
	c.mu.Lock()
	if !c.entries[codex].expiresAt.IsZero() {
		t.Fatal("Codex binding has an expiry")
	}
	if c.entries[claude].expiresAt.IsZero() {
		t.Fatal("Claude binding lost its expiry")
	}
	future := time.Now().Add(365 * 24 * time.Hour)
	if !c.entries[codex].validAt(future) || c.entries[claude].validAt(future) {
		t.Fatal("provider retention differs from policy")
	}
	old := c.entries[claude]
	old.expiresAt = time.Now().Add(-time.Hour)
	c.entries[claude] = old
	c.groups[claude] = old
	c.mu.Unlock()
	c.cleanup()
	if _, ok := c.Get(claude); ok {
		t.Fatal("expired Claude binding survived cleanup")
	}
	if id, ok := c.GetAndRefresh(codex); !ok || id != "codex-account" {
		t.Fatal("Codex binding lost on refresh")
	}
	if !c.Touch(codex, "codex-account") {
		t.Fatal("Codex binding cannot be touched")
	}
	c.InvalidateAuth("codex-account")
	if _, ok := c.Get(codex); ok {
		t.Fatal("Codex binding survived account invalidation")
	}
}

func TestCodexRetentionStillHonorsCapacity(t *testing.T) {
	c := NewSessionCacheWithCapacity(time.Hour, 1)
	defer c.Stop()
	c.Set("codex::old::model", "account")
	c.Set("codex::new::model", "account")
	if _, ok := c.Get("codex::old::model"); ok {
		t.Fatal("capacity eviction disabled")
	}
	if _, ok := c.Get("codex::new::model"); !ok {
		t.Fatal("new binding missing")
	}
}

func TestLatestSessionAuthIncludesRetainedCodexBinding(t *testing.T) {
	c := NewSessionCache(time.Hour)
	defer c.Stop()
	c.setSessionAliases("codex-account", "codex", "session", "codex::session::model")
	if got := c.latestSessionAuth("session", map[string]struct{}{"codex": {}}); got != "codex-account" {
		t.Fatalf("latestSessionAuth = %q, want codex-account", got)
	}
	if got := c.latestSessionAuth("session", map[string]struct{}{"claude": {}}); got != "" {
		t.Fatalf("unrelated provider returned %q", got)
	}
}
