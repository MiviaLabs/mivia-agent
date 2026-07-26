package cli

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestReadLineInterrupted(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	lr := newLineReader(r)
	sig := make(chan os.Signal, 1)
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_, err := lr.ReadLine(sig)
		if err != errInterrupted {
			t.Errorf("want errInterrupted, got %v", err)
		}
		close(done)
	}()
	<-started
	// Ensure the goroutine is blocked in select before signaling.
	for i := 0; i < 100; i++ {
		runtime.Gosched()
	}
	sig <- os.Interrupt
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for interrupt")
	}
}
