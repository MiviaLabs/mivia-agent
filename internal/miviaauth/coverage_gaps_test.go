package miviaauth

import (
	"strings"
	"testing"
)

// TestVersionedAPIPath_UnparsableURLIsNotVersioned pins url.Parse's own
// error branch: a URL Go's parser rejects outright is reported as
// not-versioned rather than propagating the parse error (there is nothing
// useful to do with it here - NewClient's own HTTPS validation catches a
// genuinely bad URL next).
func TestVersionedAPIPath_UnparsableURLIsNotVersioned(t *testing.T) {
	_, _, ok := versionedAPIPath("http://[::1]:namedport")
	if ok {
		t.Fatal("versionedAPIPath reported a URL it could not even parse as versioned")
	}
}

// TestDefaultService_InvalidServerURLSurfaces pins DefaultService's
// NewClient-failure wrap: an invalid MIVIA_API_BASE_URL override must fail
// DefaultService with a wrapped error, not a nil Service.
func TestDefaultService_InvalidServerURLSurfaces(t *testing.T) {
	t.Setenv("MIVIA_API_BASE_URL", "not a url at all")
	if _, err := DefaultService(); err == nil {
		t.Fatal("DefaultService accepted an invalid server URL override")
	} else if !strings.Contains(err.Error(), "default service") {
		t.Fatalf("err = %v, want the default-service wrap", err)
	}
}
