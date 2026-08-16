package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

// fuzzPlanHistories are the seeded histories for FuzzPlanInvariants. All of
// them pass provider.ValidateToolPairing so the fuzzer exercises retention,
// elision, and compaction logic rather than shape rejection.
func fuzzPlanHistories() [][]provider.Message {
	callOld := plannerToolCall("call-old", "read_file", `{"path":"old.txt"}`)
	callNew := plannerToolCall("call-new", "read_file", `{"path":"new.txt"}`)
	// Oversized-unit history: the old exchange is a 10-message unit
	// (assistant with 9 calls + 9 results) that exceeds the default-8
	// recent-tail cap. Before the break fix the tail walk skipped the unit and
	// then filled the older "old objective", leaving a hole in the retained
	// optional tail. All bodies are below the elision floor so the unit
	// reaches retention unmodified.
	oldCalls := make([]provider.ToolCall, 9)
	for i := range oldCalls {
		oldCalls[i] = plannerToolCall(fmt.Sprintf("call-old-%d", i), "read_file", `{"path":"old.txt"}`)
	}
	oversized := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "old objective"},
		{Role: provider.RoleAssistant, ToolCalls: oldCalls},
	}
	for _, call := range oldCalls {
		oversized = append(oversized, provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: "result"})
	}
	oversized = append(oversized,
		provider.Message{Role: provider.RoleAssistant, Content: "done"},
		provider.Message{Role: provider.RoleUser, Content: "current objective"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
		provider.Message{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "small"},
	)
	return [][]provider.Message{
		{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "current objective"},
			{Role: provider.RoleAssistant, Content: "older answer"},
		},
		{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "old objective"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callOld}},
			{Role: provider.RoleTool, ToolCallID: callOld.ID, Name: callOld.Function.Name, Content: "result"},
			{Role: provider.RoleAssistant, Content: "done"},
			{Role: provider.RoleUser, Content: "current objective"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
			{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "small"},
		},
		{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "old objective"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callOld}},
			{Role: provider.RoleTool, ToolCallID: callOld.ID, Name: callOld.Function.Name, Content: strings.Repeat("x", 3000)},
			{Role: provider.RoleAssistant, Content: "done"},
			{Role: provider.RoleUser, Content: "current objective"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
			{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "small"},
		},
		oversized,
	}
}

// FuzzPlanInvariants asserts the planner's documented postconditions on every
// input: no panic, and on success the retained set stays tool-paired, stays
// within the budget, never grows the prompt, and keeps the latest user
// objective. It additionally asserts that the retained OPTIONAL tail is a
// contiguous suffix of the newest optional messages (DC-6): an older optional
// message kept while a newer optional unit was dropped is a hole. Errors are
// allowed; they are how the planner refuses.
func FuzzPlanInvariants(f *testing.F) {
	histories := fuzzPlanHistories()
	f.Add(int64(0), int64(65536), int64(0), true)
	f.Add(int64(1), int64(65536), int64(64), false)
	f.Add(int64(2), int64(65536), int64(64), true)
	f.Add(int64(3), int64(65536), int64(0), true)
	f.Fuzz(func(t *testing.T, historyBytes, budgetBytes, tailBytes int64, force bool) {
		mod := func(value, base int64) int { return int(((value % base) + base) % base) }
		index := mod(historyBytes, int64(len(histories)))
		budget := mod(budgetBytes, 65536) + 1
		// Map 0..66 onto the RecentTail domain: 0 is the default-8 marker,
		// 1..64 are in-range, 65 is over the cap, 66 is the negative case.
		tail := mod(tailBytes, 67)
		if tail == 66 {
			tail = -1
		}
		input := PlanInput{
			Messages:   histories[index],
			Budget:     budget,
			RecentTail: tail,
			Force:      force,
		}
		plan, err := Plan(input)
		if err != nil {
			return
		}
		if err := provider.ValidateToolPairing(plan.Messages); err != nil {
			t.Fatalf("retained messages broke tool pairing: %v", err)
		}
		if plan.AfterTokens > plan.BeforeTokens {
			t.Fatalf("AfterTokens %d > BeforeTokens %d", plan.AfterTokens, plan.BeforeTokens)
		}
		if plan.AfterTokens > budget {
			t.Fatalf("AfterTokens %d exceeds budget %d", plan.AfterTokens, budget)
		}
		if !containsPlannerMessage(plan.Messages, provider.RoleUser, "current objective") {
			t.Fatal("current objective was not retained")
		}
		// The retained optional tail must be a contiguous suffix of the newest
		// optional messages: an older optional message kept while a newer
		// optional unit was dropped is a hole (DC-6).
		if plan.Compacted && !optionalTailIsSuffix(input, plan) {
			t.Fatal("retained optional tail has a hole: an older optional message was kept while a newer optional unit was dropped")
		}
	})
}

// optionalTailIsSuffix reports whether the retained optional messages of a
// compacted plan form a contiguous suffix of the optional messages in the
// pre-retention (post-elision) state. It replicates only the deterministic
// elision pipeline (currentObjective -> mandatoryIndexes -> elideForCompaction,
// the SAME helper planCompact calls, so tool-result and reasoning elision
// cannot drift) to learn the pre-retention fingerprints; the retention
// selection itself is
// read from the real Plan result, so the check observes behavior instead of
// re-implementing the walk under test. All seeded histories have distinct
// message fingerprints, so membership and sequence comparison are exact.
func optionalTailIsSuffix(input PlanInput, plan PlanResult) bool {
	working := cloneMessages(input.Messages)
	_, objectiveIndex, err := currentObjective(working, input.CurrentObjective)
	if err != nil {
		// Plan already succeeded on this input, so the replicated pipeline
		// cannot fail; refuse to assert rather than misreport.
		return true
	}
	mandatory := mandatoryIndexes(working, objectiveIndex, input.PreserveNames)
	working, _, _, _ = elideForCompaction(working, objectiveIndex, mandatory, nil, contextstate.Principal{})
	fingerprint := func(message provider.Message) string {
		b, err := contextstate.MarshalCanonical(plannerMessages([]provider.Message{message}))
		if err != nil {
			return ""
		}
		return string(b)
	}
	mandatoryFP := make(map[string]struct{}, len(mandatory))
	origOptional := make([]string, 0, len(working))
	for index, message := range working {
		fp := fingerprint(message)
		if _, ok := mandatory[index]; ok {
			mandatoryFP[fp] = struct{}{}
			continue
		}
		origOptional = append(origOptional, fp)
	}
	retainedOptional := make([]string, 0, len(plan.Messages))
	for _, message := range plan.Messages {
		fp := fingerprint(message)
		if _, ok := mandatoryFP[fp]; ok {
			continue
		}
		retainedOptional = append(retainedOptional, fp)
	}
	if len(retainedOptional) > len(origOptional) {
		return false
	}
	suffix := origOptional[len(origOptional)-len(retainedOptional):]
	for index := range retainedOptional {
		if retainedOptional[index] != suffix[index] {
			return false
		}
	}
	return true
}

// elisionNoticeRef extracts the content ref named by an elision notice, or ""
// when the notice is the plain ref-less form. It is the non-fatal counterpart
// of the *testing.T helper elidedRefFromNotice for fuzz targets.
func elisionNoticeRef(content string) string {
	const marker = "ref:output:"
	start := strings.Index(content, marker)
	if start < 0 {
		return ""
	}
	return content[start : start+len(marker)+64]
}

// FuzzPlanSpoolInvariants asserts the H-1 spool invariant on every seeded
// history: after a plan run with a live spool, every stored body is reachable
// via a ref named in a retained message — store.Len() equals the number of
// distinct retained refs, and each retained ref loads for the grant principal.
// Retention must never orphan spooled bytes, and a failed plan must never
// spool anything (the pre-fix pipeline spooled at elision time, before
// retention ran, so both assertions fail on the old code). It also pins
// plan.AfterTokens <= budget on the spool path — the only path that installs
// refs — closing the gap where FuzzPlanInvariants asserts the budget invariant
// only with Spool=nil (H-1).
func FuzzPlanSpoolInvariants(f *testing.F) {
	histories := fuzzPlanHistories()
	f.Add(int64(0), int64(65536), int64(0), true)
	f.Add(int64(1), int64(65536), int64(64), false)
	f.Add(int64(2), int64(65536), int64(64), true)
	f.Add(int64(3), int64(65536), int64(0), true)
	f.Fuzz(func(t *testing.T, historyBytes, budgetBytes, tailBytes int64, force bool) {
		mod := func(value, base int64) int { return int(((value % base) + base) % base) }
		index := mod(historyBytes, int64(len(histories)))
		budget := mod(budgetBytes, 65536) + 1
		// Map 0..66 onto the RecentTail domain: 0 is the default-8 marker,
		// 1..64 are in-range, 65 is over the cap, 66 is the negative case.
		tail := mod(tailBytes, 67)
		if tail == 66 {
			tail = -1
		}
		store := remainder.NewMemoryStore()
		spool := remainder.NewSpool(store)
		principal, err := contextstate.NewPrincipal("workspace", "session-fuzz", "subject")
		if err != nil {
			t.Fatal(err)
		}
		input := PlanInput{
			Messages:   histories[index],
			Budget:     budget,
			RecentTail: tail,
			Force:      force,
			Spool:      spool,
			Principal:  principal,
		}
		plan, err := Plan(input)
		if err != nil {
			// Errors are how the planner refuses; with the fix nothing is
			// spooled on a failed plan, because spooling happens only after
			// retention succeeds.
			if store.Len() != 0 {
				t.Fatalf("plan failed but %d bodies were spooled", store.Len())
			}
			return
		}
		// AfterTokens <= Budget must hold on the spool path too: it is the
		// only path that installs refs (installRetainedElisionRefs). Retention
		// caps the plain-notice retained cost at target = floor(Budget/2) and
		// each retained elided tool unit costs >= 36 tokens while the ref swap
		// adds <= 33 tokens per ref, so AfterTokens <= Budget holds by
		// construction (see the comment in planCompact). FuzzPlanInvariants
		// pins the invariant only with Spool=nil, so this closes the gap for
		// the ref-installing path and guards against notice-format or
		// target-ratio drift (H-1).
		if plan.AfterTokens > budget {
			t.Fatalf("AfterTokens %d exceeds budget %d on the spool path", plan.AfterTokens, budget)
		}
		refs := make(map[string]struct{})
		for _, message := range plan.Messages {
			if ref := elisionNoticeRef(message.Content); ref != "" {
				refs[ref] = struct{}{}
			}
		}
		if store.Len() != len(refs) {
			t.Fatalf("store holds %d bodies but retained messages name %d distinct refs: a spooled body is unreachable", store.Len(), len(refs))
		}
		for ref := range refs {
			if _, err := spool.Load(context.Background(), principal.SessionID, ref); err != nil {
				t.Fatalf("retained ref %q not loadable: %v", ref, err)
			}
		}
	})
}
