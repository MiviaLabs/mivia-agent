package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigFile writes body to path, creating the parent directory.
func writeConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadSubagentStorePathReadsExplicitKey pins the success path: an
// explicitly-set [subagents] store_path is returned verbatim with set=true,
// and no provider resolution runs for a provider-less file.
func TestLoadSubagentStorePathReadsExplicitKey(t *testing.T) {
	root := t.TempDir()
	path := writeRepoConfigAt(t, root, strings.Join([]string{
		"[workflows]",
		"",
		"[subagents]",
		"store_path = \"stores/sessions\"",
		"",
	}, "\n"))

	got, set, err := LoadSubagentStorePath(path)
	if err != nil {
		t.Fatalf("LoadSubagentStorePath: %v", err)
	}
	if !set {
		t.Fatal("set = false, want true for an explicitly-set store_path")
	}
	if got != "stores/sessions" {
		t.Fatalf("store path = %q, want the raw recorded value", got)
	}
}

// TestLoadSubagentStorePathUnsetCases covers the two "no explicit value"
// returns: a blank path argument and a config file that never sets the key.
// Both must report set=false with no error, so the caller falls through to
// the next candidate instead of adopting a default.
func TestLoadSubagentStorePathUnsetCases(t *testing.T) {
	root := t.TempDir()
	silent := writeRepoConfigAt(t, root, "[workflows]\n")
	blankValue := filepath.Join(t.TempDir(), "blank.toml")
	writeConfigFile(t, blankValue, "[subagents]\nstore_path = \"   \"\n")

	cases := []struct {
		name string
		path string
	}{
		{name: "empty path", path: ""},
		{name: "whitespace path", path: "   "},
		{name: "file without the key", path: silent},
		{name: "file with a blank value", path: blankValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, set, err := LoadSubagentStorePath(tc.path)
			if err != nil {
				t.Fatalf("LoadSubagentStorePath: %v", err)
			}
			if set {
				t.Fatalf("set = true, want false for %s", tc.name)
			}
			if got != "" {
				t.Fatalf("store path = %q, want empty when unset", got)
			}
		})
	}
}

// TestLoadSubagentStorePathReadErrorNamesPath pins the read-failure return: a
// missing file is an error naming the path, never a silent unset result that
// would hide a mistyped --config pin.
func TestLoadSubagentStorePathReadErrorNamesPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.toml")

	got, set, err := LoadSubagentStorePath(missing)
	if err == nil {
		t.Fatal("expected a read error for a missing config file")
	}
	if !strings.Contains(err.Error(), "read config") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v, want a read-config error naming %s", err, missing)
	}
	if got != "" || set {
		t.Fatalf("got = (%q, %v), want the zero result on error", got, set)
	}
}

// TestLoadSubagentStorePathDecodeErrorSurfaces pins that a corrupt file fails
// loudly rather than resolving to an unset store path.
func TestLoadSubagentStorePathDecodeErrorSurfaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.toml")
	writeConfigFile(t, path, "[subagents\n")

	if _, set, err := LoadSubagentStorePath(path); err == nil || set {
		t.Fatalf("got set=%v err=%v, want a decode error", set, err)
	}
}
