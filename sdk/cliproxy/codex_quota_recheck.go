package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexUsageURL            = "https://chatgpt.com/backend-api/wham/usage"
	codexProviderKey         = "codex"
	minQuotaRecheckInterval  = time.Minute
	codexUsageProbeTimeout   = 15 * time.Second
	codexUsageMaxResponseLen = 1 << 20
)

// quotaRecheckManager is the slice of the auth manager the recheck needs.
type quotaRecheckManager interface {
	List() []*coreauth.Auth
	ResetQuota(ctx context.Context, authID string) (*coreauth.Auth, []string, error)
}

// codexQuotaRechecker clears a Codex quota cooldown once upstream reports the
// usage window is no longer exhausted. The stored cooldown end comes from the
// error that started it and cannot see an early upstream reset.
type codexQuotaRechecker struct {
	manager quotaRecheckManager
	cfg     func() *config.Config
	url     string
}

// codexQuotaRecheckInterval returns the configured recheck interval, or zero when disabled.
func codexQuotaRecheckInterval(cfg *config.Config) time.Duration {
	if cfg == nil {
		return 0
	}
	raw := strings.TrimSpace(cfg.Routing.QuotaRecheckInterval)
	if raw == "" {
		return 0
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval <= 0 {
		return 0
	}
	return max(interval, minQuotaRecheckInterval)
}

// run checks on every configured interval until ctx ends. The interval is read
// again each cycle so a config reload takes effect without a restart.
func (r *codexQuotaRechecker) run(ctx context.Context) {
	for {
		interval := codexQuotaRecheckInterval(r.cfg())
		wait := interval
		if interval <= 0 {
			wait = minQuotaRecheckInterval
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if interval > 0 {
			r.recheck(ctx)
		}
	}
}

// recheck probes every Codex credential in a quota cooldown and resets those
// that upstream reports as usable.
func (r *codexQuotaRechecker) recheck(ctx context.Context) {
	now := time.Now()
	for _, auth := range r.manager.List() {
		if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, codexProviderKey) {
			continue
		}
		if !inQuotaCooldown(auth, now) {
			continue
		}
		usable, err := r.probe(ctx, auth)
		if err != nil {
			log.Debugf("codex quota recheck: %s: %v", auth.ID, err)
			continue
		}
		if !usable {
			continue
		}
		if _, _, err := r.manager.ResetQuota(coreauth.WithSkipPersist(ctx), auth.ID); err != nil {
			log.Warnf("codex quota recheck: reset %s: %v", auth.ID, err)
			continue
		}
		log.Infof("codex quota recheck: upstream reports %s usable, cleared quota cooldown", auth.ID)
	}
}

// inQuotaCooldown reports whether the credential, or any of its models, is
// cooling down because of a quota error.
func inQuotaCooldown(auth *coreauth.Auth, now time.Time) bool {
	if auth.Quota.Exceeded && auth.Quota.NextRecoverAt.After(now) {
		return true
	}
	for _, state := range auth.ModelStates {
		if state != nil && state.Quota.Exceeded && state.Quota.NextRecoverAt.After(now) {
			return true
		}
	}
	return false
}

// probe asks the upstream usage endpoint whether the credential may make requests.
func (r *codexQuotaRechecker) probe(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	token, _ := auth.Metadata["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return false, fmt.Errorf("no access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if accountID, _ := auth.Metadata["account_id"].(string); strings.TrimSpace(accountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	client := helps.NewProxyAwareHTTPClient(ctx, r.cfg(), auth, codexUsageProbeTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("usage endpoint returned %d", resp.StatusCode)
	}
	var body struct {
		RateLimit *struct {
			Allowed      *bool `json:"allowed"`
			LimitReached *bool `json:"limit_reached"`
		} `json:"rate_limit"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, codexUsageMaxResponseLen)).Decode(&body); err != nil {
		return false, fmt.Errorf("decode usage: %w", err)
	}
	if body.RateLimit == nil || body.RateLimit.Allowed == nil || body.RateLimit.LimitReached == nil {
		return false, fmt.Errorf("usage response has no rate_limit verdict")
	}
	return *body.RateLimit.Allowed && !*body.RateLimit.LimitReached, nil
}

// startCodexQuotaRecheck launches the recheck loop when the service has an
// in-process auth manager.
func (s *Service) startCodexQuotaRecheck(ctx context.Context) {
	if s.coreManager == nil {
		return
	}
	rechecker := &codexQuotaRechecker{
		manager: s.coreManager,
		cfg: func() *config.Config {
			s.configUpdateMu.Lock()
			defer s.configUpdateMu.Unlock()
			return s.cfg
		},
		url: codexUsageURL,
	}
	go rechecker.run(ctx)
}
