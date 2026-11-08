package xai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// AccountContext identifies the selected personal, team, or organization login.
// OAuth subject alone identifies the user and can be shared across these contexts.
type AccountContext struct {
	UserID           string `json:"user_id,omitempty"`
	XUserID          string `json:"x_user_id,omitempty"`
	PrincipalType    string `json:"principal_type,omitempty"`
	PrincipalID      string `json:"principal_id,omitempty"`
	TeamID           string `json:"team_id,omitempty"`
	OrganizationID   string `json:"organization_id,omitempty"`
	SubscriptionTier string `json:"subscription_tier,omitempty"`
}

// ResolveAccountContext queries the subscription and selected principal before
// choosing a credential filename. An unavailable profile must not fall back to
// the subject-only filename and overwrite a different account context.
func (a *XAIAuth) ResolveAccountContext(ctx context.Context, storage *TokenStorage) error {
	if storage == nil || strings.TrimSpace(storage.AccessToken) == "" {
		return fmt.Errorf("xai account profile: missing access token")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CLIChatProxyBaseURL+"/user?include=subscription", nil)
	if err != nil {
		return fmt.Errorf("xai account profile: create request: %w", err)
	}
	version := helps.GetXAIClientVersion()
	req.Header.Set("Authorization", "Bearer "+storage.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-xai-token-auth", "xai-grok-cli")
	req.Header.Set("x-grok-client-version", version)
	req.Header.Set("User-Agent", "grok-pager/"+version+" grok-shell/"+version)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("xai account profile: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("xai account profile: HTTP %d; credential not saved", resp.StatusCode)
	}
	var profile struct {
		UserID           string `json:"userId"`
		XUserID          string `json:"xUserId"`
		PrincipalType    string `json:"principalType"`
		PrincipalID      string `json:"principalId"`
		TeamID           string `json:"teamId"`
		OrganizationID   string `json:"organizationId"`
		SubscriptionTier string `json:"subscriptionTier"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&profile); err != nil {
		return fmt.Errorf("xai account profile: invalid response; credential not saved")
	}
	account := AccountContext{
		UserID: strings.TrimSpace(profile.UserID), XUserID: strings.TrimSpace(profile.XUserID),
		PrincipalType: strings.TrimSpace(profile.PrincipalType), PrincipalID: strings.TrimSpace(profile.PrincipalID),
		TeamID: strings.TrimSpace(profile.TeamID), OrganizationID: strings.TrimSpace(profile.OrganizationID),
		SubscriptionTier: strings.TrimSpace(profile.SubscriptionTier),
	}
	if account.SubscriptionTier == "" {
		return fmt.Errorf("xai account profile: missing subscription tier; credential not saved")
	}
	storage.AccountContext = account
	return nil
}

// CredentialFileNameForStorage includes both billing context and tier, so two
// subscriptions of the same OAuth user are persisted independently.
func CredentialFileNameForStorage(storage *TokenStorage) string {
	identity, _ := json.Marshal([]string{strings.TrimSpace(storage.Subject), storage.UserID, storage.XUserID,
		storage.PrincipalType, storage.PrincipalID, storage.TeamID, storage.OrganizationID, storage.SubscriptionTier})
	name := CredentialFileName(storage.Email, string(identity))
	tier := strings.ToLower(sanitizeFileSegment(storage.SubscriptionTier))
	if len(tier) > 48 {
		tier = tier[:48]
	}
	if tier != "" {
		name = "xai-" + tier + "-" + strings.TrimPrefix(name, "xai-")
	}
	return name
}

// AccountMetadata keeps the resolved login context visible in persisted records.
func (ts *TokenStorage) AccountMetadata() map[string]any {
	raw, _ := json.Marshal(ts.AccountContext)
	var metadata map[string]any
	_ = json.Unmarshal(raw, &metadata)
	return metadata
}
