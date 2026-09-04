package memory

import "testing"

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func testEntry(title string, scope Scope) Entry {
	return Entry{Title: title, Scope: scope, Verdict: VerdictGood,
		Created: "2026-08-09", Summary: "summary of " + title,
		Why: "why " + title, Good: "- worked", Bad: "- none"}
}

func newTestStore(t *testing.T, _ string, orgID string) Store {
	t.Helper()
	store, err := Open(Config{Backend: BackendMemory, OrgID: orgID,
		MaxEntryBytes: 8192, MaxEntries: 500, MaxSearchResults: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTierTestStore(t *testing.T, backend, orgID string) Store {
	return newTestStore(t, backend, orgID)
}
