package auth

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestXAIAuthenticatorProviderAndRefreshLead(t *testing.T) {
	authenticator := NewXAIAuthenticator()
	if authenticator.Provider() != "xai" {
		t.Fatalf("Provider() = %q, want xai", authenticator.Provider())
	}
	lead := authenticator.RefreshLead()
	if lead == nil || *lead <= 0 {
		t.Fatalf("RefreshLead() = %v, want positive duration", lead)
	}
}

func TestManagerLoginXAISharedEmail(t *testing.T) {
	dir := t.TempDir()
	store := NewFileTokenStore()
	authenticator := &dummyAuthenticator{provider: "xai"}
	manager := NewManager(store, authenticator)
	cfg := &config.Config{AuthDir: dir}
	login := func(tier, token string) string {
		t.Helper()
		storage := &xaiauth.TokenStorage{Email: "shared@example.com", Subject: "same-subject", AccessToken: token,
			AccountContext: xaiauth.AccountContext{SubscriptionTier: tier}}
		name := xaiauth.CredentialFileNameForStorage(storage)
		authenticator.record = &coreauth.Auth{
			ID: name, FileName: name, Provider: "xai",
			Storage:  storage,
			Metadata: map[string]any{"type": "xai", "email": "shared@example.com", "sub": "same-subject", "subscription_tier": tier, "access_token": token},
		}
		_, path, err := manager.Login(context.Background(), "xai", cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	personal := login("XPremiumPlus", "personal-token")
	company := login("XUltra", "company-token")
	if personal == company {
		t.Fatal("second account overwrote the first")
	}
	if got := login("XPremiumPlus", "personal-token-refreshed"); got != personal {
		t.Fatalf("repeat login created another file: %s", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d credential files, want 2", len(entries))
	}
	for path, want := range map[string]string{personal: "personal-token-refreshed", company: "company-token"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved map[string]any
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if saved["access_token"] != want {
			t.Fatalf("%s: token = %v, want %s", path, saved["access_token"], want)
		}
	}
}
