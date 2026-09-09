package conversation

// crossNamespaceSuffixIndex's own boundary contract: which ids carry a
// namespace at all, and which rows are eligible to be matched by suffix.
// The happy path (a live PROGRESS update reaching a differently-namespaced
// row) is covered through the panel in filespanel_progress_test.go; these
// pin the three edges that decide whether the suffix comparison is even
// attempted, each of which is a way to attribute a subagent update to the
// WRONG row if it moves.

import "testing"

// TestCrossNamespaceSuffixIndex_ColonAtStartStillCarriesASuffix pins that
// the namespace check is "is there a colon", not "is there a non-empty
// namespace". An id like ":task-1" has an empty namespace but a real
// suffix, so it must still be eligible to match a namespaced row - the
// guard rejects only an id with NO colon, which has no suffix to compare.
func TestCrossNamespaceSuffixIndex_ColonAtStartStillCarriesASuffix(t *testing.T) {
	rows := []subagentRow{{ID: "call-a:task-1"}}
	if got := crossNamespaceSuffixIndex(rows, ":task-1"); got != 0 {
		t.Fatalf("crossNamespaceSuffixIndex(%q) = %d, want 0: an empty namespace still leaves a suffix to match", ":task-1", got)
	}
}

// TestCrossNamespaceSuffixIndex_RowWithColonAtStartIsEligible is the same
// boundary on the ROW side: a row id of ":task-1" is namespaced (its
// namespace is empty), so its suffix is comparable and it must not be
// skipped as if it had no colon at all.
func TestCrossNamespaceSuffixIndex_RowWithColonAtStartIsEligible(t *testing.T) {
	rows := []subagentRow{{ID: ":task-1"}}
	if got := crossNamespaceSuffixIndex(rows, "call-a:task-1"); got != 0 {
		t.Fatalf("crossNamespaceSuffixIndex = %d, want 0: a row whose namespace is empty is still suffix-comparable", got)
	}
}

// TestCrossNamespaceSuffixIndex_ColonlessRowIsNeverMatched pins the other
// half of the row guard: a row with NO colon carries no namespace and so
// has no suffix to compare. Matching it against a namespaced id's suffix
// would attribute a namespaced subagent's progress to an unnamespaced row
// that merely happens to be named like the bare task id.
func TestCrossNamespaceSuffixIndex_ColonlessRowIsNeverMatched(t *testing.T) {
	rows := []subagentRow{{ID: "task-1"}}
	if got := crossNamespaceSuffixIndex(rows, "call-a:task-1"); got != -1 {
		t.Fatalf("crossNamespaceSuffixIndex = %d, want -1: a colonless row has no namespace to look past", got)
	}
}
