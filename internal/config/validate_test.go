package config

import "testing"

// TestResolvedValidateRejectsEmptyRequiredFields pins each of Validate's
// required-field checks: provider name, model, base_url, and api_key_env
// must each independently reject an empty value.
func TestResolvedValidateRejectsEmptyRequiredFields(t *testing.T) {
	base := Resolved{ProviderName: "deepseek", Model: "model", BaseURL: "https://example.test", APIKeyEnv: "KEY"}
	tests := []struct {
		name   string
		mutate func(*Resolved)
		want   string
	}{
		{"empty provider name", func(r *Resolved) { r.ProviderName = "" }, "provider name is empty"},
		{"empty model", func(r *Resolved) { r.Model = "" }, "model is empty"},
		{"empty base_url", func(r *Resolved) { r.BaseURL = "" }, "base_url is empty"},
		{"empty api_key_env", func(r *Resolved) { r.APIKeyEnv = "" }, "api_key_env is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := base
			tt.mutate(&res)
			err := res.Validate()
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Validate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestValidEnvNameRejectsOutOfRangeAndBadFirstChar pins the two failure
// branches of validEnvName that TestResolvedValidateRejectsUnsafeAPIKeyEnvironmentName
// does not reach: a name over the 128-byte cap, and a name whose first
// character is not a letter or underscore (digits are valid afterward, but
// not as the first character).
func TestValidEnvNameRejectsOutOfRangeAndBadFirstChar(t *testing.T) {
	tooLong := ""
	for i := 0; i < 129; i++ {
		tooLong += "A"
	}
	tests := []struct {
		name string
		env  string
	}{
		{"over length cap", tooLong},
		{"digit as first character", "1FOO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if validEnvName(tt.env) {
				t.Fatalf("validEnvName(%q) = true, want false", tt.env)
			}
		})
	}
}
