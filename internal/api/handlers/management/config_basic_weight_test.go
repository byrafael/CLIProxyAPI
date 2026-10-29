package management

import "testing"

func TestNormalizeRoutingStrategyWeightedRoundRobin(t *testing.T) {
	for _, input := range []string{"weighted-round-robin", "weightedroundrobin", "wrr"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "weighted-round-robin" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want weighted-round-robin, true", input, got, ok)
		}
	}
}

func TestNormalizeResetRoutingStrategies(t *testing.T) {
	for _, strategy := range []string{"earliest-reset-first", "reset-weighted-round-robin"} {
		got, ok := normalizeRoutingStrategy("  " + strategy + "  ")
		if !ok || got != strategy {
			t.Fatalf("strategy = %q, %v", got, ok)
		}
	}
}
