package main

import "encoding/json"

func jsonRoundTrip(v any, out any) { b, _ := json.Marshal(v); json.Unmarshal(b, out) }
