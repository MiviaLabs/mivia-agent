package controller

import (
	"context"
	"fmt"
	"testing"
	"time"
	"unicode/utf8"
)

// TestProbeTruncateRunesBoundary pins the rune-safe truncation contract at
// the byte limits: a cut that lands inside a multi-byte rune drops only the
// incomplete tail bytes, a genuine U+FFFD rune at the cut is KEPT (it is a
// 3-byte rune, not the 1-byte incomplete-tail marker), and the zero, negative,
// and no-op bounds behave as documented. The result must always be valid
// UTF-8 within max bytes.
func TestProbeTruncateRunesBoundary(t *testing.T) {
	// Cut inside a 2-byte rune (é is 0xC3 0xA9): s[:3] ends one byte into
	// the second é, so the partial byte is dropped and "é" survives.
	if got := truncateRunes("éé", 3); got != "é" || !utf8.ValidString(got) {
		t.Fatalf("truncateRunes(\"éé\", 3) = %q (%d bytes), want \"é\" valid UTF-8", got, len(got))
	}
	// Cut inside a 3-byte rune (€ is 0xE2 0x82 0xAC): s[:3] = é + one byte of
	// €, so the partial byte is dropped.
	if got := truncateRunes("é€", 3); got != "é" || !utf8.ValidString(got) {
		t.Fatalf("truncateRunes(\"é€\", 3) = %q, want \"é\"", got)
	}
	// A genuine U+FFFD rune at the cut (3 bytes) is kept: DecodeLastRuneInString
	// reports RuneError with size 3, which is a complete rune, not a partial
	// tail. s[:4] of "x\uFFFD\uFFFD" (7 bytes) is exactly "x\uFFFD".
	if got := truncateRunes("x\uFFFD\uFFFD", 4); got != "x\uFFFD" || !utf8.ValidString(got) {
		t.Fatalf("truncateRunes(\"x\\uFFFD\\uFFFD\", 4) = %q, want %q", got, "x\uFFFD")
	}
	// max = 0 truncates to empty; max >= len and negative max leave s whole.
	if got := truncateRunes("abc", 0); got != "" {
		t.Fatalf("truncateRunes(\"abc\", 0) = %q, want \"\"", got)
	}
	if got := truncateRunes("abc", 5); got != "abc" {
		t.Fatalf("truncateRunes(\"abc\", 5) = %q, want \"abc\"", got)
	}
	if got := truncateRunes("abc", -1); got != "abc" {
		t.Fatalf("truncateRunes(\"abc\", -1) = %q, want \"abc\"", got)
	}
}

// TestProbePanelLimiterSlotAccountingReturnsEverySlot pins that every panel
// actor lease release path returns its slot: plain Release on a pending
// lease, ReleaseBeforeActor, AttachLocal followed by Release, and a double
// Release (idempotent). After all four slots are released, a fresh acquire
// must succeed without blocking and the per-run registry must be empty — a
// leaked slot would wedge panel runs at the four-slot cap.
func TestProbePanelLimiterSlotAccountingReturnsEverySlot(t *testing.T) {
	limiter := NewPanelActorLimiter()
	ctx := context.Background()

	leases := make([]*panelActorLease, 0, 4)
	for i := 0; i < 4; i++ {
		lease, err := limiter.Acquire(ctx, fmt.Sprintf("wfr-probe-%d", i))
		if err != nil {
			t.Fatalf("Acquire(%d): %v", i, err)
		}
		leases = append(leases, lease)
	}

	// Release path 1: plain Release on a pending lease.
	leases[0].Release()
	// Release path 2: ReleaseBeforeActor on a pending lease (no actor admitted).
	leases[1].ReleaseBeforeActor()
	// Release path 3: AttachLocal (a local actor exists) then Release.
	leases[2].AttachLocal()
	leases[2].Release()
	// Release path 4: Release twice is idempotent.
	leases[3].Release()
	leases[3].Release()

	// A fifth distinct run ID must acquire a slot immediately after the four
	// releases; a leaked slot would block the fresh acquire.
	done := make(chan error, 1)
	go func() {
		lease, err := limiter.Acquire(ctx, "wfr-probe-after")
		if err == nil {
			lease.Release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fresh acquire after all releases failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fresh acquire blocked after all leases released; a slot leaked")
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.byRun) != 0 {
		t.Fatalf("limiter registry holds %d entries after all releases, want 0", len(limiter.byRun))
	}
}
