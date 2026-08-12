package textutil

// HasControlByte reports whether s contains a C0 control byte (0x00-0x1F) or
// DEL (0x7F). Callers that build an identity or digest by concatenating
// untrusted strings with a byte-level separator use this to reject a value
// that could smuggle that separator and collide two different identities.
func HasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}
