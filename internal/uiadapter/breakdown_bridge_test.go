package uiadapter

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// TestEveryBreakdownFieldCrossesTheBridge is the test that catches the
// NEXT dropped field, not just the last one.
//
// The bridge copies chat's breakdown into ports' field by field. A field
// added to both structs but forgotten in the copy reads as a permanent
// zero on screen with nothing failing anywhere: the sum still adds up,
// the row just always says 0. Asserting the copy by reflection means the
// omission fails here the day the field is added.
//
// Pending is ports-only: it is what the provider prices beyond what the
// composition explains, filled in later by WithLiveTotal, so chat has
// nothing to copy into it.
func TestEveryBreakdownFieldCrossesTheBridge(t *testing.T) {
	src := chat.ContextBreakdown{}
	sv := reflect.ValueOf(&src).Elem()
	// A distinct value per field, so a copy that crosses two fields over
	// is caught as surely as one that drops a field.
	for i := 0; i < sv.NumField(); i++ {
		f := sv.Field(i)
		switch f.Kind() {
		case reflect.Int:
			f.SetInt(int64(100 + i))
		default:
			t.Fatalf("chat.ContextBreakdown.%s is a %s; extend this test",
				sv.Type().Field(i).Name, f.Kind())
		}
	}

	got := toPortsBreakdown(src)
	gv := reflect.ValueOf(got)

	for i := 0; i < sv.NumField(); i++ {
		name := sv.Type().Field(i).Name
		dst := gv.FieldByName(name)
		if !dst.IsValid() {
			t.Errorf("chat.ContextBreakdown.%s has no counterpart in ports.ContextBreakdown", name)
			continue
		}
		want := sv.Field(i).Int()
		if dst.Int() != want {
			t.Errorf("%s did not cross the bridge: got %d, want %d", name, dst.Int(), want)
		}
	}

	// And nothing on the ports side is left unexplained except Pending.
	for i := 0; i < gv.NumField(); i++ {
		name := gv.Type().Field(i).Name
		if name == "Pending" {
			continue
		}
		if !sv.FieldByName(name).IsValid() {
			t.Errorf("ports.ContextBreakdown.%s has no source field; the bridge cannot fill it", name)
		}
	}
	if got.Pending != 0 {
		t.Errorf("Pending = %d, want 0: the bridge must not invent it", got.Pending)
	}
}
