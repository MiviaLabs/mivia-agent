// Package pathutil provides a small, dependency-free helper for splitting a
// file path into its base and extension parts. It is a leaf package: it
// imports only the standard library.
package pathutil

import "os"

// SplitExt splits p into its base and extension parts. ext is the suffix of
// the final path element that begins at its last dot, dot included; base is
// everything before that suffix, so base + ext always equals p.
//
// A dot run that reaches the start of the final element is not an extension:
// ".env" splits as (".env", ""), "." as (".", ""), and ".." as ("..", "").
// Dots in directory names do not count: "a.dir/b" splits as ("a.dir/b", ""),
// while "archive.tar.gz" splits as ("archive.tar", ".gz") and "archive." as
// ("archive", "."). SplitExt("") returns ("", "").
func SplitExt(p string) (base, ext string) {
	// Find the last dot of the final path element: scan backwards from the
	// end and stop at the first path separator.
	dot := -1
	for i := len(p) - 1; i >= 0 && !os.IsPathSeparator(p[i]); i-- {
		if p[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return p, ""
	}
	// The dot is an extension boundary only when the final element has at
	// least one non-dot rune before it; otherwise the element is a leading
	// dot run like ".env", ".", or ".." and has no extension.
	for j := dot - 1; j >= 0 && !os.IsPathSeparator(p[j]); j-- {
		if p[j] != '.' {
			return p[:dot], p[dot:]
		}
	}
	return p, ""
}
