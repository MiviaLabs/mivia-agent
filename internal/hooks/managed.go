package hooks

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Managed hooks exist so an operator can install a control the user cannot
// disable. That only means anything if the file lives somewhere the user - and
// the agent running as the user - cannot write.
//
// It is therefore deliberately NOT under ~/.mivia. A file in the user's own
// home that is auto-trusted is self-authorization one directory over: exactly
// the class closed by refusing a `trust` key inside the config being trusted.
// Anything able to write the user's home could grant itself the tier that
// always runs and that headless runs honour without a bypass flag.
//
// The boundary is the filesystem's, not ours: a root-owned file, in a
// root-owned directory, with no group or world write bit and no symbolic link
// on the final component. A platform that cannot express that gets NO managed
// tier - an empty ManagedConfigPath - rather than a weaker one, because a tier
// named "the user cannot disable this" that the user can in fact write is worse
// than no tier at all.

// ManagedGroups loads the operator-owned hook config, if the platform has a
// provenance boundary and the file clears it.
//
// A file that fails the check is reported and ignored: an operator's
// misconfigured file must be loud, and must not stop the CLI from starting.
func ManagedGroups() ([]Group, []string) {
	path := ManagedConfigPath()
	if path == "" {
		return nil, nil
	}
	if _, err := os.Lstat(path); err != nil {
		// Absent is the default. Anything else about the path itself is
		// reported by loadManagedFrom below.
		if os.IsNotExist(err) {
			return nil, nil
		}
	}
	groups, err := loadManagedFrom(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("ignoring managed hook config %s: %v", path, err)}
	}
	return groups, nil
}

// managedOwnership is the provenance predicate, kept pure so both directions
// are testable without needing to create a root-owned file.
func managedOwnership(uid int, mode os.FileMode) error {
	if uid != 0 {
		return fmt.Errorf("owned by uid %d, not root: a managed hook must live where the user running mivia cannot write it", uid)
	}
	if mode.Perm()&0o022 != 0 {
		return fmt.Errorf("group- or world-writable (%v): a managed hook must not be rewritable by a non-root account", mode.Perm())
	}
	return nil
}

// loadManagedFrom verifies the provenance boundary and parses the file.
//
// The parent directory is checked too. A root-owned file in a user-writable
// directory can simply be replaced, so verifying the file alone would verify
// nothing.
func loadManagedFrom(path string) ([]Group, error) {
	if err := checkManagedPath(filepath.Dir(path), true); err != nil {
		return nil, fmt.Errorf("directory %s: %w", filepath.Dir(path), err)
	}
	if err := checkManagedPath(path, false); err != nil {
		return nil, err
	}
	data, err := readManagedFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, path)
}

func checkManagedPath(path string, wantDir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("must not be a symbolic link: a link lets whoever can create it choose which file carries operator authority")
	}
	if wantDir && !info.IsDir() {
		return fmt.Errorf("is not a directory")
	}
	if !wantDir && !info.Mode().IsRegular() {
		return fmt.Errorf("is not a regular file")
	}
	uid, ok := fileOwner(info)
	if !ok {
		return fmt.Errorf("ownership is not reportable on this platform")
	}
	return managedOwnership(uid, info.Mode())
}

func readManagedFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("changed while reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHookConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHookConfigBytes {
		return nil, fmt.Errorf("exceeds %d bytes", maxHookConfigBytes)
	}
	return data, nil
}

// maxHookConfigBytes bounds a hook config read. It mirrors the bound the user
// config path declares.
const maxHookConfigBytes = 1 << 20
