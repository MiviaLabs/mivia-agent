package agent

import (
	"strings"
	"testing"
)

// EXPECTED_NOTICE is the fixed model-visible marker a duplicate delivery must
// carry in place of the recorded body (Wave C). The construction-path
// coverage (buildExecResult's legacy unit tests) is gone with the legacy
// engine; the SDK path's equivalent behavior is pinned in
// sdk_duplicate_test.go. This file keeps only the shared constant's own
// invariants.
const EXPECTED_NOTICE = "note: duplicate delivery suppressed"

// TestDuplicateNoticeSizeBounded pins the future production constant's size:
// the notice is a short fixed marker, far below the 1 KiB bound, so it can
// never itself be truncated. GREEN by construction.
func TestDuplicateNoticeSizeBounded(t *testing.T) {
	if len(EXPECTED_NOTICE) >= 1024 {
		t.Fatalf("EXPECTED_NOTICE is %d bytes, want < 1024 so the notice never needs truncation", len(EXPECTED_NOTICE))
	}
}

// TestDuplicateDeliveryNoticeSizeBounded pins the production constant itself
// (TestDuplicateNoticeSizeBounded above pins only the assertion prefix): the
// full notice must stay under 1 KiB so it can never itself be truncated, and
// must start with the literal the RED tests assert on.
func TestDuplicateDeliveryNoticeSizeBounded(t *testing.T) {
	if len(duplicateDeliveryNotice) >= 1024 {
		t.Fatalf("duplicateDeliveryNotice is %d bytes, want < 1024 so the notice never needs truncation", len(duplicateDeliveryNotice))
	}
	if !strings.HasPrefix(duplicateDeliveryNotice, EXPECTED_NOTICE) {
		t.Fatalf("duplicateDeliveryNotice %q does not start with the asserted prefix %q", duplicateDeliveryNotice, EXPECTED_NOTICE)
	}
}
