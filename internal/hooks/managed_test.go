package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point of the managed tier is a file the user - and the agent
// running as the user - cannot write. A file the current user owns fails, and
// on a platform with no way to express that boundary there is no tier at all.
func TestManagedProvenanceRejectsAUserOwnedFile(t *testing.T) {
	if ManagedConfigPath() == "" {
		t.Skip("no managed provenance boundary on this platform")
	}
	if err := managedOwnership(os.Getuid()+1, 0o644); err == nil {
		t.Fatal("a file owned by anyone but root must be refused")
	}
	if err := managedOwnership(0, 0o644); err != nil {
		t.Fatalf("a root-owned, non-group-writable file must be accepted: %v", err)
	}
}

func TestManagedProvenanceRejectsGroupAndWorldWritableModes(t *testing.T) {
	if ManagedConfigPath() == "" {
		t.Skip("no managed provenance boundary on this platform")
	}
	for _, mode := range []os.FileMode{0o664, 0o666, 0o622, 0o777} {
		if err := managedOwnership(0, mode); err == nil {
			t.Errorf("mode %v must be refused: anyone in the group can rewrite the hook", mode)
		} else if !strings.Contains(err.Error(), "writable") {
			t.Errorf("mode %v: error must name the writability problem, got %v", mode, err)
		}
	}
}

// A symlink at the managed path lets whoever can create it choose which file
// carries operator authority.
func TestManagedConfigRefusesASymlink(t *testing.T) {
	if ManagedConfigPath() == "" {
		t.Skip("no managed provenance boundary on this platform")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "planted.toml")
	if err := os.WriteFile(target, []byte(trustBase), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "managed.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// Checked directly rather than through loadManagedFrom: the parent
	// directory of a temp dir is user-owned, so the directory check would
	// refuse first and this property would never be exercised.
	if err := checkManagedPath(link, false); err == nil {
		t.Fatal("a symlinked managed config must be refused")
	} else if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error must name the symlink refusal, got %v", err)
	}
	if _, err := loadManagedFrom(link); err == nil {
		t.Fatal("loadManagedFrom must refuse a symlinked managed config")
	}
}

// A root-owned file in a user-writable directory can simply be replaced, so
// verifying the file alone would verify nothing.
func TestManagedProvenanceChecksTheParentDirectory(t *testing.T) {
	if ManagedConfigPath() == "" {
		t.Skip("no managed provenance boundary on this platform")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; the fixture cannot be non-root-owned")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "managed.toml")
	if err := os.WriteFile(path, []byte(trustBase), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := loadManagedFrom(path)
	if err == nil {
		t.Fatal("a managed config in a user-owned directory must be refused")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Fatalf("error must name the directory it refused, got %v", err)
	}
}

// A managed file the current user owns is exactly the escalation the tier must
// not permit, and it is the shape ~/.mivia/managed.toml would always have had.
func TestManagedConfigRefusesAFileTheCurrentUserOwns(t *testing.T) {
	if ManagedConfigPath() == "" {
		t.Skip("no managed provenance boundary on this platform")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; the fixture cannot be non-root-owned")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "managed.toml")
	if err := os.WriteFile(path, []byte(trustBase), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := loadManagedFrom(path); err == nil {
		t.Fatal("a managed config owned by the running user must be refused")
	}
}

func TestManagedConfigAbsentIsNotAnError(t *testing.T) {
	groups, warnings := ManagedGroups()
	if len(groups) != 0 {
		t.Fatalf("no managed file is installed in this environment; got %d groups", len(groups))
	}
	if len(warnings) != 0 {
		t.Fatalf("an absent managed config is the default, not a warning: %v", warnings)
	}
}

// The managed path is deliberately NOT under the user's home. A file there is
// writable by the user and by the agent running as them, so auto-trusting it
// would be self-authorization one directory over.
func TestManagedPathIsNotUnderTheUserHome(t *testing.T) {
	path := ManagedConfigPath()
	if path == "" {
		t.Skip("no managed provenance boundary on this platform")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if strings.HasPrefix(filepath.Clean(path), filepath.Clean(home)+string(filepath.Separator)) {
		t.Fatalf("managed config path %q is inside the user home %q", path, home)
	}
}
