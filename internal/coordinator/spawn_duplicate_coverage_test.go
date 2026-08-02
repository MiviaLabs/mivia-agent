package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// duplicateKeyedCreateRepo makes recovery report the key as not-found while
// CreateRun refuses the same key with ErrDuplicate - the exact shape of a
// replayed idempotency index that still maps K to a run recovery could not
// resolve. createAndStartRun must consult recovery again on the duplicate
// (spawn.go:49-56) rather than surfacing a bare 'create run: duplicate' error.
type duplicateKeyedCreateRepo struct {
	*ledger.MemoryLedgerRepository
}

func (duplicateKeyedCreateRepo) GetRunByIdempotencyKey(context.Context, string) (ledger.RunSnapshot, error) {
	return ledger.RunSnapshot{}, ledger.ErrNotFound
}

func (duplicateKeyedCreateRepo) CreateRun(context.Context, string, ledger.RunSnapshot) error {
	return ledger.ErrDuplicate
}

// TestCreateAndStartRunConsultsRecoveryOnDuplicateKey pins spawn.go:50: when
// CreateRun refuses a keyed run with ErrDuplicate, createAndStartRun must
// consult idempotency recovery before giving up, so a concurrent winner's run
// can be deduped onto instead of surfacing a raw duplicate error.
func TestCreateAndStartRunConsultsRecoveryOnDuplicateKey(t *testing.T) {
	repo := &duplicateKeyedCreateRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)

	h, err := c.Spawn(context.Background(), []subagents.Task{idempotencyTask()}, "K")
	if err == nil {
		t.Fatalf("Spawn returned handle %v, want a duplicate-key error", h)
	}
	if !errors.Is(err, ledger.ErrDuplicate) {
		t.Fatalf("Spawn error = %v, want it to wrap %v", err, ledger.ErrDuplicate)
	}
}
