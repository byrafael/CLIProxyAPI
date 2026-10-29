package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestResetWeightedSpreadsTraffic(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "devin"} {
		for _, scheduled := range []bool{false, true} {
			t.Run(provider+"/scheduler="+strconv.FormatBool(scheduled), func(t *testing.T) {
				now := time.Now()
				early := resetTestAuth("early", provider, now, now.Add(24*time.Hour))
				late := resetTestAuth("late", provider, now, now.Add(6*24*time.Hour))
				unknown := &Auth{ID: "unknown", Provider: provider, Status: StatusActive}
				for _, a := range []*Auth{early, late} {
					reset := now.Add(6 * 24 * time.Hour)
					if a.ID == "early" {
						reset = now.Add(24 * time.Hour)
					}
					a.Quota.Signals["weekly_quota_reset_at"] = reset.Format(time.RFC3339)
				}
				registerSchedulerModels(t, provider, "weighted-reset-model", early.ID, late.ID, unknown.ID)
				selector := &WeightedRoundRobinSelector{ResetWeighted: true}
				scheduler := newSchedulerForTest(selector, early, late, unknown)
				auths := []*Auth{early, late, unknown}
				counts := map[string]int{}
				longestRun, run, previous := 0, 0, ""
				for i := 0; i < 1200; i++ {
					var got *Auth
					var err error
					if scheduled {
						// Lifecycle and request-result updates must keep the rotation.
						if i%10 == 0 {
							for _, candidate := range auths {
								scheduler.upsertAuth(candidate.Clone())
							}
						}
						got, err = scheduler.pickSingle(context.Background(), provider, "weighted-reset-model", cliproxyexecutor.Options{}, nil)
					} else {
						got, err = selector.Pick(context.Background(), provider, "weighted-reset-model", cliproxyexecutor.Options{}, auths)
					}
					if err != nil {
						t.Fatal(err)
					}
					counts[got.ID]++
					if scheduled {
						scheduler.upsertAuthResult(got.Clone(), []string{"weighted-reset-model"}, false)
					}
					if got.ID == previous {
						run++
					} else {
						previous, run = got.ID, 1
					}
					if run > longestRun {
						longestRun = run
					}
				}
				if counts[early.ID] <= counts[late.ID] || counts[late.ID] <= counts[unknown.ID] || counts[unknown.ID] < 150 {
					t.Fatalf("traffic did not balance with a reset bias: %v", counts)
				}
				if longestRun > 4 {
					t.Fatalf("routing sent %d consecutive requests to one account", longestRun)
				}
				t.Logf("1200 selections: %v; longest consecutive run %d", counts, longestRun)
			})
		}
	}
}

func TestResetWeightedFallbackAndEligibility(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		for _, data := range []string{"equal", "missing", "stale", "expired"} {
			t.Run(data+"/scheduler="+strconv.FormatBool(scheduled), func(t *testing.T) {
				now := time.Now()
				a := resetTestAuth("a", "codex", now, now.Add(time.Hour))
				b := resetTestAuth("b", "codex", now, now.Add(time.Hour))
				switch data {
				case "missing":
					a.Quota = QuotaState{}
					b.Quota = QuotaState{}
				case "stale":
					a.Quota.ObservedAt = now.Add(-25 * time.Hour)
					b.Quota.ObservedAt = a.Quota.ObservedAt
				case "expired":
					a = resetTestAuth("a", "codex", now, now.Add(-time.Hour))
					b = resetTestAuth("b", "codex", now, now.Add(-time.Hour))
				}
				zero := resetTestAuth("zero", "codex", now, now.Add(time.Minute))
				zero.Attributes = map[string]string{AttributeWeight: "0", "priority": "100"}
				cooled := resetTestAuth("cooled", "codex", now, now.Add(time.Minute))
				cooled.Unavailable = true
				cooled.Quota.Exceeded = true
				cooled.Quota.Reason = "credential_quota"
				cooled.Quota.NextRecoverAt = now.Add(time.Hour)
				registerSchedulerModels(t, "codex", "fallback-weighted-model", a.ID, b.ID, zero.ID, cooled.ID)
				selector := &WeightedRoundRobinSelector{ResetWeighted: true}
				scheduler := newSchedulerForTest(selector, a, b, zero, cooled)
				counts := map[string]int{}
				for i := 0; i < 100; i++ {
					var got *Auth
					var err error
					if scheduled {
						got, err = scheduler.pickSingle(context.Background(), "codex", "fallback-weighted-model", cliproxyexecutor.Options{}, nil)
					} else {
						got, err = selector.Pick(context.Background(), "codex", "fallback-weighted-model", cliproxyexecutor.Options{}, []*Auth{a, b, zero, cooled})
					}
					if err != nil {
						t.Fatal(err)
					}
					counts[got.ID]++
				}
				if counts["a"] != 50 || counts["b"] != 50 {
					t.Fatalf("baseline/eligibility: %v", counts)
				}
				if scheduled {
					got, err := scheduler.pickSingle(context.Background(), "codex", "fallback-weighted-model", cliproxyexecutor.Options{}, map[string]struct{}{"a": {}})
					if err != nil || got.ID != "b" {
						t.Fatalf("retry exclusion: %v, %v", got, err)
					}
				}
			})
		}
	}
}

func TestResetWeightedRenewalAndPriority(t *testing.T) {
	now := time.Now()
	a := resetTestAuth("a", "codex", now, now.Add(time.Hour))
	b := resetTestAuth("b", "codex", now, now.Add(6*24*time.Hour))
	registerSchedulerModels(t, "codex", "renewal-weighted-model", a.ID, b.ID)
	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{ResetWeighted: true}, a, b)
	count := func() map[string]int {
		counts := map[string]int{}
		for i := 0; i < 500; i++ {
			got, err := scheduler.pickSingle(context.Background(), "codex", "renewal-weighted-model", cliproxyexecutor.Options{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			counts[got.ID]++
		}
		return counts
	}
	before := count()
	if before["a"] <= before["b"] || before["b"] == 0 {
		t.Fatalf("initial bias: %v", before)
	}
	a = resetTestAuth("a", "codex", now, now.Add(7*24*time.Hour))
	b = resetTestAuth("b", "codex", now, now.Add(time.Hour))
	scheduler.rebuild([]*Auth{a, b})
	after := count()
	if after["b"] <= after["a"] || after["a"] == 0 {
		t.Fatalf("renewal bias: %v", after)
	}
	a.Attributes = map[string]string{"priority": "10"}
	scheduler.rebuild([]*Auth{a, b})
	if counts := count(); counts["a"] != 500 {
		t.Fatalf("explicit priority lost: %v", counts)
	}
}

func TestResetWeightedMixedProviders(t *testing.T) {
	now := time.Now()
	a := resetTestAuth("mixed-a", "codex", now, now.Add(24*time.Hour))
	b := resetTestAuth("mixed-b", "claude", now, now.Add(6*24*time.Hour))
	registerSchedulerModels(t, "codex", "mixed-weighted-model", a.ID)
	registerSchedulerModels(t, "claude", "mixed-weighted-model", b.ID)
	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{ResetWeighted: true}, a, b)
	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		got, provider, err := scheduler.pickMixed(context.Background(), []string{"codex", "claude"}, "mixed-weighted-model", cliproxyexecutor.Options{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if provider != got.Provider {
			t.Fatalf("provider=%s, picked=%s", provider, got.Provider)
		}
		counts[got.ID]++
	}
	if counts[a.ID] < 650 || counts[a.ID] > 750 || counts[b.ID] < 250 {
		t.Fatalf("mixed traffic: %v", counts)
	}
}

func TestResetWeightedKeepsSessionBindings(t *testing.T) {
	now := time.Now()
	a := resetTestAuth("sticky-a", "codex", now, now.Add(time.Hour))
	b := resetTestAuth("sticky-b", "codex", now, now.Add(6*24*time.Hour))
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &WeightedRoundRobinSelector{ResetWeighted: true}, TTL: time.Hour})
	defer selector.Stop()
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "existing-session"}}
	first, err := selector.Pick(context.Background(), "codex", "sticky-model", opts, []*Auth{a, b})
	if err != nil {
		t.Fatal(err)
	}
	a = resetTestAuth("sticky-a", "codex", now, now.Add(7*24*time.Hour))
	b = resetTestAuth("sticky-b", "codex", now, now.Add(time.Hour))
	for i := 0; i < 10; i++ {
		got, err := selector.Pick(context.Background(), "codex", "sticky-model", opts, []*Auth{a, b})
		if err != nil || got.ID != first.ID {
			t.Fatalf("session moved: %v, %v", got, err)
		}
	}
	counts := map[string]int{}
	for i := 0; i < 500; i++ {
		fresh := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "new-" + strconv.Itoa(i)}}
		got, err := selector.Pick(context.Background(), "codex", "sticky-model", fresh, []*Auth{a, b})
		if err != nil {
			t.Fatal(err)
		}
		counts[got.ID]++
	}
	if counts[b.ID] <= counts[a.ID] || counts[a.ID] == 0 {
		t.Fatalf("new sessions did not balance: %v", counts)
	}
}

func TestResetWeightedHonorsCapacityWeights(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(strconv.FormatBool(scheduled), func(t *testing.T) {
			now := time.Now()
			a := resetTestAuth("capacity-a", "codex", now, now.Add(24*time.Hour))
			b := resetTestAuth("capacity-b", "codex", now, now.Add(24*time.Hour))
			a.Attributes = map[string]string{AttributeWeight: "2"}
			registerSchedulerModels(t, "codex", "capacity-model", a.ID, b.ID)
			selector := &WeightedRoundRobinSelector{ResetWeighted: true}
			scheduler := newSchedulerForTest(selector, a, b)
			counts := map[string]int{}
			for i := 0; i < 300; i++ {
				var got *Auth
				var err error
				if scheduled {
					got, err = scheduler.pickSingle(context.Background(), "codex", "capacity-model", cliproxyexecutor.Options{}, nil)
				} else {
					got, err = selector.Pick(context.Background(), "codex", "capacity-model", cliproxyexecutor.Options{}, []*Auth{a, b})
				}
				if err != nil {
					t.Fatal(err)
				}
				counts[got.ID]++
			}
			if counts[a.ID] != 200 || counts[b.ID] != 100 {
				t.Fatalf("capacity weighting lost: %v", counts)
			}
		})
	}
}
