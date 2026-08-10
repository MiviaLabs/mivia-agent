package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDispatcherRecursiveDuplicateDoesNotReleaseOtherWaiters(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	d := New(Policy{})
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ownerReq := Request{ID: "same", ParentID: "root", Kind: Tool, Name: "t", Budget: 1}
	ownerDone := make(chan Result, 1)
	go func() { ownerDone <- d.Invoke(context.Background(), ownerReq) }()
	<-started
	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(context.Background(), Request{ID: "same", ParentID: "root", Kind: Tool, Name: "t", Budget: 1})
	}()
	waitForIDKeyedWaiterRegistered(t, d, "same", "root")

	recursive := d.Invoke(context.Background(), Request{ID: "same", ParentID: "same", Kind: Tool, Name: "t"})
	if recursive.Err == nil {
		t.Fatal("recursive invocation succeeded")
	}
	select {
	case waiter := <-waiterDone:
		t.Fatalf("recursive result released another waiter: %+v", waiter)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	owner := <-ownerDone
	waiter := <-waiterDone
	if owner.Err != nil || waiter.Err != nil || waiter.Metadata.Status != "duplicate" {
		t.Fatalf("results = owner:%+v waiter:%+v", owner, waiter)
	}
}
