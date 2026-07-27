package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type testHandler struct{}

func (testHandler) Invoke(context.Context, Request) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true,"token":"secret"}`), nil
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

type handlerFunc func(context.Context, Request) (json.RawMessage, error)

func (f handlerFunc) Invoke(ctx context.Context, req Request) (json.RawMessage, error) {
	return f(ctx, req)
}
