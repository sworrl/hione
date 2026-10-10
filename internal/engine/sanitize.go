package engine

import (
	"math"
	"reflect"
)

// NoValue replaces NaN and Inf before JSON encoding (encoding/json refuses them). Readers treat
// anything at or below -900 as "not measured".
const NoValue = -999.0

// Sanitize walks v (a pointer) and replaces every NaN or infinite float64 with NoValue.
func Sanitize(v any) { walk(reflect.ValueOf(v)) }

func walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walk(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				walk(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walk(v.Index(i))
		}
	case reflect.Float64, reflect.Float32:
		if v.CanSet() {
			f := v.Float()
			if math.IsNaN(f) || math.IsInf(f, 0) {
				v.SetFloat(NoValue)
			}
		}
	}
}
