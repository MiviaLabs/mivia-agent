package cliagents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestMeasureSchemaMassExcludesAdmittedTools: once a deferred tool is admitted
// it is no longer locked, but it stays inside the advertised total - it was
// advertised (and priced) before admission too (plan tools-advertising/01).
func TestMeasureSchemaMassExcludesAdmittedTools(t *testing.T) {
	base := tierRegistry("read_file", "grep", "glob")
	advertised := tierRegistry("read_file", "grep", "glob").OpenAITools()
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep"}, {Name: "glob"}}}
	held := measureSchemaMass(advertised, base, plan, nil, "reader", "attach")
	mass := measureSchemaMass(advertised, base, plan, []string{"grep"}, "reader", "tool_admission")
	if mass.Locked != 1 {
		t.Fatalf("locked = %d, want only the still-locked tool counted", mass.Locked)
	}
	if mass.LockedTokens >= held.LockedTokens {
		t.Fatalf("locked tokens = %d, want less than the pre-admission %d", mass.LockedTokens, held.LockedTokens)
	}
	if mass.Advertised != held.Advertised || mass.Tokens != held.Tokens {
		t.Fatalf("advertised total changed across admission (mass=%+v, held=%+v); the wire tools[] must stay pinned", mass, held)
	}
	all := measureSchemaMass(advertised, base, plan, []string{"grep", "glob"}, "reader", "tool_admission")
	if all.Locked != 0 || all.LockedTokens != 0 {
		t.Fatalf("mass = %+v, want nothing locked once everything is admitted", all)
	}
	if strings.Contains(all.String(), "locked") {
		t.Fatalf("operator line still claims a locked tier: %q", all.String())
	}
}

// TestSchemaMassStopsCountingAnAdmittedToolAsWithheld pins the wire-level
// point of plan tools-advertising/01: admitting a tool moves it out of the
// locked count, but the advertised total (what serializes onto the wire)
// does NOT change - it already included every deferred candidate before
// admission, which is exactly what keeps the provider's prompt-cache prefix
// stable across a load_tools call.
func TestSchemaMassStopsCountingAnAdmittedToolAsWithheld(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	before := fixture.state.SchemaMassSnapshot()
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	after := fixture.state.SchemaMassSnapshot()
	if after.Locked != before.Locked-1 {
		t.Fatalf("locked = %d, want one fewer than the pre-admission %d", after.Locked, before.Locked)
	}
	if after.Tokens != before.Tokens || after.Advertised != before.Advertised {
		t.Fatalf("advertised total changed on admission (before=%+v after=%+v); the wire tools[] must stay byte-stable", before, after)
	}
	if after.LockedTokens >= before.LockedTokens {
		t.Fatalf("locked tokens = %d, want less than the pre-admission %d", after.LockedTokens, before.LockedTokens)
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
	// tool the base no longer holds cannot inflate the locked figure.
	base := tierRegistry("read_file")
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep"}, {Name: "gone"}}}
	mass := measureSchemaMass(tierRegistry("read_file").OpenAITools(), base, plan, []string{"grep"}, "reader", "attach")
	if mass.Locked != 1 {
		t.Fatalf("locked = %d, want only the unadmitted candidate counted", mass.Locked)
	}
	if mass.LockedTokens != 0 {
		t.Fatalf("locked tokens = %d, want 0 when the remaining candidate is absent from the base", mass.LockedTokens)
	}
}

func TestMeasureSchemaMassWithoutABaseRegistry(t *testing.T) {
	// The advertised surface can still be priced when no pre-scope base is
	// available; only the locked figure is unknowable.
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep"}}}
	mass := measureSchemaMass(tierRegistry("read_file").OpenAITools(), nil, plan, nil, "reader", "attach")
	if mass.Locked != 1 {
		t.Fatalf("locked = %d, want the candidate still counted", mass.Locked)
	}
	if mass.LockedTokens != 0 {
		t.Fatalf("locked tokens = %d, want 0 with no base to price against", mass.LockedTokens)
	}
}

// TestLoadToolsSurfacesThePublicationBound: the publication bound is charged
// inside StageToolAdmission, so the tool has to hand its refusal back to the
// model rather than reporting a successful load.
func TestLoadToolsSurfacesThePublicationBound(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	effective := []string{"read_file", "list_dir", "grep", "glob", "search", "fetch_url",
		"find_references", "write_file", "search_replace", "multi_edit"}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, effective)
	tool, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	if !ok {
		t.Fatal("load_tools is not registered")
	}
	// Take the names from the live plan: which catalogue tools actually
	// register depends on the environment (an API key, a run allowlist).
	var deferred []string
	for _, candidate := range fixture.state.TierPlan.Candidates {
		deferred = append(deferred, candidate.Name)
	}
	if len(deferred) <= tools.MaxAdmissionPublications {
		t.Fatalf("fixture deferred %d tools, need more than %d", len(deferred), tools.MaxAdmissionPublications)
	}
	for i := 0; i < tools.MaxAdmissionPublications; i++ {
		if _, err := tool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"names":[%q]}`, deferred[i]))); err != nil {
			t.Fatalf("publication %d: %v", i, err)
		}
		// A turn boundary publishes the stage and charges the bound.
		if _, err := fixture.sess.SendUser(context.Background(), "go", io.Discard); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	_, err := tool.Execute(context.Background(),
		json.RawMessage(fmt.Sprintf(`{"names":[%q]}`, deferred[tools.MaxAdmissionPublications])))
	if err == nil || !strings.Contains(err.Error(), "surface widenings") {
		t.Fatalf("error = %v, want the publication bound surfaced to the model", err)
	}
}

// --- the names array is bounded -----------------------------------------

// TestLoadToolsNamesDeclaresAnItemBound: the shared validator enforces
// minItems/maxItems, but only for tools that declare them. Without a bound the
// model can hand load_tools an unbounded array.
func TestLoadToolsNamesDeclaresAnItemBound(t *testing.T) {
	tool := &loadToolsTool{}
	props, ok := tool.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatal("parameters carry no properties object")
	}
	names, ok := props["names"].(map[string]any)
	if !ok {
		t.Fatal("parameters declare no names property")
	}
	maxItems, ok := names["maxItems"].(float64)
	if !ok {
		t.Fatalf("names = %v, want a float64 maxItems the shared validator reads", names)
	}
	if maxItems <= 0 || maxItems > 1000 {
		t.Fatalf("maxItems = %v, want a bound that is real but comfortably above any deferred set", maxItems)
	}
}

// TestLoadToolsUnknownNamesErrorIsBounded: the unknown list is echoed back
// verbatim, so an O(n) amplification of model-supplied text ends up durably
// written to the content store even though the model-visible copy is capped.
func TestLoadToolsUnknownNamesErrorIsBounded(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	tool := &loadToolsTool{session: sess, candidates: []tools.TierCandidate{{Name: "grep"}}}
	names := make([]string, 10000)
	for i := range names {
		names[i] = strings.Repeat("x", 120)
	}
	_, err := tool.resolveRequested(loadToolsArgs{Names: names})
	if err == nil {
		t.Fatal("10000 unknown names were accepted")
	}
	if len(err.Error()) > 8000 {
		t.Fatalf("error is %d bytes; the echoed unknown list is unbounded", len(err.Error()))
	}
}
