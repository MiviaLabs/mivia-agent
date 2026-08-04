package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

type testHandler struct{}

func (testHandler) Invoke(context.Context, Request) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true,"token":"secret"}`), nil
}

func TestDispatcherAddsCallerToHandlerContext(t *testing.T) {
	d := New(Policy{})
	if err := d.Register(Skill, "caller", handlerFunc(func(ctx context.Context, _ Request) (json.RawMessage, error) {
		caller, ok := CallerFrom(ctx)
		if !ok || caller.SessionID != "session-a" || caller.TurnID != "turn-2" || caller.Depth != 3 || caller.Role != "reviewer" {
			t.Fatalf("caller = %#v, present=%v", caller, ok)
		}
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	if result := d.Invoke(context.Background(), Request{ID: "caller", Kind: Skill, Name: "caller", SessionID: "session-a", TurnID: "turn-2", Depth: 3, Role: "reviewer"}); result.Err != nil {
		t.Fatal(result.Err)
	}
}

// useRedactionPolicy installs a process-wide policy for the duration of a test.
// Audit metadata is redacted by configuration only, so every assertion that a
// credential disappears has to name the configuration that removed it.
func useRedactionPolicy(t *testing.T, patterns, keyNames []string) {
	t.Helper()
	policy, err := redact.Compile(patterns, keyNames, "")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(nil) })
}

func TestDispatcherPolicyRedactionAndTimeout(t *testing.T) {
	useRedactionPolicy(t, nil, []string{"token"})
	var e Event
	d := New(Policy{Sink: func(x Event) { e = x }})
	if err := d.Register(Skill, "x", testHandler{}); err != nil {
		t.Fatal(err)
	}
	r := d.Invoke(context.Background(), Request{ID: "1", Kind: Skill, Name: "x", Input: json.RawMessage(`{"token":"secret"}`), Timeout: time.Second})
	if r.Err != nil || e.Metadata.InputPreview == "" {
		t.Fatalf("%+v %+v", r, e)
	}
	if strings.Contains(e.Metadata.InputPreview, "secret") {
		t.Fatalf("configured key name did not elide the value: %q", e.Metadata.InputPreview)
	}
}

// The fail-open posture, tested at the audit boundary: an unconfigured
// workspace writes whatever the handler saw into the Metadata previews. The
// sink is what makes the previews exist at all (plan 11); it does not affect
// what redaction does or does not remove, which is the point under test.
func TestDispatcherWithNoPolicyRedactsNothing(t *testing.T) {
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(nil) })
	var e Event
	d := New(Policy{Sink: func(x Event) { e = x }})
	if err := d.Register(Skill, "x", testHandler{}); err != nil {
		t.Fatal(err)
	}
	const input = `{"token":"ghp_unconfigured123"}`
	if r := d.Invoke(context.Background(), Request{ID: "1", Kind: Skill, Name: "x", Input: json.RawMessage(input), Timeout: time.Second}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if !strings.Contains(e.Metadata.InputPreview, "ghp_unconfigured123") {
		t.Fatalf("input redacted with no policy configured: %q", e.Metadata.InputPreview)
	}
	if !strings.Contains(e.Metadata.OutputPreview, "secret") {
		t.Fatalf("output redacted with no policy configured: %q", e.Metadata.OutputPreview)
	}
}

// Plan 10 §4a: prompt and reasoning are the agent's own instructions and
// deliberation, not the user's secrets. Eliding them made audit metadata
// useless for reconstructing agent behaviour while protecting nothing, so they
// are dropped from the key list and are NOT migrated into configuration.
// A policy configured for real credentials must leave them intact.
func TestDispatcherNeverRedactsPromptOrReasoning(t *testing.T) {
	useRedactionPolicy(t, []string{`(?:sk-|ghp_)[A-Za-z0-9._~-]+`}, []string{"token", "secret", "password", "authorization"})
	var e Event
	d := New(Policy{Sink: func(x Event) { e = x }})
	if err := d.Register(Skill, "x", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`{"reasoning":"chose-plan-b"}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	if r := d.Invoke(context.Background(), Request{ID: "1", Kind: Skill, Name: "x", Input: json.RawMessage(`{"prompt":"summarise the diff"}`), Timeout: time.Second}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if !strings.Contains(e.Metadata.InputPreview, "summarise the diff") {
		t.Fatalf("prompt was elided from audit metadata: %q", e.Metadata.InputPreview)
	}
	if !strings.Contains(e.Metadata.OutputPreview, "chose-plan-b") {
		t.Fatalf("reasoning was elided from audit metadata: %q", e.Metadata.OutputPreview)
	}
}

func TestDispatcherPolicyDropsAllowMap(t *testing.T) {
	d := New(Policy{})
	if err := d.Register(Skill, "x", testHandler{}); err != nil {
		t.Fatal(err)
	}
	if got := d.Policy(); got.Allow != nil {
		t.Fatalf("derived policy retained allow map: %#v", got.Allow)
	}
}

// Retargets TestRedactTextPEM: the PEM rule still works, but it is now the
// workspace's configured pattern rather than a compiled-in regex.
func TestRedactMetaPEMWithConfiguredPolicy(t *testing.T) {
	useRedactionPolicy(t, []string{`(?is)-----BEGIN [A-Z0-9 ]+PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]+PRIVATE KEY-----|$)`}, nil)
	begin := "-----BEGIN RSA " + "PRIVATE KEY-----"
	end := "-----END RSA " + "PRIVATE KEY-----"
	got := redactMeta([]byte(begin + "\nsecret-body\n" + end))
	if strings.Contains(got, "secret-body") {
		t.Fatalf("private key leaked: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("expected placeholder: %q", got)
	}
}

// truncateText is not redaction: the 256-byte cap is audit-volume control and
// stays regardless of policy.
func TestRedactMetaTruncatesWithoutPolicy(t *testing.T) {
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(nil) })
	if got := redactMeta([]byte(strings.Repeat("a", 512))); len(got) != 256 {
		t.Fatalf("truncation cap lost: len=%d", len(got))
	}
}
func TestDispatcherRejectsRecursionAndDepth(t *testing.T) {
	d := New(Policy{MaxDepth: 1})
	_ = d.Register(Skill, "x", testHandler{})
	if d.Invoke(context.Background(), Request{ID: "x", Kind: Skill, Name: "x", Depth: 2}).Err == nil {
		t.Fatal("depth accepted")
	}
}

func TestDispatcherSuppressesDuplicateAndSerializesScope(t *testing.T) {
	var calls int
	blocked := make(chan struct{})
	started := make(chan struct{})
	d := New(Policy{})
	if err := d.Register(Skill, "x", handlerFunc(func(ctx context.Context, _ Request) (json.RawMessage, error) {
		calls++
		close(started)
		<-blocked
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	rch := make(chan Result, 1)
	go func() {
		rch <- d.Invoke(context.Background(), Request{ID: "same", Kind: Skill, Name: "x", Scope: "resource"})
	}()
	<-started
	r2ch := make(chan Result, 1)
	go func() {
		r2ch <- d.Invoke(context.Background(), Request{ID: "same", Kind: Skill, Name: "x", Scope: "resource"})
	}()
	close(blocked)
	r1 := <-rch
	r2 := <-r2ch
	if r1.Err != nil || r2.Err != nil || calls != 1 || r2.Metadata.Status != "duplicate" {
		t.Fatalf("calls=%d r1=%+v r2=%+v", calls, r1, r2)
	}
}

func TestDispatcherDuplicateWaiterCancellationKeepsOwnerActive(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	d := New(Policy{})
	if err := d.Register(Skill, "x", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		close(started)
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}

	ownerDone := make(chan Result, 1)
	go func() {
		ownerDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Skill, Name: "x"})
	}()
	<-started

	waiterCtx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(waiterCtx, Request{ID: "same", Kind: Skill, Name: "x"})
	}()
	cancel()
	waiter := <-waiterDone
	if waiter.Err == nil || waiter.Metadata.Status != "canceled" {
		t.Fatalf("waiter=%+v, want canceled duplicate", waiter)
	}

	thirdDone := make(chan Result, 1)
	go func() {
		thirdDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Skill, Name: "x"})
	}()
	select {
	case third := <-thirdDone:
		t.Fatalf("third invocation returned before owner completed: %+v", third)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)

	owner := <-ownerDone
	third := <-thirdDone
	if owner.Err != nil || third.Err != nil || third.Metadata.Status != "duplicate" || calls.Load() != 1 {
		t.Fatalf("calls=%d owner=%+v third=%+v", calls.Load(), owner, third)
	}
}

func TestDispatcherReportsActualAttemptsAndCancellation(t *testing.T) {
	n := 0
	events := []string{}
	d := New(Policy{MaxRetries: 2, Sink: func(e Event) { events = append(events, e.Type) }})
	_ = d.Register(Skill, "retry", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		n++
		if n < 2 {
			return nil, context.DeadlineExceeded
		}
		return json.RawMessage(`{}`), nil
	}))
	r := d.Invoke(context.Background(), Request{Kind: Skill, Name: "retry", Retry: 2})
	if r.Err != nil || r.Attempts != 2 {
		t.Fatalf("%+v attempts=%d", r, r.Attempts)
	}
	if len(events) < 3 {
		t.Fatalf("lifecycle events=%v", events)
	}
}
func TestDispatcherRejectsIDReuseAndCumulativeBudget(t *testing.T) {
	d := New(Policy{MaxBudget: 3})
	_ = d.Register(Skill, "x", testHandler{})
	if d.Invoke(context.Background(), Request{ID: "id", Kind: Skill, Name: "x", Budget: 2, Input: json.RawMessage(`{"a":1}`)}).Err != nil {
		t.Fatal("first rejected")
	}
	if d.Invoke(context.Background(), Request{ID: "id", Kind: Skill, Name: "x", Budget: 1, Input: json.RawMessage(`{"a":2}`)}).Err == nil {
		t.Fatal("id reuse accepted")
	}
	if d.Invoke(context.Background(), Request{ID: "id2", Kind: Skill, Name: "x", Budget: 2, Input: json.RawMessage(`{"a":3}`), TurnID: "turn"}).Err != nil {
		t.Fatal("budget setup rejected")
	}
	if d.Invoke(context.Background(), Request{ID: "id3", Kind: Skill, Name: "x", Budget: 2, Input: json.RawMessage(`{"a":4}`), TurnID: "turn"}).Err == nil {
		t.Fatal("cumulative budget accepted")
	}
}

func TestDispatcherRejectsNegativeBudgetBeforeCumulativeAccounting(t *testing.T) {
	d := New(Policy{MaxBudget: 3})
	_ = d.Register(Skill, "x", testHandler{})

	if r := d.Invoke(context.Background(), Request{ID: "negative", Kind: Skill, Name: "x", Budget: -2}); r.Err == nil || r.Err.Error() != "budget must be non-negative" {
		t.Fatalf("negative budget result = %+v, want rejection", r)
	}
	if r := d.Invoke(context.Background(), Request{ID: "first", Kind: Skill, Name: "x", Budget: 3, TurnID: "turn"}); r.Err != nil {
		t.Fatalf("positive budget after rejection failed: %v", r.Err)
	}
	if r := d.Invoke(context.Background(), Request{ID: "second", Kind: Skill, Name: "x", Budget: 1, TurnID: "turn"}); r.Err == nil || r.Err.Error() != "cumulative budget exceeded" {
		t.Fatalf("cumulative budget result = %+v, want limit rejection", r)
	}
}

func TestDispatcherOnCloseConcurrentWithCloseInvokesHookOnce(t *testing.T) {
	d := New(Policy{})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	d.OnClose(func() {
		close(started)
		<-release
	})

	closeDone := make(chan struct{})
	go func() {
		d.Close()
		close(closeDone)
	}()
	<-started

	registered := make(chan struct{})
	go func() {
		d.OnClose(func() { calls.Add(1) })
		close(registered)
	}()
	<-registered
	close(release)
	<-closeDone

	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent close hook calls = %d, want 1", got)
	}
}

func TestDispatcherCloseIsIdempotentAndRunsLateHooksOnce(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	d.OnClose(func() { calls.Add(1) })

	d.Close()
	d.Close()
	d.OnClose(func() { calls.Add(1) })
	d.OnClose(func() { calls.Add(1) })

	if got := calls.Load(); got != 3 {
		t.Fatalf("close hook calls = %d, want 3", got)
	}
}

func TestDispatcherConcurrentOnCloseAndCloseInvokesEveryHookOnce(t *testing.T) {
	const hookCount = 32
	d := New(Policy{})
	var calls atomic.Int32
	var registrations sync.WaitGroup
	registrations.Add(hookCount)

	for i := 0; i < hookCount; i++ {
		go func() {
			defer registrations.Done()
			d.OnClose(func() { calls.Add(1) })
		}()
	}

	closeDone := make(chan struct{})
	go func() {
		d.Close()
		close(closeDone)
	}()

	registrations.Wait()
	<-closeDone
	if got := calls.Load(); got != hookCount {
		t.Fatalf("concurrent close hook calls = %d, want %d", got, hookCount)
	}
}

// Plan 11: the previews exist for a sink. With no sink attached there is no
// consumer, so the dispatcher must not pay for them - on either the success or
// the failure path. The redactMeta call below is the control: it shows the
// payloads are perfectly previewable, so an empty field is the guard doing its
// job and not an absence of content.
func TestMetadataPreviewEmptyWithoutSink(t *testing.T) {
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(nil) })
	const input = `{"prompt":"summarise the diff"}`
	if redactMeta([]byte(input)) == "" {
		t.Fatalf("control failed: input is not previewable at all")
	}

	d := New(Policy{})
	if err := d.Register(Skill, "ok", testHandler{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(Skill, "boom", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return nil, errors.New("handler exploded")
	})); err != nil {
		t.Fatal(err)
	}

	ok := d.Invoke(context.Background(), Request{ID: "1", Kind: Skill, Name: "ok", Input: json.RawMessage(input)})
	if ok.Err != nil {
		t.Fatal(ok.Err)
	}
	if ok.Metadata.InputPreview != "" || ok.Metadata.OutputPreview != "" {
		t.Fatalf("success path computed previews with no sink attached: in=%q out=%q", ok.Metadata.InputPreview, ok.Metadata.OutputPreview)
	}

	bad := d.Invoke(context.Background(), Request{ID: "2", Kind: Skill, Name: "boom", Input: json.RawMessage(input)})
	if bad.Err == nil {
		t.Fatal("expected handler error")
	}
	if bad.Metadata.InputPreview != "" || bad.Metadata.OutputPreview != "" {
		t.Fatalf("failure path computed previews with no sink attached: in=%q out=%q", bad.Metadata.InputPreview, bad.Metadata.OutputPreview)
	}
}

// The other half of the contract: attach a sink and the previews are there, and
// bounded. The bound is the 256-byte cap, which is volume control rather than
// redaction and so holds with no policy configured.
func TestMetadataPreviewPopulatedWithSink(t *testing.T) {
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(nil) })
	big := strings.Repeat("a", 4096)
	var e Event
	d := New(Policy{Sink: func(x Event) { e = x }})
	if err := d.Register(Skill, "x", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`{"out":"` + big + `"}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	r := d.Invoke(context.Background(), Request{ID: "1", Kind: Skill, Name: "x", Input: json.RawMessage(`{"in":"` + big + `"}`)})
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	for _, c := range []struct {
		field, got string
	}{
		{"InputPreview", r.Metadata.InputPreview},
		{"OutputPreview", r.Metadata.OutputPreview},
	} {
		if c.got == "" {
			t.Fatalf("%s empty with a sink attached", c.field)
		}
		if len(c.got) > 256 {
			t.Fatalf("%s = %d bytes, want at most 256", c.field, len(c.got))
		}
	}
	if e.Metadata.InputPreview != r.Metadata.InputPreview || e.Metadata.OutputPreview != r.Metadata.OutputPreview {
		t.Fatalf("sink saw different previews than the result: %+v", e.Metadata)
	}
}

// TestDispatcherDedupsIdenticalToolCallsWithinTurn reproduces the duplicate-
// delivery failure mode: the same logical tool call (same name, same input)
// re-issued with a FRESH call ID while the same turn is active must NOT execute
// a second time - it returns the recorded result. Identical calls in a LATER
// turn, or in a different subagent task context, execute fresh.
func TestDispatcherDedupsIdenticalToolCallsWithinTurn(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["git","commit","-m","x"]}`)

	first := d.Invoke(context.Background(), Request{ID: "call-1", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1"})
	if first.Err != nil {
		t.Fatal(first.Err)
	}

	// Duplicate delivery of the same logical call: fresh ID, identical
	// name+input, same turn. This is the git commit / git mv repro.
	second := d.Invoke(context.Background(), Request{ID: "call-2", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1"})
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	if second.Metadata.Status != "duplicate" {
		t.Fatalf("second invocation status = %q, want duplicate", second.Metadata.Status)
	}
	if string(second.Output) != string(first.Output) {
		t.Fatalf("second output = %s, want recorded %s", second.Output, first.Output)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}

	// A later turn re-issuing the identical call must execute fresh.
	third := d.Invoke(context.Background(), Request{ID: "call-3", Kind: Tool, Name: "t", Input: input, TurnID: "turn:2"})
	if third.Err != nil {
		t.Fatal(third.Err)
	}
	if third.Metadata.Status == "duplicate" {
		t.Fatal("identical call in a later turn must not be deduped")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("handler executed %d times, want exactly 2", got)
	}

	// A different subagent task context in the same turn must not collide.
	fourth := d.Invoke(context.Background(), Request{ID: "call-4", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", ParentID: "task-9"})
	if fourth.Err != nil {
		t.Fatal(fourth.Err)
	}
	if fourth.Metadata.Status == "duplicate" {
		t.Fatal("identical call in a different parent (subagent) context must not be deduped")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("handler executed %d times, want exactly 3", got)
	}

	// Failure results are recorded too: a re-issued failing call does not re-run.
	d2 := New(Policy{})
	var failCalls atomic.Int32
	if err := d2.Register(Tool, "f", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		failCalls.Add(1)
		return nil, errors.New("boom")
	})); err != nil {
		t.Fatal(err)
	}
	f1 := d2.Invoke(context.Background(), Request{ID: "f-1", Kind: Tool, Name: "f", Input: input, TurnID: "turn:1"})
	if f1.Err == nil {
		t.Fatal("expected failure")
	}
	f2 := d2.Invoke(context.Background(), Request{ID: "f-2", Kind: Tool, Name: "f", Input: input, TurnID: "turn:1"})
	if f2.Err == nil {
		t.Fatal("expected recorded failure")
	}
	if got := failCalls.Load(); got != 1 {
		t.Fatalf("failing handler executed %d times, want exactly 1", got)
	}
}

type handlerFunc func(context.Context, Request) (json.RawMessage, error)

func (f handlerFunc) Invoke(ctx context.Context, req Request) (json.RawMessage, error) {
	return f(ctx, req)
}
