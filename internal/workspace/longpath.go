package workspace

// LongPath returns path with every Windows short (8.3) name component
// expanded to its long form. On platforms without short names it returns
// the path unchanged.
//
// Windows path APIs and git resolve short names to their long form, so a
// canonicality check that compares a raw path with its resolved form would
// reject every short-name path as non-canonical even though it names the
// same directory. Both sides of such a comparison must use the same
// rendering, which is what LongPath provides.
func LongPath(path string) string {
	return longPath(path)
}
