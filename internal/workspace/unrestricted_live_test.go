package workspace

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestSetUnrestrictedLiveReArmIsRaceFreeAndEffective pins the LIVE
// confinement re-arm contract: SetUnrestricted flips the escape check on a
// root that tool goroutines are concurrently resolving through, under
// -race, and the flip is observable immediately (lift lets an outside
// absolute path resolve; re-imposing rejects it again). Run with -race.
func TestSetUnrestrictedLiveReArmIsRaceFreeAndEffective(t *testing.T) {
	root := t.TempDir()
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Unrestricted() {
		t.Fatal("freshly opened root must be confined")
	}
	outside := filepath.Clean(t.TempDir()) // a second, unrelated absolute dir

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = ws.Resolve("file.txt")
				}
			}
		}()
	}

	ws.SetUnrestricted(true)
	if !ws.Unrestricted() {
		t.Fatal("SetUnrestricted(true) not observable")
	}
	if _, err := ws.Resolve(outside); err != nil {
		t.Fatalf("escape check still enforced after a live lift: %v", err)
	}

	ws.SetUnrestricted(false)
	if ws.Unrestricted() {
		t.Fatal("SetUnrestricted(false) not observable")
	}
	if _, err := ws.Resolve(outside); err == nil {
		t.Fatal("escape check not re-imposed after a live disarm")
	}

	close(stop)
	wg.Wait()
}
