package cliautomations

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// staleClaimTestMaxAge mirrors internal/automation's unexported
// defaultSweepMaxAge (resume.go, 5*time.Minute) - this package cannot
// reference it directly (unexported, different package), so the
// constant is reproduced here for test data generation only. It is NOT
// used by any production code path; runRunCommand exercises the real
// defaultSweepMaxAge via automation.Service.RunOnce->admitFire
// unchanged.
const staleClaimTestMaxAge = 5 * time.Minute

// TestRunCommandRecoversFromStaleClaim is the CLI/manual-path regression
// for the confirmed stale automation claim wedge: a claim row left
// behind by a crashed prior run (no release, acquired_at well past
// automation's defaultSweepMaxAge threshold) must not permanently block
// a later `mivia automations run <id>` - the manual RunOnce path this
// slice fixes. Before the fix, this reproduced the wedge exactly:
// runRunCommand printed a RunSkipped one-line summary forever, never a
// real execution, for an automation whose only prior claim holder was
// long dead.
//
// The claim row is inserted directly against the same context store
// runRunCommand itself opens (clichat.ContextStorePath, here pinned by
// the fixture's own [subagents] store_path=".mivia/context.db" - see
// writeAutomationsFixture), using automation's own claim-key convention
// ("automation:"+id, claim.go's claimKey - unexported, so reproduced
// literally here since this test lives in a different package) rather
// than exercising automation.Service internals this package does not
// import.
func TestRunCommandRecoversFromStaleClaim(t *testing.T) {
	const id = "run-stale-claim-recovery"
	root := writeAutomationsFixture(t, id)
	dbPath := filepath.Join(root, ".mivia", "context.db")

	// Open the store once (creating its schema, including run_claims via
	// every migration storage.OpenSQLite runs - the SAME schema
	// runRunCommand's own buildService->openAutomationStore path
	// produces), then insert a claim row simulating a crashed prior
	// fire's never-released claim.
	if err := seedCrashedClaim(t, dbPath, "automation:"+id); err != nil {
		t.Fatalf("seed crashed claim: %v", err)
	}

	stdout, _ := captureOutput(t, func() {
		if err := runRunCommand([]string{"--workspace", root, id}); err != nil {
			t.Fatalf("runRunCommand against a stale claim: %v", err)
		}
	})
	if strings.Contains(stdout, "state=skipped") {
		t.Fatalf("runRunCommand stdout = %q, want a real execution (state=succeeded), not the RunSkipped no-op - the stale claim must be recovered, not treated as a permanent wedge", stdout)
	}
	if !strings.Contains(stdout, "state=succeeded") {
		t.Fatalf("runRunCommand stdout = %q, want state=succeeded", stdout)
	}
}

// TestRunCommandStillSkipsGenuinelyActiveClaim is the CLI-path
// counterpart proving the fix is scoped to stale claims only: a claim
// acquired moments ago (well under defaultSweepMaxAge) must still
// produce the documented `automations run` no-op output
// (state=skipped), not a preempted execution.
func TestRunCommandStillSkipsGenuinelyActiveClaim(t *testing.T) {
	const id = "run-fresh-claim-blocks"
	root := writeAutomationsFixture(t, id)
	dbPath := filepath.Join(root, ".mivia", "context.db")

	if err := seedFreshClaim(t, dbPath, "automation:"+id); err != nil {
		t.Fatalf("seed fresh claim: %v", err)
	}

	stdout, _ := captureOutput(t, func() {
		if err := runRunCommand([]string{"--workspace", root, id}); err != nil {
			t.Fatalf("runRunCommand against a fresh claim: %v", err)
		}
	})
	if !strings.Contains(stdout, "state=skipped") {
		t.Fatalf("runRunCommand stdout = %q, want state=skipped (a genuinely active claim must not be preempted)", stdout)
	}
}

// seedCrashedClaim opens dbPath (via storage.OpenSQLite, running every
// migration - creates run_claims/fence_generation etc. exactly as
// runRunCommand's own store-open path does), closes it, then inserts
// one run_claims row under key with acquired_at far enough in the past
// to exceed staleClaimTestMaxAge - simulating a crashed holder that
// never released its claim.
func seedCrashedClaim(t *testing.T, dbPath, key string) error {
	t.Helper()
	return seedClaimRow(t, dbPath, key, staleClaimTestMaxAge+time.Minute)
}

// seedFreshClaim is seedCrashedClaim's counterpart: the row's
// acquired_at is "now", well under the staleness threshold.
func seedFreshClaim(t *testing.T, dbPath, key string) error {
	t.Helper()
	return seedClaimRow(t, dbPath, key, 0)
}

func seedClaimRow(t *testing.T, dbPath, key string, age time.Duration) error {
	t.Helper()
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer raw.Close()
	acquiredAt := time.Now().UTC().Add(-age).Format(time.RFC3339Nano)
	_, err = raw.Exec(`INSERT INTO run_claims(run_id, holder, acquired_at, fence, fence_generation) VALUES(?, ?, ?, 1, 1)`, key, "crashed-holder-test", acquiredAt)
	return err
}
