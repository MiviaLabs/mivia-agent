package subagents

import "testing"

func TestReservedHandlerNames(t *testing.T) {
	names := ReservedHandlerNames()
	for _, want := range []string{HandlerMultiStep, HandlerDelegate, HandlerOneshot} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing reserved handler %q", want)
		}
		if !IsReservedHandler(want) {
			t.Errorf("IsReservedHandler(%q) = false", want)
		}
	}
	if IsReservedHandler("researcher") {
		t.Fatal("researcher must not be reserved")
	}
}
