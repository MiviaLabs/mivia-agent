// Package pathutil provides a small, dependency-free helper for splitting
// file paths into base and extension parts. It is a leaf package: it imports
// only the standard library.
package pathutil

// SplitExt splits p into its base name and its extension. The extension is
// the suffix beginning at the final dot in the final path element, dot
// included, and base is the rest of p, so base+ext always equals p.
//
// Both "/" and "\\" are treated as path separators, so the result does not
// depend on the operating system. A dot that starts the final element - a
// hidden file such as ".gitignore" - is part of the base name, so
// SplitExt(".gitignore") returns (".gitignore", ""). This follows the common
// hidden-file convention and differs from path.Ext, which reports the leading
// dot as the extension. Any other final dot is the extension, including a
// trailing dot: SplitExt("archive.") returns ("archive", "."). SplitExt("")
// returns ("", "").
func SplitExt(p string) (base, ext string) {
	lastSep := -1
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			lastSep = i
			break
		}
	}
	dot := -1
	for i := len(p) - 1; i > lastSep; i-- {
		if p[i] == '.' {
			dot = i
			break
		}
	}
	// No dot in the final element, or the dot is the element's first
	// character (a hidden file): there is no extension.
	if dot <= lastSep+1 {
		return p, ""
	}
	return p[:dot], p[dot:]
}
