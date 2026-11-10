package xai

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialFileNameAccountIdentity(t *testing.T) {
	personal := credentialFileNameForAccountContext("shared@example.com", "personal-account")
	company := credentialFileNameForAccountContext("shared@example.com", "company-account")
	if personal == company {
		t.Fatal("accounts sharing an email must have different credential files")
	}
	if personal != credentialFileNameForAccountContext(" shared@example.com ", " personal-account ") {
		t.Fatal("repeated login must use the same file")
	}
	if got := credentialFileNameForAccountContext("shared@example.com", ""); got != "xai-shared@example.com.json" {
		t.Fatalf("legacy fallback = %q", got)
	}
	for _, subject := range []string{"tenant/account", "tenant-account", "../../account", "account"} {
		name := credentialFileNameForAccountContext("", subject)
		if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
			t.Fatalf("unsafe credential filename %q", name)
		}
	}
	if credentialFileNameForAccountContext("", "tenant/account") == credentialFileNameForAccountContext("", "tenant-account") {
		t.Fatal("distinct subjects must remain distinct after filename encoding")
	}
}
