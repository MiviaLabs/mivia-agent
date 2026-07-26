package storage

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMain(m *testing.M) {
	if os.Getenv("MIVIA_STORAGE_UNCOMMITTED_CHILD") == "1" {
		path := os.Getenv("MIVIA_STORAGE_CHILD_DB")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			os.Exit(2)
		}
		_, _ = db.Exec("PRAGMA journal_mode=WAL")
		tx, err := db.Begin()
		if err != nil {
			os.Exit(3)
		}
		_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS events (id TEXT PRIMARY KEY, run_id TEXT, sequence INTEGER, kind TEXT, payload BLOB)`)
		if err != nil {
			os.Exit(4)
		}
		_, err = tx.Exec(`INSERT INTO events(id,run_id,sequence,kind,payload) VALUES('crash-1','crash',1,'agent','uncommitted')`)
		if err != nil {
			os.Exit(5)
		}
		os.Exit(17)
	}
	if os.Getenv("MIVIA_STORAGE_COMMITTED_CHILD") == "1" {
		path := os.Getenv("MIVIA_STORAGE_CHILD_DB")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			os.Exit(2)
		}
		_, _ = db.Exec("PRAGMA journal_mode=WAL")
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events (id TEXT PRIMARY KEY, run_id TEXT, sequence INTEGER, kind TEXT, payload BLOB)`)
		if err != nil {
			os.Exit(4)
		}
		_, err = db.Exec(`INSERT INTO events(id,run_id,sequence,kind,payload) VALUES('crash-committed','crash',1,'agent','committed')`)
		if err != nil {
			os.Exit(5)
		}
		os.Exit(18)
	}
	os.Exit(m.Run())
}

func TestSQLite_ReopenPreservesCommittedEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(context.Background(), Event{ID: "reopen-1", RunID: "reopen", Sequence: 1, Kind: "agent", Payload: []byte("bounded")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	events, err := s.Events(context.Background(), "reopen")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || string(events[0].Payload) != "bounded" {
		t.Fatalf("events after reopen: %+v", events)
	}
}

func TestSQLite_IntegrityCheckPasses(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var result string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "ok" {
		t.Fatalf("integrity_check=%q", result)
	}
}

func TestSQLite_WALCheckpointIsObservable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 500; i++ {
		if err := s.Append(context.Background(), Event{ID: "wal-" + itoa(i), RunID: "wal", Sequence: i + 1, Kind: "agent", Payload: []byte("payload")}); err != nil {
			t.Fatal(err)
		}
	}
	wal := path + "-wal"
	if _, err := os.Stat(wal); err != nil {
		t.Fatalf("WAL not created: %v", err)
	}
	before, err := os.Stat(wal)
	if err != nil {
		t.Fatal(err)
	}
	var busy, log, checkpointed int
	if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatalf("checkpoint busy=%d log=%d checkpointed=%d", busy, log, checkpointed)
	}
	after, err := os.Stat(wal)
	if err == nil {
		t.Logf("wal_bytes_before_checkpoint=%d wal_bytes_after_checkpoint=%d", before.Size(), after.Size())
	}
}

func TestSQLite_LongReaderDoesNotPreventEventualCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(context.Background(), `SELECT COUNT(*) FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i := 0; i < 300; i++ {
		if err := s.Append(context.Background(), Event{ID: "reader-" + itoa(i), RunID: "reader", Sequence: i + 1, Kind: "agent", Payload: []byte(strings.Repeat("x", 256))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	var busy, log, checkpointed int
	if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatalf("checkpoint remained busy=%d log=%d checkpointed=%d", busy, log, checkpointed)
	}
}

func TestSQLite_BackupRestore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.db")
	backup := filepath.Join(dir, "backup.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(context.Background(), Event{ID: "backup-1", RunID: "backup", Sequence: 1, Kind: "agent", Payload: []byte("safe")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenSQLite(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	events, err := restored.Events(context.Background(), "backup")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || string(events[0].Payload) != "safe" {
		t.Fatalf("restored events: %+v", events)
	}
}

func TestSQLite_BackupWhileWritesAreActive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.db")
	backup := filepath.Join(dir, "active-backup.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	writeErrs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writeErrs <- s.Append(context.Background(), Event{ID: "active-" + itoa(i), RunID: "active", Sequence: i + 1, Kind: "agent", Payload: []byte("safe")})
		}(i)
	}
	backupErr := s.Backup(context.Background(), backup)
	wg.Wait()
	close(writeErrs)
	for err := range writeErrs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if backupErr != nil {
		t.Fatal(backupErr)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenSQLite(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var result string
	if err := restored.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "ok" {
		t.Fatalf("backup integrity=%q", result)
	}
}

func TestSQLite_DiskPressureReturnsErrorWithoutSilentLoss(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec(`PRAGMA max_page_count=32`); err != nil {
		t.Fatal(err)
	}
	var gotErr error
	for i := 0; i < 1000; i++ {
		gotErr = s.Append(context.Background(), Event{ID: "full-" + itoa(i), RunID: "full", Sequence: i + 1, Kind: "agent", Payload: []byte(strings.Repeat("x", 2048))})
		if gotErr != nil {
			break
		}
	}
	if gotErr == nil {
		t.Fatal("expected bounded database to reject a write")
	}
	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 || count >= 1000 {
		t.Fatalf("unexpected count after disk pressure: %d", count)
	}
}

func TestSQLite_UncommittedChildWriteIsRolledBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	cmd := exec.Command(os.Args[0], "-test.run=TestSQLite_UncommittedChildWriteIsRolledBack")
	cmd.Env = append(os.Environ(), "MIVIA_STORAGE_UNCOMMITTED_CHILD=1", "MIVIA_STORAGE_CHILD_DB="+path)
	if err := cmd.Run(); err == nil {
		t.Fatal("child unexpectedly committed")
	}
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("uncommitted child rows recovered: %d", count)
	}
}

func TestSQLite_CommittedChildWriteSurvivesAbruptExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	cmd := exec.Command(os.Args[0], "-test.run=TestSQLite_CommittedChildWriteSurvivesAbruptExit")
	cmd.Env = append(os.Environ(), "MIVIA_STORAGE_COMMITTED_CHILD=1", "MIVIA_STORAGE_CHILD_DB="+path)
	if err := cmd.Run(); err == nil {
		t.Fatal("child unexpectedly exited cleanly")
	}
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed child rows lost: %d", count)
	}
}

func TestSQLite_200AgentLatencyEvidence(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const agents, eventsPerAgent = 200, 2
	durations := make([]time.Duration, 0, agents*eventsPerAgent)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for agent := 0; agent < agents; agent++ {
		wg.Add(1)
		go func(agent int) {
			defer wg.Done()
			for seq := 0; seq < eventsPerAgent; seq++ {
				start := time.Now()
				err := s.Append(context.Background(), Event{ID: "metric-" + itoa(agent) + "-" + itoa(seq), RunID: runID(agent), Sequence: seq + 1, Kind: "agent", Payload: []byte("bounded")})
				if err != nil {
					t.Errorf("append: %v", err)
					return
				}
				mu.Lock()
				durations = append(durations, time.Since(start))
				mu.Unlock()
			}
		}(agent)
	}
	wg.Wait()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("agents=%d events=%d p50=%s p95=%s p99=%s max=%s", agents, len(durations), percentile(durations, .50), percentile(durations, .95), percentile(durations, .99), durations[len(durations)-1])
}

func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	i := int(float64(len(values)-1) * p)
	return values[i]
}
