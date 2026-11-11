package main

import (
	"reflect"
	"testing"

	"fleet/contract"
)

// A host body key that names hostId only case-insensitively would bypass readBody's exact-key host check.
func TestExactKeysHostBodies(t *testing.T) {
	ty := reflect.TypeOf(contract.ChildReceipt{})
	if exactKeys([]byte(`{"hostId":"a","HostID":"b"}`), ty) == nil {
		t.Fatal("HostID accepted")
	}
	if exactKeys([]byte(`{"delegation":{"ProfileID":"x"}}`), ty) == nil {
		t.Fatal("nested ProfileID accepted")
	}
	full := `"hostId":"a","executionId":"e","commandId":"c","delegation":{"input":"i","profileId":"p","requestId":"r"}`
	if err := exactKeys([]byte(`{`+full+`,"extra":1}`), ty); err != nil {
		t.Fatal(err)
	}
	if exactKeys([]byte(`{"hostId":"a"}`), ty) == nil {
		t.Fatal("missing executionId accepted")
	}
	cat := reflect.TypeOf(contract.AccountCatalogue{})
	if exactKeys([]byte(`{"accountId":"a","bindings":[{"name":"x","NAME":"y","executable":"/e","environment":{},"behaviorApproved":true}]}`), cat) == nil {
		t.Fatal("case variant in a list element accepted")
	}
	if exactKeys([]byte(`{"accountId":"a","bindings":[{"name":"x","executable":"/e","behaviorApproved":true}]}`), cat) == nil {
		t.Fatal("binding without environment accepted")
	}
}
