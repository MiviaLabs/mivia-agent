package legacytui

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// noteSession returns a session holding one queued admission note, standing in
// for a resume that dropped a stale set or a stage that could not publish.
func noteSession(t *testing.T) *chat.Session {
	t.Helper()
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, stubAgentCompleter{})
	sess.SessionDir = t.TempDir()
	sess.SetAdmissionBinding("reader", "digest-1")
	store, err := chat.NewFileSessionStore(sess.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	if err := sess.Save("snap"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAdmission("snap", contextstate.SessionAdmission{
		Agent: "reader", Digest: "a-stale-digest", Names: []string{"grep"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Load("snap"); err != nil {
		t.Fatal(err)
	}
	return sess
}

// TestTuiToolsDialogReportsSchemaMass: the TUI is the default surface, so its
// /tools overlay owns the documented schema-mass claim.
func TestTuiToolsDialogReportsSchemaMass(t *testing.T) {
	m := newSmokeModel(t)
	m.session.Tools = tierRegistry("read_file")
	m.agentState = &cli.AgentSessionState{LastSchemaMass: cli.SchemaMass{Advertised: 4, Tokens: 321, Locked: 2, LockedTokens: 210}}
	if !m.handleTuiInfoSlash("/tools", []string{"/tools"}) {
		t.Fatal("/tools was not handled")
	}
	if m.overlay == nil {
		t.Fatal("/tools opened no overlay")
	}
	out := cli.StripANSI(strings.Join(m.overlay.lines, "\n"))
	if !strings.Contains(out, "4 tools advertised") || !strings.Contains(out, "2 locked") {
		t.Fatalf("tools overlay = %q, want the schema-mass line", out)
	}
}

// TestTuiToolsDialogWithoutAgentState: a TUI built without agent state must
// still render the tool list rather than panicking on the measurement.
func TestTuiToolsDialogWithoutAgentState(t *testing.T) {
	m := newSmokeModel(t)
	m.session.Tools = tierRegistry("read_file")
	m.agentState = nil
	if !m.handleTuiInfoSlash("/tools", []string{"/tools"}) {
		t.Fatal("/tools was not handled")
	}
	out := cli.StripANSI(strings.Join(m.overlay.lines, "\n"))
	if !strings.Contains(out, "read_file") {
		t.Fatalf("tools overlay = %q, want the tool list", out)
	}
	if strings.Contains(out, "tools advertised") {
		t.Fatalf("tools overlay = %q, want no schema-mass line without a measurement", out)
	}
}

func TestTuiSurfacesAdmissionNotes(t *testing.T) {
	m := newSmokeModel(t)
	m.session = noteSession(t)
	before := len(m.messages)
	m.appendAdmissionNotes()
	if len(m.messages) == before {
		t.Fatal("the TUI swallowed an admission note")
	}
	joined := strings.Join(m.messages, "\n")
	if !strings.Contains(joined, "grep") {
		t.Fatalf("TUI notes = %q, want the dropped tool named", joined)
	}
	before = len(m.messages)
	m.appendAdmissionNotes()
	if len(m.messages) != before {
		t.Fatal("a drained note was repeated")
	}
}

func TestSkillTurnWithResourcesRequiresTools(t *testing.T) {
	// A skill carrying resources needs a live registry to scope; a tools-off
	// session must be refused rather than handed a nil one.
	m := newSmokeModel(t)
	m.session.Tools = nil
	m.session.UseTools = false
	root := t.TempDir()
	skillDir := filepath.Join(root, "probe")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: probe\nuser-invocable: true\n---\nBody.",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"t\"\npath = \"t.md\"\nsummary = \"s\"\n",
		"t.md":           "RESOURCE",
	} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("probe")
	if !ok || len(def.Resources) == 0 {
		t.Fatalf("resource skill not loaded: ok=%v resources=%d", ok, len(def.Resources))
	}
	if _, _, err := m.prepareSkillTurn(SkillSlashSpec{definition: def}); err == nil ||
		!strings.Contains(err.Error(), "require tools") {
		t.Fatalf("error = %v, want the tools-off refusal", err)
	}
}
