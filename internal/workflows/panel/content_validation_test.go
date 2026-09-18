package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// These tests drive ValidateTaskContent's individual failure branches with
// specs that pass Validate() and content that matches its digests, so each
// later stage fails on its own: digest mismatch, unparsable schema, a schema
// that will not compile, input that fails its schema, and a coordinator
// fingerprint forged for another task ID.

// shaHex is the digest format the ledger stores alongside content refs.
func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func storedTask(t *testing.T, inputJSON, schemaJSON, taskID string) (ledger.PanelTaskSpec, *ledger.StorageRepository) {
	t.Helper()
	work := panelTaskWithID(t, "content", taskID)
	work.InputRef = "ref:input:c"
	work.InputDigest = shaHex(inputJSON)
	work.InputSchemaRef = "ref:schema:c"
	work.InputSchemaDigest = shaHex(schemaJSON)
	work.OutputSchemaRef = "ref:schema:out"
	work.OutputSchemaDigest = shaHex(schemaJSON)
	work.WorkFingerprint = work.WorkFingerprintValue()
	repo := newMemoryRepo(t)
	for ref, data := range map[string]string{
		work.InputRef:        inputJSON,
		work.InputSchemaRef:  schemaJSON,
		work.OutputSchemaRef: schemaJSON,
	} {
		if err := repo.StoreContent(context.Background(), ref, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	return work, repo
}

func TestValidateTaskContentRejectsSwappedContent(t *testing.T) {
	work, repo := storedTask(t, `{"input":"x"}`, `{"type":"object"}`, "task")
	if err := repo.StoreContent(context.Background(), work.InputRef, []byte(`{"input":"y"}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTaskContent(context.Background(), repo, "run", "task", work); err == nil {
		t.Fatal("swapped input content must fail the digest check")
	}
}

func TestValidateTaskContentRejectsUnparsableSchema(t *testing.T) {
	work, repo := storedTask(t, `{"input":"x"}`, `not json`, "task")
	if err := ValidateTaskContent(context.Background(), repo, "run", "task", work); err == nil {
		t.Fatal("unparsable schema must fail")
	}
}

func TestValidateTaskContentRejectsUncompilableSchema(t *testing.T) {
	work, repo := storedTask(t, `{"input":"x"}`, `{"type": 12}`, "task")
	if err := ValidateTaskContent(context.Background(), repo, "run", "task", work); err == nil {
		t.Fatal("uncompilable schema must fail")
	}
}

func TestValidateTaskContentRejectsSchemaFailingInput(t *testing.T) {
	work, repo := storedTask(t, `{}`, `{"type":"object","required":["mandatory"]}`, "task")
	if err := ValidateTaskContent(context.Background(), repo, "run", "task", work); err == nil {
		t.Fatal("schema-failing input must fail")
	}
}

func TestValidateTaskContentRejectsFingerprintFromAnotherTask(t *testing.T) {
	work, repo := storedTask(t, `{"input":"x"}`, `{"type":"object"}`, "task")
	// The fingerprint was minted for task ID "content-other"; validating under
	// a different task ID must fail the fingerprint comparison.
	work.CoordinatorRequestFingerprint = fmt.Sprintf("sha256:%s", shaHex("other"))
	if err := ValidateTaskContent(context.Background(), repo, "run", "task", work); err == nil {
		t.Fatal("mismatched coordinator fingerprint must fail")
	}
}
