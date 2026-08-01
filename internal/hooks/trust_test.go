package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const trustBase = `[[hooks]]
event = "PreToolUse"
matcher = "run_command"

  [[hooks.handlers]]
  type = "command"
  argv = ["./gate.sh"]
  timeout = 10
  on_timeout = "block"
`

func userGroups(t *testing.T, body string) []Group {
	t.Helper()
	groups, err := Parse([]byte(body), "/home/u/.mivia/mivia.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return groups
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return OpenStore(filepath.Join(t.TempDir(), "hook-trust.json"))
}

// A fresh install runs zero hooks. A PreToolUse hook the user did not know
// about is a privilege escalation, so the default must be that it does not run.
func TestFreshInstallRunsZeroHooks(t *testing.T) {
	groups := userGroups(t, trustBase)
	decisions := Resolve(groups, newStore(t))
	if len(decisions) != 1 {
		t.Fatalf("want 1 decision, got %d", len(decisions))
	}
	if decisions[0].Status != StatusPending {
		t.Errorf("status = %q, want pending", decisions[0].Status)
	}
	if decisions[0].Tier != TierUser {
		t.Errorf("tier = %q, want user", decisions[0].Tier)
	}
	if len(Runnable(decisions)) != 0 {
		t.Fatal("an unconfirmed hook must not run")
	}
}

func TestConfirmedHookRuns(t *testing.T) {
	groups := userGroups(t, trustBase)
	store := newStore(t)
	if err := store.Confirm(groups[0]); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	decisions := Resolve(groups, store)
	if decisions[0].Status != StatusActive {
		t.Fatalf("status = %q, want active", decisions[0].Status)
	}
	if len(Runnable(decisions)) != 1 {
		t.Fatal("a confirmed hook must run")
	}
}

func TestConfirmationSurvivesReopeningTheStore(t *testing.T) {
	groups := userGroups(t, trustBase)
	path := filepath.Join(t.TempDir(), "hook-trust.json")
	if err := OpenStore(path).Confirm(groups[0]); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if Resolve(groups, OpenStore(path))[0].Status != StatusActive {
		t.Fatal("a confirmation must persist across sessions")
	}
}

// The central test of this slice: editing a confirmed hook revokes its trust.
// A name-keyed store would let hooks/fmt.sh be confirmed once and rewritten
// freely, so the confirmation would attest to a definition that no longer
// exists.
func TestEditingAConfirmedHookRevokesTrust(t *testing.T) {
	original := userGroups(t, trustBase)
	store := newStore(t)
	if err := store.Confirm(original[0]); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	edited := userGroups(t, strings.Replace(trustBase, `["./gate.sh"]`, `["./gate.sh", "--yolo"]`, 1))

	decisions := Resolve(edited, store)
	if decisions[0].Status == StatusActive {
		t.Fatal("editing a confirmed hook definition must revoke its trust")
	}
	if len(Runnable(decisions)) != 0 {
		t.Fatal("an edited hook must not run until re-confirmed")
	}
}

// "This was trusted and has since been edited" is a different message from
// "this is new". Collapsing them trains the user to re-confirm without reading.
func TestHashChangedIsDistinctFromPending(t *testing.T) {
	original := userGroups(t, trustBase)
	store := newStore(t)
	if err := store.Confirm(original[0]); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	edited := userGroups(t, strings.Replace(trustBase, "timeout = 10", "timeout = 30", 1))
	if got := Resolve(edited, store)[0].Status; got != StatusHashChanged {
		t.Fatalf("status = %q, want hash-changed", got)
	}

	fresh := userGroups(t, strings.Replace(trustBase, `["./gate.sh"]`, `["./other.sh"]`, 1))
	if got := Resolve(fresh, store)[0].Status; got != StatusPending {
		t.Fatalf("a never-seen hook is pending, not hash-changed; got %q", got)
	}
}

func TestReorderingHandlersRevokesTrust(t *testing.T) {
	two := `[[hooks]]
event = "PreToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./a.sh"]

  [[hooks.handlers]]
  type = "command"
  argv = ["./b.sh"]
`
	swapped := `[[hooks]]
event = "PreToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./b.sh"]

  [[hooks.handlers]]
  type = "command"
  argv = ["./a.sh"]
`
	store := newStore(t)
	if err := store.Confirm(userGroups(t, two)[0]); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if Resolve(userGroups(t, swapped), store)[0].Status == StatusActive {
		t.Fatal("reordering handlers changes behaviour and must revoke trust")
	}
}

func TestReformattingDoesNotRevokeTrust(t *testing.T) {
	store := newStore(t)
	if err := store.Confirm(userGroups(t, trustBase)[0]); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	reformatted := "[[hooks]]\nmatcher='run_command'\nevent='PreToolUse'\n\n[[hooks.handlers]]\non_timeout='block'\ntimeout=10\nargv=['./gate.sh']\ntype='command'\n"
	if got := Resolve(userGroups(t, reformatted), store)[0].Status; got != StatusActive {
		t.Fatalf("reformatting must not revoke trust; got %q", got)
	}
}

// The gate's own storage fails closed. A store that fails open is a store an
// attacker deletes.
func TestCorruptTrustStoreYieldsZeroHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook-trust.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store := OpenStore(path)
	if store.Err() == nil {
		t.Fatal("a corrupt store must report why it could not be read")
	}
	if len(Runnable(Resolve(userGroups(t, trustBase), store))) != 0 {
		t.Fatal("a corrupt store must yield zero hooks, never all hooks")
	}
}

// Confirming into a store we could not read would destroy every other record
// in it, so it is refused rather than silently overwriting.
func TestConfirmRefusesAnUnreadableStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook-trust.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := OpenStore(path).Confirm(userGroups(t, trustBase)[0]); err == nil {
		t.Fatal("Confirm must refuse a store it could not read")
	}
}

func TestAbsentTrustStoreYieldsZeroHooksWithoutError(t *testing.T) {
	store := OpenStore(filepath.Join(t.TempDir(), "missing.json"))
	if store.Err() != nil {
		t.Fatalf("an absent store is the fresh-install case, not an error: %v", store.Err())
	}
	if len(Runnable(Resolve(userGroups(t, trustBase), store))) != 0 {
		t.Fatal("an absent store must yield zero hooks")
	}
}

// A decline is not persisted as a "no". Persisting it would let one mis-click
// permanently disable a hook the user later wants.
func TestDeclineIsNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook-trust.json")
	store := OpenStore(path)
	if len(Runnable(Resolve(userGroups(t, trustBase), store))) != 0 {
		t.Fatal("declining runs nothing")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("declining must not write the store")
	}
	if Resolve(userGroups(t, trustBase), OpenStore(path))[0].Status != StatusPending {
		t.Fatal("a declined hook is offered again next session")
	}
}

func TestConfirmWritesExactlyOneRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook-trust.json")
	store := OpenStore(path)
	groups := userGroups(t, trustBase)
	if err := store.Confirm(groups[0]); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if err := store.Confirm(groups[0]); err != nil {
		t.Fatalf("Confirm twice: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var records []Record
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("store is not a JSON record array: %v (%s)", err, data)
	}
	if len(records) != 1 {
		t.Fatalf("confirming the same definition twice must keep one record, got %d", len(records))
	}
	if records[0].Hash != groups[0].Hash || records[0].Source != groups[0].Source {
		t.Fatalf("record = %+v", records[0])
	}
	if records[0].ConfirmedAt == "" {
		t.Error("a record must say when it was confirmed")
	}
}

func TestStoreIsNotWorldReadable(t *testing.T) {
	requirePOSIX(t)
	path := filepath.Join(t.TempDir(), "hook-trust.json")
	if err := OpenStore(path).Confirm(userGroups(t, trustBase)[0]); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("trust store mode = %v, must not be group or world accessible", info.Mode().Perm())
	}
}

// Managed hooks are the operator's, not the user's: they run without a record
// and cannot be promoted or disabled from the TUI.
func TestManagedHooksRunWithoutTheStore(t *testing.T) {
	if ManagedConfigPath() == "" {
		t.Skip("no managed provenance boundary on this platform")
	}
	groups, err := Parse([]byte(trustBase), ManagedConfigPath())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	decisions := Resolve(groups, newStore(t))
	if decisions[0].Tier != TierManaged {
		t.Fatalf("tier = %q, want managed", decisions[0].Tier)
	}
	if decisions[0].Status != StatusActive {
		t.Fatalf("status = %q, want active", decisions[0].Status)
	}
	if len(Runnable(decisions)) != 1 {
		t.Fatal("a managed hook runs without appearing in the trust store")
	}
}

func TestStorePathLivesUnderTheUserNamespace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".mivia", "hook-trust.json")
	if got := StorePath(); got != want {
		t.Fatalf("StorePath = %q, want %q", got, want)
	}
}
