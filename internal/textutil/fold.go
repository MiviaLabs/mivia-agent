package textutil

// ContainsFold reports whether s contains substr when only ASCII letters
// fold: 'A'-'Z' matches 'a'-'z' and 'a'-'z' matches 'A'-'Z'. Every other
// byte, including non-ASCII runes and malformed UTF-8, compares exactly and
// never folds. An empty substr is contained in every string. The scan uses a
// byte window of length len(substr) over s and is O(len(s)*len(substr)) in
// the worst case.
func ContainsFold(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	last := len(s) - len(substr)
	for i := 0; i <= last; i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if foldASCII(s[i+j]) != foldASCII(substr[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// foldASCII lowercases an ASCII letter and passes every other byte through
// unchanged.
func foldASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}
