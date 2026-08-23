package sdkadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	sdkws "github.com/MiviaLabs/mivia-ai-sdk/workspace"
)

// TestWorkspaceTypeAlias asserts that the bridge's Workspace type
// is the SDK's *workspace.Workspace so future code that has
// either name shares the same underlying type.
func TestWorkspaceTypeAlias(t *testing.T) {
	var w *sdkadapter.Workspace
	if _, ok := interface{}(w).(*sdkws.Workspace); !ok {
		t.Fatal("sdkadapter.Workspace is not a *mivia-ai-sdk/workspace.Workspace alias")
	}
}

// TestOptionsTypeAlias asserts that Options is the SDK's Options
// alias.
func TestOptionsTypeAlias(t *testing.T) {
	var opts sdkadapter.Options
	if _, ok := interface{}(opts).(sdkws.Options); !ok {
		t.Fatal("sdkadapter.Options is not a *mivia-ai-sdk/workspace.Options alias")
	}
}

// TestDefaultMaxReadBytesConstant asserts that the re-exported
// constant matches the SDK's value (10 MiB = 10 << 20 = 10485760).
// The SDK locks this constant as a wire-shape boundary; a
// drift would silently change the bridge's read-size cap.
func TestDefaultMaxReadBytesConstant(t *testing.T) {
	if sdkadapter.DefaultMaxReadBytes != 10<<20 {
		t.Fatalf("DefaultMaxReadBytes = %d, want %d (10 << 20)",
			sdkadapter.DefaultMaxReadBytes, 10<<20)
	}
	if sdkadapter.DefaultMaxReadBytes != sdkws.DefaultMaxReadBytes {
		t.Fatalf("DefaultMaxReadBytes mismatch: bridge=%d sdk=%d",
			sdkadapter.DefaultMaxReadBytes, sdkws.DefaultMaxReadBytes)
	}
}

// TestUnboundedConstant asserts that the Unbounded sentinel
// matches the SDK's value (-1). The sentinel is the input to
// ReadFileLimit that signals "no read-size cap".
func TestUnboundedConstant(t *testing.T) {
	if sdkadapter.Unbounded != -1 {
		t.Fatalf("Unbounded = %d, want -1", sdkadapter.Unbounded)
	}
	if sdkadapter.Unbounded != sdkws.Unbounded {
		t.Fatalf("Unbounded mismatch: bridge=%d sdk=%d",
			sdkadapter.Unbounded, sdkws.Unbounded)
	}
}

// TestWorkspaceSentinelsExported asserts that the four workspace
// sentinels are re-exported. A CLI caller that uses
// errors.Is(err, sdkadapter.ErrEscape) gets the same sentinel
// identity the SDK would have returned.
func TestWorkspaceSentinelsExported(t *testing.T) {
	sentinels := []struct {
		name string
		got  error
		want error
	}{
		{"ErrEscape", sdkadapter.ErrEscape, sdkws.ErrEscape},
		{"ErrInvalidLimit", sdkadapter.ErrInvalidLimit, sdkws.ErrInvalidLimit},
		{"ErrSecretPath", sdkadapter.ErrSecretPath, sdkws.ErrSecretPath},
		{"ErrTooLarge", sdkadapter.ErrTooLarge, sdkws.ErrTooLarge},
	}
	for _, s := range sentinels {
		if s.got != s.want {
			t.Errorf("%s mismatch: bridge=%v sdk=%v", s.name, s.got, s.want)
		}
	}
}

// TestWorkspaceOpenRejectsEmptyRoot asserts that Open fails
// fast when given an empty root path. The SDK validates Root
// non-emptiness; the bridge does not duplicate the validation,
// it forwards and lets the SDK return its own error.
func TestWorkspaceOpenRejectsEmptyRoot(t *testing.T) {
	_, err := sdkadapter.Open("")
	if err == nil {
		t.Fatal("Open with empty root returned no error; want a non-nil error")
	}
}
