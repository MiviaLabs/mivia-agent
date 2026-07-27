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
