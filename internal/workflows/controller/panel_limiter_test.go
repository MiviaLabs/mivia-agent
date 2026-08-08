package controller

import (
	"context"
	"testing"
	"time"
)

func TestPanelActorLimiterBlocksFifthActor(t *testing.T) {
	limiter := NewPanelActorLimiter()
	leases := make([]*panelActorLease, 4)
	for i := range leases {
		lease, err := limiter.Acquire(context.Background(), string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		leases[i] = lease
	}
	entered := make(chan struct{})
	go func() {
		lease, err := limiter.Acquire(context.Background(), "fifth")
		if err == nil {
			close(entered)
			lease.Release()
		}
	}()
	select {
	case <-entered:
		t.Fatal("fifth actor acquired a slot before release")
	case <-time.After(20 * time.Millisecond):
	}
	leases[0].Release()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fifth actor stayed blocked after release")
	}
	for _, lease := range leases[1:] {
		lease.Release()
	}
}

func TestPanelActorLimiterSharedRunFailureKeepsAttachedPermit(t *testing.T) {
	limiter := NewPanelActorLimiter()
	first, err := limiter.Acquire(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	second, err := limiter.Acquire(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	second.ReleaseBeforeActor()
	first.AttachLocal()
	leases := make([]*panelActorLease, 3)
	for i := range leases {
		lease, acquireErr := limiter.Acquire(context.Background(), "other-"+string(rune('a'+i)))
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		leases[i] = lease
	}
	blocked := make(chan struct{})
	go func() {
		lease, acquireErr := limiter.Acquire(context.Background(), "fifth")
		if acquireErr == nil {
			close(blocked)
			lease.ReleaseBeforeActor()
		}
	}()
	select {
	case <-blocked:
		t.Fatal("fifth permit entered while attached shared run was active")
	case <-time.After(20 * time.Millisecond):
	}
	first.Release()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("fifth permit did not enter after local actor release")
	}
	for _, lease := range leases {
		lease.ReleaseBeforeActor()
	}
}
