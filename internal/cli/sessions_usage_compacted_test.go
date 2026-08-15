package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// runSessionsUsage's whole reason to exist is that the catalog's stored
// token_count "goes stale the moment compaction rewrites the conversation"
// (sessions_command.go), so it recomputes over the loaded post-compaction
// messages. Nothing tested that: the existing usage test seeds two plain
// messages and never compacts, so a regression that reported the stored
// count would have kept the suite green and the desktop app would seed its
// context indicator from a pre-compaction number.

// sessionsUsageTokens runs `sessions usage <name> --json` and returns the
// reported prompt estimate.
func sessionsUsageTokens(t *testing.T, ws, name string) float64 {
	t.Helper()
	var buf bytes.Buffer
	if err := runSessionsWithIO([]string{"usage", name, "--workspace", ws, "--json"}, &buf, &bytes.Buffer{}); err != nil {
		t.Fatalf("sessions usage %s: %v", name, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("decode sessions usage JSON: %v\nraw: %s", err, buf.String())
	}
	used, ok := raw["used_tokens"].(float64)
	if !ok {
		t.Fatalf("sessions usage JSON has no used_tokens: %s", buf.String())
	}
	return used
}

// TestSessionsUsageReflectsCompaction pins the recomputation: the same query
// run before and after a real compaction must report a smaller estimate,
// because it is computed over the loaded messages. Both readings go through
// the same rebuilt tool registry, so the difference between them is the
// conversation and nothing else - comparing against the live session's own
// ContextUsage instead would only measure the tool schemas the command adds.
func TestSessionsUsageReflectsCompaction(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()

	before := sessionsUsageTokens(t, ws, sess.SessionID)
	if before <= 0 {
		t.Fatalf("seeded session used_tokens = %v, want > 0", before)
	}
	if err := sess.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := sessionsUsageTokens(t, ws, sess.SessionID)
	if after <= 0 {
		t.Fatalf("used_tokens = %v, want > 0 for a compacted session with history", after)
	}
	if after >= before {
		t.Fatalf("sessions usage did not follow the compaction: %v -> %v", before, after)
	}
}

// TestSessionsListTokenCountIsNoEstimateForAContextSession pins why
// runSessionsUsage exists at all. `sessions list` reports token_count 0 for a
// context session - the listing's UNION arm over context_sessions has no
// per-session token total to report - so a consumer that seeds a context
// indicator from the listing shows an empty gauge for a session with real
// history. sessions usage is the only source of that number.
func TestSessionsListTokenCountIsNoEstimateForAContextSession(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()

	if err := sess.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	var listBuf bytes.Buffer
	if err := runSessionsList([]string{"--workspace", ws, "--json"}, &listBuf); err != nil {
		t.Fatalf("sessions list: %v", err)
	}
	var infos []map[string]any
	if err := json.Unmarshal(listBuf.Bytes(), &infos); err != nil {
		t.Fatalf("decode sessions list JSON: %v\nraw: %s", err, listBuf.String())
	}
	var listed float64
	var found bool
	for _, info := range infos {
		if name, _ := info["name"].(string); name == sess.SessionID {
			listed, _ = info["token_count"].(float64)
			found = true
		}
	}
	if !found {
		t.Fatalf("compacted session %s is missing from sessions list: %s", sess.SessionID, listBuf.String())
	}
	if listed != 0 {
		t.Fatalf("sessions list token_count = %v for a context session; this test and the "+
			"runSessionsUsage doc comment assume the listing carries no usable estimate", listed)
	}
	if used := sessionsUsageTokens(t, ws, sess.SessionID); used <= 0 {
		t.Fatalf("sessions usage used_tokens = %v, want the recomputed estimate the listing cannot give", used)
	}
}
