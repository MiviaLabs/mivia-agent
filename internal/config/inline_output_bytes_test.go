package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestInlineOutputBytesExplicitZeroPreserved pins the config contract that an
// explicit [subagents] inline_output_bytes = 0 means "always use refs (never
// inline)" and must survive resolution. Before the fix, an explicit 0 was
// indistinguishable from an absent key, so resolveSubagentConfig overwrote it
// with the 4096 default and the documented "always use refs" mode was
// unreachable through config. Fails before the fix (resolves to 4096), passes
// after.
func TestInlineOutputBytesExplicitZeroPreserved(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, `
[subagents]
inline_output_bytes = 0
`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.InlineOutputBytes != 0 {
		t.Fatalf("explicit inline_output_bytes = 0 resolved to %d, want 0 (always use refs)", res.Subagents.InlineOutputBytes)
	}
}

// TestInlineOutputBytesAbsentDefaultsTo4096 is the negative guard: an absent
// key keeps the historical 4096 default so existing configs load unchanged.
func TestInlineOutputBytesAbsentDefaultsTo4096(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.InlineOutputBytes != defaultInlineOutputBytes {
		t.Fatalf("absent inline_output_bytes resolved to %d, want default %d", res.Subagents.InlineOutputBytes, defaultInlineOutputBytes)
	}
}

// TestInlineOutputBytesExplicitPositivePreserved pins that an explicit
// positive value passes through resolution unchanged.
func TestInlineOutputBytesExplicitPositivePreserved(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, `
[subagents]
inline_output_bytes = 100
`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.InlineOutputBytes != 100 {
		t.Fatalf("explicit inline_output_bytes = 100 resolved to %d, want 100", res.Subagents.InlineOutputBytes)
	}
}

// writeWorkspaceOverlayConfig writes workspaceRoot/.mivia/mivia.toml carrying
// only a [subagents] store_backend/store_path plus extra, the shape a real
// project workspace overlay takes when it redirects durable storage without
// touching output-inlining policy.
func writeWorkspaceOverlayConfig(t *testing.T, workspaceRoot, extra string) {
	t.Helper()
	workspaceConfigPath := workspace.NamespacePath(workspaceRoot, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(workspaceConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	overlay := "[subagents]\nstore_backend = \"sqlite\"\nstore_path = \"" +
		filepath.ToSlash(filepath.Join(workspaceRoot, "workspace.db")) + "\"\n" + extra
	if err := os.WriteFile(workspaceConfigPath, []byte(overlay), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestInlineOutputBytesExplicitZeroSurvivesWorkspaceOverlay pins that an
// explicit [subagents] inline_output_bytes = 0 in the base config survives a
// distinct workspace overlay (.mivia/mivia.toml under WorkspaceRoot) that
// omits the key. Before the fix, probeInlineOutputBytes unconditionally
// overwrote inlineOutputBytesSet on every layer decode, so the overlay's
// absence reset the base's explicit-0 presence flag and
// resolveSubagentConfig replaced the operator's "always use refs" setting
// with the 4096 default - ref-only subagent output silently became
// inline-eligible in the model-visible envelope. Fails before the fix
// (resolves to 4096), passes after.
func TestInlineOutputBytesExplicitZeroSurvivesWorkspaceOverlay(t *testing.T) {
	base := writeMinimalConfig(t, `
[subagents]
inline_output_bytes = 0
`)
	workspaceRoot := t.TempDir()
	writeWorkspaceOverlayConfig(t, workspaceRoot, "") // no inline_output_bytes key

	res, err := Load(LoadOptions{ConfigPath: base, WorkspaceRoot: workspaceRoot})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.InlineOutputBytes != 0 {
		t.Fatalf("base explicit inline_output_bytes = 0 lost to workspace overlay: resolved to %d, want 0 (always use refs)", res.Subagents.InlineOutputBytes)
	}
}

// TestInlineOutputBytesOverlayExplicitZeroWins is the negative-path guard for
// the overlay presence fix. It pins both sides of the contract the fix must
// not disturb: an overlay that explicitly sets inline_output_bytes = 0 still
// wins over an absent base key (explicit presence keeps being honored, the fix
// only stops a layer's ABSENCE from erasing presence), and a base/overlay pair
// that both omit the key still resolves to the 4096 default (the absent-key
// default contract is preserved).
func TestInlineOutputBytesOverlayExplicitZeroWins(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		overlay string
		want    int
	}{
		{
			name:    "base absent, overlay explicit 0 wins",
			base:    "",
			overlay: "inline_output_bytes = 0\n",
			want:    0,
		},
		{
			name:    "base absent, overlay absent keeps default",
			base:    "",
			overlay: "",
			want:    defaultInlineOutputBytes,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := writeMinimalConfig(t, tt.base)
			workspaceRoot := t.TempDir()
			writeWorkspaceOverlayConfig(t, workspaceRoot, tt.overlay)

			res, err := Load(LoadOptions{ConfigPath: base, WorkspaceRoot: workspaceRoot})
			if err != nil {
				t.Fatal(err)
			}
			if res.Subagents.InlineOutputBytes != tt.want {
				t.Fatalf("inline_output_bytes resolved to %d, want %d", res.Subagents.InlineOutputBytes, tt.want)
			}
		})
	}
}
