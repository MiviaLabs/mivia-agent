package delivery

import "testing"

// TestIsRemotePushRejection_NilErrIsFalse pins the nil-error guard
// directly.
func TestIsRemotePushRejection_NilErrIsFalse(t *testing.T) {
	if isRemotePushRejection(nil) {
		t.Fatal("isRemotePushRejection(nil) = true, want false")
	}
}
