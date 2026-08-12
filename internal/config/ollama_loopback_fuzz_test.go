package config

import "testing"

// FuzzOllamaLoopbackURLPredicates is belt-and-suspenders coverage for the two
// ollama URL predicates over ARBITRARY bytes: the existing
// FuzzValidateBaseURLNeverPanics only exercises providerName "deepseek", so
// this target drives validateBaseURL(raw, "ollama") and IsOllamaLoopback(raw)
// together on the same raw input. Both predicates must be total - they return
// false/error, never panic - and every value the loopback predicate approves
// must also pass structural validation for the ollama provider. The length cap
// (maxBaseURLLength) is the only rejection a loopback-approved value can
// legitimately hit, so the cross-check is guarded by it.
func FuzzOllamaLoopbackURLPredicates(f *testing.F) {
	seedOllamaLoopbackCorpus(f)
	f.Fuzz(func(t *testing.T, seed []byte) {
		raw := string(seed)
		loopback := IsOllamaLoopback(raw)
		vErr := validateBaseURL(raw, "ollama")
		if loopback && len(raw) <= maxBaseURLLength && vErr != nil {
			t.Fatalf("IsOllamaLoopback(%q) = true but validateBaseURL(%q, \"ollama\") = %v", raw, raw, vErr)
		}
	})
}

// seedOllamaLoopbackCorpus primes the target with loopback-approved,
// structurally-valid-but-non-loopback, and malformed inputs; the fuzzer's
// arbitrary bytes cover everything else.
func seedOllamaLoopbackCorpus(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("http://127.0.0.1:11434/v1"))
	f.Add([]byte("http://localhost:11434/v1"))
	f.Add([]byte("http://[::1]:11434/v1"))
	f.Add([]byte("https://127.0.0.1/v1"))
	f.Add([]byte("http://127.0.0.1"))
	f.Add([]byte("http://127.0.0.1:notaport"))
	f.Add([]byte("http://203.0.113.7:11434/v1"))
	f.Add([]byte("http://user:pass@127.0.0.1:11434/v1"))
	f.Add([]byte("http://127.0.0.1:11434/v1#frag"))
	f.Add([]byte("://bad"))
	f.Add([]byte("127.0.0.1:11434"))
	f.Add([]byte("ftp://127.0.0.1:11434"))
}
