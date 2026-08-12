package config

import (
	"testing"
)

// TestIsOllamaLoopback pins the planned IsOllamaLoopback predicate: a raw
// endpoint string counts as a local Ollama server only when it is an
// http(s) URL whose host is a genuine loopback address (127.0.0.1, ::1, or
// the literal "localhost", case-insensitive) on the default Ollama port.
// Host-name lookalikes (suffix hosts, trailing-dot FQDNs, IPv4-mapped IPv6,
// alternate spellings of 127.0.0.1), userinfo, fragments, non-http schemes,
// scheme-relative or scheme-less strings, and empty/garbage input must all be
// rejected.
func TestIsOllamaLoopback(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		// Loopback endpoints that must be recognized.
		{name: "ipv4 loopback", raw: "http://127.0.0.1:11434", want: true},
		{name: "ipv4 loopback with path", raw: "http://127.0.0.1:11434/v1", want: true},
		{name: "ipv6 loopback with path", raw: "http://[::1]:11434/v1", want: true},
		{name: "localhost", raw: "http://localhost:11434", want: true},
		{name: "localhost https with path", raw: "https://localhost:11434/v1", want: true},
		{name: "localhost case-insensitive", raw: "http://LOCALHOST:11434", want: true},

		// Lookalikes and malformed inputs that must be rejected.
		{name: "suffix host", raw: "http://localhost.evil.com", want: false},
		{name: "ipv4 lookalike suffix", raw: "http://127.0.0.1.nip.io", want: false},
		{name: "userinfo password", raw: "http://user:pass@127.0.0.1:11434", want: false},
		{name: "userinfo user only", raw: "http://user@127.0.0.1:11434", want: false},
		{name: "fragment", raw: "http://127.0.0.1:11434#frag", want: false},
		{name: "trailing dot host", raw: "http://localhost.:11434", want: false},
		{name: "hex ipv4", raw: "http://0x7f000001", want: false},
		{name: "short ipv4", raw: "http://127.1", want: false},
		{name: "integer ipv4", raw: "http://2130706433", want: false},
		{name: "bad scheme", raw: "ftp://127.0.0.1", want: false},
		{name: "no scheme", raw: "127.0.0.1:11434", want: false},
		{name: "scheme-relative", raw: "//127.0.0.1:11434", want: false},
		{name: "ipv4-mapped ipv6", raw: "http://[::ffff:127.0.0.1]:11434", want: false},
		{name: "empty", raw: "", want: false},
		{name: "not a url", raw: "not a url", want: false},
		{name: "remote ollama", raw: "https://ollama.com/v1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOllamaLoopback(tt.raw); got != tt.want {
				t.Fatalf("IsOllamaLoopback(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
