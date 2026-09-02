package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Two paths execute a model's tool call, and they must agree.
//
// `dispatcherShim.Run` (internal/agent) serves a tool the model may already
// call. `serveUnadmittedTool` -> `runDeferredToolNow` (internal/chat) serves
// one that is advertised but not yet admitted. They share no Go interface -
// one is an sdktools.Tool method, the other an eight-parameter Session method
// - so nothing named them siblings, and no compiler or existing gate could
// see them drift.
//
// They drifted badly. The deferred path was written as a second
// implementation and honoured FOUR of the nine contracts the admitted path
// had accumulated. Each missing one shipped as its own bug with its own
// symptom: a turn that hung with no prompt, a call that ran and was reported
// as never having run (so the model retried it and the side effect happened
// twice), a refusal rendered as a green success, an unbounded call, a tool
// deduped against its own capability, a denylisted tool executing.
//
// Nine fixes, one defect. `.agents/memories/sibling-implementations-drift.md`
// already names this as the repository's most expensive recurring class, and
// the gate-authoring skill already prescribes the cure - "when an interface
// has more than one implementation, the gate is a conformance suite, not
// another per-implementation test". The rule did not fire here only because
// the two paths are not an interface with two implementations; they are two
// functions nobody had written down as related.
//
// This is that table. It drives both paths through the REAL attach path and
// a real session turn, and asserts on what an operator or the model can
// actually observe, never on internals. A third path must join it or its
// absence is visible.

// execPath selects which of the two implementations a case exercises.
//
// The selector is the TIER, because that is what actually routes the call: a
// core tool is admitted and reaches dispatcherShim.Run, a deferred one is not
// and reaches serveUnadmittedTool. Nothing else about the two cases differs,
// which is what makes a disagreement between them a real finding rather than
// a difference in setup.
type execPath struct {
	name string
	core bool
}

var execPaths = []execPath{
	{name: "admitted", core: true},
	{name: "deferred", core: false},
}

// tiersFor puts the probe in the core tier or leaves it deferred. read_file is
// always core so the tier feature is active in both cases (a plan with nothing
// deferred is inert by design, and would silently make both cases the same
// path).
func (p execPath) tiersFor(probe string) (core []string, effective []string) {
	if p.core {
		return []string{"read_file", probe}, []string{"read_file", "grep", probe}
	}
	return []string{"read_file"}, []string{"read_file", "grep", probe}
}

// --- the contracts -------------------------------------------------------

// C1: every tool call is bounded by a deadline.
//
// The admitted path arms one from the tool's Capability.Timeout; the deferred
// path armed nothing, so the FIRST call to a deferred run_command ran
// unbounded while the identical call one step later was bounded.
func TestEveryPathBoundsAToolCall(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &deadlineProbe{name: "probe_tool"}
			runProbeTurn(t, path, probe, `{}`)
			if !probe.ran.Load() {
				t.Fatal("the probe never executed, so this case proves nothing")
			}
			checkContract(t, "bounded-call", path.name, probe.hadDeadline.Load(),
				"this path handed the tool a context with NO deadline: a tool "+
					"that hangs hangs the turn")
			// The VALUE, not merely that some deadline exists. The dispatcher
			// arms req.Timeout itself, so a path that dropped
			// capability.Timeout and fell back to the 60s default still shows
			// a deadline - which is verbatim the failure this contract claims
			// to catch. The probe declares 30s; anything near 60 means its own
			// declaration was discarded.
			checkContract(t, "declared-timeout-honoured", path.name,
				probe.budget.Load() > 0 && probe.budget.Load() < int64(45*time.Second),
				fmt.Sprintf("the tool declared a 30s timeout and got %v; its own "+
					"declaration was dropped in favour of the default",
					time.Duration(probe.budget.Load())))
		})
	}
}

// C2: a tool's dedup declaration is honoured.
//
// ExecutionRead calls always execute fresh; Write/External dedup so a
// duplicate delivery cannot repeat a side effect. The deferred path sent
// neither SkipDedup nor Step, so a read-class tool deduped against its own
// capability and the model got a stale body back.
func TestEveryPathHonoursTheDedupDeclaration(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &countingProbe{name: "probe_tool", class: tools.ExecutionRead}
			runProbeTurnCalls(t, path, probe,
				namedCall("c1", "probe_tool", `{}`),
				namedCall("c2", "probe_tool", `{}`))
			checkContract(t, "fresh-read-executes-twice", path.name,
				probe.runs.Load() == 2,
				fmt.Sprintf("a READ-class tool executed %d time(s) for two identical "+
					"calls; its capability says reads always run fresh, so the "+
					"second was answered from a record of the first", probe.runs.Load()))
		})
	}
}

// C3: a call that RAN and failed is reported as failed, and never as one that
// did not happen.
//
// The deferred path answered every error with "authorized but was not yet
// loaded ... retry the call on your next step", so the model retried a call
// whose side effects had already landed.
func TestEveryPathReportsAFailureAsAFailure(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &failingProbe{name: "probe_tool"}
			body := runProbeTurn(t, path, probe, `{}`)
			if !probe.ran.Load() {
				t.Fatal("the probe never executed, so this case proves nothing")
			}
			ranAndSaidSo := !strings.Contains(body, "not yet loaded") &&
				!strings.Contains(body, "retry the call")
			checkContract(t, "failure-reported-as-failure", path.name, ranAndSaidSo,
				fmt.Sprintf("a tool that RAN and failed was reported to the model "+
					"as never having run, with an instruction to retry - every "+
					"side effect it already landed happens twice: %q", body))
			checkContract(t, "failure-names-its-cause", path.name,
				strings.Contains(body, "probe failed"),
				fmt.Sprintf("the model was not told why the call failed: %q", body))
		})
	}
}

// C4: an approval policy of "deny" stops the call on every path.
//
// The deferred path invoked the dispatcher directly, underneath the wrapper
// where approval lived, and executed a write tool under a deny policy.
func TestEveryPathRefusesUnderADenyPolicy(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &countingProbe{name: "probe_tool", class: tools.ExecutionWrite}
			runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.ApprovalPolicy = config.ApprovalPolicyDeny
			}, namedCall("c1", "probe_tool", `{}`))
			checkContract(t, "deny-policy-refuses", path.name, probe.runs.Load() == 0,
				"a write tool EXECUTED under a \"deny\" approval policy")
		})
	}
}

// C6: the model-facing result is capped on every path.
//
// The deferred path capped its success bodies and, for a while, not its
// failures - a caller-authored reason with no bound of its own. Capping now
// belongs to the shim both paths share, and this holds that true.
func TestEveryPathCapsTheResult(t *testing.T) {
	const cap = 200
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &bulkyProbe{name: "probe_tool", size: 8000}
			body := runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.MaxToolResultChars = cap
			}, namedCall("c1", "probe_tool", `{}`))
			// Within a small multiple of the cap, not 20x. A loose bound let a
			// mutation raise the effective cap fifteenfold and stay green. The
			// slack covers the elision notice and any ref the spool mints.
			checkContract(t, "result-capped", path.name,
				len(body) > 0 && len(body) < cap*3,
				fmt.Sprintf("the model received %d bytes for a tool whose result "+
					"cap is %d; an uncapped body blows the context window the cap "+
					"exists to protect", len(body), cap))
		})
	}
}

// C7: a tool named in ref_only_tools never inlines its body.
//
// RefOnlyTools is applied by wrapping tools IN THE SDK REGISTRY. A deferred
// tool is deliberately absent from that registry, so the wrapper never saw it
// and an operator who named one in ref_only_tools silently got the full body
// inline - the setting applying to a tool after it loaded and not before.
func TestEveryPathHonoursRefOnlyTools(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &bulkyProbe{name: "probe_tool", size: 40000}
			body := runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.RefOnlyTools = []string{"probe_tool"}
			}, namedCall("c1", "probe_tool", `{}`))
			// The BODY must be gone, not merely accompanied by a notice: a
			// wrapper that emitted the notice and then appended the 40 000
			// bytes would pass a contains-"elided" check. And a ref must have
			// been minted, or the body is unrecoverable - read_output has
			// nothing to fetch.
			checkContract(t, "ref-only-is-not-inlined", path.name,
				len(body) < 4000 && !strings.Contains(body, strings.Repeat("x", 200)),
				fmt.Sprintf("the operator named this tool in ref_only_tools and "+
					"the model received its body inline anyway (%d bytes)", len(body)))
			checkContract(t, "ref-only-mints-a-ref", path.name,
				strings.Contains(body, "read_output"),
				fmt.Sprintf("the body was elided with no ref to fetch it back, so "+
					"it is simply lost: %q", body))
		})
	}
}

// C8: a tool that does NOT declare a capability must still dedup.
//
// The unclassified default is ExecutionExternal (tools.CapabilityOf) - a tool
// that says nothing about itself is assumed to have side effects. The shim
// used `tools.Capability{}` instead, whose zero Class is ExecutionRead, so
// Dedups() was false and SkipDedup was TRUE: an unclassified tool never
// deduped, and a duplicate delivery re-ran its side effect.
//
// Unclassified is not hypothetical. workflow_run, workflow_deliver,
// post_message and every ledger tool ship without a Capability method.
func TestEveryPathDedupsAnUnclassifiedTool(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &unclassifiedProbe{name: "probe_tool"}
			runProbeTurnCalls(t, path, probe,
				namedCall("c1", "probe_tool", `{}`),
				namedCall("c2", "probe_tool", `{}`))
			checkContract(t, "unclassified-dedups", path.name, probe.runs.Load() == 1,
				fmt.Sprintf("an unclassified tool executed %d times for two "+
					"identical calls in one turn; a tool that declares nothing is "+
					"assumed to have side effects, and a duplicate delivery must "+
					"not repeat them", probe.runs.Load()))
		})
	}
}

// C9: a result is charged against the turn's batch budget on every path.
//
// Turn shaping is applied by wrapping registry tools, and a deferred tool is
// not in the registry - so a deferred result escaped the batch budget while
// the identical admitted one was charged and degraded. The budget defaults to
// derived-positive in every shipped session, so the divergence was live: the
// bound that protects the context window applied to a tool after it loaded
// and not before.
func TestEveryPathChargesTheBatchBudget(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			var degraded int
			probe := &bulkyProbe{name: "probe_tool", size: 60000}
			runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.BatchResultBudgetBytes = 4096
				f.sess.OnAgentEvent = func(e agent.Event) {
					if strings.Contains(e.Detail, "batch budget") {
						degraded++
					}
				}
			}, namedCall("c1", "probe_tool", `{}`))
			checkContract(t, "batch-budget-charged", path.name, degraded > 0,
				"a 60KB result was not charged against a 4KB batch budget, so "+
					"nothing degraded and the bound that protects the context "+
					"window did not apply to this path")
		})
	}
}

// C10: a Write-class tool called twice in one turn runs ONCE.
//
// C2 asserts the read direction (fresh every time). The direction that
// protects side effects - a duplicate delivery answered from the record
// instead of re-executing - was untested, so hardcoding SkipDedup: true in
// the shim passed the whole table while every duplicate re-ran its write.
func TestEveryPathSuppressesADuplicateWrite(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &countingProbe{name: "probe_tool", class: tools.ExecutionWrite}
			runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.ApprovalPolicy = config.ApprovalPolicyAuto
			},
				namedCall("c1", "probe_tool", `{}`),
				namedCall("c2", "probe_tool", `{}`))
			checkContract(t, "duplicate-write-suppressed", path.name,
				probe.runs.Load() == 1,
				fmt.Sprintf("a WRITE tool executed %d times for two identical "+
					"calls in one turn; a duplicate delivery must not repeat a "+
					"side effect", probe.runs.Load()))
		})
	}
}

// C11: a tool's own MaxResultBytes bounds its result.
//
// C6 only sets the session-wide cap, so a mutation making effectiveResultCap
// ignore capability.MaxResultBytes passed - and that is the bound a tool sets
// for itself, which no operator setting replaces.
func TestEveryPathHonoursTheToolsOwnResultBound(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &boundedProbe{name: "probe_tool", size: 40000, max: 300}
			body := runProbeTurnWith(t, path, probe, nil,
				namedCall("c1", "probe_tool", `{}`))
			checkContract(t, "capability-bound-honoured", path.name,
				len(body) > 0 && len(body) < 2000,
				fmt.Sprintf("the tool declared MaxResultBytes=300 and the model "+
					"received %d bytes; the tool's own bound was ignored", len(body)))
		})
	}
}

// C12: a PreToolUse hook block reaches the model with the hook's reason, and
// the tool does not run.
//
// No case in this table used hooks at all, so dropping the hook gate, or the
// advisory it hands the model, passed everything.
func TestEveryPathReportsAHookBlock(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &countingProbe{name: "probe_tool", class: tools.ExecutionRead}
			body := runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.SetDispatcher(blockingHookDispatcher(t, f))
			}, namedCall("c1", "probe_tool", `{}`))
			checkContract(t, "hook-block-stops-the-call", path.name,
				probe.runs.Load() == 0,
				"a PreToolUse hook denied the call and the tool ran anyway")
			checkContract(t, "hook-block-reason-reaches-the-model", path.name,
				strings.Contains(body, "guard refused"),
				fmt.Sprintf("the model was not told why its call was blocked: %q", body))
		})
	}
}

// C13: a hook's ADVISORY reaches the model on a call it allowed.
//
// The block contract above reads the blocked envelope, which comes from the
// dispatcher rather than from HookContext - so dropping the advisory entirely
// still passed it. This is the case that sees it: the hook allows, and the
// text it produced for the model must arrive with the result.
func TestEveryPathDeliversAHookAdvisory(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &countingProbe{name: "probe_tool", class: tools.ExecutionRead}
			body := runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.SetDispatcher(advisoryHookDispatcher(t, f))
			}, namedCall("c1", "probe_tool", `{}`))
			checkContract(t, "hook-advisory-reaches-the-model", path.name,
				strings.Contains(body, "guard advises"),
				fmt.Sprintf("a hook produced advisory text for the model and it "+
					"never arrived: %q", body))
			checkContract(t, "advised-call-still-runs", path.name,
				probe.runs.Load() == 1,
				"the hook allowed the call and it did not run")
		})
	}
}

// advisoryHookDispatcher allows every call and attaches advisory text.
func advisoryHookDispatcher(t *testing.T, f *deferredFixture) *runtime.Dispatcher {
	t.Helper()
	d, err := runtime.NewToolDispatcher(f.state.ToolBase, runtime.Policy{
		PreInvokeHook: func(context.Context, runtime.Request) runtime.HookVerdict {
			return runtime.HookVerdict{Context: "guard advises caution"}
		},
	})
	if err != nil {
		t.Fatalf("build advisory dispatcher: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// blockingHookDispatcher is a dispatcher whose PreToolUse gate denies with a
// recognisable reason.
func blockingHookDispatcher(t *testing.T, f *deferredFixture) *runtime.Dispatcher {
	t.Helper()
	// Built from the FULL tool set, so the block is what stops the call rather
	// than the dispatcher simply not knowing the tool - which is the shape my
	// first attempt had, and it reported "unknown tool" instead of the hook's
	// reason.
	d, err := runtime.NewToolDispatcher(f.state.ToolBase, runtime.Policy{
		PreInvokeHook: func(context.Context, runtime.Request) runtime.HookVerdict {
			return runtime.HookVerdict{
				Denied: true, Reason: "guard refused this call",
				Runs: []runtime.HookRun{{Event: "PreToolUse", Program: "guard.sh", Denied: true}},
			}
		},
	})
	if err != nil {
		t.Fatalf("build hook dispatcher: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// --- the harness ---------------------------------------------------------

func runProbeTurn(t *testing.T, path execPath, probe tools.Tool, args string) string {
	t.Helper()
	return runProbeTurnCalls(t, path, probe, namedCall("c1", probe.Name(), args))
}

func runProbeTurnCalls(t *testing.T, path execPath, probe tools.Tool, calls ...provider.ToolCall) string {
	t.Helper()
	return runProbeTurnWith(t, path, probe, nil, calls...)
}

// runProbeTurnWith drives ONE real session turn in which the model calls the
// probe, and returns the tool-result body the model was handed.
//
// It goes through attachSessionDispatcher and Session.SendUser - the same
// entry points a user session uses - so a contract honoured only by a test
// seam cannot pass here.
func runProbeTurnWith(t *testing.T, path execPath, probe tools.Tool, tune func(*deferredFixture), calls ...provider.ToolCall) string {
	t.Helper()
	completer := &scriptedCompleter{turns: []provider.Response{
		toolCallResponse(calls...),
		{Content: "done"},
	}}
	core, effective := path.tiersFor(probe.Name())
	fixture := newDeferredFixture(t, completer, core, effective, probe)
	// Auto by default so approval is not a confound in the cases that are
	// about something else. An UNSET policy is not neutral here: the two
	// paths disagree about what it means, which is its own case below.
	fixture.sess.ApprovalPolicy = config.ApprovalPolicyAuto
	if tune != nil {
		tune(fixture)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "use the probe", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	return toolResultBodies(completer)
}

// toolResultBodies is every tool-role message the model received, which is
// where a path's answer about a call actually lands.
func toolResultBodies(c *scriptedCompleter) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, msgs := range c.messages {
		for _, m := range msgs {
			if m.Role == provider.RoleTool {
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// --- probes --------------------------------------------------------------

type deadlineProbe struct {
	name        string
	ran         atomic.Bool
	hadDeadline atomic.Bool
	// budget is the deadline's distance from now, so a contract can assert
	// the tool's DECLARED timeout was used and not merely that some deadline
	// exists.
	budget atomic.Int64
}

func (p *deadlineProbe) Name() string               { return p.name }
func (p *deadlineProbe) Description() string        { return "records its deadline" }
func (p *deadlineProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *deadlineProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionExternal, Timeout: 30 * time.Second}
}
func (p *deadlineProbe) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	p.ran.Store(true)
	deadline, ok := ctx.Deadline()
	p.hadDeadline.Store(ok)
	if ok {
		p.budget.Store(int64(time.Until(deadline)))
	}
	return "probe ok", nil
}

type countingProbe struct {
	name  string
	class tools.ExecutionClass
	runs  atomic.Int32
}

func (p *countingProbe) Name() string               { return p.name }
func (p *countingProbe) Description() string        { return "counts its runs" }
func (p *countingProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *countingProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: p.class}
}
func (p *countingProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return fmt.Sprintf("run %d", p.runs.Add(1)), nil
}

type failingProbe struct {
	name string
	ran  atomic.Bool
}

func (p *failingProbe) Name() string               { return p.name }
func (p *failingProbe) Description() string        { return "always fails" }
func (p *failingProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *failingProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionExternal}
}
func (p *failingProbe) Execute(context.Context, json.RawMessage) (string, error) {
	p.ran.Store(true)
	return "", fmt.Errorf("probe failed")
}

// C5: the two paths must agree about what an UNSET approval policy means.
//
// They do not. sdkadapter.NeedsApprovalLayer treats an empty policy as "this
// caller opted out of the approval layer entirely", so the admitted path runs
// the call. decideDeferredApproval defaults an empty policy to write-only, so
// the deferred path demands approval, finds no gate, and refuses.
//
// The operator sees a tool that works once it has been loaded and is denied
// before that, with a message about an approver they never configured. Both
// behaviours are defensible on their own; having both in one session is not,
// and no per-path test could see it.
func TestEveryPathAgreesOnAnUnsetApprovalPolicy(t *testing.T) {
	var ran []string
	for _, path := range execPaths {
		probe := &countingProbe{name: "probe_tool", class: tools.ExecutionExternal}
		runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
			f.sess.ApprovalPolicy = "" // exactly as an unconfigured session arrives
		}, namedCall("c1", "probe_tool", `{}`))
		if probe.runs.Load() > 0 {
			ran = append(ran, path.name)
		}
	}
	checkContract(t, "unset-approval-policy-agrees", "all",
		len(ran) == 0 || len(ran) == len(execPaths),
		fmt.Sprintf("with no approval policy configured, the call executed on %v "+
			"and not on the other path(s); the same session then answers the "+
			"same tool call two different ways depending only on whether the "+
			"tool had been loaded yet", ran))
}

const conformancePolicyPath = "../../.mivia/policy/tool-execution-conformance.json"

// checkContract records one contract result against the declared divergences.
//
// It fails BOTH ways on purpose. An undeclared divergence fails, which is the
// gate. A declared one that now HOLDS also fails, because a stale entry is
// how an exemption list stops describing the code and starts hiding the next
// real divergence - the same rule the viewer-surface table uses.
func checkContract(t *testing.T, contract, path string, holds bool, failure string) {
	t.Helper()
	consultedExemptions.Store(contract+"/"+path, true)
	reason, declared := conformanceExemptions(t)[contract+"/"+path]
	switch {
	case holds && declared:
		t.Errorf("%s/%s is declared as a divergence in %s, but it HOLDS now. "+
			"Delete the entry: a stale exemption hides the next real one.\n"+
			"The declared reason was: %s", contract, path, conformancePolicyPath, reason)
	case !holds && declared:
		t.Logf("%s/%s: declared divergence, accepted. %s", contract, path, reason)
	case !holds:
		t.Errorf("%s/%s: %s\n\nThe two tool-execution paths must agree. If this "+
			"one genuinely may not, declare it in %s WITH A REASON - an entry "+
			"with no reason is a bypass with extra steps.",
			contract, path, failure, conformancePolicyPath)
	}
}

func conformanceExemptions(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(conformancePolicyPath)
	if err != nil {
		t.Fatalf("read %s: %v", conformancePolicyPath, err)
	}
	var policy struct {
		Exempt map[string]string `json:"exempt"`
	}
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatalf("parse %s: %v", conformancePolicyPath, err)
	}
	for key, reason := range policy.Exempt {
		if len(strings.TrimSpace(reason)) < 40 {
			t.Errorf("%s declares %s with a reason too short to be one (%q); an "+
				"exemption that says nothing is a bypass with extra steps",
				conformancePolicyPath, key, reason)
		}
	}
	return policy.Exempt
}

type bulkyProbe struct {
	name string
	size int
}

func (p *bulkyProbe) Name() string               { return p.name }
func (p *bulkyProbe) Description() string        { return "returns a lot" }
func (p *bulkyProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *bulkyProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (p *bulkyProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return strings.Repeat("x", p.size), nil
}

// unclassifiedProbe implements no Capability method at all, like every
// workflow ledger tool and the messaging tools.
type unclassifiedProbe struct {
	name string
	runs atomic.Int32
}

func (p *unclassifiedProbe) Name() string               { return p.name }
func (p *unclassifiedProbe) Description() string        { return "declares nothing" }
func (p *unclassifiedProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *unclassifiedProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return fmt.Sprintf("run %d", p.runs.Add(1)), nil
}

// consultedExemptions records every contract/path key a case actually checked,
// so the orphan sweep below can tell a live exemption from dead weight.
var consultedExemptions sync.Map

// Every declared divergence must belong to a contract that still runs.
//
// The staleness check inside checkContract only fires for a case that
// EXECUTES: rename a contract id or delete its test, and the exemption becomes
// dead weight with no signal - and worse, silently pre-exempts any future
// contract that reuses the id. A review found that hole; nothing here had ever
// asserted the policy file describes the suite.
//
// It runs last (the name sorts after every TestEveryPath* case, and `go test`
// runs a file's tests in source order within a package) and reads what those
// cases recorded.
func TestZZZNoDeclaredDivergenceIsOrphaned(t *testing.T) {
	var orphans []string
	for key, reason := range conformanceExemptions(t) {
		if _, consulted := consultedExemptions.Load(key); !consulted {
			orphans = append(orphans, fmt.Sprintf("%s (%.60s...)", key, reason))
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("these divergences are declared in %s but no contract checked "+
			"them: %v\n\nA contract was renamed or deleted and its exemption "+
			"outlived it. Delete the entry: it proves nothing now, and it will "+
			"silently pre-exempt the next contract that reuses the id.",
			conformancePolicyPath, orphans)
	}
}

// boundedProbe declares its OWN result bound via Capability.MaxResultBytes.
type boundedProbe struct {
	name string
	size int
	max  int
}

func (p *boundedProbe) Name() string               { return p.name }
func (p *boundedProbe) Description() string        { return "declares its own bound" }
func (p *boundedProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *boundedProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, MaxResultBytes: p.max}
}
func (p *boundedProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return strings.Repeat("x", p.size), nil
}
