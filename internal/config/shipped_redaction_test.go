package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The recommended patterns are a shipped artifact: if they under-redact, every
// workspace that copies mivia.toml.example inherits the hole. Two real defects
// were found this way — the keyed rule consumed "Authorization: Bearer" and
// stopped, and it could not match a quoted JSON key at all, which is exactly
// the shape tool argv arrives in.
func TestShippedRedactionPatternsCoverRealCredentialShapes(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))

	for _, name := range []string{"mivia.toml", "mivia.toml.example"} {
		t.Run(name, func(t *testing.T) {
			res, err := Load(LoadOptions{ConfigPath: filepath.Join(root, ".mivia", name)})
			if err != nil {
				t.Fatal(err)
			}
			const leak = "zzz-leaked-value"
			for _, in := range []string{
				`{"path":"x","token":"zzz-leaked-value"}`,
				`{"api_key":"zzz-leaked-value"}`,
				`{"command":"curl -H 'Authorization: Bearer zzz-leaked-value'"}`,
				`{"content":"API_KEY=zzz-leaked-value"}`,
				"Authorization: Bearer zzz-leaked-value",
				"api_key=zzz-leaked-value",
				"password = zzz-leaked-value",
			} {
				if got := res.RedactionPolicy.Text(in); strings.Contains(got, leak) {
					t.Errorf("shipped patterns leak\n in: %s\nout: %s", in, got)
				}
			}
			// Ordinary text must survive: over-redaction makes previews useless.
			for _, benign := range []string{
				`{"path":"main.go","limit":100}`,
				"go test ./... ok",
			} {
				if got := res.RedactionPolicy.Text(benign); got != benign {
					t.Errorf("shipped patterns over-redact\n in: %s\nout: %s", benign, got)
				}
			}
		})
	}
}
