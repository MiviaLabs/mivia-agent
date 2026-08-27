package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolateHomeRedirectsAndRestores(t *testing.T) {
	before, hadBefore := os.LookupEnv("HOME")
	cleanup, err := IsolateHome()
	if err != nil {
		t.Fatalf("IsolateHome: %v", err)
	}
	during := os.Getenv("HOME")
	if during == "" {
		t.Fatal("isolated HOME is empty")
	}
	if hadBefore && during == before {
		t.Fatalf("HOME was not redirected: still %q", during)
	}
	if !strings.Contains(filepath.Base(during), "mivia-test-home-") {
		t.Fatalf("isolated HOME = %q, want a mivia-test-home- temp dir", during)
	}
	if info, err := os.Stat(during); err != nil || !info.IsDir() {
		t.Fatalf("isolated HOME is not a directory: %v", err)
	}

	cleanup()

	after, hadAfter := os.LookupEnv("HOME")
	if hadBefore != hadAfter || after != before {
		t.Fatalf("HOME not restored: before=%q(%v) after=%q(%v)", before, hadBefore, after, hadAfter)
	}
	if _, err := os.Stat(during); !os.IsNotExist(err) {
		t.Fatalf("isolated home directory survived cleanup: %v", err)
	}
}

// TestIsolateHomeSetsXDGDirs proves the XDG variables follow HOME, so a
// resolver that prefers them cannot escape back to the real machine state.
func TestIsolateHomeSetsXDGDirs(t *testing.T) {
	cleanup, err := IsolateHome()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	home := os.Getenv("HOME")
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		got := os.Getenv(key)
		if got == "" {
			t.Fatalf("%s is unset inside an isolated home", key)
		}
		if !strings.HasPrefix(got, home) {
			t.Fatalf("%s = %q, want it under the isolated home %q", key, got, home)
		}
	}
}

// TestIsolateHomeCleanupIsIdempotent: a deferred cleanup that also runs from
// TestMain must not panic or restore twice.
func TestIsolateHomeCleanupIsIdempotent(t *testing.T) {
	cleanup, err := IsolateHome()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	cleanup()
}
