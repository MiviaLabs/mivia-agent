package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSubagentStoreBackendResolution pins [subagents] store_backend
// normalization and validation at load (regression for
// cfg-subagents-store-backend-unvalidated). Before the fix, non-canonical
// spellings such as "SQLite" loaded unvalidated and survived to the CLI,
// where exact "sqlite" equality checks silently selected the in-memory
// backend and lost orchestration history on process exit with no error.
func TestLoadSubagentStoreBackendResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		name                 string
		subagents            string // [subagents] TOML body; "" = no section
		want                 string
		wantErr              string
		wantDefaultStorePath bool
	}{
		{name: "absent key defaults to memory", subagents: "", want: "memory"},
		{name: "memory is canonical", subagents: "store_backend = \"memory\"\n", want: "memory"},
		{name: "sqlite is canonical", subagents: "store_backend = \"sqlite\"\n", want: "sqlite", wantDefaultStorePath: true},
		{name: "SQLite normalizes to sqlite", subagents: "store_backend = \"SQLite\"\n", want: "sqlite", wantDefaultStorePath: true},
		{name: "sqlite with surrounding spaces normalizes", subagents: "store_backend = \" sqlite \"\n", want: "sqlite", wantDefaultStorePath: true},
		{name: "invalid value fails the load", subagents: "store_backend = \"bogus\"\n", wantErr: "[subagents] store_backend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "mivia.toml")
			body := "[provider]\nname = \"deepseek\"\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n[chat]\nmax_tokens = 8192\n"
			if tt.subagents != "" {
				body += "[subagents]\n" + tt.subagents
			}
			if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := Load(LoadOptions{ConfigPath: cfg})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Subagents.StoreBackend != tt.want {
				t.Fatalf("Subagents.StoreBackend = %q, want %q", res.Subagents.StoreBackend, tt.want)
			}
			if res.StoreBackend != tt.want {
				t.Fatalf("Resolved.StoreBackend = %q, want %q", res.StoreBackend, tt.want)
			}
			if tt.wantDefaultStorePath {
				if res.StorePath != defaultStorePath() {
					t.Fatalf("StorePath = %q, want default %q", res.StorePath, defaultStorePath())
				}
				if res.StorePathSet {
					t.Fatal("StorePathSet must stay false when store_path is absent")
				}
			} else if res.StorePath != "" {
				t.Fatalf("StorePath = %q, want empty for backend %q", res.StorePath, tt.want)
			}
		})
	}
}
