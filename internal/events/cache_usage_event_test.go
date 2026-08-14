package events

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCacheUsageEventIsSealedAndContentFree(t *testing.T) {
	event, err := NewCacheUsageEvent("deepseek", "deepseek-v4-pro", "implicit", 100, 80, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	// input_tokens/cached_input_tokens are legitimate fields on this event;
	// only the generic message-content keys and an injected secret must be
	// absent.
	for _, forbidden := range []string{`"content"`, "secret-sentinel"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("typed event contains forbidden field/value %q: %s", forbidden, encoded)
		}
	}
	if event.Provider != "deepseek" || event.CachedInputTokens != 80 {
		t.Fatalf("event fields = %+v", event)
	}
}

func TestCacheUsageEventRejectsUnsealedConstruction(t *testing.T) {
	var event CacheUsageEvent
	if err := event.Validate(); err == nil {
		t.Fatal("zero-value event must fail validation")
	}
}

func TestCacheUsageEventRejectsEmptyProviderOrModel(t *testing.T) {
	if _, err := NewCacheUsageEvent("", "model", "implicit", 1, 1, 0); err == nil {
		t.Fatal("empty provider accepted")
	}
	if _, err := NewCacheUsageEvent("provider", "", "implicit", 1, 1, 0); err == nil {
		t.Fatal("empty model accepted")
	}
}

func TestCacheUsageEventRejectsNegativeTokenCounts(t *testing.T) {
	if _, err := NewCacheUsageEvent("p", "m", "implicit", -1, 0, 0); err == nil {
		t.Fatal("negative input tokens accepted")
	}
	if _, err := NewCacheUsageEvent("p", "m", "implicit", 0, -1, 0); err == nil {
		t.Fatal("negative cached tokens accepted")
	}
	if _, err := NewCacheUsageEvent("p", "m", "implicit", 0, 0, -1); err == nil {
		t.Fatal("negative write tokens accepted")
	}
}

func TestCacheUsageEventRejectsControlCharacters(t *testing.T) {
	if _, err := NewCacheUsageEvent("p\nforged", "m", "implicit", 0, 0, 0); err == nil {
		t.Fatal("control character in provider accepted")
	}
}

// Model names have no upstream length cap in internal/config (only
// non-empty/valid-UTF8/no-control-chars are enforced), so a long but
// legitimate model identifier must not silently lose cache-usage
// observability - see the bug-audit finding this regression-tests.
func TestCacheUsageEventAcceptsLongModelName(t *testing.T) {
	longModel := strings.Repeat("a", 200)
	if _, err := NewCacheUsageEvent("provider", longModel, "implicit", 1, 1, 0); err != nil {
		t.Fatalf("200-char model name rejected: %v", err)
	}
}

func TestCacheUsageEventRejectsOverlongModelName(t *testing.T) {
	tooLong := strings.Repeat("a", 257)
	if _, err := NewCacheUsageEvent("provider", tooLong, "implicit", 1, 1, 0); err == nil {
		t.Fatal("257-char model name accepted")
	}
}

func TestCacheUsageEventHitPercent(t *testing.T) {
	event, err := NewCacheUsageEvent("p", "m", "implicit", 100, 80, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := event.HitPercent(); got != 80 {
		t.Fatalf("HitPercent = %d, want 80", got)
	}
	zero, err := NewCacheUsageEvent("p", "m", "implicit", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := zero.HitPercent(); got != 0 {
		t.Fatalf("HitPercent with zero input = %d, want 0", got)
	}
}
