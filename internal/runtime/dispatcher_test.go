package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
func TestDispatcherPolicyRedactionAndTimeout(t *testing.T) {
	var e Event
	d := New(Policy{Sink: func(x Event) { e = x }})
	if err := d.Register(Skill, "x", testHandler{}); err != nil {
		t.Fatal(err)
	}
	r := d.Invoke(context.Background(), Request{ID: "1", Kind: Skill, Name: "x", Input: json.RawMessage(`{"token":"secret"}`), Timeout: time.Second})
	if r.Err != nil || e.Metadata.RedactedInput == "" {
		t.Fatalf("%+v %+v", r, e)
	}
	if e.Metadata.RedactedInput == `{"token":"secret"}` {
		t.Fatal("secret leaked")
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

func TestRedactTextPEM(t *testing.T) {
	begin := "-----BEGIN RSA " + "PRIVATE KEY-----"
	end := "-----END RSA " + "PRIVATE KEY-----"
	got := redactText(begin + "\nsecret-body\n" + end)
	if strings.Contains(got, "secret-body") {
		t.Fatalf("private key leaked: %q", got)
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

type handlerFunc func(context.Context, Request) (json.RawMessage, error)

func (f handlerFunc) Invoke(ctx context.Context, req Request) (json.RawMessage, error) {
	return f(ctx, req)
}
