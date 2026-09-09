// validation_fanout_test.go pins the fan-out pre-flight rejection's
// message: it must name the actual batch size and the actual configured
// limit, not a generic "exceeds fan-out limit" a caller cannot act on
// without re-deriving both numbers itself.
package coordinator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestValidateTasksFanoutRejectionNamesCountAndLimit is the regression: a
// caller must see the batch size it sent and the configured cap it
// exceeded, straight in the error, so it can immediately split the batch
// without guessing.
func TestValidateTasksFanoutRejectionNamesCountAndLimit(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "instant", &instantHandler{})
	p := subagents.New(d, subagents.Policy{Workers: 1, MaxFanout: 3})
	c := New(ledger.NewMemoryLedgerRepository(), p)

	tasks := make([]subagents.Task, 5)
	for i := range tasks {
		tasks[i] = subagents.Task{ID: fmt.Sprintf("t%d", i), Name: "instant"}
	}
	err := c.validateTasks(tasks)
	if err == nil {
		t.Fatal("expected a fan-out rejection for 5 tasks against a limit of 3")
	}
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("error %q does not name the batch size (5)", err.Error())
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("error %q does not name the configured limit (3)", err.Error())
	}
}

// TestValidateTasksFanoutAcceptsExactLimit is the boundary counterweight: a
// batch exactly at the limit must not be rejected.
func TestValidateTasksFanoutAcceptsExactLimit(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "instant", &instantHandler{})
	p := subagents.New(d, subagents.Policy{Workers: 1, MaxFanout: 3})
	c := New(ledger.NewMemoryLedgerRepository(), p)

	tasks := make([]subagents.Task, 3)
	for i := range tasks {
		tasks[i] = subagents.Task{ID: fmt.Sprintf("t%d", i), Name: "instant"}
	}
	if err := c.validateTasks(tasks); err != nil {
		t.Fatalf("validateTasks at exactly the limit: %v, want nil", err)
	}
}

// TestSpawnFanoutRejectionSurfacesFromExecute proves the message reaches
// the actual dispatch_tasks caller, end to end, not just validateTasks in
// isolation.
func TestSpawnFanoutRejectionSurfacesFromExecute(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "instant", &instantHandler{})
	p := subagents.New(d, subagents.Policy{Workers: 1, MaxFanout: 2})
	c := New(ledger.NewMemoryLedgerRepository(), p)

	_, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t0", Name: "instant"},
		{ID: "t1", Name: "instant"},
		{ID: "t2", Name: "instant"},
	}, "")
	if err == nil {
		t.Fatal("expected a fan-out rejection for 3 tasks against a limit of 2")
	}
	if !strings.Contains(err.Error(), "3") || !strings.Contains(err.Error(), "2") {
		t.Fatalf("Spawn error %q does not name both the batch size and the limit", err.Error())
	}
}
