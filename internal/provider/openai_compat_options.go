package provider

import "reflect"

func cloneMap[T any](source map[string]T) map[string]T {
	if source == nil {
		return nil
	}
	clone := make(map[string]T, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneBodyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = cloneBodyValue(value)
	}
	return clone
}

func cloneBodyValue(value any) any {
	clone := cloneBodyReflect(reflect.ValueOf(value))
	if !clone.IsValid() {
		return nil
	}
	return clone.Interface()
}

func cloneBodyReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := cloneBodyReflect(value.Elem())
		return clone.Convert(value.Elem().Type())
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			clone.SetMapIndex(iter.Key(), cloneBodyReflect(iter.Value()))
		}
		return clone
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			clone.Index(i).Set(cloneBodyReflect(value.Index(i)))
		}
		return clone
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.New(value.Type().Elem())
		clone.Elem().Set(cloneBodyReflect(value.Elem()))
		return clone
	default:
		return value
	}
}
