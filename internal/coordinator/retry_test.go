package coordinator

import (
	"math"
	"testing"
	"time"
)

// TestEffectiveBackoffNeverWrapsNegative is the DC-8 regression test for the
// retry backoff overflow. With an UNCAPPED policy (MaxBackoff 0) and the
// default 100ms base with factor 2.0, attempt 37 computes
// 1e8 * 2^37 ~ 1.374e19 ns, which exceeds int64's maximum (9.223e18). The
// out-of-range float64->int64 conversion in time.Duration(backoff) yields a
// negative duration on amd64/arm64, so the retry re-queues with no delay
// (now.Add(negative) lands in the past) instead of backing off - a retry
// storm. The test FAILS on the unfixed code at attempt 37 and passes after
// the clamp below the int64 conversion boundary is added.
func TestEffectiveBackoffNeverWrapsNegative(t *testing.T) {
	p := RetryPolicy{
		MaxRetries:     40,
		BaseBackoff:    100 * time.Millisecond,
		MaxBackoff:     0, // uncapped: the overflow-triggering configuration
		BackoffFactor:  2.0,
		JitterFraction: 0, // deterministic: the rand branch is never entered
	}
	for attempt := 0; attempt < 40; attempt++ {
		if got := p.EffectiveBackoff(attempt); got <= 0 {
			t.Fatalf("EffectiveBackoff(%d) = %v, want > 0 (backoff must never wrap negative)", attempt, got)
		}
	}
}

// TestEffectiveBackoffBoundaries pins the DC-6 0/max/max+1 boundary on the
// attempt dimension and the MaxBackoff-unset vs set boundary on the policy
// dimension, plus the documented negative-path defaults.
func TestEffectiveBackoffBoundaries(t *testing.T) {
	uncapped := RetryPolicy{
		MaxRetries:     40,
		BaseBackoff:    100 * time.Millisecond,
		BackoffFactor:  2.0,
		JitterFraction: 0,
	}

	t.Run("attempt zero returns exactly the base", func(t *testing.T) {
		if got := uncapped.EffectiveBackoff(0); got != 100*time.Millisecond {
			t.Fatalf("EffectiveBackoff(0) = %v, want 100ms", got)
		}
	})

	t.Run("attempt 36 (max in-range) stays positive and at least the base", func(t *testing.T) {
		// 1e8 * 2^36 = 6.8719476736e18 ns: the last attempt whose product for
		// this policy still converts to a positive int64.
		if got := uncapped.EffectiveBackoff(36); got <= 0 || got < uncapped.BaseBackoff {
			t.Fatalf("EffectiveBackoff(36) = %v, want > 0 and >= 100ms base", got)
		}
	})

	t.Run("attempt 37 (max+1) never wraps negative", func(t *testing.T) {
		// 1e8 * 2^37 ~ 1.374e19 ns overflows int64 (9.223e18): this FAILS on
		// the unfixed code, where time.Duration(backoff) is negative.
		if got := uncapped.EffectiveBackoff(37); got <= 0 {
			t.Fatalf("EffectiveBackoff(37) = %v, want > 0", got)
		}
	})

	t.Run("capped policy stays positive and at or under the cap", func(t *testing.T) {
		capped := RetryPolicy{
			MaxRetries:     40,
			BaseBackoff:    100 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
			JitterFraction: 0,
		}
		for attempt := 0; attempt < 40; attempt++ {
			got := capped.EffectiveBackoff(attempt)
			if got <= 0 || got > capped.MaxBackoff {
				t.Fatalf("capped EffectiveBackoff(%d) = %v, want 0 < d <= 30s", attempt, got)
			}
		}
	})

	t.Run("zero MaxRetries disables retry", func(t *testing.T) {
		p := RetryPolicy{MaxRetries: 0, BaseBackoff: time.Second}
		if got := p.EffectiveBackoff(37); got != 0 {
			t.Fatalf("EffectiveBackoff with MaxRetries 0 = %v, want 0", got)
		}
	})

	t.Run("negative attempt clamps to zero", func(t *testing.T) {
		if got := uncapped.EffectiveBackoff(-1); got != 100*time.Millisecond {
			t.Fatalf("EffectiveBackoff(-1) = %v, want 100ms (attempt clamped to 0)", got)
		}
	})

	t.Run("zero factor defaults to 2.0", func(t *testing.T) {
		p := RetryPolicy{
			MaxRetries:     40,
			BaseBackoff:    100 * time.Millisecond,
			BackoffFactor:  0, // documented default: treated as 2.0
			JitterFraction: 0,
		}
		if got := p.EffectiveBackoff(36); got <= 0 {
			t.Fatalf("factor-default EffectiveBackoff(36) = %v, want > 0", got)
		}
		if got := p.EffectiveBackoff(37); got <= 0 {
			t.Fatalf("factor-default EffectiveBackoff(37) = %v, want > 0 (no wrap)", got)
		}
	})
}

// FuzzEffectiveBackoff is a deterministic, in-process fuzz target over the
// pure function RetryPolicy.EffectiveBackoff (no I/O, goroutines, or timers;
// it matches the repo's in-process fuzz convention). Every iteration asserts
// the returned delay is a positive, in-range time.Duration, which the unfixed
// code violates for uncapped policies at late attempts.
func FuzzEffectiveBackoff(f *testing.F) {
	// Seed corpus: boundary attempts (0, 36 = last in-range, 37 = max+1 wrap,
	// 40) crossed with capped (30s) and uncapped (0) MaxBackoff. The
	// attempt-37 uncapped seed fails on the unfixed code.
	for _, attempt := range []byte{0, 36, 37, 40} {
		for _, maxNS := range []uint64{0, 30_000_000_000} { // 0 = uncapped, else 30s
			seed := make([]byte, 15)
			seed[0] = attempt
			seed[1] = 39 // MaxRetries byte; decodes as 39%64+1 = 40 (any nonzero value exercises the same path)
			// BaseBackoff = 100ms = 100_000_000 ns = 0x05F5E100 (big-endian in bytes 2..6).
			seed[2], seed[3], seed[4], seed[5], seed[6] = 0x00, 0x05, 0xF5, 0xE1, 0x00
			// MaxBackoff big-endian in bytes 7..11.
			seed[7] = byte(maxNS >> 32)
			seed[8] = byte(maxNS >> 24)
			seed[9] = byte(maxNS >> 16)
			seed[10] = byte(maxNS >> 8)
			seed[11] = byte(maxNS)
			// Factor byte 0 -> 2.0 default; jitter byte 0 -> deterministic.
			f.Add(seed)
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		seed := func(i int) byte {
			if i < len(data) {
				return data[i]
			}
			return 0
		}
		// Fuzz byte layout mirrors the seed layout above.
		attempt := int(seed(0)) % 65      // 0..64
		maxRetries := int(seed(1))%64 + 1 // 1..64; the MaxRetries==0 branch is covered by unit tests
		baseNS := uint64(seed(2))<<32 | uint64(seed(3))<<24 | uint64(seed(4))<<16 | uint64(seed(5))<<8 | uint64(seed(6))
		maxNS := uint64(seed(7))<<32 | uint64(seed(8))<<24 | uint64(seed(9))<<16 | uint64(seed(10))<<8 | uint64(seed(11))
		factor := float64(seed(12)%81) / 10.0 // 0..8, 0 = 2.0 default (documented contract)
		jitter := float64(seed(13)%11) / 10.0 // 0..1.0

		p := RetryPolicy{
			MaxRetries:     maxRetries,
			BaseBackoff:    time.Duration(baseNS),
			MaxBackoff:     time.Duration(maxNS),
			BackoffFactor:  factor,
			JitterFraction: jitter,
		}
		got := p.EffectiveBackoff(attempt)
		// The invariant must hold for any jitter draw: the jitter multiply can
		// push a capped value over the conversion boundary too.
		if got <= 0 {
			t.Fatalf("EffectiveBackoff(%d) = %v, want > 0 (policy %+v)", attempt, got, p)
		}
		if got > math.MaxInt64 {
			t.Fatalf("EffectiveBackoff(%d) = %v, want <= math.MaxInt64 (policy %+v)", attempt, got, p)
		}
	})
}
