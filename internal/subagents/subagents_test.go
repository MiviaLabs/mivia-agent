package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

type h struct{}

func (h) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(`{"done":true}`), nil
}
func TestPoolDependencyOrderAndDeterminism(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	_ = d.Register(runtime.Subagent, "b", h{})
	p := New(d, Policy{Workers: 2})
	got, err := p.Run(context.Background(), []Task{{ID: "b", Name: "b", DependsOn: []string{"a"}}, {ID: "a", Name: "a"}})
	if err != nil || len(got) != 2 || got[0].TaskID != "b" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPoolUsesFreshOpaqueInvocationIDs(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", handlerFunc(func(_ context.Context, req runtime.Request) (json.RawMessage, error) {
		mu.Lock()
		ids = append(ids, req.ID)
		mu.Unlock()
		return json.RawMessage(`{"done":true}`), nil
	}))
	p := New(d, Policy{Workers: 2})
	if _, err := p.Run(context.Background(), []Task{{ID: "same", Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(context.Background(), []Task{{ID: "same", Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] == "same" || ids[1] == "same" || ids[0] == ids[1] {
		t.Fatalf("runtime invocation IDs = %q, want distinct opaque IDs", ids)
	}
}
func TestPoolRejectsCycles(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	p := New(d, Policy{})
	if _, err := p.Run(context.Background(), []Task{{ID: "a", Name: "a", DependsOn: []string{"a"}}}); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestPoolRejectsInvocationKeyCollision(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	p := New(d, Policy{})
	tasks := []Task{{ID: "a", Name: "a", InvocationKey: "same"}, {ID: "b", Name: "a", InvocationKey: "same"}}
	if _, err := p.Run(context.Background(), tasks); err == nil {
		t.Fatal("invocation key collision accepted")
	}
}

func TestPoolRejectsNegativeBudgetBeforeCumulativeAccounting(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	p := New(d, Policy{MaxBudget: 3})

	_, err := p.validate([]Task{
		{ID: "negative", Budget: -2},
		{ID: "positive", Budget: 3},
		{ID: "overflow", Budget: 1},
	})
	if err == nil || err.Error() != "budget must be non-negative" {
		t.Fatalf("validate error = %v, want negative-budget rejection", err)
	}
}

func TestPoolRecordsBlockedTaskWhenDependencyFails(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return nil, context.Canceled }))
	_ = d.Register(runtime.Subagent, "next", h{})
	p := New(d, Policy{})
	got, err := p.Run(context.Background(), []Task{{ID: "next", Name: "next", DependsOn: []string{"fail"}}, {ID: "fail", Name: "fail"}})
	if err != nil || got[0].Status != "blocked" {
		t.Fatalf("%+v %v", got, err)
	}
}

type handlerFunc func(context.Context, runtime.Request) (json.RawMessage, error)

func (f handlerFunc) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}

func TestPoolTaskTimeoutSurfacesTimedOutStatus(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(time.Second):
			return json.RawMessage(`{"ok":true}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := New(d, Policy{Workers: 1})
	start := time.Now()
	got, err := p.Run(context.Background(), []Task{{
		ID: "t1", Name: "slow", Timeout: 30 * time.Millisecond,
	}})
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("pool hang: %s", elapsed)
	}
	if err != nil {
		t.Fatalf("unexpected run err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results=%d", len(got))
	}
	if got[0].Status != "timed_out" {
		t.Fatalf("status=%q want timed_out err=%v", got[0].Status, got[0].Err)
	}
	if got[0].Err == nil {
		t.Fatal("expected error on timed_out task")
	}
}

func TestPoolCancelStillReportsEveryTask(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	blockStarted := make(chan struct{})
	fastDone := make(chan struct{})
	_ = d.Register(runtime.Subagent, "block", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(blockStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	_ = d.Register(runtime.Subagent, "fast", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		close(fastDone)
		return json.RawMessage(`{"done":true}`), nil
	}))
	p := New(d, Policy{Workers: 2})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []Result, 1)
	go func() {
		got, _ := p.Run(ctx, []Task{
			{ID: "a", Name: "fast"},
			{ID: "b", Name: "block"},
		})
		done <- got
	}()
	// Wait for both handlers (channel sync, no sleep).
	for _, ch := range []<-chan struct{}{blockStarted, fastDone} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("handler did not start/finish")
		}
	}
	cancel()
	select {
	case got := <-done:
		by := map[string]Result{}
		for _, r := range got {
			by[r.TaskID] = r
		}
		if by["a"].Status != "completed" {
			t.Fatalf("fast task status=%q want completed", by["a"].Status)
		}
		if by["b"].Status != "canceled" && by["b"].Status != "timed_out" && by["b"].Status != "failed" {
			t.Fatalf("block task status=%q want canceled-ish", by["b"].Status)
		}
	case <-time.After(time.Second):
		t.Fatal("pool.Run did not return after cancel")
	}
}

func TestPoolPolicyTimeoutFallback(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(time.Second):
			return json.RawMessage(`{}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := New(d, Policy{Workers: 1, Timeout: 25 * time.Millisecond})
	start := time.Now()
	got, err := p.Run(context.Background(), []Task{{ID: "t1", Name: "slow"}}) // no per-task timeout
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("hang: %s", elapsed)
	}
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	if got[0].Status != "timed_out" {
		t.Fatalf("status=%q err=%v", got[0].Status, got[0].Err)
	}
	if !strings.Contains(got[0].Err.Error(), "deadline") && got[0].Err != context.DeadlineExceeded {
		// status is enough; err should be present
		if got[0].Err == nil {
			t.Fatal("expected timeout error")
		}
	}
}

func TestResultStatusWrappedErrors(t *testing.T) {
	tests := []struct {
		name       string
		handlerErr func() error
		wantStatus string
	}{
		{
			name:       "wrapped_deadline_returns_timed_out",
			handlerErr: func() error { return fmt.Errorf("wrapped: %w", context.DeadlineExceeded) },
			wantStatus: "timed_out",
		},
		{
			name:       "wrapped_canceled_returns_canceled",
			handlerErr: func() error { return fmt.Errorf("wrapped: %w", context.Canceled) },
			wantStatus: "canceled",
		},
		{
			name:       "generic_error_returns_failed",
			handlerErr: func() error { return fmt.Errorf("generic failure") },
			wantStatus: "failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := runtime.New(runtime.Policy{})
			_ = d.Register(runtime.Subagent, "test", handlerFunc(func(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
				return nil, tc.handlerErr()
			}))
			p := New(d, Policy{Workers: 1})
			got, err := p.Run(context.Background(), []Task{{
				ID: "t1", Name: "test", Timeout: 10 * time.Millisecond,
			}})
			if err != nil {
				t.Fatalf("unexpected run err: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("results=%d, want 1", len(got))
			}
			if got[0].Status != tc.wantStatus {
				t.Fatalf("status=%q want %q err=%v", got[0].Status, tc.wantStatus, got[0].Err)
			}
		})
	}
}

func TestResultStatusWrappedContextErrors(t *testing.T) {
	// Direct unit test of resultStatus with a context whose Err() is a deadline.
	// The handler error is also a wrapped deadline, testing both the ctx.Err()
	// branch and the err branch.
	taskCtx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	<-taskCtx.Done() // ensure deadline fires

	// Handler wraps the deadline error, simulating MultiStepHandler/OneShotHandler.
	wrappedErr := fmt.Errorf("multi-step subagent %q: %w", "test", context.DeadlineExceeded)

	got := resultStatus(taskCtx, context.Background(), wrappedErr)
	if got != "timed_out" {
		t.Fatalf("resultStatus=%q want timed_out", got)
	}

	// Also test wrapped canceled with a canceled context.
	cancelCtx, cancelCancel := context.WithCancel(context.Background())
	cancelCancel()
	<-cancelCtx.Done()

	wrappedCancelErr := fmt.Errorf("subagent %q: %w", "test", context.Canceled)
	gotCancel := resultStatus(cancelCtx, context.Background(), wrappedCancelErr)
	if gotCancel != "canceled" {
		t.Fatalf("resultStatus=%q want canceled", gotCancel)
	}

	// Verify the errors.Is path works: the wrapped error should be detected.
	if !errors.Is(wrappedErr, context.DeadlineExceeded) {
		t.Fatal("errors.Is(wrappedErr, context.DeadlineExceeded) should be true")
	}
	if !errors.Is(wrappedCancelErr, context.Canceled) {
		t.Fatal("errors.Is(wrappedCancelErr, context.Canceled) should be true")
	}
}
