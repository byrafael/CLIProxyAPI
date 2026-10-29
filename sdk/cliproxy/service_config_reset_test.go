package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestEarliestResetRoutingConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Routing.Strategy = "earliest-reset-first"
	state := normalizedRoutingRuntimeState(cfg)
	if _, ok := newRoutingSelector(state).(*coreauth.EarliestResetFirstSelector); !ok {
		t.Fatalf("selector = %T", newRoutingSelector(state))
	}
	cfg.Routing.SessionAffinity = true
	selector := newRoutingSelector(normalizedRoutingRuntimeState(cfg))
	affinity, ok := selector.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("affinity selector = %T", selector)
	}
	defer affinity.Stop()
}
