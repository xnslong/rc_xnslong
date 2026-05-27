package delivery

import (
	"math"
	"math/rand"
	"time"

	"github.com/xnslong/rc_xnslong/internal/port"
)

// calculateBackoff computes the backoff delay for a given retry attempt
// using exponential backoff with optional jitter.
//
// Formula:
//
//	raw = min(base x multiplier^attempt, max)       // max is applied when > 0
//	actual = raw x (1 - jitter x random())           // jitter is applied when > 0
//
// The optional randFn parameter allows injecting a deterministic random
// function for testing. When omitted, the global math/rand.Float64 is used.
func calculateBackoff(attempt int, policy port.RetryPolicy, randFn ...func() float64) time.Duration {
	raw := float64(policy.BaseDelayMs) * math.Pow(policy.Multiplier, float64(attempt))

	if policy.MaxDelayMs > 0 {
		raw = math.Min(raw, float64(policy.MaxDelayMs))
	}

	if policy.Jitter > 0 {
		fn := rand.Float64
		if len(randFn) > 0 && randFn[0] != nil {
			fn = randFn[0]
		}
		jitter := policy.Jitter * fn()
		raw = raw * (1 - jitter)
	}

	return time.Duration(raw) * time.Millisecond
}
