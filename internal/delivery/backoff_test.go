package delivery

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// TestCalculateBackoff_FirstRetry verifies 7.1.1: first retry attempt.
// base=1000, multiplier=2, attempt=1, jitter=0 => 1000 x 2^1 = 2000
func TestCalculateBackoff_FirstRetry(t *testing.T) {
	policy := port.RetryPolicy{
		BaseDelayMs: 1000,
		MaxDelayMs:  1000000,
		Multiplier:  2,
		Jitter:      0,
	}
	result := calculateBackoff(1, policy)
	assert.Equal(t, 2000*time.Millisecond, result)
}

// TestCalculateBackoff_SecondRetry verifies 7.1.2: second retry attempt.
// base=1000, multiplier=2, attempt=2, jitter=0 => 1000 x 2^2 = 4000
func TestCalculateBackoff_SecondRetry(t *testing.T) {
	policy := port.RetryPolicy{
		BaseDelayMs: 1000,
		MaxDelayMs:  1000000,
		Multiplier:  2,
		Jitter:      0,
	}
	result := calculateBackoff(2, policy)
	assert.Equal(t, 4000*time.Millisecond, result)
}

// TestCalculateBackoff_MaxCap verifies 7.1.3: max cap enforcement.
// base=1000, multiplier=2, max=10000, attempt=5, jitter=0
// raw = 1000 x 2^5 = 32000, capped at 10000
func TestCalculateBackoff_MaxCap(t *testing.T) {
	policy := port.RetryPolicy{
		BaseDelayMs: 1000,
		MaxDelayMs:  10000,
		Multiplier:  2,
		Jitter:      0,
	}
	result := calculateBackoff(5, policy)
	assert.Equal(t, 10000*time.Millisecond, result)
}

// TestCalculateBackoff_JitterBoundary verifies 7.1.4: jitter boundary values.
// base=1000, multiplier=1, attempt=1, jitter=0.2
// raw = min(1000 x 1^1, max) = 1000
// actual = 1000 x (1 - 0.2 x rand)
//
// With mocked rand=0: actual = 1000
// With mocked rand=1: actual = 800
func TestCalculateBackoff_JitterBoundary(t *testing.T) {
	policy := port.RetryPolicy{
		BaseDelayMs: 1000,
		MaxDelayMs:  1000000,
		Multiplier:  1,
		Jitter:      0.2,
	}

	t.Run("rand_equals_0", func(t *testing.T) {
		result := calculateBackoff(1, policy, func() float64 { return 0 })
		assert.Equal(t, 1000*time.Millisecond, result)
	})

	t.Run("rand_equals_1", func(t *testing.T) {
		result := calculateBackoff(1, policy, func() float64 { return 1 })
		assert.Equal(t, 800*time.Millisecond, result)
	})
}
