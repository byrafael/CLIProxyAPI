package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fakeRecheckManager struct {
	auths []*coreauth.Auth
	reset []string
}

func (m *fakeRecheckManager) List() []*coreauth.Auth { return m.auths }

func (m *fakeRecheckManager) ResetQuota(_ context.Context, id string) (*coreauth.Auth, []string, error) {
	m.reset = append(m.reset, id)
	return nil, nil, nil
}

func cooledCodexAuth(id string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Provider: "codex",
		Metadata: map[string]any{"access_token": "tok", "account_id": "acct"},
		Quota:    coreauth.QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(100 * time.Hour)},
	}
}

func newRechecker(t *testing.T, mgr *fakeRecheckManager, status int, body string) (*codexQuotaRechecker, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("ChatGPT-Account-Id") != "acct" {
			t.Errorf("probe missing credentials: %v", r.Header)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &codexQuotaRechecker{manager: mgr, cfg: func() *config.Config { return &config.Config{} }, url: srv.URL}, &hits
}

const usableBody = `{"rate_limit":{"allowed":true,"limit_reached":false}}`

func TestRecheckResetsCooldownWhenUpstreamUsable(t *testing.T) {
	mgr := &fakeRecheckManager{auths: []*coreauth.Auth{cooledCodexAuth("a")}}
	r, _ := newRechecker(t, mgr, http.StatusOK, usableBody)
	r.recheck(context.Background())
	if len(mgr.reset) != 1 || mgr.reset[0] != "a" {
		t.Fatalf("reset = %v, want [a]", mgr.reset)
	}
}

func TestRecheckKeepsCooldownWhenUpstreamStillLimited(t *testing.T) {
	for name, body := range map[string]string{
		"limit reached": `{"rate_limit":{"allowed":false,"limit_reached":true}}`,
		"not allowed":   `{"rate_limit":{"allowed":false,"limit_reached":false}}`,
		"no verdict":    `{}`,
		"malformed":     `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			mgr := &fakeRecheckManager{auths: []*coreauth.Auth{cooledCodexAuth("a")}}
			r, _ := newRechecker(t, mgr, http.StatusOK, body)
			r.recheck(context.Background())
			if len(mgr.reset) != 0 {
				t.Fatalf("reset = %v, want none", mgr.reset)
			}
		})
	}
}

func TestRecheckKeepsCooldownOnUpstreamError(t *testing.T) {
	mgr := &fakeRecheckManager{auths: []*coreauth.Auth{cooledCodexAuth("a")}}
	r, _ := newRechecker(t, mgr, http.StatusUnauthorized, usableBody)
	r.recheck(context.Background())
	if len(mgr.reset) != 0 {
		t.Fatalf("reset = %v, want none", mgr.reset)
	}
}

func TestRecheckSkipsCredentialsThatAreNotCooledCodex(t *testing.T) {
	healthy := cooledCodexAuth("healthy")
	healthy.Quota = coreauth.QuotaState{}
	expired := cooledCodexAuth("expired")
	expired.Quota.NextRecoverAt = time.Now().Add(-time.Minute)
	other := cooledCodexAuth("claude")
	other.Provider = "claude"
	disabled := cooledCodexAuth("disabled")
	disabled.Disabled = true
	mgr := &fakeRecheckManager{auths: []*coreauth.Auth{healthy, expired, other, disabled, nil}}
	r, hits := newRechecker(t, mgr, http.StatusOK, usableBody)
	r.recheck(context.Background())
	if hits.Load() != 0 || len(mgr.reset) != 0 {
		t.Fatalf("probed %d times, reset %v; want neither", hits.Load(), mgr.reset)
	}
}

func TestRecheckDetectsPerModelQuotaCooldown(t *testing.T) {
	auth := cooledCodexAuth("a")
	auth.Quota = coreauth.QuotaState{}
	auth.ModelStates = map[string]*coreauth.ModelState{
		"gpt-6.1-sol": {Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(time.Hour)}},
	}
	mgr := &fakeRecheckManager{auths: []*coreauth.Auth{auth}}
	r, _ := newRechecker(t, mgr, http.StatusOK, usableBody)
	r.recheck(context.Background())
	if len(mgr.reset) != 1 {
		t.Fatalf("reset = %v, want one", mgr.reset)
	}
}

func TestCodexQuotaRecheckInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"":      0,
		"bogus": 0,
		"-5m":   0,
		"0s":    0,
		"10m":   10 * time.Minute,
		"5s":    time.Minute,
		" 30m ": 30 * time.Minute,
	}
	for raw, want := range cases {
		cfg := &config.Config{}
		cfg.Routing.QuotaRecheckInterval = raw
		if got := codexQuotaRecheckInterval(cfg); got != want {
			t.Errorf("interval(%q) = %v, want %v", raw, got, want)
		}
	}
	if got := codexQuotaRecheckInterval(nil); got != 0 {
		t.Errorf("interval(nil) = %v, want 0", got)
	}
}
