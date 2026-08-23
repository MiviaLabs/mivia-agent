package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// prepFailManager fails Prepare with the given error and never falls
// back on its own; it records the ctx each call saw.
type prepCallRecorder struct {
	mu    sync.Mutex
	ctxs  []context.Context
	fail  error
	calls int
}

func (p *prepCallRecorder) Prepare(ctx context.Context, _ contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.mu.Lock()
	p.calls++
	p.ctxs = append(p.ctxs, ctx)
	p.mu.Unlock()
	if ctx.Err() != nil {
		return contextmgr.Preparation{}, ctx.Err()
	}
	if p.fail != nil {
		return contextmgr.Preparation{}, p.fail
	}
	return contextmgr.Preparation{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "prepared"}},
	}, nil
}

func (p *prepCallRecorder) Discard(contextmgr.Preparation) {}

// TestSDKPrepareOnceRecordsPreparationErr pins that the first
// Prepare failure sets loop.PreparationErr (identity, not a wrap) so
// the turn commit can carry it, while the error still propagates -
// matching legacy prepareStep (context.go:27).
func TestSDKPrepareOnceRecordsPreparationErr(t *testing.T) {
	boom := errors.New("prep exploded")
	mgr := &prepCallRecorder{fail: boom}
	loop := &Loop{}
	opts := Options{PreparationManager: mgr}
	_, err := prepareSDKOnce(context.Background(), loop, opts, nil)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the preparation failure propagated", err)
	}
	if loop.PreparationErr != boom {
		t.Fatalf("loop.PreparationErr = %v, want the same error identity as Prepare returned", loop.PreparationErr)
	}
}

// TestSDKPrepareOnceFallbackGateMatchesLegacy pins the legacy
// fallback gate (context.go:28): an interrupted ctx on a fresh loop
// with zero WorkLimits retries Prepare once on context.Background();
// success clears PreparationErr, and the fallback ran on a live ctx.
func TestSDKPrepareOnceFallbackGateMatchesLegacy(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	mgr := &prepCallRecorder{}
	loop := &Loop{}
	opts := Options{PreparationManager: mgr}
	msgs, err := prepareSDKOnce(canceled, loop, opts, nil)
	if err != nil {
		t.Fatalf("err = %v, want the fallback Prepare to succeed", err)
	}
	if mgr.calls != 2 {
		t.Fatalf("Prepare calls = %d, want 2 (interrupted attempt plus background fallback)", mgr.calls)
	}
	fallbackCtx := mgr.ctxs[1]
	if fallbackCtx == nil || fallbackCtx.Err() != nil || fallbackCtx == canceled {
		t.Fatal("fallback Prepare did not run on a live context.Background ctx")
	}
	if loop.PreparationErr != nil {
		t.Fatalf("loop.PreparationErr = %v, want nil after a successful fallback", loop.PreparationErr)
	}
	if !loop.HasPreparation {
		t.Fatal("loop.HasPreparation = false, want the fallback preparation recorded")
	}
	if len(msgs) == 0 {
		t.Fatal("msgs empty, want the prepared history converted for the SDK")
	}
}
