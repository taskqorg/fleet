package main

import (
	"reflect"
	"testing"

	"fleet/contract"
)

// A host body key that names hostId only case-insensitively would bypass readBody's exact-key host check.
func TestCaseExactRefusesHostIDVariant(t *testing.T) {
	ty := reflect.TypeOf(contract.ChildReceipt{})
	if caseExact([]byte(`{"hostId":"a","HostID":"b"}`), ty) == nil {
		t.Fatal("HostID accepted")
	}
	if caseExact([]byte(`{"delegation":{"ProfileID":"x"}}`), ty) == nil {
		t.Fatal("nested ProfileID accepted")
	}
	if err := caseExact([]byte(`{"hostId":"a","extra":1}`), ty); err != nil {
		t.Fatal(err)
	}
}
