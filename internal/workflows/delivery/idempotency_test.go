package delivery

import (
	"strings"
	"testing"
)

func TestDeliveryKey(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := DeliveryKey("run-1", "digest-1")
		b := DeliveryKey("run-1", "digest-1")
		if a != b {
			t.Errorf("DeliveryKey not deterministic: %q vs %q", a, b)
		}
	})

	t.Run("prefix", func(t *testing.T) {
		key := DeliveryKey("run-1", "digest-1")
		if !strings.HasPrefix(key, "wfdel:") {
			t.Errorf("key %q missing wfdel: prefix", key)
		}
	})

	t.Run("hex encoded sha256", func(t *testing.T) {
		hex := strings.TrimPrefix(DeliveryKey("run-1", "digest-1"), "wfdel:")
		if len(hex) != 64 {
			t.Fatalf("hex part length = %d, want 64 (sha256)", len(hex))
		}
		for _, r := range hex {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Fatalf("non-hex character %q in key %q", r, hex)
			}
		}
	})

	t.Run("distinct across run ids", func(t *testing.T) {
		a := DeliveryKey("run-1", "digest-1")
		b := DeliveryKey("run-2", "digest-1")
		if a == b {
			t.Errorf("keys equal across run ids: %q", a)
		}
	})

	t.Run("distinct across workflow digests", func(t *testing.T) {
		a := DeliveryKey("run-1", "digest-1")
		b := DeliveryKey("run-1", "digest-2")
		if a == b {
			t.Errorf("keys equal across digests: %q", a)
		}
	})

	t.Run("empty inputs still yield a key", func(t *testing.T) {
		if key := DeliveryKey("", ""); !strings.HasPrefix(key, "wfdel:") {
			t.Errorf("key %q for empty inputs should still be prefixed", key)
		}
	})
}
