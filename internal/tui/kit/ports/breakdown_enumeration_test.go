package ports

import (
	"reflect"
	"testing"
)

// The defect class this file exists to make impossible:
//
//	A struct's own fields are enumerated BY HAND in a helper - a slice of
//	pointers to scale, a copy that preserves counts - and the helper
//	drifts from the struct. Adding a field is one edit; keeping every
//	enumeration of it correct is four, and the compiler checks none of
//	them.
//
// It is silent in both directions. A token bucket missing from buckets()
// is not scaled, so the rows stop summing to the total printed beside
// them; a count missing from countsOnly() is wiped on every rescale, so a
// row reads "(0)" forever. Neither fails a test that asserts on the
// fields it happens to know about, which is why an adversarial review of
// the Skills field found eight such mutations surviving the whole suite.
//
// The gate is a conformance table over the STRUCT ITSELF rather than
// another per-field assertion: reflection enumerates the fields, so a new
// field joins the table by existing and cannot be forgotten. Per-field
// tests answer "does this one work?"; only this answers "are they all
// accounted for?", which is the question that keeps being wrong.

// classifyFields splits a breakdown's fields into token buckets and
// counts by BEHAVIOUR, not by name or by type: a field that moves Total()
// is a token cost, and one that does not is a count. Deriving it keeps
// the test from carrying its own copy of the very list it is checking.
func classifyFields(t *testing.T) (tokens, counts []string) {
	t.Helper()
	rt := reflect.TypeOf(ContextBreakdown{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		var probe ContextBreakdown
		f := reflect.ValueOf(&probe).Elem().Field(i)
		switch f.Kind() {
		case reflect.Int64:
			f.SetInt(1000)
		case reflect.Int:
			f.SetInt(1000)
		default:
			t.Fatalf("ContextBreakdown.%s is a %s; this gate only understands int/int64", name, f.Kind())
		}
		if probe.Total() != 0 {
			tokens = append(tokens, name)
			continue
		}
		counts = append(counts, name)
	}
	return tokens, counts
}

// fieldPtrSet returns the addresses of the named fields of b.
func fieldPtrSet(b *ContextBreakdown, names []string) map[uintptr]string {
	out := make(map[uintptr]string, len(names))
	v := reflect.ValueOf(b).Elem()
	for _, n := range names {
		out[v.FieldByName(n).Addr().Pointer()] = n
	}
	return out
}

// TestEveryTokenFieldIsScaled: buckets() is what WithLiveTotal rescales
// through. A token field missing from it keeps its raw value while the
// others shrink, so the rows contradict the total displayed beside them.
func TestEveryTokenFieldIsScaled(t *testing.T) {
	tokens, _ := classifyFields(t)
	var b ContextBreakdown
	want := fieldPtrSet(&b, tokens)

	seen := map[uintptr]bool{}
	for _, p := range b.buckets() {
		addr := reflect.ValueOf(p).Pointer()
		name, known := want[addr]
		if !known {
			t.Errorf("buckets() returned a pointer that is not a token field of the struct")
			continue
		}
		if seen[addr] {
			t.Errorf("buckets() lists %s twice; it would be scaled twice", name)
		}
		seen[addr] = true
	}
	for addr, name := range want {
		if !seen[addr] {
			t.Errorf("%s is a token field but is missing from buckets(): it will not be rescaled, and the rows will stop summing to the total beside them", name)
		}
	}
}

// TestConversationBucketsAreExactlyTheReclaimableFields: the split
// between floor and conversation is the actionable one - what compaction
// can give back. Floor membership is derived from Floor()'s own
// behaviour, so a field added to Floor() but forgotten in
// conversationBuckets() (or the reverse) is caught here rather than by
// someone noticing a gauge that does not move after a compaction.
func TestConversationBucketsAreExactlyTheReclaimableFields(t *testing.T) {
	tokens, _ := classifyFields(t)
	rt := reflect.TypeOf(ContextBreakdown{})

	var wantNames []string
	for _, name := range tokens {
		var probe ContextBreakdown
		idx, _ := rt.FieldByName(name)
		reflect.ValueOf(&probe).Elem().FieldByIndex(idx.Index).SetInt(1000)
		if probe.Floor() == 0 { // moved Total but not Floor: reclaimable
			wantNames = append(wantNames, name)
		}
	}

	var b ContextBreakdown
	want := fieldPtrSet(&b, wantNames)
	got := map[uintptr]bool{}
	for _, p := range b.conversationBuckets() {
		got[reflect.ValueOf(p).Pointer()] = true
	}
	for addr, name := range want {
		if !got[addr] {
			t.Errorf("%s is reclaimable (it moves Total but not Floor) yet is missing from conversationBuckets(): a shrinking total will not scale it", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("conversationBuckets() has %d entries, want %d: it lists something the floor owns", len(got), len(want))
	}
}

// TestCountsOnlyKeepsEveryCountAndDropsEveryCost: countsOnly is the
// degenerate result - nothing priced yet, or a total of zero. A count it
// drops reads "(0)" on screen forever; a cost it keeps is a number with
// nothing behind it.
func TestCountsOnlyKeepsEveryCountAndDropsEveryCost(t *testing.T) {
	tokens, counts := classifyFields(t)
	if len(counts) == 0 {
		t.Fatal("no count fields found; the classifier is broken, not the code")
	}

	var full ContextBreakdown
	v := reflect.ValueOf(&full).Elem()
	for i := 0; i < v.NumField(); i++ {
		v.Field(i).SetInt(int64(500 + i))
	}

	got := reflect.ValueOf(full.countsOnly())
	for _, name := range counts {
		if got.FieldByName(name).Int() != v.FieldByName(name).Int() {
			t.Errorf("countsOnly() dropped the count %s: the row will read (0) after every rescale", name)
		}
	}
	for _, name := range tokens {
		if got.FieldByName(name).Int() != 0 {
			t.Errorf("countsOnly() kept the cost %s: a token figure survives with nothing behind it", name)
		}
	}
}
