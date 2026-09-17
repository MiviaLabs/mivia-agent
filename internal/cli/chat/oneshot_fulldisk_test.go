package chat

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// grantUserFullDisk writes an operator-owned full-disk grant into the HOME
// that isolatedSessionsWorkspace already redirected to a temp dir. It is the
// persisted `[workspace_access] full_disk = true` provenance - the operator's
// OWN config, never the workspace's - that
// config.UserFullDiskAccessForWorkspace is willing to honour.
func grantUserFullDisk(t *testing.T) {
	t.Helper()
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME is empty; isolatedSessionsWorkspace must run first")
	}
	path := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[workspace_access]\nfull_disk = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunCompactHonorsPersistedFullDiskGrant pins that the one-shot `mivia
// compact` path consults the operator's persisted grant instead of hardcoding
// confinement. It asserts through the never-silent disclosure because that
// notice is emitted from the same resolved value the path hands to
// ConfigureChatWorkspace: a compact that silently re-confines the registry
// cannot print it.
func TestRunCompactHonorsPersistedFullDiskGrant(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	grantUserFullDisk(t)
	name := seedCompactableCatalogSession(t, ws)

	restore := captureStderr(t)
	var buf bytes.Buffer
	err := runCompactWithIO([]string{"--session", name, "--workspace", ws, "--json"}, &buf)
	stderr := restore()
	if err != nil {
		t.Fatalf("compact %s: %v", name, err)
	}
	if !strings.Contains(stderr, config.FullDiskNoticeText) {
		t.Fatalf("compact did not honour the persisted full-disk grant: stderr = %q, want the FULL DISK ACCESS disclosure", stderr)
	}
}

// TestRunCompactWithoutGrantStaysConfined is the negative control for the test
// above: without the operator's persisted grant the same path must stay
// confined and silent, so the assertion there is proving the grant reached it
// rather than an unconditional banner.
func TestRunCompactWithoutGrantStaysConfined(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	name := seedCompactableCatalogSession(t, ws)

	restore := captureStderr(t)
	var buf bytes.Buffer
	err := runCompactWithIO([]string{"--session", name, "--workspace", ws, "--json"}, &buf)
	stderr := restore()
	if err != nil {
		t.Fatalf("compact %s: %v", name, err)
	}
	if strings.Contains(stderr, config.FullDiskNoticeText) {
		t.Fatalf("compact announced full-disk access with no grant: stderr = %q", stderr)
	}
}

// TestRunCompactIgnoresWorkspaceFullDiskGrant pins the provenance invariant on
// this path: isolatedSessionsWorkspace's own .mivia/mivia.toml is
// workspace-controlled, so a full_disk key planted there must never lift
// confinement. Only the operator's user config may.
func TestRunCompactIgnoresWorkspaceFullDiskGrant(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	existing, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(existing, []byte("\n[workspace_access]\nfull_disk = true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	name := seedCompactableCatalogSession(t, ws)

	restore := captureStderr(t)
	var buf bytes.Buffer
	err = runCompactWithIO([]string{"--session", name, "--workspace", ws, "--json"}, &buf)
	stderr := restore()
	if err != nil {
		t.Fatalf("compact %s: %v", name, err)
	}
	if strings.Contains(stderr, config.FullDiskNoticeText) {
		t.Fatalf("workspace config lifted its own confinement: stderr = %q", stderr)
	}
}

// TestRunSessionsUsageHonorsPersistedFullDiskGrant is the same pin for the
// `mivia sessions usage` path, which builds its registry through the identical
// ConfigureChatWorkspace call.
func TestRunSessionsUsageHonorsPersistedFullDiskGrant(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	grantUserFullDisk(t)
	name := seedCompactableCatalogSession(t, ws)

	restore := captureStderr(t)
	err := runSessionsUsage([]string{"--workspace", ws, "--json", name}, io.Discard)
	stderr := restore()
	if err != nil {
		t.Fatalf("sessions usage %s: %v", name, err)
	}
	if !strings.Contains(stderr, config.FullDiskNoticeText) {
		t.Fatalf("sessions usage did not honour the persisted full-disk grant: stderr = %q", stderr)
	}
}

// TestRunSessionsUsageWithoutGrantStaysConfined is the negative control for
// the sessions-usage pin above.
func TestRunSessionsUsageWithoutGrantStaysConfined(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	name := seedCompactableCatalogSession(t, ws)

	restore := captureStderr(t)
	err := runSessionsUsage([]string{"--workspace", ws, "--json", name}, io.Discard)
	stderr := restore()
	if err != nil {
		t.Fatalf("sessions usage %s: %v", name, err)
	}
	if strings.Contains(stderr, config.FullDiskNoticeText) {
		t.Fatalf("sessions usage announced full-disk access with no grant: stderr = %q", stderr)
	}
}
