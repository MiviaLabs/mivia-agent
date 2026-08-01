//go:build !unix

package hooks

import "os"

// ManagedConfigPath is empty on this platform: mivia has no way to verify that
// a file is owned by an administrator and not by the user running the agent.
// A tier called "the user cannot disable this" that the user can in fact write
// is worse than no tier, so there is none.
func ManagedConfigPath() string { return "" }

func fileOwner(os.FileInfo) (int, bool) { return 0, false }
