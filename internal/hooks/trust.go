package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Tier is a hook's derived trust level. It is never declared: a file cannot
// name its own tier, or a hostile config would simply write the one that always
// runs. Tier comes from which fixed path the group loaded from.
type Tier string

const (
	// TierManaged is an operator-placed hook at a path ordinary users and the
	// agent cannot write. It runs without confirmation.
	TierManaged Tier = "managed"
	// TierUser is a hook from ~/.mivia/mivia.toml. It runs only once its
	// definition hash is confirmed.
	TierUser Tier = "user"
)

// Status is whether a group may run, and why not when it may not.
type Status string

const (
	// StatusActive means the group runs.
	StatusActive Status = "active"
	// StatusPending means the group has never been confirmed.
	StatusPending Status = "pending"
	// StatusHashChanged means the group WAS confirmed and its definition has
	// since been edited. It is displayed distinctly from pending: "this was
	// trusted and has changed" is a materially different message from "this is
	// new", and collapsing the two trains a re-confirmation reflex.
	StatusHashChanged Status = "hash-changed"
)

// maxStoreBytes bounds the trust store read.
const maxStoreBytes = 1 << 20

// Record is one confirmation.
//
// Trust is keyed strictly on (Source, Hash). Event and Program are recorded
// only so /hooks can tell an edited hook from a new one; they are never
// consulted to decide whether a hook may run.
type Record struct {
	Source      string `json:"source"`
	Hash        string `json:"hash"`
	Event       string `json:"event"`
	Program     string `json:"program"`
	ConfirmedAt string `json:"confirmed_at"`
}

// Store is the on-disk record of which hook definitions the user confirmed.
//
// It is runtime state, not configuration, which is why it is the one JSON file
// this layer introduces while every config surface stays TOML.
type Store struct {
	path    string
	records []Record
	loadErr error
}

// StorePath is the fixed location of the trust store.
func StorePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".mivia", "hook-trust.json")
}

// OpenStore reads the trust store. It never fails the caller: an unreadable
// store is recorded and every hook stays untrusted. Failing open here would
// make the gate deletable - remove the file, run everything.
func OpenStore(path string) *Store {
	store := &Store{path: path}
	if path == "" {
		store.loadErr = errors.New("no home directory: the hook trust store has no location")
		return store
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// A fresh install. Zero hooks run, and that is not an error.
		return store
	}
	if err != nil {
		store.loadErr = fmt.Errorf("read hook trust store %s: %w", path, err)
		return store
	}
	if len(data) > maxStoreBytes {
		store.loadErr = fmt.Errorf("hook trust store %s exceeds %d bytes", path, maxStoreBytes)
		return store
	}
	if err := json.Unmarshal(data, &store.records); err != nil {
		store.loadErr = fmt.Errorf("parse hook trust store %s: %w", path, err)
		store.records = nil
	}
	return store
}

// Err reports why the store could not be read, if it could not be.
func (s *Store) Err() error { return s.loadErr }

// Status classifies one group against the store.
func (s *Store) Status(group Group) Status {
	if s.loadErr != nil {
		return StatusPending
	}
	for _, record := range s.records {
		if record.Source == group.Source && record.Hash == group.Hash {
			return StatusActive
		}
	}
	// Not confirmed. Distinguish "edited since confirmation" from "new" using
	// the recorded identity fields - same file, same event, same program.
	for _, record := range s.records {
		if record.Source == group.Source && record.Event == string(group.Event) &&
			record.Program == groupProgram(group) {
			return StatusHashChanged
		}
	}
	return StatusPending
}

// Confirm records a group as trusted.
//
// It refuses a store it could not read: rewriting one would destroy every other
// confirmation in it, so a corrupt store is repaired by the user, not silently
// replaced by us.
func (s *Store) Confirm(group Group) error {
	if s.loadErr != nil {
		return fmt.Errorf("refusing to write the hook trust store: %w", s.loadErr)
	}
	for _, record := range s.records {
		if record.Source == group.Source && record.Hash == group.Hash {
			return nil
		}
	}
	s.records = append(s.records, Record{
		Source:      group.Source,
		Hash:        group.Hash,
		Event:       string(group.Event),
		Program:     groupProgram(group),
		ConfirmedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return s.write()
}

// write replaces the store atomically. A half-written store would read as
// corrupt on the next launch, which fails closed - correct, but it would
// silently drop confirmations the user made.
func (s *Store) write() error {
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return fmt.Errorf("encode hook trust store: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".hook-trust-*.json")
	if err != nil {
		return fmt.Errorf("create temporary trust store: %w", err)
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	// The store records which programs may execute on this machine. Nothing
	// outside the account needs to read it, and nothing at all may write it.
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("set trust store mode: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write trust store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close trust store: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("install trust store: %w", err)
	}
	return nil
}

// groupProgram is the group's identity for display purposes: the first
// handler's program. It is deliberately not part of the trust key.
func groupProgram(group Group) string {
	if len(group.Handlers) == 0 || len(group.Handlers[0].Argv) == 0 {
		return ""
	}
	return group.Handlers[0].Argv[0]
}

// Decision is one group's resolved tier and status.
type Decision struct {
	Group  Group
	Tier   Tier
	Status Status
}

// TierOf derives a group's tier from the file it loaded from. The config has no
// say: there is no `trust` key, and the loader resolves both paths itself.
func TierOf(group Group) Tier {
	if managed := ManagedConfigPath(); managed != "" && group.Source == managed {
		return TierManaged
	}
	return TierUser
}

// Resolve classifies every group. Managed hooks are active by construction;
// user hooks are active only while their exact definition is confirmed.
func Resolve(groups []Group, store *Store) []Decision {
	decisions := make([]Decision, 0, len(groups))
	for _, group := range groups {
		decision := Decision{Group: group, Tier: TierOf(group), Status: StatusActive}
		if decision.Tier == TierUser {
			decision.Status = store.Status(group)
		}
		decisions = append(decisions, decision)
	}
	return decisions
}
