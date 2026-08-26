package composition

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestRegistryInputFieldsMirrorDefaultOptions(t *testing.T) {
	optsType := reflect.TypeOf(tools.DefaultOptions{})
	inputType := reflect.TypeOf(RegistryInput{})

	inputFieldMap := make(map[string]reflect.Type)
	for i := 0; i < inputType.NumField(); i++ {
		field := inputType.Field(i)
		inputFieldMap[field.Name] = field.Type
	}

	for i := 0; i < optsType.NumField(); i++ {
		field := optsType.Field(i)
		inType, ok := inputFieldMap[field.Name]
		if !ok {
			t.Errorf("RegistryInput is missing field %s (present in tools.DefaultOptions)", field.Name)
			continue
		}
		if inType != field.Type {
			t.Errorf("field %s type mismatch: tools.DefaultOptions has %v, RegistryInput has %v",
				field.Name, field.Type, inType)
		}
	}
}
