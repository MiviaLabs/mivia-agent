// Package pathutil provides small, dependency-free helpers for working with
// file paths. It is a leaf package: it imports only the standard library.
package pathutil

// SplitExt splits a file path into its base and extension parts. The
// extension is the suffix that begins at the final dot in the path's final
// element (the part after the last path separator) and runs to the end,
// including the dot; the base is the path with that suffix removed, so
// base + ext always equals p.
//
// A dot that is the first character of the final element does not begin an
// extension, so SplitExt(".bashrc") returns (".bashrc", ""). A trailing dot
// is an extension of one dot: SplitExt("file.") returns ("file", "."). Both
// "/" and "\\" count as path separators, so the result is identical on every
// platform. When p has no extension, base is p and ext is "".
//
// SplitExt("") returns ("", "").
func SplitExt(p string) (base, ext string) {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			break // a dot before the last element does not start an extension
		}
		if p[i] != '.' {
			continue
		}
		if i == 0 || p[i-1] == '/' || p[i-1] == '\\' {
			break // the dot opens the final element: hidden file, no extension
		}
		return p[:i], p[i:]
	}
	return p, ""
}
