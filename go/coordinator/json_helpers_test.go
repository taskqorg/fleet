package main

// Accessors of decoded JSON values for the tests' checks.

func astr(v any) string          { s, _ := v.(string); return s }
func mapOf(v any) map[string]any { m, _ := v.(map[string]any); return m }
func anys(v any) []any           { xs, _ := v.([]any); return xs }
