package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite"
)

// claimsErrorConnector wraps a real modernc sqlite connector and injects a
// failure into any statement containing failSubstr, or into Close when
// closeErr is set. A closed pool covers most generic error branches, but the
// branches that need a specific statement to fail (or the driver Close to
// error) cannot be driven there, so the claim methods' defensive error paths
// stay pinned by tests through this wrapper.
type claimsErrorConnector struct {
	dsn        string
	failSubstr string
	closeErr   error
}

func (c claimsErrorConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := (&sqlite3.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &claimsErrorConn{Conn: conn, failSubstr: c.failSubstr, closeErr: c.closeErr}, nil
}

func (c claimsErrorConnector) Driver() driver.Driver { return &sqlite3.Driver{} }

type claimsErrorConn struct {
	driver.Conn
	failSubstr string
	closeErr   error
}

func (c *claimsErrorConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.failSubstr != "" && strings.Contains(query, c.failSubstr) {
		return nil, errors.New("injected claims query failure")
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *claimsErrorConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.failSubstr != "" && strings.Contains(query, c.failSubstr) {
		return nil, errors.New("injected claims exec failure")
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *claimsErrorConn) Close() error {
	// Close the real driver conn even when the injected error fires, or the
	// underlying file handle leaks and Windows TempDir cleanup fails with
	// ERROR_SHARING_VIOLATION; Close still surfaces the injected error.
	err := c.Conn.Close()
	if c.closeErr != nil {
		return c.closeErr
	}
	return err
}

func openClaimsErrorStore(t *testing.T, path, failSubstr string, closeErr error) *SQLite {
	t.Helper()
	db := sql.OpenDB(claimsErrorConnector{dsn: sqliteDSN(path), failSubstr: failSubstr, closeErr: closeErr})
	t.Cleanup(func() { _ = db.Close() })
	return &SQLite{db: db}
}

// TestSQLiteClaimSuccessAndGuardBranches covers the read, takeover, and
// release success paths plus the empty-holder guards that a live store
// reaches without any injected failure.
func TestSQLiteClaimSuccessAndGuardBranches(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.GetClaim(ctx, "missing-run"); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("GetClaim(missing) = %v, want ErrClaimNotHeld", err)
	}
	if err := store.ClaimRun(ctx, "run-1", "h1"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	claim, err := store.GetClaim(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if claim.RunID != "run-1" || claim.Holder != "h1" {
		t.Fatalf("GetClaim = %+v, want run-1/h1", claim)
	}

	if err := store.TakeoverClaim(ctx, "run-1", ""); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("TakeoverClaim empty holder = %v, want ErrClaimNotHeld", err)
	}
	if err := store.TakeoverClaim(ctx, "run-1", "h2"); err != nil {
		t.Fatalf("TakeoverClaim: %v", err)
	}
	taken, err := store.GetClaim(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetClaim after takeover: %v", err)
	}
	if taken.Holder != "h2" {
		t.Fatalf("holder after takeover = %q, want h2", taken.Holder)
	}
	if taken.Fence <= 1 {
		t.Fatalf("fence after takeover = %d, want > 1", taken.Fence)
	}
	if _, err := time.Parse(time.RFC3339Nano, taken.AcquiredAt); err != nil {
		t.Fatalf("acquired_at %q is not RFC3339 nano: %v", taken.AcquiredAt, err)
	}

	if err := store.TakeoverExpiredClaim(ctx, "run-1", "", time.Hour); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("TakeoverExpiredClaim empty holder = %v, want ErrClaimNotHeld", err)
	}
	if _, err := store.TakeoverExpiredClaimFenced(ctx, "run-1", "", time.Hour); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("TakeoverExpiredClaimFenced empty holder = %v, want ErrClaimNotHeld", err)
	}

	fenced, err := store.ClaimRunFenced(ctx, "run-2", "owner")
	if err != nil {
		t.Fatalf("ClaimRunFenced: %v", err)
	}
	if err := store.ReleaseClaimFenced(ctx, fenced); err != nil {
		t.Fatalf("ReleaseClaimFenced: %v", err)
	}
}

// TestSQLiteClaimErrorBranches drives the claim methods' error paths with a
// connector that fails a chosen statement, plus a seeded live claim for the
// takeover path whose follow-up read fails after the update matched nothing.
func TestSQLiteClaimErrorBranches(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claims.db")
	seed, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.ClaimRun(ctx, "run-1", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	t.Run("claim run insert error", func(t *testing.T) {
		store := openClaimsErrorStore(t, path, "INSERT INTO run_claims", nil)
		if _, err := store.ClaimRunFenced(ctx, "run-1", "h1"); err == nil {
			t.Fatal("ClaimRunFenced must error on injected insert failure")
		}
	})

	t.Run("takeover insert error", func(t *testing.T) {
		store := openClaimsErrorStore(t, path, "INSERT INTO run_claims", nil)
		if err := store.TakeoverClaim(ctx, "run-1", "h2"); err == nil {
			t.Fatal("TakeoverClaim must error on injected insert failure")
		}
	})

	t.Run("takeover expired update error", func(t *testing.T) {
		store := openClaimsErrorStore(t, path, "UPDATE run_claims SET holder", nil)
		if _, err := store.TakeoverExpiredClaimFenced(ctx, "run-1", "h2", 0); err == nil {
			t.Fatal("TakeoverExpiredClaimFenced must error on injected update failure")
		}
	})

	t.Run("takeover expired read error", func(t *testing.T) {
		// The seeded claim is fresh, so with a one-hour max age the update
		// matches nothing (ErrNoRows) and the follow-up liveness read is the
		// statement that fails.
		store := openClaimsErrorStore(t, path, "SELECT 1 FROM run_claims", nil)
		if _, err := store.TakeoverExpiredClaimFenced(ctx, "run-1", "h2", time.Hour); err == nil {
			t.Fatal("TakeoverExpiredClaimFenced must error on injected read failure")
		}
	})

	t.Run("append non-constraint error", func(t *testing.T) {
		store := openClaimsErrorStore(t, path, "INSERT INTO events", nil)
		event := Event{ID: "e1", RunID: "run-1", Sequence: 1, Kind: "test", Payload: []byte("x")}
		if err := store.AppendClaimedFenced(ctx, event, Claim{RunID: "run-1", Holder: "h1", Fence: 1}); err == nil {
			t.Fatal("AppendClaimedFenced must error on injected insert failure")
		}
	})

	t.Run("release fenced delete error", func(t *testing.T) {
		store := openClaimsErrorStore(t, path, "DELETE FROM run_claims", nil)
		if err := store.ReleaseClaimFenced(ctx, Claim{RunID: "run-1", Holder: "h1", Fence: 1}); err == nil {
			t.Fatal("ReleaseClaimFenced must error on injected delete failure")
		}
	})

	t.Run("release delete error", func(t *testing.T) {
		store := openClaimsErrorStore(t, path, "DELETE FROM run_claims", nil)
		if err := store.ReleaseClaim(ctx, "run-1", "h1"); err == nil {
			t.Fatal("ReleaseClaim must error on injected delete failure")
		}
	})
}

// TestSQLiteCloseErrorBranches pins both error bodies of the idempotent
// Close: a checkpoint failure on an already-closed pool, and a driver-level
// Close failure after a successful checkpoint.
func TestSQLiteCloseErrorBranches(t *testing.T) {
	t.Run("checkpoint failure on closed pool", func(t *testing.T) {
		db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "close.db")))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		store := &SQLite{db: db}
		if err := store.Close(); err == nil {
			t.Fatal("Close on a closed pool must surface the checkpoint error")
		}
	})

	t.Run("driver close failure after checkpoint", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "close2.db")
		seed, err := OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := seed.Close(); err != nil {
			t.Fatal(err)
		}
		store := openClaimsErrorStore(t, path, "", errors.New("injected driver close failure"))
		if err := store.Close(); err == nil {
			t.Fatal("Close must surface the driver close error")
		}
	})
}

// TestSQLiteFenceGenerationMigration_Fresh pins the fresh-DB shape that
// backs IsRunHeld and IsRunTokenFenced: a fresh DB exposes the
// fence_generation column on run_claims AND the fenced_tokens table.
func TestSQLiteFenceGenerationMigration_Fresh(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var columnCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('run_claims') WHERE name = 'fence_generation'`,
	).Scan(&columnCount); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if columnCount != 1 {
		t.Fatalf("fresh DB fence_generation columns = %d, want 1", columnCount)
	}

	var tableCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='fenced_tokens'`,
	).Scan(&tableCount); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("fresh DB fenced_tokens tables = %d, want 1", tableCount)
	}
}

// TestSQLiteFenceGenerationMigration_LegacyDefaultsToZero pins the
// pre-existing-DB migration: a database without fence_generation reopens
// idempotently with the new column defaulting to 0, and reopening a second
// time remains a no-op.
func TestSQLiteFenceGenerationMigration_LegacyDefaultsToZero(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	// Seed a legacy database using raw SQL: the legacy run_claims has only
	// the four columns the original migration produced. A hand-built schema
	// that omits fence_generation is the realistic pre-migration shape.
	raw, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `CREATE TABLE run_claims (run_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at TEXT NOT NULL, fence INTEGER NOT NULL DEFAULT 1)`
	if _, err := raw.ExecContext(ctx, legacySchema); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO run_claims(run_id, holder, acquired_at) VALUES(?, ?, ?)`,
		"legacy-run", "legacy-holder", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy raw: %v", err)
	}

	// Reopen with the current schema; the fence_generation migration must
	// apply idempotently and default to 0 for the existing row.
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen with migrations: %v", err)
	}
	var fenceGen int64
	if err := store.db.QueryRowContext(ctx,
		`SELECT fence_generation FROM run_claims WHERE run_id = ?`,
		"legacy-run",
	).Scan(&fenceGen); err != nil {
		t.Fatalf("read fence_generation: %v", err)
	}
	if fenceGen != 0 {
		t.Fatalf("legacy row fence_generation = %d, want 0", fenceGen)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close after read: %v", err)
	}

	// Reopening a second time must remain a no-op: the migration guard
	// clause "duplicate column" is the contract that keeps it so.
	reopened, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
}

// TestTakeoverClaimConcurrentMutateSurvivesSQLiteBusy reproduces the SDK
// CheckTakeoverFencesConcurrentMutate contention pattern at the storage
// level: a goroutine spins AppendClaimedFenced (the single Exec the SDK's
// Mutate calls) while the main goroutine runs TakeoverClaimFenced. Before
// the retry wrapper, the Takeover's BeginTx could fail with SQLITE_BUSY
// (snapshot-level, not lock-wait-level) and busy_timeout could not clear
// it; the retry now clears it on a fresh snapshot. The test is not
// deterministic across machines (CI may have different timings), so it
// is gated behind testing.Short and uses a generous deadline.
func TestTakeoverClaimConcurrentMutateSurvivesSQLiteBusy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping contention test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "busy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const runID = "run-busy"
	ownerClaim, err := store.ClaimRunFenced(ctx, runID, "owner")
	if err != nil {
		t.Fatalf("ClaimRunFenced: %v", err)
	}

	var stopSpinning atomic.Bool
	var spinErr atomic.Pointer[error]
	var wg sync.WaitGroup
	const spinners = 8
	for i := 0; i < spinners; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			seq := 1
			for !stopSpinning.Load() {
				event := Event{ID: "spin", RunID: runID, Sequence: seq, Kind: "spin", Payload: []byte("x")}
				if err := store.AppendClaimedFenced(ctx, event, ownerClaim); err != nil && !errors.Is(err, ErrClaimHeld) && !errors.Is(err, ErrDuplicate) {
					e := err
					spinErr.Store(&e)
					return
				}
				seq++
				if seq > 10000 {
					seq = 1
				}
			}
		}(i)
	}

	if _, err := store.TakeoverClaimFenced(ctx, runID, "new-owner"); err != nil {
		stopSpinning.Store(true)
		wg.Wait()
		t.Fatalf("TakeoverClaimFenced under contention: %v", err)
	}
	stopSpinning.Store(true)
	wg.Wait()
	if ptr := spinErr.Load(); ptr != nil {
		t.Fatalf("spinner goroutine failed: %v", *ptr)
	}

	fenced, err := store.IsRunTokenFenced(ctx, runID, "owner")
	if err != nil {
		t.Fatalf("IsRunTokenFenced: %v", err)
	}
	if !fenced {
		t.Fatal("IsRunTokenFenced(owner) = false, want true after Takeover")
	}
}
