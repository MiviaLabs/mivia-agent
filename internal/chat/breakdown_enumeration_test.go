package chat

import (
	"reflect"
	"testing"
)

// The sibling of internal/uikit/ports/breakdown_enumeration_test.go, for
// the same defect class: a struct's fields enumerated by hand in a
// helper, drifting from the struct.
//
// Here every field is an int, so token costs and counts cannot be told
// apart by type. They are told apart by BEHAVIOUR instead - a field that
// moves Total() is a cost - which also means this gate carries no copy of
// the list it is checking. A field added to ContextBreakdown joins the
// table by existing.

// splitChatFields classifies fields as costs or counts by whether they
// move Total().
func splitChatFields(t *testing.T) (costs, counts []string) {
	t.Helper()
	rt := reflect.TypeOf(ContextBreakdown{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		var probe ContextBreakdown
		f := reflect.ValueOf(&probe).Elem().Field(i)
		if f.Kind() != reflect.Int {
			t.Fatalf("ContextBreakdown.%s is a %s; this gate only understands int", name, f.Kind())
		}
		f.SetInt(1000)
		if probe.Total() != 0 {
			costs = append(costs, name)
			continue
		}
		counts = append(counts, name)
	}
	return costs, counts
}

// TestEveryCostFieldIsRescaled: fields() is what scaleTo walks. A cost
// missing from it is left at its raw value while the rest are scaled to
// the calibrated total, so the rows stop adding up to the number the
// gauge shows - the exact "two disagreeing numbers for one quantity"
// ContextUsage's own comment forbids.
func TestEveryCostFieldIsRescaled(t *testing.T) {
	costs, counts := splitChatFields(t)

	var b ContextBreakdown
	v := reflect.ValueOf(&b).Elem()
	want := map[uintptr]string{}
	for _, n := range costs {
		want[v.FieldByName(n).Addr().Pointer()] = n
	}
	forbidden := map[uintptr]string{}
	for _, n := range counts {
		forbidden[v.FieldByName(n).Addr().Pointer()] = n
	}

	seen := map[uintptr]bool{}
	for _, p := range b.fields() {
		addr := reflect.ValueOf(p).Pointer()
		if name, bad := forbidden[addr]; bad {
			t.Errorf("fields() lists the count %s; scaling it would corrupt a number of things into a number of tokens", name)
			continue
		}
		name, known := want[addr]
		if !known {
			t.Error("fields() returned a pointer that is not a field of the struct")
			continue
		}
		if seen[addr] {
			t.Errorf("fields() lists %s twice", name)
		}
		seen[addr] = true
	}
	for addr, name := range want {
		if !seen[addr] {
			t.Errorf("%s is a token cost but is missing from fields(): scaleTo will leave it unscaled and the rows will contradict the total", name)
		}
	}
}

// TestChatCountsOnlyKeepsEveryCount: countsOnly is what an unpriced or
// failed estimate degrades to. A count it drops reads "(0)" on screen for
// the rest of the session.
func TestChatCountsOnlyKeepsEveryCount(t *testing.T) {
	costs, counts := splitChatFields(t)

	var full ContextBreakdown
	v := reflect.ValueOf(&full).Elem()
	for i := 0; i < v.NumField(); i++ {
		v.Field(i).SetInt(int64(500 + i))
	}

	got := reflect.ValueOf(full.countsOnly())
	for _, name := range counts {
		if got.FieldByName(name).Int() != v.FieldByName(name).Int() {
			t.Errorf("countsOnly() dropped the count %s", name)
		}
	}
	for _, name := range costs {
		if got.FieldByName(name).Int() != 0 {
			t.Errorf("countsOnly() kept the cost %s", name)
		}
	}
}

// TestFloorAndConversationPartitionEveryCost: Total is Floor plus
// Conversation, so a cost that is in neither is invisible to both and a
// cost in both is counted twice. Either way the two-tone gauge splits the
// window wrongly, and the totals still look plausible.
func TestFloorAndConversationPartitionEveryCost(t *testing.T) {
	costs, _ := splitChatFields(t)
	rt := reflect.TypeOf(ContextBreakdown{})

	for _, name := range costs {
		var probe ContextBreakdown
		idx, _ := rt.FieldByName(name)
		reflect.ValueOf(&probe).Elem().FieldByIndex(idx.Index).SetInt(1000)

		inFloor := probe.Floor() != 0
		inConv := probe.Conversation() != 0
		switch {
		case inFloor && inConv:
			t.Errorf("%s is counted in BOTH Floor and Conversation; Total double counts it", name)
		case !inFloor && !inConv:
			t.Errorf("%s is in neither Floor nor Conversation; the gauge's two-tone split cannot see it", name)
		}
	}
}
