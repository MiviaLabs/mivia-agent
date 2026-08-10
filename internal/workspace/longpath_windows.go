//go:build windows

package workspace

import "golang.org/x/sys/windows"

// longPath expands 8.3 short-name components to their long form. A path
// that cannot be resolved (for example a not-yet-created directory) is
// returned unchanged: the short form is still a valid path and later
// operations resolve it.
func longPath(path string) string {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	// A nil buffer with length zero returns the required buffer size
	// (including the terminating NUL) instead of failing.
	size, err := windows.GetLongPathName(ptr, nil, 0)
	if err != nil || size == 0 {
		return path
	}
	buf := make([]uint16, size)
	written, err := windows.GetLongPathName(ptr, &buf[0], size)
	if err != nil || written == 0 {
		return path
	}
	return windows.UTF16ToString(buf[:written])
}
