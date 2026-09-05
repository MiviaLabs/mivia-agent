package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestLookupDeferredToolNoWiringWhenResolverReturnsNil(t *testing.T) {
	dispatcher := runtime.New(runtime.Policy{})
	t.Cleanup(dispatcher.Close)
	resolver := func() *tools.Registry { return nil }

	base, tool, lookup := lookupDeferredTool(dispatcher, resolver, nil, "grep")
	if lookup != deferredNoWiring {
		t.Fatalf("lookup = %v, want deferredNoWiring when the resolver returns a nil registry", lookup)
	}
	if base != nil || tool != nil {
		t.Fatalf("expected nil base/tool on the no-wiring degrade, got base=%v tool=%v", base, tool)
	}
}

func TestRegisterDeferredToolReturnsFalseOnRegistrationFailure(t *testing.T) {
	dispatcher := runtime.New(runtime.Policy{})
	t.Cleanup(dispatcher.Close)
	tool := fixedBodyTool{name: "grep"}

	// RegisterTool rejects a nil registry outright, and this tool was never
	// separately registered, so dispatcher.Has must also be false -
	// registerDeferredTool must report the failure, not paper over it.
	if ok := registerDeferredTool(dispatcher, nil, tool); ok {
		t.Fatal("registerDeferredTool reported success despite RegisterTool failing")
	}
}

func TestAdmitForExecutionReportsInstallFailure(t *testing.T) {
	dispatcher := runtime.New(runtime.Policy{})
	t.Cleanup(dispatcher.Close)
	tool := fixedBodyTool{name: "grep"}

	result := admitForExecution(dispatcher, nil, tool, "grep")
	if result.Execute != nil {
		t.Fatal("admitForExecution returned an executable tool despite the install failing")
	}
	if !strings.Contains(result.Content, "could not be installed") {
		t.Fatalf("Content = %q, want it to explain the install failure", result.Content)
	}
}

func TestAdmitDeferredCallPropagatesSpendAdmissionError(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "grep"})
	s.ToolBaseResolver = func() *tools.Registry { return full }

	dispatcher := runtime.New(runtime.Policy{})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)

	// Exhaust the admission-attempt budget before the call, so
	// spendAdmissionFor's own ChargeAdmissionAttempt fails.
	s.mu.Lock()
	s.admissionAttempts = tools.MaxAdmissionAttempts
	s.mu.Unlock()

	result := s.admitDeferredCall(context.Background(), 1, dispatcher, s.ToolBaseResolver, nil, "grep", json.RawMessage(`{}`), nil)
	if !result.Handled || result.Execute != nil {
		t.Fatalf("result = %+v, want a handled, non-executing denial", result)
	}
	if !strings.Contains(result.Content, "exhausted") {
		t.Fatalf("Content = %q, want it to explain the exhausted admission budget", result.Content)
	}
}
