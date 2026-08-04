package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// EXPECTED_NOTICE is the fixed model-visible marker a duplicate delivery must
// carry in place of the recorded body (Wave C). The production constant's full
// text is decided in the prod wave; it must start with this prefix, so the
// literal below is the assertion target for the RED tests.
const EXPECTED_NOTICE = "note: duplicate delivery suppressed"

// countingSpoolStore counts every byte write the remainder spool performs, so
// the duplicate path can be pinned to never consult the spool at all: a
// duplicate's body must never be stored behind a ref the model could page
// back, because the notice already suppressed the delivery.
type countingSpoolStore struct {
	remainder.ContentStore
	stores int
}

func (c *countingSpoolStore) StoreContent(ctx context.Context, ref string, data []byte) error {
	c.stores++
	return c.ContentStore.StoreContent(ctx, ref, data)
}

// TestBuildExecResultDuplicateBodyReplacedByNotice is the RED core of Wave C:
// a duplicate delivery must reach the model as the fixed notice, never as the
// recorded body. Today buildExecResult passes the duplicate's body through
// verbatim, so the notice assertion fails and the body-absence assertion
// fails.
func TestBuildExecResultDuplicateBodyReplacedByNotice(t *testing.T) {
	reg := tools.NewRegistry()
	task := &toolTask{call: tc("call_d1", "some_tool", `{}`)}

	exec := buildExecResult(0, task, reg, Options{}, runtime.Result{
		Output:   []byte("full body"),
		Metadata: runtime.Metadata{Status: "duplicate"},
	})

	if !strings.Contains(exec.result, EXPECTED_NOTICE) {
		t.Fatalf("duplicate body = %q, want it replaced by the notice %q", exec.result, EXPECTED_NOTICE)
	}
	if strings.Contains(exec.result, "full body") {
		t.Fatalf("duplicate body still carries the recorded body verbatim: %q", exec.result)
	}
	if !strings.Contains(exec.parts.cappedBody, EXPECTED_NOTICE) {
		t.Fatalf("duplicate parts.cappedBody = %q, want the notice %q", exec.parts.cappedBody, EXPECTED_NOTICE)
	}
	if strings.Contains(exec.parts.cappedBody, "full body") {
		t.Fatalf("duplicate parts.cappedBody still carries the recorded body: %q", exec.parts.cappedBody)
	}
}

// TestBuildExecResultNonDuplicateUntouched pins the green side of the split:
// a non-duplicate result keeps its body verbatim and gains no notice.
func TestBuildExecResultNonDuplicateUntouched(t *testing.T) {
	reg := tools.NewRegistry()
	task := &toolTask{call: tc("call_c1", "some_tool", `{}`)}

	exec := buildExecResult(0, task, reg, Options{}, runtime.Result{
		Output:   []byte("real tool output"),
		Metadata: runtime.Metadata{Status: "completed"},
	})

	if !strings.Contains(exec.result, "real tool output") {
		t.Fatalf("completed body = %q, want it untouched", exec.result)
	}
	if strings.Contains(exec.result, EXPECTED_NOTICE) {
		t.Fatalf("completed body gained the duplicate notice: %q", exec.result)
	}
}

// TestBuildExecResultDuplicateOfFailureKeepsErr: a duplicate of a failed call
// must still surface the failure, and its body must carry the notice. The err
// half passes today; the body half is RED until the notice lands.
func TestBuildExecResultDuplicateOfFailureKeepsErr(t *testing.T) {
	reg := tools.NewRegistry()
	task := &toolTask{call: tc("call_f1", "some_tool", `{}`)}
	boom := errors.New("boom")

	exec := buildExecResult(0, task, reg, Options{}, runtime.Result{
		Output:   []byte("failed output"),
		Err:      boom,
		Metadata: runtime.Metadata{Status: "duplicate"},
	})

	if exec.err == nil || exec.err != boom {
		t.Fatalf("duplicate failure dropped the error: got %v, want %v", exec.err, boom)
	}
	if !strings.Contains(exec.result, EXPECTED_NOTICE) {
		t.Fatalf("duplicate failure body = %q, want it replaced by the notice %q", exec.result, EXPECTED_NOTICE)
	}
	if strings.Contains(exec.result, "failed output") {
		t.Fatalf("duplicate failure body still carries the recorded body: %q", exec.result)
	}
}

// TestBuildExecResultDuplicateNoSpoolRef: a duplicate body must never be
// spooled behind a remainder ref. Today the duplicate body passes through the
// ordinary cap path and IS spooled when it exceeds the cap, so a ref is minted
// and the store is written - both assertions are RED. After the fix the notice
// (short, under any cap) is the only thing capping could ever see, so no ref
// is minted and the spool is never consulted.
func TestBuildExecResultDuplicateNoSpoolRef(t *testing.T) {
	reg := tools.NewRegistry()
	task := &toolTask{call: tc("call_s1", "some_tool", `{}`)}
	store := &countingSpoolStore{ContentStore: remainder.NewMemoryStore()}
	opts := Options{
		MaxToolResultChars: 64,
		SessionID:          "sess",
		RemainderSpool:     remainder.NewSpool(store),
	}

	exec := buildExecResult(0, task, reg, opts, runtime.Result{
		Output:   []byte(strings.Repeat("x", 4096)),
		Metadata: runtime.Metadata{Status: "duplicate"},
	})

	if exec.parts.refA != "" {
		t.Fatalf("duplicate minted remainder ref %q; a suppressed duplicate must never be spooled under the notice", exec.parts.refA)
	}
	if store.stores != 0 {
		t.Fatalf("duplicate wrote the body to the spool %d time(s); a suppressed duplicate must never spool", store.stores)
	}
	if !strings.Contains(exec.parts.cappedBody, EXPECTED_NOTICE) {
		t.Fatalf("duplicate cappedBody = %q, want the notice %q", exec.parts.cappedBody, EXPECTED_NOTICE)
	}
}

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
