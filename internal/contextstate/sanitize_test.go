package contextstate

import (
	"errors"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		policy  RedactionPolicy
		data    []byte
		wantErr bool
	}{
		{"empty policy", RedactionPolicy{}, []byte("hello"), false},
		{"pattern match", RedactionPolicy{Configured: true, Patterns: []string{"secret"}}, []byte("my secret"), true},
		{"pattern no match", RedactionPolicy{Configured: true, Patterns: []string{"secret"}}, []byte("hello"), false},
		{"empty pattern", RedactionPolicy{Configured: true, Patterns: []string{""}}, []byte("hello"), true},
		{"invalid pattern", RedactionPolicy{Configured: true, Patterns: []string{"[invalid"}}, []byte("hello"), true},
		{"key match", RedactionPolicy{Configured: true, KeyNames: []string{"api_key"}}, []byte("the api_key is here"), true},
		{"key match case insensitive", RedactionPolicy{Configured: true, KeyNames: []string{"API_KEY"}}, []byte("the api_key is here"), true},
		{"key no match", RedactionPolicy{Configured: true, KeyNames: []string{"api_key"}}, []byte("hello"), false},
		{"empty key name", RedactionPolicy{Configured: true, KeyNames: []string{""}}, []byte("hello"), true},
		{"whitespace key name", RedactionPolicy{Configured: true, KeyNames: []string{"  "}}, []byte("hello"), true},
		{"custom classifier clean", RedactionPolicy{Configured: true, Classifier: func(d []byte) error { return nil }}, []byte("hello"), false},
		{"custom classifier rejects", RedactionPolicy{Configured: true, Classifier: func(d []byte) error { return ErrInvalidDTO }}, []byte("hello"), true},
		{"custom classifier other error", RedactionPolicy{Configured: true, Classifier: func(d []byte) error { return errors.New("other") }}, []byte("hello"), true},
		{"pattern and key", RedactionPolicy{Configured: true, Patterns: []string{"aaa"}, KeyNames: []string{"bbb"}}, []byte("hello"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Classify(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("Classify() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidDTO) {
				t.Errorf("Classify() error = %v, want ErrInvalidDTO wrapped", err)
			}
		})
	}
}

func TestContextError(t *testing.T) {
	t.Parallel()
	// nil context returns nil
	if err := contextError(nil); err != nil {
		t.Errorf("contextError(nil) = %v, want nil", err)
	}
}

func TestPolicyConfigured(t *testing.T) {
	tests := []struct {
		name   string
		policy RedactionPolicy
		want   bool
	}{
		{"empty", RedactionPolicy{}, false},
		{"configured only", RedactionPolicy{Configured: true}, false},
		{"with patterns", RedactionPolicy{Configured: true, Patterns: []string{"x"}}, true},
		{"with keys", RedactionPolicy{Configured: true, KeyNames: []string{"y"}}, true},
		{"with classifier", RedactionPolicy{Configured: true, Classifier: func([]byte) error { return nil }}, true},
	}
	for _, tt := range tests {
		if got := policyConfigured(tt.policy); got != tt.want {
			t.Errorf("policyConfigured(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSanitizeSourcePayload(t *testing.T) {
	t.Parallel()
	principal, err := NewPrincipal("ws", "s", "sub")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("unconfigured policy stores hash only", func(t *testing.T) {
		result, err := SanitizeSourcePayload(nil, principal, []byte("hello"), RedactionPolicy{})
		if err != nil {
			t.Fatal(err)
		}
		if !result.HashOnly {
			t.Error("unconfigured policy should produce hash-only")
		}
		if result.Dereferenceable {
			t.Error("unconfigured policy should not be dereferenceable")
		}
	})

	t.Run("configured clean policy stores bytes", func(t *testing.T) {
		result, err := SanitizeSourcePayload(nil, principal, []byte("hello"), RedactionPolicy{
			Configured: true, Patterns: []string{"never-match"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.HashOnly {
			t.Error("clean configured policy should not be hash-only")
		}
		if !result.Dereferenceable {
			t.Error("clean configured policy should be dereferenceable")
		}
		if string(result.Bytes) != "hello" {
			t.Errorf("bytes = %q, want %q", string(result.Bytes), "hello")
		}
	})

	t.Run("flagged payload with redactor", func(t *testing.T) {
		result, err := SanitizeSourcePayload(nil, principal, []byte("secret=abc"), RedactionPolicy{
			Configured: true,
			Patterns:   []string{"secret=\\w+"},
			Redactor:   func(d []byte) []byte { return []byte("[REDACTED]") },
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.HashOnly {
			t.Error("redacted clean should not be hash-only")
		}
	})

	t.Run("invalid principal rejected", func(t *testing.T) {
		_, err := SanitizeSourcePayload(nil, Principal{}, []byte("hello"), RedactionPolicy{})
		if err == nil {
			t.Fatal("expected error for unbound principal")
		}
	})

	t.Run("invalid UTF-8 rejected", func(t *testing.T) {
		_, err := SanitizeSourcePayload(nil, principal, []byte("hello\xffworld"), RedactionPolicy{})
		if err == nil {
			t.Fatal("expected error for invalid UTF-8")
		}
	})

	t.Run("large payload accepted for chunking", func(t *testing.T) {
		// SourceEventBytes is chunk size, not whole-payload reject.
		SetLimits(Limits{SourceEventBytes: 10})
		defer SetLimits(DefaultLimits())
		got, err := SanitizeSourcePayload(nil, principal, []byte(strings.Repeat("x", 11)), RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
		if err != nil {
			t.Fatalf("large payload must not be rejected at sanitize: %v", err)
		}
		if got.Ref.Size != 11 {
			t.Fatalf("size = %d, want 11", got.Ref.Size)
		}
	})
}

func TestRedactSourcePayload(t *testing.T) {
	tests := []struct {
		name    string
		policy  RedactionPolicy
		data    []byte
		wantSto bool
	}{
		{"unconfigured", RedactionPolicy{}, []byte("hello"), false},
		{"clean pattern", RedactionPolicy{Configured: true, Patterns: []string{"x"}}, []byte("hello"), true},
		{"dirty no redactor", RedactionPolicy{Configured: true, Patterns: []string{"hello"}}, []byte("hello"), false},
		{"dirty with redactor that cleans", RedactionPolicy{Configured: true, Patterns: []string{"hello"}, Redactor: func(d []byte) []byte { return []byte("goodbye") }}, []byte("hello"), true},
		{"dirty with redactor that fails", RedactionPolicy{Configured: true, Patterns: []string{"."}, Redactor: func(d []byte) []byte { return d }}, []byte("hello"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, storable := redactSourcePayload(tt.data, tt.policy)
			if storable != tt.wantSto {
				t.Errorf("redactSourcePayload() storable = %v, want %v", storable, tt.wantSto)
			}
		})
	}
}
