package legacytui

import (
	"sync"
	"testing"
	"time"
)

func TestWaitWorkerGroupRespectsTimeout(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1) // never Done - simulate hung agent worker
	start := time.Now()
	waitWorkerGroup(&wg, 50*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("waitWorkerGroup hung: %s", elapsed)
	}
	wg.Done()
}

func TestWaitWorkerGroupReturnsWhenDone(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	released := make(chan struct{})
	go func() {
		<-released
		wg.Done()
	}()
	// Release worker after wait starts (async), without time.Sleep.
	go func() {
		close(released)
	}()
	start := time.Now()
	waitWorkerGroup(&wg, 2*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("expected prompt return, took %s", elapsed)
	}
}
