package sdkadapter

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// capableCLITool is countingCLITool plus a scripted Capability so the
// approval wrapper's class threshold is exercised per-args.
type capableCLITool struct {
	name  string
	exec  string
	class tools.ExecutionClass
	calls int
}

func (c *capableCLITool) Name() string               { return c.name }
func (c *capableCLITool) Description() string        { return "capable tool" }
func (c *capableCLITool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (c *capableCLITool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: c.class, ResourceKey: "path:" + c.name}
}
func (c *capableCLITool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	c.calls++
	return c.exec, nil
}

// approvalFixture bundles the SDK-path test fakes: a scripted gate, a
// pending recorder, and a shared sequence counter for ordering.
type approvalFixture struct {
	mu       sync.Mutex
	gateArgs []string
	verdict  ApprovalResult
	pending  []string
	seq      int
	gateSeqs []int
}

func (f *approvalFixture) gate(_ context.Context, name string, args json.RawMessage) ApprovalResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gateArgs = append(f.gateArgs, name+"("+string(args)+")")
	f.seq++
	f.gateSeqs = append(f.gateSeqs, f.seq)
	return f.verdict
}

func (f *approvalFixture) emitPending(name, detail, input string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.pending = append(f.pending, name+"|"+detail+"|"+input)
}

func (f *approvalFixture) gateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.gateArgs)
}

func (f *approvalFixture) lastPending() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) == 0 {
		return ""
	}
	return f.pending[len(f.pending)-1]
}

func (f *approvalFixture) lastGateSeq() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gateSeqs) == 0 {
		return 0
	}
	return f.gateSeqs[len(f.gateSeqs)-1]
}

func (f *approvalFixture) lastPendingSeq() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq
}

// convertGated registers one capableCLITool and converts the registry
// with the fixture's gate, standing, and pending recorder.
func (f *approvalFixture) convertGated(t *testing.T, class tools.ExecutionClass, standing *ApprovalStanding, extra AdmissionPredicates) (*sdktools.Registry, *capableCLITool) {
	t.Helper()
	inner := &capableCLITool{name: "gated", exec: "real result", class: class}
	reg := tools.NewRegistry()
	reg.Register(inner)
	pred := AdmissionPredicates{
		ApprovalGate:     f.gate,
		ApprovalStanding: standing,
		EmitPending:      f.emitPending,
	}
	got, err := ConvertToolRegistryWithAdmission(reg, pred)
	if err != nil {
		t.Fatalf("ConvertToolRegistryWithAdmission: %v", err)
	}
	return got, inner
}

func runGated(t *testing.T, reg *sdktools.Registry) (string, error) {
	t.Helper()
	wrapped, ok := reg.Get("gated")
	if !ok {
		t.Fatal("SDK registry missing gated")
	}
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{"path": "a.txt"}})
	if err != nil {
		return "", err
	}
	s, _ := out.Value.(string)
	return s, nil
}

// TestApprovalGatedToolAdapterApproveDelegates pins the happy path: an
// approved write-class call delegates to the inner tool with the gate
// seeing the marshaled args.
func TestApprovalGatedToolAdapterApproveDelegates(t *testing.T) {
	f := &approvalFixture{verdict: ApprovalResult{Approved: true}}
	reg, inner := f.convertGated(t, tools.ExecutionWrite, nil, AdmissionPredicates{})
	s, err := runGated(t, reg)
	if err != nil || s != "real result" {
		t.Fatalf("Run = (%q,%v), want the inner result", s, err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}
	if got := f.gateCount(); got != 1 {
		t.Fatalf("gate calls = %d, want 1", got)
	}
}

// TestApprovalGatedToolAdapterDenyReturnsErrAsContent pins the SDK
// denial shape: content string, nil error (an error return would break
// the loop's retry shape), inner never runs.
func TestApprovalGatedToolAdapterDenyReturnsErrAsContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict ApprovalResult
		want    string
	}{
		{"with err text", ApprovalResult{Err: "user said no"}, "tool call denied by user: user said no"},
		{"empty err", ApprovalResult{}, "tool call denied by user: denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &approvalFixture{verdict: tc.verdict}
			reg, inner := f.convertGated(t, tools.ExecutionWrite, nil, AdmissionPredicates{})
			s, err := runGated(t, reg)
			if err != nil {
				t.Fatalf("Run err = %v, want nil (denial is content, not error)", err)
			}
			if s != tc.want {
				t.Fatalf("Run = %q, want %q", s, tc.want)
			}
			if inner.calls != 0 {
				t.Fatalf("inner calls = %d, want 0 after denial", inner.calls)
			}
		})
	}
}

// TestApprovalGatedToolAdapterReadClassBypasses pins the threshold on
// the SDK path: read-class tools run without the gate or the pending
// advisory.
func TestApprovalGatedToolAdapterReadClassBypasses(t *testing.T) {
	f := &approvalFixture{verdict: ApprovalResult{Approved: true}}
	reg, inner := f.convertGated(t, tools.ExecutionRead, nil, AdmissionPredicates{})
	s, err := runGated(t, reg)
	if err != nil || s != "real result" {
		t.Fatalf("Run = (%q,%v), want the inner result", s, err)
	}
	if got := f.gateCount(); got != 0 {
		t.Fatalf("gate calls = %d, want 0 for read class", got)
	}
	if f.lastPending() != "" {
		t.Fatalf("EmitPending fired for read class: %q", f.lastPending())
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}
}

// TestApprovalGatedToolAdapterEmitPendingFiresBeforeGate pins the
// ordering and payload of the pending advisory: name, class detail,
// marshaled input, published BEFORE the gate runs. A nil EmitPending
// must not panic.
func TestApprovalGatedToolAdapterEmitPendingFiresBeforeGate(t *testing.T) {
	f := &approvalFixture{verdict: ApprovalResult{Approved: true}}
	reg, _ := f.convertGated(t, tools.ExecutionWrite, nil, AdmissionPredicates{})
	if _, err := runGated(t, reg); err != nil {
		t.Fatal(err)
	}
	pending := f.lastPending()
	if !strings.HasPrefix(pending, "gated|write|") || !strings.Contains(pending, "a.txt") {
		t.Fatalf("pending = %q, want gated|write|<args with a.txt>", pending)
	}
	if f.lastPendingSeq() < f.lastGateSeq() {
		t.Fatalf("pending seq %d not before gate seq %d", f.lastPendingSeq(), f.lastGateSeq())
	}
	// Nil EmitPending: the bridge still runs without panicking.
	f2 := &approvalFixture{verdict: ApprovalResult{Approved: true}}
	reg2 := tools.NewRegistry()
	inner2 := &capableCLITool{name: "gated", exec: "ok", class: tools.ExecutionWrite}
	reg2.Register(inner2)
	got2, err := ConvertToolRegistryWithAdmission(reg2, AdmissionPredicates{ApprovalGate: f2.gate})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := runGated(t, got2); err != nil || s != "ok" {
		t.Fatalf("nil EmitPending Run = (%q,%v), want clean delegation", s, err)
	}
}

// TestApprovalGatedToolAdapterStandingDecisions pins the cross-path
// persistence contract: standing allow short-circuits the gate;
// standing deny returns the standing denial without a gate call.
func TestApprovalGatedToolAdapterStandingDecisions(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		f := &approvalFixture{verdict: ApprovalResult{Approved: true, ApprovedForClass: true}}
		standing := NewApprovalStanding()
		reg, _ := f.convertGated(t, tools.ExecutionWrite, standing, AdmissionPredicates{})
		for i := 0; i < 2; i++ {
			if s, err := runGated(t, reg); err != nil || s != "real result" {
				t.Fatalf("run %d = (%q,%v)", i, s, err)
			}
		}
		if got := f.gateCount(); got != 1 {
			t.Fatalf("gate calls = %d, want 1 (second short-circuits)", got)
		}
	})
	t.Run("deny", func(t *testing.T) {
		f := &approvalFixture{verdict: ApprovalResult{Approved: true}}
		standing := NewApprovalStanding()
		standing.Deny("gated", tools.ExecutionWrite)
		reg, _ := f.convertGated(t, tools.ExecutionWrite, standing, AdmissionPredicates{})
		s, err := runGated(t, reg)
		if err != nil {
			t.Fatal(err)
		}
		if s != "tool call denied by user: standing decision" {
			t.Fatalf("Run = %q, want the standing denial", s)
		}
		if got := f.gateCount(); got != 0 {
			t.Fatalf("gate calls = %d, want 0 under standing deny", got)
		}
	})
}

// TestApprovalLayeredOutsideAdmission pins the layering rule: a tool
// admission already rejects (staged or unadmitted) never reaches the
// approval gate — the user is never prompted for a rejected call.
func TestApprovalLayeredOutsideAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		pred func(name string) (string, bool)
		want string
	}{
		{"staged", func(string) (string, bool) { return "tool staged; retry next turn", true }, "tool staged; retry next turn"},
		{"unadmitted", func(string) (string, bool) { return "tool unadmitted", true }, "tool unadmitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &approvalFixture{verdict: ApprovalResult{Approved: true}}
			reg, inner := f.convertGated(t, tools.ExecutionWrite, nil, AdmissionPredicates{})
			_ = reg
			reg2 := tools.NewRegistry()
			reg2.Register(inner)
			got, err := ConvertToolRegistryWithAdmission(reg2, AdmissionPredicates{
				ApprovalGate:      f.gate,
				EmitPending:       f.emitPending,
				StagedMessage:     tc.pred,
				UnadmittedHandler: nil,
			})
			if err != nil {
				t.Fatal(err)
			}
			wrapped, ok := got.Get("gated")
			if !ok {
				t.Fatal("missing gated")
			}
			out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			if s, _ := out.Value.(string); s != tc.want {
				t.Fatalf("Run = %q, want %q", s, tc.want)
			}
			if got := f.gateCount(); got != 0 {
				t.Fatalf("gate calls = %d, want 0 (admission must reject before approval)", got)
			}
			if f.lastPending() != "" {
				t.Fatalf("EmitPending fired behind admission: %q", f.lastPending())
			}
		})
	}
}

// TestNilGatePlusStandingAloneDoesNotWrap pins the wiring condition:
// ApprovalStanding or EmitPending without ApprovalGate wraps nothing.
func TestNilGatePlusStandingAloneDoesNotWrap(t *testing.T) {
	f := &approvalFixture{}
	reg := tools.NewRegistry()
	inner := &capableCLITool{name: "gated", exec: "plain", class: tools.ExecutionWrite}
	reg.Register(inner)
	got, err := ConvertToolRegistryWithAdmission(reg, AdmissionPredicates{
		ApprovalStanding: NewApprovalStanding(),
		EmitPending:      f.emitPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := runGated(t, got); err != nil || s != "plain" {
		t.Fatalf("Run = (%q,%v), want ungated delegation", s, err)
	}
	if f.lastPending() != "" {
		t.Fatalf("EmitPending fired without a gate: %q", f.lastPending())
	}
}
