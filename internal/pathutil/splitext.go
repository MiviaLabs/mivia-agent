// Package pathutil provides a small, dependency-free helper for splitting file
// paths into their base and extension parts. It is a leaf package: it imports
// only the standard library.
package pathutil

// SplitExt splits a file path p into its base and extension parts; base + ext
// == p always holds.
//
// The extension is the suffix that begins at the final dot in the final path
// element (the text after the last '/' or '\\' separator). A dot that is the
// first character of the final element (a hidden-file name such as ".bashrc")
// is part of the name, not an extension, and a dot with nothing after it is
// not an extension either. Dots in earlier elements never count, so
// SplitExt("dir.with.dots/file") returns ("dir.with.dots/file", "").
//
// SplitExt("") returns ("", "").
func SplitExt(p string) (base, ext string) {
	elemStart := lastSeparatorIndex(p) + 1
	dot := -1
	for i := len(p) - 1; i >= elemStart; i-- {
		if p[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 || dot == elemStart || dot == len(p)-1 {
		return p, ""
	}
	return p[:dot], p[dot:]
}

// lastSeparatorIndex returns the index of the last '/' or '\\' in p, or -1
// when p contains no separator.
func lastSeparatorIndex(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return i
		}
	}
	return -1
}
