package main

import (
	"testing"
	"time"
)

func TestDeliveryKeylessDue(t *testing.T) {
	last := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	t.Setenv("FLEET_DELIVERY_KEYLESS_MS", "")
	if deliveryKeylessDue(last.Add(5*time.Minute), last) {
		t.Fatal("keyless read due before the 6-minute default")
	}
	if !deliveryKeylessDue(last.Add(6*time.Minute), last) {
		t.Fatal("keyless read not due after the 6-minute default")
	}
	t.Setenv("FLEET_DELIVERY_KEYLESS_MS", "1000")
	if !deliveryKeylessDue(last.Add(time.Second), last) {
		t.Fatal("keyless read not due after FLEET_DELIVERY_KEYLESS_MS=1000")
	}
}
