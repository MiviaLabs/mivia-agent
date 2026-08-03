package runtime

import (
	"context"
	"testing"
)

func TestTaskIdentityRoundTrip(t *testing.T) {
	ctx := ContextWithTaskIdentity(context.Background(), TaskIdentity{
		RunID: "r1", TaskID: "t1", Agent: "worker",
	})
	id, ok := TaskIdentityFrom(ctx)
	if !ok {
		t.Fatal("expected identity")
	}
	if id.RunID != "r1" || id.TaskID != "t1" || id.Agent != "worker" {
		t.Fatalf("got %+v", id)
	}
	if _, ok := TaskIdentityFrom(context.Background()); ok {
		t.Fatal("empty context should have no identity")
	}
	if _, ok := TaskIdentityFrom(ContextWithTaskIdentity(context.Background(), TaskIdentity{RunID: "r"})); ok {
		t.Fatal("missing task id should not count")
	}
}
