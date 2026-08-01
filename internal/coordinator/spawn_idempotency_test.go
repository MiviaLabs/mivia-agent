package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func newIdempotencyCoordinator(repo ledger.LedgerRepository) Coordinator {
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)}); err != nil {
		panic(err)
	}
	return New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
}

func idempotencyTask() subagents.Task {
	return subagents.Task{
		ID: "task-1", Name: "worker", Input: json.RawMessage(`"requested work"`),
		Timeout: time.Second, Budget: 7, Scope: "scope", Permission: "permission",
		AgentName: "worker", AgentDigest: "sha256:agent-v1", Skill: "audit",
		ProviderName: "deepseek", Model: "deepseek-v4-flash",
	}
}

func callerContext(sessionID, turnID, role string) context.Context {
	return runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: sessionID, TurnID: turnID, Role: role})
}

func TestSpawnFingerprintIgnoresCallerIdentity(t *testing.T) {
	base := idempotencyTask()
	want, err := requestFingerprint([]subagents.Task{base})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		mutate func(*subagents.Task)
	}{
		{"session", func(task *subagents.Task) { task.SessionID = "session-b" }},
		{"turn", func(task *subagents.Task) { task.TurnID = "turn:2" }},
		{"role", func(task *subagents.Task) { task.Role = "other" }},
		{"depth", func(task *subagents.Task) { task.Depth = 3 }},
		{"owner", func(task *subagents.Task) { task.Owner = "other-owner" }},
		{"invocation key", func(task *subagents.Task) { task.InvocationKey = "invocation" }},
		{"idempotency key", func(task *subagents.Task) { task.IdempotencyKey = "task-key" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			task := base
			tc.mutate(&task)
			got, err := requestFingerprint([]subagents.Task{task})
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("fingerprint changed for caller identity: got %q, want %q", got, want)
			}
		})
	}
}

func TestSpawnFingerprintCoversRequestedWork(t *testing.T) {
	base := idempotencyTask()
	want, err := requestFingerprint([]subagents.Task{base})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		mutate func(*subagents.Task)
	}{
		{"id", func(task *subagents.Task) { task.ID = "task-2" }},
		{"name", func(task *subagents.Task) { task.Name = "other-worker" }},
		{"dependencies", func(task *subagents.Task) { task.DependsOn = []string{"task-1"} }},
		{"input", func(task *subagents.Task) { task.Input = json.RawMessage(`"other work"`) }},
		{"timeout", func(task *subagents.Task) { task.Timeout = 2 * time.Second }},
		{"budget", func(task *subagents.Task) { task.Budget = 8 }},
		{"scope", func(task *subagents.Task) { task.Scope = "other-scope" }},
		{"permission", func(task *subagents.Task) { task.Permission = "other-permission" }},
		{"agent name", func(task *subagents.Task) { task.AgentName = "other-agent" }},
		{"agent digest", func(task *subagents.Task) { task.AgentDigest = "sha256:agent-v2" }},
		{"skill", func(task *subagents.Task) { task.Skill = "other-skill" }},
		{"provider", func(task *subagents.Task) { task.ProviderName = "zai" }},
		{"model", func(task *subagents.Task) { task.Model = "glm-5.2" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			task := base
			tc.mutate(&task)
			got, err := requestFingerprint([]subagents.Task{task})
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("fingerprint did not change for requested work")
			}
		})
	}
}

func TestSpawnIdempotencyKeyDedupesAcrossTurns(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository())
	task := idempotencyTask()
	task.SessionID, task.TurnID, task.Role, task.Depth = "session-a", "turn:1", "role", 1
	first, err := c.Spawn(callerContext("session-a", "turn:1", "role"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	nextTurn := task
	nextTurn.TurnID = "turn:2"
	second, err := c.Spawn(callerContext("session-a", "turn:2", "role"), []subagents.Task{nextTurn}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same work on a later turn did not reuse its run")
	}
}

func TestSpawnConflictStillReportedForDifferentWork(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository())
	ctx := callerContext("session-a", "turn:1", "role")
	if _, err := c.Spawn(ctx, []subagents.Task{idempotencyTask()}, "key"); err != nil {
		t.Fatal(err)
	}
	task := idempotencyTask()
	task.Input = json.RawMessage(`"different work"`)
	if _, err := c.Spawn(ctx, []subagents.Task{task}, "key"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different work error = %v, want %v", err, ErrIdempotencyConflict)
	}
}

func TestSpawnIdempotencyScopeIsThePrincipal(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository())
	task := idempotencyTask()
	first, err := c.Spawn(callerContext("session-a", "turn:1", "role-a"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	samePrincipal, err := c.Spawn(callerContext("session-a", "turn:2", "role-a"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if first != samePrincipal {
		t.Fatal("same principal did not reuse its run")
	}
	otherRole, err := c.Spawn(callerContext("session-a", "turn:3", "role-b"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if otherRole == first {
		t.Fatal("different role reused another principal's run")
	}
}

func TestSpawnForeignPrincipalGetsANewRun(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository())
	task := idempotencyTask()
	owner, err := c.Spawn(callerContext("session-a", "turn:1", "role"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := c.Spawn(callerContext("session-b", "turn:1", "role"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if foreign == owner {
		t.Fatal("foreign principal reused the owner's run")
	}
}

func TestSpawnForeignPrincipalIsIndistinguishableFromFirstUse(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository())
	task := idempotencyTask()
	owner, err := c.Spawn(callerContext("session-a", "turn:1", "role"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := c.Spawn(callerContext("session-b", "turn:1", "role"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := c.Spawn(callerContext("session-b", "turn:1", "role"), []subagents.Task{task}, "unused-key")
	if err != nil {
		t.Fatal(err)
	}
	if foreign == owner || foreign == fresh {
		t.Fatal("foreign key use did not create an independent run")
	}
}

func TestSpawnKeyNamespaceIsUnambiguous(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository())
	task := idempotencyTask()
	first, err := c.Spawn(callerContext("session", "turn:1", "x"), []subagents.Task{task}, "y:z")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Spawn(callerContext("session", "turn:1", "x:y"), []subagents.Task{task}, "z")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("ambiguous key namespace reused a run")
	}
}

func TestSpawnScopeInputIsUnambiguous(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository())
	task := idempotencyTask()
	first, err := c.Spawn(callerContext("a:b", "turn:1", "c"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Spawn(callerContext("a", "turn:1", "b:c"), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("ambiguous caller fields reused a run")
	}
}

func TestSpawnWithoutCallerIdentityKeepsSharedScope(t *testing.T) {
	c := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository())
	task := idempotencyTask()
	first, err := c.Spawn(context.Background(), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Spawn(runtime.ContextWithCaller(context.Background(), runtime.Caller{}), []subagents.Task{task}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("missing caller identity did not retain the shared compatibility scope")
	}
	firstUnkeyed, err := c.Spawn(context.Background(), []subagents.Task{task}, "")
	if err != nil {
		t.Fatal(err)
	}
	secondUnkeyed, err := c.Spawn(context.Background(), []subagents.Task{task}, "")
	if err != nil {
		t.Fatal(err)
	}
	if firstUnkeyed == secondUnkeyed {
		t.Fatal("unkeyed spawns unexpectedly shared a run")
	}
}
