package subagents

import (
	"strings"
	"testing"
	"time"
)

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
