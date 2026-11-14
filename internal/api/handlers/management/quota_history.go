package management

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotahistory"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// quotaHistoryInterval matches the panel's default auto-refresh.
	quotaHistoryInterval = 15 * time.Minute
	// quotaHistoryFirstRun gives credentials time to load after startup.
	quotaHistoryFirstRun = time.Minute
	// quotaHistoryBackoff skips a credential after the provider rate-limits it.
	quotaHistoryBackoff = time.Hour
	quotaHistoryTimeout = 20 * time.Second
	quotaHistoryFile    = "quota-history.json"
)

// Usage endpoints the panel reads for the same windows. Variables only so tests can
// point them at a local server.
var (
	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
)

// Headers the panel sends for the same reads (src/utils/quota/constants.ts).
var (
	claudeUsageHeaders = map[string]string{
		"User-Agent":     "claude-cli/2.1.280 (external, cli)",
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
	}
	codexUsageHeaders = map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)",
	}
)

// quotaHistoryRecorder samples each enabled Claude and Codex OAuth credential's
// primary quota window on a schedule and keeps 30 days of it on disk.
type quotaHistoryRecorder struct {
	mu        sync.Mutex
	path      string
	history   quotahistory.History
	backoffTo map[string]time.Time
}

func (h *Handler) quotaHistoryPath() string {
	if h == nil || strings.TrimSpace(h.configFilePath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(h.configFilePath), quotaHistoryFile)
}

// StartQuotaHistoryRecorder runs the recorder until ctx ends. Without a config file
// path there is nowhere to persist history, so it does nothing.
func (h *Handler) StartQuotaHistoryRecorder(ctx context.Context) {
	path := h.quotaHistoryPath()
	if path == "" || h.authManager == nil {
		return
	}
	recorder := &quotaHistoryRecorder{path: path, history: quotahistory.Load(path), backoffTo: map[string]time.Time{}}
	h.mu.Lock()
	h.quotaHistory = recorder
	h.mu.Unlock()

	go func() {
		timer := time.NewTimer(quotaHistoryFirstRun)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				h.sampleQuotaHistory(ctx, recorder, time.Now())
				// Jitter spreads reads so every credential is not hit on the same second.
				timer.Reset(quotaHistoryInterval + time.Duration(rand.Int64N(int64(time.Minute))))
			}
		}
	}()
}

func (h *Handler) sampleQuotaHistory(ctx context.Context, recorder *quotaHistoryRecorder, now time.Time) {
	changed := false
	for _, auth := range h.authManager.List() {
		if ctx.Err() != nil {
			return
		}
		name, provider, ok := quotaHistoryTarget(auth)
		if !ok {
			continue
		}
		recorder.mu.Lock()
		skipUntil := recorder.backoffTo[name]
		recorder.mu.Unlock()
		if now.Before(skipUntil) {
			continue
		}
		window, status, err := h.readPrimaryWindow(ctx, auth, provider, now)
		if status == http.StatusTooManyRequests {
			recorder.mu.Lock()
			recorder.backoffTo[name] = now.Add(quotaHistoryBackoff)
			recorder.mu.Unlock()
		}
		if err != nil {
			log.Debugf("quota history: %s %s: %v", provider, name, err)
			continue
		}
		recorder.mu.Lock()
		if recorder.history.Record(quotahistory.SeriesKey(name, window.Name), provider, window, now) {
			changed = true
		}
		recorder.mu.Unlock()
	}
	if !changed {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if err := quotahistory.Save(recorder.path, recorder.history); err != nil {
		log.Warnf("quota history: save %s: %v", recorder.path, err)
	}
}

// quotaHistoryTarget returns the panel account id (the credential's file name) and
// provider for credentials the recorder samples: enabled Claude and Codex OAuth logins.
func quotaHistoryTarget(auth *coreauth.Auth) (string, string, bool) {
	if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return "", "", false
	}
	if auth.AuthKind() == coreauth.AuthKindAPIKey {
		return "", "", false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider != "claude" && provider != "codex" {
		return "", "", false
	}
	// Same rule as the credentials list, so keys match the panel's file.name.
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	if name == "" {
		return "", "", false
	}
	return name, provider, true
}

func (h *Handler) readPrimaryWindow(ctx context.Context, auth *coreauth.Auth, provider string, now time.Time) (quotahistory.Window, int, error) {
	ctx, cancel := context.WithTimeout(ctx, quotaHistoryTimeout)
	defer cancel()

	token, err := h.resolveTokenForAuth(ctx, auth, "")
	if err != nil || strings.TrimSpace(token) == "" {
		return quotahistory.Window{}, 0, fmt.Errorf("resolve token: %w", err)
	}
	url, headers := claudeUsageURL, claudeUsageHeaders
	if provider == "codex" {
		url, headers = codexUsageURL, codexUsageHeaders
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return quotahistory.Window{}, 0, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if provider == "codex" {
		if accountID, ok := auth.Metadata["account_id"].(string); ok && strings.TrimSpace(accountID) != "" {
			req.Header.Set("Chatgpt-Account-Id", accountID)
		}
	}

	resp, err := (&http.Client{Transport: h.apiCallTransport(auth, "")}).Do(req)
	if err != nil {
		return quotahistory.Window{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return quotahistory.Window{}, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return quotahistory.Window{}, resp.StatusCode, fmt.Errorf("usage returned %d", resp.StatusCode)
	}

	var window quotahistory.Window
	var ok bool
	if provider == "codex" {
		window, ok = quotahistory.CodexPrimary(body, now)
	} else {
		window, ok = quotahistory.ClaudePrimary(body)
	}
	if !ok {
		return quotahistory.Window{}, resp.StatusCode, fmt.Errorf("no usable window in usage response")
	}
	return window, resp.StatusCode, nil
}

// GetQuotaHistory serves the recorded history in the panel's stored format.
// Before the first sample (or without a recorder) it returns an empty history.
func (h *Handler) GetQuotaHistory(c *gin.Context) {
	h.mu.Lock()
	recorder := h.quotaHistory
	h.mu.Unlock()
	if recorder == nil {
		c.JSON(http.StatusOK, quotahistory.History{})
		return
	}
	recorder.mu.Lock()
	raw, err := quotahistory.History(recorder.history).MarshalJSON()
	recorder.mu.Unlock()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "encode quota history"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}
