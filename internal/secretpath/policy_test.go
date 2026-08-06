package secretpath

import "testing"

func TestNewMatchesConfiguredRules(t *testing.T) {
	policy, err := New([]string{".ENV", "id_rsa"}, []string{".env.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".env", "config.env.backup", "keys/ID_RSA.pub"} {
		if !policy.Match(path) {
			t.Fatalf("Match(%q) = false, want true", path)
		}
	}
	for _, path := range []string{".env.example", "main.go"} {
		if policy.Match(path) {
			t.Fatalf("Match(%q) = true, want false", path)
		}
	}
}

func TestNewRejectsUnsafeException(t *testing.T) {
	for _, exception := range []string{"", "/.env.example", "../.env.example"} {
		t.Run(exception, func(t *testing.T) {
			if _, err := New([]string{".env"}, []string{exception}); err == nil {
				t.Fatal("New() error = nil, want error")
			}
		})
	}
}
