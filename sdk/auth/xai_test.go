package auth

import (
	"context"
	"encoding/json"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"os"
	"testing"
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
	login := func(subject, token string) string {
		t.Helper()
		name := xaiauth.CredentialFileName("shared@example.com", subject)
		authenticator.record = &coreauth.Auth{
			ID: name, FileName: name, Provider: "xai",
			Storage:  &xaiauth.TokenStorage{Email: "shared@example.com", Subject: subject, AccessToken: token},
			Metadata: map[string]any{"type": "xai", "email": "shared@example.com", "sub": subject, "access_token": token},
		}
		_, path, err := manager.Login(context.Background(), "xai", cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	personal := login("personal-account", "personal-token")
	company := login("company-account", "company-token")
	if personal == company {
		t.Fatal("second account overwrote the first")
	}
	if got := login("personal-account", "personal-token-refreshed"); got != personal {
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
