package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotahistory"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestQuotaHistoryTargetsEnabledClaudeAndCodexLogins(t *testing.T) {
	cases := []struct {
		auth     *coreauth.Auth
		name     string
		provider string
		ok       bool
	}{
		{&coreauth.Auth{ID: "a", FileName: "claude-a.json", Provider: "claude"}, "claude-a.json", "claude", true},
		{&coreauth.Auth{ID: "codex-b", Provider: "Codex"}, "codex-b", "codex", true},
		{&coreauth.Auth{ID: "c", FileName: "codex-c.json", Provider: "codex", Disabled: true}, "", "", false},
		{&coreauth.Auth{ID: "d", FileName: "xai-d.json", Provider: "xai"}, "", "", false},
		{&coreauth.Auth{ID: "e", Provider: "claude", Attributes: map[string]string{"api_key": "sk"}}, "", "", false},
		{nil, "", "", false},
	}
	for _, tc := range cases {
		name, provider, ok := quotaHistoryTarget(tc.auth)
		if name != tc.name || provider != tc.provider || ok != tc.ok {
			t.Fatalf("quotaHistoryTarget(%+v) = %q %q %v", tc.auth, name, provider, ok)
		}
	}
}

func TestGetQuotaHistoryServesThePanelFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}

	serve := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/quota/history", nil)
		h.GetQuotaHistory(ctx)
		return rec
	}

	if rec := serve(); rec.Code != http.StatusOK || rec.Body.String() != `{"version":1,"series":{}}` {
		t.Fatalf("without recorder: %d %s", rec.Code, rec.Body.String())
	}

	at := time.UnixMilli(29000000 * time.Minute.Milliseconds())
	history := quotahistory.History{}
	history.Record("codex-a.json#weekly", "codex", quotahistory.Window{Name: "weekly", Remaining: 80, ResetsAt: at, PeriodHours: 168}, at)
	h.quotaHistory = &quotaHistoryRecorder{path: filepath.Join(t.TempDir(), "q.json"), history: history}

	want := `{"version":1,"series":{"codex-a.json#weekly":{"p":"codex","h":168,"d":[[29000000,80,29000000]]}}}`
	if rec := serve(); rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("with recorder: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSampleQuotaHistoryRecordsAndPersists(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	var claudeCalls, codexCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/claude":
			claudeCalls++
			if r.Header.Get("Authorization") != "Bearer claude-token" || r.Header.Get("anthropic-beta") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"five_hour":{"utilization":10,"resets_at":"2026-11-14T10:00:00Z"},"seven_day":{"utilization":25,"resets_at":"2026-11-17T09:00:00Z"}}`))
		case "/codex":
			codexCalls++
			if r.Header.Get("Chatgpt-Account-Id") != "acct-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer upstream.Close()
	claudeUsageURL, codexUsageURL = upstream.URL+"/claude", upstream.URL+"/codex"
	t.Cleanup(func() {
		claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
		codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"
	})

	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{ID: "claude-a", FileName: "claude-a.json", Provider: "claude", Metadata: map[string]any{"access_token": "claude-token"}},
		{ID: "codex-b", FileName: "codex-b.json", Provider: "codex", Metadata: map[string]any{"access_token": "codex-token", "account_id": "acct-1"}},
		{ID: "codex-off", FileName: "codex-off.json", Provider: "codex", Disabled: true, Metadata: map[string]any{"access_token": "x"}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
	}
	dir := t.TempDir()
	h := NewHandler(&config.Config{}, filepath.Join(dir, "config.yaml"), manager)
	recorder := &quotaHistoryRecorder{path: h.quotaHistoryPath(), history: quotahistory.History{}, backoffTo: map[string]time.Time{}}

	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	h.sampleQuotaHistory(context.Background(), recorder, now)

	saved := quotahistory.Load(filepath.Join(dir, "quota-history.json"))
	series := saved["claude-a.json#weekly"]
	if series == nil || len(series.Points) != 1 || series.Points[0].Remaining != 75 || series.Provider != "claude" {
		t.Fatalf("saved claude series = %+v (all: %v)", series, saved)
	}
	if len(saved) != 1 {
		t.Fatalf("saved series = %v, want only claude (codex was rate limited, disabled skipped)", saved)
	}

	// The rate-limited credential is skipped until its backoff ends.
	h.sampleQuotaHistory(context.Background(), recorder, now.Add(15*time.Minute))
	if claudeCalls != 2 || codexCalls != 1 {
		t.Fatalf("calls claude=%d codex=%d, want 2 and 1", claudeCalls, codexCalls)
	}
	h.sampleQuotaHistory(context.Background(), recorder, now.Add(quotaHistoryBackoff+time.Minute))
	if codexCalls != 2 {
		t.Fatalf("codex calls after backoff = %d, want 2", codexCalls)
	}
}

func TestQuotaHistoryRecorderStopWaitsAndRestarts(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := NewHandler(&config.Config{}, filepath.Join(t.TempDir(), "config.yaml"), coreauth.NewManager(nil, nil, nil))
	for i := 0; i < 2; i++ {
		stop := h.StartQuotaHistoryRecorder()
		returned := make(chan struct{})
		go func() { stop(); close(returned) }()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Fatalf("stop %d did not return", i)
		}
	}
	// Without a config path there is nothing to persist, so nothing starts.
	(&Handler{}).StartQuotaHistoryRecorder()()
}
