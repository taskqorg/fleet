package main

// Test-only JSON map accessors kept out of production executor code (E-MAP in design_rules_test.go).

func jStr(v any, keys ...string) string { s, _ := jAt(v, keys...).(string); return s }
func jObj(v any, keys ...string) M      { m, _ := jAt(v, keys...).(M); return m }

func jBoolOr(def bool, v any, keys ...string) bool {
	if b, ok := jAt(v, keys...).(bool); ok {
		return b
	}
	return def
}
