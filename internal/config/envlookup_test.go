package config

import (
	"os"
	"path/filepath"
	"testing"

	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
)

func TestLookup(t *testing.T) {

	t.Run("nil file map, no env", func(t *testing.T) {
		v, ok := Lookup("MIVIA_TEST_LOOKUP_NONEXISTENT_XYZ", nil)
		if ok {
			t.Errorf("Lookup() = %q, true, want false", v)
		}
	})

	t.Run("file map hit", func(t *testing.T) {
		v, ok := Lookup("MIVIA_TEST_LOOKUP_NONEXISTENT_XYZ", map[string]string{"MIVIA_TEST_LOOKUP_NONEXISTENT_XYZ": "from-file"})
		if !ok || v != "from-file" {
			t.Errorf("Lookup() = %q, %v, want from-file, true", v, ok)
		}
	})

	t.Run("file map empty string falls through", func(t *testing.T) {
		v, ok := Lookup("MIVIA_TEST_LOOKUP_NONEXISTENT_XYZ", map[string]string{"MIVIA_TEST_LOOKUP_NONEXISTENT_XYZ": ""})
		if ok && v != "" {
			t.Errorf("Lookup() = %q, %v, want empty or false", v, ok)
		}
	})

	t.Run("env var takes priority", func(t *testing.T) {
		t.Setenv("MIVIA_TEST_LOOKUP_PRIORITY_XYZ", "from-env")
		v, ok := Lookup("MIVIA_TEST_LOOKUP_PRIORITY_XYZ", map[string]string{"MIVIA_TEST_LOOKUP_PRIORITY_XYZ": "from-file"})
		if !ok || v != "from-env" {
			t.Errorf("Lookup() = %q, %v, want from-env, true", v, ok)
		}
	})

	t.Run("empty env falls to file", func(t *testing.T) {
		t.Setenv("MIVIA_TEST_LOOKUP_EMPTY_XYZ", "")
		v, ok := Lookup("MIVIA_TEST_LOOKUP_EMPTY_XYZ", map[string]string{"MIVIA_TEST_LOOKUP_EMPTY_XYZ": "from-file"})
		if !ok || v != "from-file" {
			t.Errorf("Lookup() = %q, %v, want from-file, true", v, ok)
		}
	})
}

func TestLookupEnvEmptyFallback(t *testing.T) {
	// Set env to empty, no file - should return empty value from env.
	key := "MIVIA_TEST_LOOKUP_EMPTYFALLBACK_XYZ"
	os.Setenv(key, "")
	defer os.Unsetenv(key)

	v, ok := Lookup(key, nil)
	// The function returns (v, true) if the env key exists even with empty
	// value on the second pass.
	if !ok {
		t.Errorf("Lookup() = %q, %v, want true for empty env var", v, ok)
	}
}

// TestLookupSDKLoadRoundTrip writes a dotenv file through the SDK loader and
// confirms the wrapper can read what the SDK returned, proving the two halves
// (Load and Lookup) compose.
func TestLookupSDKLoadRoundTrip(t *testing.T) {
	t.Run("sdk load round-trips through wrapper", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		if err := os.WriteFile(path, []byte("K=v\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := sdkenvfile.Load(path)
		if err != nil {
			t.Fatalf("sdkenvfile.Load: %v", err)
		}
		v, ok := Lookup("K", m)
		if !ok || v != "v" {
			t.Errorf("Lookup(K, m) = %q, %v, want \"v\", true", v, ok)
		}
	})
}
