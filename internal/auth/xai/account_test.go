package xai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type accountTransport func(*http.Request) (*http.Response, error)

func (f accountTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveAccountContextAndFilename(t *testing.T) {
	profiles := []string{
		`{"userId":"same-user","principalId":"same-user","principalType":"user","subscriptionTier":"XPremiumPlus"}`,
		`{"userId":"same-user","principalId":"team-1","principalType":"team","teamId":"team-1","organizationId":"org-1","subscriptionTier":"XUltra"}`,
		`{"userId":"same-user","principalId":"team-2","principalType":"team","teamId":"team-2","organizationId":"org-1","subscriptionTier":"XUltra"}`,
	}
	names := map[string]bool{}
	for _, profile := range profiles {
		service := &XAIAuth{httpClient: &http.Client{Transport: accountTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != CLIChatProxyBaseURL+"/user?include=subscription" {
				t.Fatalf("profile URL = %s", r.URL)
			}
			if r.Header.Get("Authorization") != "Bearer test-access" || r.Header.Get("x-xai-token-auth") != "xai-grok-cli" {
				t.Fatal("missing profile authorization")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(profile))}, nil
		})}}
		storage := &TokenStorage{Subject: "same-subject", Email: "same@example.com", AccessToken: "test-access"}
		if err := service.ResolveAccountContext(context.Background(), storage); err != nil {
			t.Fatal(err)
		}
		name := CredentialFileNameForStorage(storage)
		if names[name] {
			t.Fatalf("account context overwritten: %s", name)
		}
		names[name] = true
		if name != CredentialFileNameForStorage(storage) {
			t.Fatal("filename must be stable")
		}
		if !strings.Contains(name, strings.ToLower(storage.SubscriptionTier)) {
			t.Fatal("filename must show tier")
		}
		if storage.AccountMetadata()["subscription_tier"] != storage.SubscriptionTier {
			t.Fatal("tier metadata missing")
		}
	}
}

func TestResolveAccountContextFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		err        error
	}{
		{name: "unavailable", err: errors.New("network unavailable")},
		{name: "unauthorized", status: 401, body: `{"error":"unauthorized"}`},
		{name: "malformed", status: 200, body: `{`},
		{name: "missing tier", status: 200, body: `{"userId":"same-user"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &XAIAuth{httpClient: &http.Client{Transport: accountTransport(func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			storage := &TokenStorage{AccessToken: "test"}
			if err := service.ResolveAccountContext(context.Background(), storage); err == nil {
				t.Fatal("unresolved account must fail before saving")
			}
		})
	}
}
