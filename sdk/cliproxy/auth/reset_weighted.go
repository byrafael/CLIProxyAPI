package auth

import "time"

// resetWeightVector combines capacity weights with weekly-reset urgency. The
// multiplier rises linearly from 1x at seven days to 4x at reset, keeping later
// accounts in rotation. Unknown, stale and expired resets receive the 1x baseline.
// Integer hundredths avoid abrupt day-sized steps without rounding weights to zero.
func resetWeightVector(auths []*Auth, now time.Time) map[string]int64 {
	weights := make(map[string]int64, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		weight := authWeight(auth)
		if weight <= 0 {
			continue
		}
		factor := int64(100)
		if reset := weeklyQuotaReset(auth, now); !reset.IsZero() {
			const week = 7 * 24 * time.Hour
			remaining := reset.Sub(now)
			if remaining < week {
				factor += int64(300 * (week - remaining) / week)
			}
		}
		weights[auth.ID] = weight * factor
	}
	return weights
}

func scheduledResetWeights(entries []*scheduledAuth, predicate func(*scheduledAuth) bool, now time.Time) map[string]int64 {
	auths := make([]*Auth, 0, len(entries))
	for _, entry := range entries {
		if entry != nil && entry.auth != nil && (predicate == nil || predicate(entry)) {
			auths = append(auths, entry.auth)
		}
	}
	return resetWeightVector(auths, now)
}
