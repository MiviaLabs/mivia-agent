package subagents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

type validationCoverageHandler struct{}

func (validationCoverageHandler) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func TestPoolLimitAccessorsAndNilValidation(t *testing.T) {
	pool := New(nil, Policy{MaxFanout: 7, MaxDepth: 3, MaxBudget: 99, Timeout: 2 * time.Second})
	if pool.MaxFanout() != 7 || pool.MaxDepth() != 3 || pool.MaxBudget() != 99 || pool.Timeout() != 2*time.Second {
		t.Fatalf("pool limits = fanout %d depth %d budget %d timeout %s", pool.MaxFanout(), pool.MaxDepth(), pool.MaxBudget(), pool.Timeout())
	}
	if err := pool.ValidateTask(Task{}); err == nil || !strings.Contains(err.Error(), "nil subagent pool") {
		t.Fatalf("nil-dispatcher pool validation = %v", err)
	}
	var nilPool *Pool
	if err := nilPool.ValidateTask(Task{}); err == nil || !strings.Contains(err.Error(), "nil subagent pool") {
		t.Fatalf("nil pool validation = %v", err)
	}
}

func TestPoolValidateTaskDelegatesToDispatcher(t *testing.T) {
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", validationCoverageHandler{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	pool := New(dispatcher, Policy{})
	if err := pool.ValidateTask(Task{ID: "task-1", Name: "worker", Budget: 1}); err != nil {
		t.Fatalf("valid task error = %v", err)
	}
	if err := pool.ValidateTask(Task{ID: "task-2", Name: "missing"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("missing handler error = %v", err)
	}
}
