package chatsync

import (
	"errors"
	"os"
	"testing"
)

// TestTruncateOutboxFileNilIsAnErrorNotAPanic pins the platform seam against
// the outbox's dead contract: Dead() is "eventsFile == nil", and the rollback
// paths call the truncate with that field, so a nil file must come back as an
// error on every platform. The POSIX variant inherits this from
// (*os.File).Truncate; the Windows variant reads f.Name() and has to guard it
// itself, which is why this test is not built per platform.
func TestTruncateOutboxFileNilIsAnErrorNotAPanic(t *testing.T) {
	err := truncateOutboxFile(nil, 0)
	if err == nil {
		t.Fatal("truncateOutboxFile(nil) = nil, want an error: a dead outbox must not report a successful truncate")
	}
	if !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("truncateOutboxFile(nil) = %v, want ErrInvalid to match the POSIX (*os.File).Truncate contract", err)
	}
}
