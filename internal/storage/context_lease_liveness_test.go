package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	_ "modernc.org/sqlite"
)

// setLeaseHolder stamps context_sessions.lease_holder directly so tests can
// construct a specific recorded holder without racing a real heartbeat.
func setLeaseHolder(t *testing.T, s *SQLite, principal contextstate.Principal, holder string) {
	t.Helper()
	var value any
	if holder != "" {
		value = holder
	}
	if _, err := s.db.Exec(`UPDATE context_sessions SET lease_holder=? WHERE workspace_id=? AND session_id=? AND subject_id=?`, value, principal.WorkspaceID, principal.SessionID, principal.SubjectID); err != nil {
		t.Fatalf("set lease_holder: %v", err)
	}
}

// deadHolderToken builds a token for THIS live pid but a wrong starttime -
// the pid-reuse shape, which is proof the recorded holder is gone even
// though a process with that pid exists.
func deadHolderToken(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	boot, err := leaseBootID()
	if err != nil {
		t.Skipf("no boot id on this platform: %v", err)
	}
	return strings.Join([]string{leaseHolderVersion, host, boot, strconv.Itoa(os.Getpid()), "1"}, "|")
}

// TestReclaimSessionTakesOverProvablyDeadHolder is the crash-recovery
// regression: a FRESH lease whose recorded holder is provably dead (same
// host and boot, pid reused) must be taken over immediately instead of
// blocking resume for the rest of the lease TTL.
func TestReclaimSessionTakesOverProvablyDeadHolder(t *testing.T) {
	ctx := context.Background()
	s, owner := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, owner)
	fresh := time.Now()
	setLeaseAt(t, s, owner, &fresh)
	setLeaseHolder(t, s, owner, deadHolderToken(t))

	rival, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimSession(ctx, rival, owner.SessionID); err != nil {
		t.Fatalf("ReclaimSession with provably dead holder = %v, want immediate takeover", err)
	}
	var digest string
	if err := s.db.QueryRow(`SELECT capability_digest FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != rival.CapabilityDigest() {
		t.Fatalf("capability_digest not transferred to the rival after dead-holder takeover")
	}
}

// TestReclaimSessionStillRefusesLiveHolder pins the fail-closed side: a
// fresh lease held by a genuinely live process (this test process itself)
// is refused with the typed live error, exactly as before.
func TestReclaimSessionStillRefusesLiveHolder(t *testing.T) {
	ctx := context.Background()
	s, owner := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, owner)
	fresh := time.Now()
	setLeaseAt(t, s, owner, &fresh)
	holder := currentLeaseHolder()
	if holder == "" {
		t.Skip("cannot mint a holder token on this platform")
	}
	setLeaseHolder(t, s, owner, holder)

	rival, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReclaimSession(ctx, rival, owner.SessionID)
	if !errors.Is(err, contextstate.ErrSessionLiveElsewhere) {
		t.Fatalf("ReclaimSession with live holder = %v, want ErrSessionLiveElsewhere", err)
	}
}

// TestReclaimSessionUnknownHolderFallsBackToTTL pins the conservative
// fallback: a fresh lease whose holder is absent, garbled, or from another
// host/boot cannot be proven dead and keeps the pure-TTL refusal.
func TestReclaimSessionUnknownHolderFallsBackToTTL(t *testing.T) {
	host, _ := os.Hostname()
	cases := []struct {
		name   string
		holder string
	}{
		{name: "absent", holder: ""},
		{name: "garbled", holder: "not-a-token"},
		{name: "other-host", holder: leaseHolderVersion + "|other-host|boot|1|1"},
		{name: "other-boot", holder: leaseHolderVersion + "|" + host + "|other-boot|1|1"},
		{name: "future-version", holder: "v9|" + host + "|boot|1|1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, owner := openContextTestStore(t)
			defer s.Close()
			seedContextSession(t, s, owner)
			fresh := time.Now()
			setLeaseAt(t, s, owner, &fresh)
			setLeaseHolder(t, s, owner, tc.holder)

			rival, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.ReclaimSession(ctx, rival, owner.SessionID)
			if !errors.Is(err, contextstate.ErrSessionLiveElsewhere) {
				t.Fatalf("ReclaimSession with %s holder = %v, want TTL refusal", tc.name, err)
			}
		})
	}
}

// TestRenewLeaseStampsHolderAndReleaseClears pins the write side: a
// heartbeat renewal records both the timestamp and the holder identity, and
// a clean release clears both.
func TestRenewLeaseStampsHolderAndReleaseClears(t *testing.T) {
	ctx := context.Background()
	s, owner := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, owner)

	if err := s.RenewLease(ctx, owner, owner.SessionID); err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	var leaseAt sql.NullInt64
	var holder sql.NullString
	if err := s.db.QueryRow(`SELECT lease_at, lease_holder FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&leaseAt, &holder); err != nil {
		t.Fatal(err)
	}
	if !leaseAt.Valid {
		t.Fatal("lease_at not stamped by RenewLease")
	}
	if want := currentLeaseHolder(); want != "" && (!holder.Valid || holder.String != want) {
		t.Fatalf("lease_holder = %+v, want this process's token %q", holder, want)
	}

	if err := s.ReleaseLease(ctx, owner, owner.SessionID); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	if err := s.db.QueryRow(`SELECT lease_at, lease_holder FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&leaseAt, &holder); err != nil {
		t.Fatal(err)
	}
	if leaseAt.Valid || holder.Valid {
		t.Fatalf("release left lease state behind: lease_at=%+v holder=%+v", leaseAt, holder)
	}
}

// TestLeaseHolderDeadProofRules unit-tests the proof predicate through its
// seams: only same-host, same-boot, pid-gone-or-reused counts as dead.
func TestLeaseHolderDeadProofRules(t *testing.T) {
	origHost, origRead, origGOOS := leaseHostname, leaseReadFile, leaseGOOS
	defer func() { leaseHostname, leaseReadFile, leaseGOOS = origHost, origRead, origGOOS }()

	leaseGOOS = "linux"
	leaseHostname = func() (string, error) { return "h1", nil }
	files := map[string]string{
		"/proc/sys/kernel/random/boot_id": "boot-1\n",
		"/proc/42/stat":                   "42 (some (proc) name) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 777 0",
	}
	leaseReadFile = func(path string) ([]byte, error) {
		if content, ok := files[path]; ok {
			return []byte(content), nil
		}
		return nil, fmt.Errorf("open %s: %w", path, fs.ErrNotExist)
	}

	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "pid gone", token: "v1|h1|boot-1|43|100", want: true},
		{name: "pid reused (starttime differs)", token: "v1|h1|boot-1|42|100", want: true},
		{name: "pid alive (starttime matches)", token: "v1|h1|boot-1|42|777", want: false},
		{name: "different host", token: "v1|h2|boot-1|43|100", want: false},
		{name: "different boot", token: "v1|h1|boot-2|43|100", want: false},
		{name: "garbled", token: "v1|h1", want: false},
		{name: "empty", token: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := leaseHolderDead(tc.token); got != tc.want {
				t.Fatalf("leaseHolderDead(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}

	t.Run("non-linux never proves death", func(t *testing.T) {
		leaseGOOS = "darwin"
		defer func() { leaseGOOS = "linux" }()
		if leaseHolderDead("v1|h1|boot-1|43|100") {
			t.Fatal("non-linux platform claimed proof of death")
		}
	})
}

// TestMigrationV15ConvergesFromEveryPriorVersion mirrors the v14 migration
// test: a store at v14 and a fresh store both converge on v15 with the
// lease_holder column present and writable.
func TestMigrationV15ConvergesFromEveryPriorVersion(t *testing.T) {
	apply := []func(*sql.DB) error{
		applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3, applyContextSchemaV4,
		applyContextSchemaV5, applyContextSchemaV6, applyContextSchemaV7, applyContextSchemaV8,
		applyContextSchemaV9, applyContextSchemaV10, applyContextSchemaV11, applyContextSchemaV12,
		applyContextSchemaV13, applyContextSchemaV14,
	}
	tests := []struct {
		name     string
		versions int
	}{
		{name: "fresh", versions: 0},
		{name: "v14", versions: 14},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if test.versions > 0 {
				if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < test.versions; i++ {
					if err := apply[i](db); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := migrateContextSchema(db); err != nil {
				t.Fatalf("migrateContextSchema from %s: %v", test.name, err)
			}
			var version int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != currentContextSchemaVersion {
				t.Fatalf("user_version = %d after %s, want %d", version, test.name, currentContextSchemaVersion)
			}
			if !contextVersionTablePresent(db, 15) {
				t.Fatalf("context_sessions_v15_contract is missing after %s", test.name)
			}
			if _, err := db.Exec(`INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,lease_at,lease_holder) VALUES('ws','subj','sess','digest',0,0,0,'provider','model',1,1700000000,'v1|h|b|1|1')`); err != nil {
				t.Fatalf("insert with lease_holder: %v", err)
			}
		})
	}
}
