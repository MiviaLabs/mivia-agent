package cliagents

// memory_support_coverage_test.go covers the small memory-state
// accessors in memory_support.go that legacytui consumes only through
// the dispatch wiring.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestMemoryOfAndMemoryConfigOfNilSafe(t *testing.T) {
	if got := MemoryOf(nil); got != nil {
		t.Errorf("MemoryOf(nil) = %v, want nil", got)
	}
	if got := MemoryConfigOf(nil); got.InjectCore {
		t.Errorf("MemoryConfigOf(nil).InjectCore must be false, got %v", got.InjectCore)
	}
}

func TestCoreMemoryBlockForStateAndOpts(t *testing.T) {
	// Both helpers must not panic on nil state/opts.
	if got := CoreMemoryBlockForState(nil); got != "" {
		t.Errorf("CoreMemoryBlockForState(nil) = %q, want empty", got)
	}
	if got := CoreMemoryBlockForOpts(SessionDispatcherOpts{}); got != "" {
		t.Errorf("CoreMemoryBlockForOpts(empty) = %q, want empty", got)
	}
}

func TestOpenMemoryStoreRejectsMissingPath(t *testing.T) {
	// OpenMemoryStore with an empty path must not panic.
	_, _ = OpenMemoryStoreWithReadOnly("", config.MemoryConfig{}, false)
	// And with a temp dir but invalid backend must error.
	_, err := OpenMemoryStoreWithReadOnly(t.TempDir(), config.MemoryConfig{StoreBackend: "garbage"}, false)
	if err == nil {
		t.Fatal("OpenMemoryStoreWithReadOnly(garbage backend) must error")
	}
}

func TestOpenMemoryStoreWithReadOnlyPathEscape(t *testing.T) {
	// A relative store_path escaping the workspace root must be
	// rejected (lines 27-29).
	_, err := OpenMemoryStoreWithReadOnly(t.TempDir(), config.MemoryConfig{StorePath: "../escape"}, false)
	if err == nil {
		t.Fatal("OpenMemoryStoreWithReadOnly(../escape) must error")
	}
	// A relative store_path inside the root is joined to the root
	// and opened (the backend may then fail; we only exercise the
	// join branch, line 30).
	_, _ = OpenMemoryStoreWithReadOnly(t.TempDir(), config.MemoryConfig{StorePath: "inside.db"}, true)
}

func TestOpenMemoryStoreWithReadOnlyHardensAdHocStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; file modes always read back as 0666/0777")
	}
	root := t.TempDir()
	adHocPath := config.TempStorePath(root, "memory")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(adHocPath)) })
	mc := config.MemoryConfig{StorePath: adHocPath}
	store, err := OpenMemoryStoreWithReadOnly(root, mc, false)
	if err != nil {
		t.Fatalf("OpenMemoryStoreWithReadOnly = %v, want nil", err)
	}
	defer store.Close()
	st, err := os.Stat(adHocPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("ad-hoc store mode = %o, want 600", perm)
	}
	dirSt, err := os.Stat(filepath.Dir(adHocPath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirSt.Mode().Perm(); perm != 0o700 {
		t.Errorf("ad-hoc store dir mode = %o, want 700", perm)
	}
}

// TestOpenMemoryStoreWithReadOnlyHardensUncleanTempPath is the F1 review
// finding: the hardening gate compares the resolved path against
// TempStorePath by string equality. TempStorePath is Join-cleaned, but an
// operator-supplied absolute path is not - so a store_path that names the
// temp store with a dot-dot or double-slash segment must still match the
// gate and get the 0600/0700 treatment. Before the Clean fix this test
// failed: the file was created 0644 in a 0755 directory under the shared
// temp dir.
func TestOpenMemoryStoreWithReadOnlyHardensUncleanTempPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; file modes always read back as 0666/0777")
	}
	root := t.TempDir()
	tempPath := config.TempStorePath(root, "memory")
	// String concatenation, not filepath.Join: Join would clean the x/..
	// segment away and the test would never exercise an unclean spelling.
	unclean := filepath.Dir(tempPath) + string(filepath.Separator) + "x" +
		string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(tempPath)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(config.TempStorePath(root, "memory"))) })
	mc := config.MemoryConfig{StorePath: unclean}
	store, err := OpenMemoryStoreWithReadOnly(root, mc, false)
	if err != nil {
		t.Fatalf("OpenMemoryStoreWithReadOnly = %v, want nil", err)
	}
	defer store.Close()
	resolved := filepath.Clean(unclean)
	if resolved != config.TempStorePath(root, "memory") {
		t.Fatalf("test setup: cleaned path %q is not the temp store %q", resolved, config.TempStorePath(root, "memory"))
	}
	st, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("unclean temp store_path mode = %o, want 600", perm)
	}
	dirSt, err := os.Stat(filepath.Dir(resolved))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirSt.Mode().Perm(); perm != 0o700 {
		t.Errorf("unclean temp store_path dir mode = %o, want 700", perm)
	}
}

func TestOpenMemoryStoreWithReadOnlyStorePathDoesNotHarden(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; file modes always read back as 0666/0777")
	}
	root := t.TempDir()
	storePath := filepath.Join(root, "custom-memory.db")
	mc := config.MemoryConfig{StorePath: storePath}
	store, err := OpenMemoryStoreWithReadOnly(root, mc, false)
	if err != nil {
		t.Fatalf("OpenMemoryStoreWithReadOnly = %v, want nil", err)
	}
	defer store.Close()
	st, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm == 0o600 {
		t.Errorf("explicit store_path mode = %o, must not be forced to 600", perm)
	}
	dirSt, err := os.Stat(filepath.Dir(storePath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirSt.Mode().Perm(); perm == 0o700 {
		t.Errorf("explicit store_path dir mode = %o, must not be forced to 700", perm)
	}
}

// TestSameFilePath pins the OS-aware file-path comparison behind the
// hardening gate. Windows filesystems match case-insensitively, so a
// case-differing spelling that names the temp store must still pass the
// gate; elsewhere case alone distinguishes paths. goos is a parameter, not
// runtime.GOOS, so the Windows branch is testable from every OS - the chmod
// e2e tests skip on Windows, which left the gate's Windows behavior
// untested everywhere before this seam existed.
func TestSameFilePath(t *testing.T) {
	cases := []struct {
		name string
		goos string
		a    string
		b    string
		want bool
	}{
		{name: "identical paths match everywhere", goos: "linux", a: "/tmp/mivia/m.db", b: "/tmp/mivia/m.db", want: true},
		{name: "identical windows paths match", goos: "windows", a: `C:\Temp\mivia\m.db`, b: `C:\Temp\mivia\m.db`, want: true},
		{name: "case folds on windows", goos: "windows", a: `C:\Temp\Mivia\M.DB`, b: `c:\temp\mivia\m.db`, want: true},
		{name: "case matters on unix", goos: "linux", a: "/tmp/Mivia/m.db", b: "/tmp/mivia/m.db", want: false},
		{name: "cleaned dotdot matches on windows", goos: "windows", a: `C:\Temp\x\..\mivia\m.db`, b: `C:\Temp\mivia\m.db`, want: true},
		{name: "cleaned dotdot matches on unix", goos: "linux", a: "/tmp/x/../mivia/m.db", b: "/tmp/mivia/m.db", want: true},
		{name: "different paths never match", goos: "windows", a: `C:\Temp\other\m.db`, b: `C:\Temp\mivia\m.db`, want: false},
		{name: "unix backslash folds to a separator - the documented limit", goos: "linux", a: "/tmp/mivia\\m.db", b: "/tmp/mivia/m.db", want: true},
		{name: "trailing separator normalizes away", goos: "linux", a: "/tmp/mivia/m.db/", b: "/tmp/mivia/m.db", want: true},
		{name: "empty paths never match", goos: "linux", a: "", b: "/tmp/mivia/m.db", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameFilePath(tc.goos, tc.a, tc.b); got != tc.want {
				t.Errorf("SameFilePath(%q, %q, %q) = %v, want %v", tc.goos, tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestLoadAdHocWorkspaceStoreHardensThroughOpen binds the whole chain the
// hardening depends on: config.Load on a directory with no project config
// fills StorePath with the temp-tier default, and the SAME as-loaded config
// handed to OpenMemoryStoreWithReadOnly opens a hardened store. The
// half-tests on each side (the resolveMemoryConfig tier tests in
// internal/config, and the hand-built hardening tests below) would both
// keep passing if the two sides ever disagreed about the key or the root -
// the exact silent un-hardening class the Clean fix (d71dde0f) closed for
// operator spellings. This pin fails when they drift. Review finding 4a.
func TestLoadAdHocWorkspaceStoreHardensThroughOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; file modes always read back as 0666/0777")
	}
	root := t.TempDir()
	// Explicit ConfigPath keeps the real user config (and any org_id in it)
	// out of the load; the minimal provider block is what Load's validation
	// requires, and it names no [memory] section, so the three-tier fill
	// runs with defaults.
	cfgPath := filepath.Join(root, "mivia.toml")
	minimal := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{ name = \"deepseek-v4-flash\", context_window_tokens = 128000 }]\n"
	if err := os.WriteFile(cfgPath, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath, WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
	if res.Memory.StorePath != "" {
		t.Fatalf("Load filled StorePath = %q, want empty for Markdown memory", res.Memory.StorePath)
	}
	if res.Memory.OrgID != "" {
		t.Fatalf("org_id = %q from a config that names none; the user config leaked into the load", res.Memory.OrgID)
	}
	store, err := OpenMemoryStoreWithReadOnly(root, res.Memory, false)
	if err != nil {
		t.Fatalf("OpenMemoryStoreWithReadOnly = %v, want nil", err)
	}
	defer store.Close()
	st, err := os.Stat(workspace.GlobalContextStorePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("as-loaded temp store mode = %o, want 600", perm)
	}
	dirSt, err := os.Stat(filepath.Dir(workspace.GlobalContextStorePath(root)))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirSt.Mode().Perm(); perm != 0o700 {
		t.Errorf("as-loaded temp store dir mode = %o, want 700", perm)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(config.TempStorePath(root, "memory"))) })
}

func TestAgentSessionStateDisplayHelpers(t *testing.T) {
	// DisplaySource and CurrentAgentName on nil and on a selected
	// agent: nil-safe reads that the TUI dialog renders.
	var nilState *AgentSessionState
	if got := nilState.DisplaySource(); got != string(config.AgentSourceBuiltIn) {
		t.Errorf("DisplaySource(nil) = %q", got)
	}
	state := &AgentSessionState{}
	if got := state.DisplaySource(); got != string(config.AgentSourceBuiltIn) {
		t.Errorf("DisplaySource(no selection) = %q, want %q", got, string(config.AgentSourceBuiltIn))
	}
	if got := state.DisplayName(); got != config.RootAgentName {
		t.Errorf("DisplayName(no selection) = %q, want %q", got, config.RootAgentName)
	}
}
