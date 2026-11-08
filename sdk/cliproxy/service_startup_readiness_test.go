package cliproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestServicePublishesAccountModelsBeforeListenerStarts(t *testing.T) {
	dir := t.TempDir()
	id := "codex-before-listen.json"
	if err := os.WriteFile(filepath.Join(dir, id), []byte(`{"type":"codex","email":"test@example.com","access_token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(id)
	defer reg.UnregisterClient(id)
	checked := false
	stop := errors.New("stop after readiness check")
	service, err := NewBuilder().WithConfig(&config.Config{Host: "127.0.0.1", Port: 0, AuthDir: dir}).WithConfigPath(cfgPath).
		WithHooks(Hooks{OnBeforeStart: func(*config.Config) {
			checked = true
			if len(reg.GetModelsForClient(id)) == 0 {
				t.Error("HTTP listener would start before account models are routable")
			}
		}}).
		WithWatcherFactory(func(string, string, func(*config.Config)) (*WatcherWrapper, error) { return nil, stop }).Build()
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Run(context.Background()); !errors.Is(err, stop) {
		t.Fatalf("Run = %v", err)
	}
	if !checked {
		t.Fatal("startup readiness check was not reached")
	}
}
