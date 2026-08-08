package cli

import (
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// A binary upgrade that adds a field to the definition types changes the
// compiled digest, because that digest is a hash of the marshalled Go struct.
// The workflow text does not change by one byte. Resume must still work: it is
// the recovery path, and an upgrade is exactly what it must survive.
//
// This reproduces the stranding directly. The run and its snapshot carry the
// digest the ADMITTING binary computed. The current binary computes a
// different one for the same text. Before the fix, resume refused and every
// in-flight run was lost on upgrade.
func TestResumeSurvivesADigestChangeFromABinaryUpgrade(t *testing.T) {
	run, raw := deliveryAgreementFixture(t, "draft")
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}

	// Stand in for the admitting binary: both recorded digests hold a value
	// this binary no longer computes for the same definition text.
	const admittedByAnOlderBinary = "61a3dfcbd998841387f09d52dac98a5e91b979de95489a6144de02ffb87ee469"
	snapshot.DefinitionDigest = admittedByAnOlderBinary
	rawOld, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run.WorkflowDigest = admittedByAnOlderBinary
	run.SnapshotDigest = workflowledger.SnapshotDigest(rawOld)

	if _, _, _, err := validateWorkflowResumeSnapshot(run, rawOld); err != nil {
		t.Fatalf("resume after a binary upgrade must work: %v", err)
	}
}

// The two recorded digests must still agree with each other. A run row paired
// with a snapshot from a different admission is a real mismatch and must fail.
func TestResumeRefusesARunPairedWithAnotherAdmissionsSnapshot(t *testing.T) {
	run, raw := deliveryAgreementFixture(t, "draft")
	run.WorkflowDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	_, _, _, err := validateWorkflowResumeSnapshot(run, raw)
	if err == nil || !strings.Contains(err.Error(), "does not match the admitted definition") {
		t.Fatalf("mismatched admission error = %v, want a definition mismatch", err)
	}
}

// The definition text itself stays pinned. The snapshot digest covers the
// stored TOML, so text that was altered after admission still fails.
func TestResumeRefusesAnAlteredDefinitionText(t *testing.T) {
	run, raw := deliveryAgreementFixture(t, "draft")
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.DefinitionTOML = append(snapshot.DefinitionTOML, []byte("\n# altered after admission\n")...)
	tampered, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// run.SnapshotDigest still pins the ORIGINAL snapshot bytes.
	if _, _, _, err := validateWorkflowResumeSnapshot(run, tampered); err == nil {
		t.Fatal("altered definition text must fail the snapshot digest check")
	}
}
