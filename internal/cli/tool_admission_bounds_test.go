package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestMeasureSchemaMassExcludesAdmittedTools: once a deferred tool is admitted
// its schema is inside the advertised total, so counting it as withheld reports
// the same tokens twice and tells the operator the tier is still saving them.
func TestMeasureSchemaMassExcludesAdmittedTools(t *testing.T) {
	base := tierRegistry("read_file", "grep", "glob")
	advertised := tierRegistry("read_file", "grep")
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep"}, {Name: "glob"}}}
	held := measureSchemaMass(advertised, base, plan, nil, "reader", "attach")
	mass := measureSchemaMass(advertised, base, plan, []string{"grep"}, "reader", "tool_admission")
	if mass.Deferred != 1 {
		t.Fatalf("deferred = %d, want only the still-withheld tool counted", mass.Deferred)
	}
	if mass.HeldTokens >= held.HeldTokens {
		t.Fatalf("held tokens = %d, want less than the pre-admission %d", mass.HeldTokens, held.HeldTokens)
	}
	all := measureSchemaMass(advertised, base, plan, []string{"grep", "glob"}, "reader", "tool_admission")
	if all.Deferred != 0 || all.HeldTokens != 0 {
		t.Fatalf("mass = %+v, want nothing withheld once everything is admitted", all)
	}
	if strings.Contains(all.String(), "deferred") {
		t.Fatalf("operator line still claims a deferred tier: %q", all.String())
	}
}

// TestSchemaMassStopsCountingAnAdmittedToolAsWithheld is the same property
// end to end: the advertised total grows by exactly what leaves the held total.
func TestSchemaMassStopsCountingAnAdmittedToolAsWithheld(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	before := fixture.state.schemaMassSnapshot()
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	after := fixture.state.schemaMassSnapshot()
	if after.Deferred != before.Deferred-1 {
		t.Fatalf("deferred = %d, want one fewer than the pre-admission %d", after.Deferred, before.Deferred)
	}
	grew := after.Tokens - before.Tokens
	freed := before.HeldTokens - after.HeldTokens
	if grew <= 0 || grew != freed {
		t.Fatalf("advertised grew by %d but the held total dropped by %d; the admitted schema is counted twice", grew, freed)
	}
}

// --- the attempt bound reaches every load_tools exit ---------------------

// TestLoadToolsChargesTheAttemptBoundOnFailedCalls: the bound exists to stop a
// model looping on load_tools. A call that never reaches the staging path is
// still an attempt, and used to be free.
func TestLoadToolsChargesTheAttemptBoundOnFailedCalls(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	tool := &loadToolsTool{session: sess, candidates: []tools.TierCandidate{{Name: "grep"}}}
	var last error
	for i := 0; i <= tools.MaxAdmissionAttempts; i++ {
		_, last = tool.Execute(context.Background(), json.RawMessage(`{"names":["no_such_tool"]}`))
		if last == nil {
			t.Fatalf("attempt %d: an unknown name was accepted", i)
		}
	}
	if !strings.Contains(last.Error(), "exhausted") {
		t.Fatalf("error after %d failing calls = %v, want the attempt bound", tools.MaxAdmissionAttempts+1, last)
	}
}

func TestLoadToolsChargesTheAttemptBoundOnMalformedArguments(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	tool := &loadToolsTool{session: sess, candidates: []tools.TierCandidate{{Name: "grep"}}}
	for i := 0; i < tools.MaxAdmissionAttempts; i++ {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`not json`)); err == nil {
			t.Fatalf("attempt %d: malformed arguments were accepted", i)
		}
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`))
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("error = %v, want the bound already spent by the malformed calls", err)
	}
}

func TestMeasureSchemaMassSkipsAnAdmittedToolMissingFromTheBase(t *testing.T) {
	// An admitted name is skipped before the base lookup, so a plan naming a
	// tool the base no longer holds cannot inflate the withheld figure.
	base := tierRegistry("read_file")
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep"}, {Name: "gone"}}}
	mass := measureSchemaMass(tierRegistry("read_file"), base, plan, []string{"grep"}, "reader", "attach")
	if mass.Deferred != 1 {
		t.Fatalf("deferred = %d, want only the unadmitted candidate counted", mass.Deferred)
	}
	if mass.HeldTokens != 0 {
		t.Fatalf("held tokens = %d, want 0 when the remaining candidate is absent from the base", mass.HeldTokens)
	}
}

func TestMeasureSchemaMassWithoutABaseRegistry(t *testing.T) {
	// The advertised surface can still be priced when no pre-scope base is
	// available; only the withheld figure is unknowable.
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep"}}}
	mass := measureSchemaMass(tierRegistry("read_file"), nil, plan, nil, "reader", "attach")
	if mass.Deferred != 1 {
		t.Fatalf("deferred = %d, want the candidate still counted", mass.Deferred)
	}
	if mass.HeldTokens != 0 {
		t.Fatalf("held tokens = %d, want 0 with no base to price against", mass.HeldTokens)
	}
}
