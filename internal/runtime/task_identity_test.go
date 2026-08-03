package runtime

import (
	"context"
	"reflect"
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

// TestMailboxAccessContextRoundTrip covers the bundled MailboxAccess context
// plumbing (plan 54): set via ContextWithMailboxAccess, read back via
// MailboxAccessFrom, behavioral checks on the returned bundle, and the legacy
// MailboxDrainFrom reader on the bundle.
func TestMailboxAccessContextRoundTrip(t *testing.T) {
	drain := func() []ParentMessage { return nil }
	interrupt := func() <-chan struct{} { return make(chan struct{}) }
	pending := func() bool { return true }
	pendingInterrupt := func() bool { return true }

	access := MailboxAccess{
		Drain:            drain,
		Interrupt:        interrupt,
		Pending:          pending,
		PendingInterrupt: pendingInterrupt,
	}
	ctx := ContextWithMailboxAccess(context.Background(), access)

	got, ok := MailboxAccessFrom(ctx)
	if !ok {
		t.Fatal("MailboxAccessFrom: expected ok on bundled ctx")
	}
	if !sameFunc(got.Drain, drain) {
		t.Error("MailboxAccessFrom: Drain field is not the stub drain")
	}
	if !sameFunc(got.Interrupt, interrupt) {
		t.Error("MailboxAccessFrom: Interrupt field is not the stub interrupt")
	}
	if !sameFunc(got.Pending, pending) {
		t.Error("MailboxAccessFrom: Pending field is not the stub pending")
	}
	if !sameFunc(got.PendingInterrupt, pendingInterrupt) {
		t.Error("MailboxAccessFrom: PendingInterrupt field is not the stub pendingInterrupt")
	}

	// Behavioral checks on the returned bundle.
	if ch := got.Interrupt(); ch == nil {
		t.Error("Interrupt returned a nil channel, want a fresh one")
	}
	if got.Interrupt() == got.Interrupt() {
		t.Error("Interrupt should return a fresh channel per call")
	}
	if !got.Pending() {
		t.Error("Pending returned false, want true")
	}
	if !got.PendingInterrupt() {
		t.Error("PendingInterrupt returned false, want true")
	}
	if got.Drain() != nil {
		t.Error("Drain returned non-nil, want nil")
	}

	// The bundle's Drain must be visible through the legacy reader.
	drainFn, ok := MailboxDrainFrom(ctx)
	if !ok {
		t.Fatal("MailboxDrainFrom: expected ok on bundled ctx")
	}
	if !sameFunc(drainFn, drain) {
		t.Error("MailboxDrainFrom: did not surface the bundle's Drain func")
	}
}

// TestMailboxAccessContextBareAndLegacy covers the nil-safe readers on a bare
// context and the backward-compat ContextWithMailboxDrain wrapper (plan 54).
func TestMailboxAccessContextBareAndLegacy(t *testing.T) {
	// Nil-safe readers on a bare context.
	zero, ok := MailboxAccessFrom(context.Background())
	if ok {
		t.Error("MailboxAccessFrom(background): expected ok=false")
	}
	if zero.Drain != nil || zero.Interrupt != nil || zero.Pending != nil || zero.PendingInterrupt != nil {
		t.Errorf("MailboxAccessFrom(background): expected zero value, got %+v", zero)
	}
	if fn, ok := MailboxDrainFrom(context.Background()); ok || fn != nil {
		t.Errorf("MailboxDrainFrom(background): got (%v, %v), want (nil, false)", fn, ok)
	}

	// Backward-compat wrapper: ContextWithMailboxDrain still round-trips.
	legacy := func() []ParentMessage { return []ParentMessage{{Kind: "steer"}} }
	legacyCtx := ContextWithMailboxDrain(context.Background(), legacy)
	gotFn, ok := MailboxDrainFrom(legacyCtx)
	if !ok {
		t.Fatal("MailboxDrainFrom: expected ok on legacy ctx")
	}
	if !sameFunc(gotFn, legacy) {
		t.Error("MailboxDrainFrom: legacy wrapper did not return the stored drain")
	}
	msgs := gotFn()
	if len(msgs) != 1 || msgs[0].Kind != "steer" {
		t.Errorf("legacy drain returned %+v, want the stored messages", msgs)
	}
}

// sameFunc reports whether two function values share the same code pointer.
// Go functions are not comparable (except to nil), so equality is checked via
// reflect.
func sameFunc[T any](a, b T) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}
