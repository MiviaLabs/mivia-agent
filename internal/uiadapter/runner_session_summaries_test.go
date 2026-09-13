package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestListSessionSummariesHidesLegacyReservedRows pins the picker's
// legacy-row filter: a catalog row saved under a legacy reserved
// automation name ("__auto__<automation_id>__<run_id>") must not appear
// in the summaries the /resume picker shows, while an ordinary saved
// session survives the same filter.
func TestListSessionSummariesHidesLegacyReservedRows(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	store := approvalTestStore(t)
	sess := contextBoundSession(t, res, store, "picker-main")
	if err := sess.Save("__auto__auto-1__run-1"); err != nil {
		t.Fatalf("seed legacy reserved row: %v", err)
	}
	if err := sess.Save("session-normal"); err != nil {
		t.Fatalf("seed normal row: %v", err)
	}
	r := &CommandRunner{sess: sess}

	summaries, err := r.listSessionSummaries()
	if err != nil {
		t.Fatalf("listSessionSummaries: %v", err)
	}
	for _, s := range summaries {
		if s.ID == "__auto__auto-1__run-1" {
			t.Fatalf("summary %q leaked a legacy reserved automation row into the picker", s.ID)
		}
	}
	found := false
	for _, s := range summaries {
		if s.ID == "session-normal" {
			found = true
		}
	}
	if !found {
		t.Fatalf("summaries = %+v, want the normal row to survive the filter", summaries)
	}
}
